package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dorkitude/cfctl/internal/r2"
)

// R2's S3 API takes x-amz-storage-class STANDARD / STANDARD_IA; the REST
// names (Standard / InfrequentAccess) must be translated on the S3 path, and
// S3 names given to the REST path translated back.
func TestR2PutStorageClassNames(t *testing.T) {
	f, _ := r2Fake(t, nil)
	var mu sync.Mutex
	var classes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		classes = append(classes, r.Method+" "+r.Header.Get("x-amz-storage-class"))
		mu.Unlock()
		w.Header().Set("ETag", `"abc"`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(r2.S3EndpointEnv, srv.URL)

	file := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(file, []byte("hi"), 0o644)

	// S3 path (forced by --cache-control).
	stRun(t, "", "r2", "put", "bkt/a.txt", "--file", file, "--cache-control", "max-age=60", "--storage-class", "InfrequentAccess")
	if len(classes) != 1 || classes[0] != "PUT STANDARD_IA" {
		t.Errorf("S3 storage class = %v, want PUT STANDARD_IA", classes)
	}

	// REST path with an S3-style name.
	stRun(t, "", "r2", "put", "bkt/b.txt", "--file", file, "--storage-class", "STANDARD_IA")
	r := stFind(t, f, "PUT", "/r2/buckets/bkt/objects/b.txt")
	if got := r.Header.Get("cf-r2-storage-class"); got != "InfrequentAccess" {
		t.Errorf("REST storage class = %q, want InfrequentAccess", got)
	}
}

func TestR2StorageClassMapping(t *testing.T) {
	for in, want := range map[string]string{"Standard": "STANDARD", "InfrequentAccess": "STANDARD_IA", "STANDARD_IA": "STANDARD_IA", "GLACIER": "GLACIER"} {
		if got := r2.S3StorageClass(in); got != want {
			t.Errorf("S3StorageClass(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"STANDARD": "Standard", "standard_ia": "InfrequentAccess", "InfrequentAccess": "InfrequentAccess"} {
		if got := r2.RESTStorageClass(in); got != want {
			t.Errorf("RESTStorageClass(%q) = %q, want %q", in, got, want)
		}
	}
}

// KV key names are percent-encoded like encodeURIComponent (the API docs ask
// for ":", "!" and "%" to be encoded; wrangler uses encodeURIComponent).
func TestKVKeyNameEncoding(t *testing.T) {
	f := stFake(t, kvRoutes())
	key := "user:1+a@b/c d!%"
	want := "user%3A1%2Ba%40b%2Fc%20d%21%25"
	stRun(t, "", "kv", "key", "put", "my-cache", key, "v")
	stRun(t, "", "kv", "key", "delete", "my-cache", key, "--yes")
	stRun(t, "", "kv", "key", "get", "my-cache", key)
	seen := map[string]bool{}
	for _, r := range f.Requests() {
		if strings.Contains(r.EscapedPath, "/values/") {
			if !strings.HasSuffix(r.EscapedPath, "/values/"+want) {
				t.Errorf("%s key escaped as %s", r.Method, r.EscapedPath)
			}
			if r.Path != "/accounts/"+acctID+"/storage/kv/namespaces/"+kvNS+"/values/"+key {
				t.Errorf("%s decoded path = %s", r.Method, r.Path)
			}
			seen[r.Method] = true
		}
	}
	for _, m := range []string{"PUT", "DELETE", "GET"} {
		if !seen[m] {
			t.Errorf("no %s request for the key", m)
		}
	}
}
