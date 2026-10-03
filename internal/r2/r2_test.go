package r2

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dorkitude/cfctl/internal/api"
)

func TestOpClass(t *testing.T) {
	cases := map[string]string{
		"PutObject": ClassA, "CopyObject": ClassA, "CreateMultipartUpload": ClassA, "UploadPart": ClassA,
		"CompleteMultipartUpload": ClassA, "ListObjects": ClassA, "ListObjectsV2": ClassA, "ListBuckets": ClassA,
		"ListMultipartUploads": ClassA, "PutBucketCors": ClassA, "PutBucketLifecycleConfiguration": ClassA,
		"LifecycleStorageTierTransition": ClassA,
		"GetObject":                      ClassB, "HeadObject": ClassB, "HeadBucket": ClassB, "GetBucketCors": ClassB,
		"DeleteObject": ClassFree, "DeleteBucket": ClassFree, "AbortMultipartUpload": ClassFree,
	}
	for action, want := range cases {
		if got := OpClass(action); got != want {
			t.Errorf("OpClass(%s) = %s, want %s", action, got, want)
		}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEstimate(t *testing.T) {
	// Within the free tier: nothing to pay.
	if c := Estimate(Standard, Usage{StorageBytes: 9 * GB, ClassA: 999_999, ClassB: 9_999_999}); c.Total != 0 {
		t.Errorf("free tier: %+v", c)
	}
	// 10.2 GB → 0.2 over → rounds up to 1 GB-month; 1 request over A → 1M; 10M+1 B → 1M.
	c := Estimate(Standard, Usage{StorageBytes: 10.2 * GB, ClassA: 1_000_001, ClassB: 10_000_001})
	if !near(c.Storage, 0.015) || !near(c.ClassA, 4.50) || !near(c.ClassB, 0.36) || !near(c.Total, 4.875) {
		t.Errorf("rounding: %+v", c)
	}
	// 100 GB Standard, 3.5M A, 25M B: 90 GB × 0.015, 3M A (2.5 → 3) × 4.50, 15M B × 0.36.
	c = Estimate(Standard, Usage{StorageBytes: 100 * GB, ClassA: 3_500_000, ClassB: 25_000_000})
	if !near(c.Storage, 1.35) || !near(c.ClassA, 13.5) || !near(c.ClassB, 5.4) {
		t.Errorf("standard: %+v", c)
	}
	// Infrequent Access: no free tier; retrieval billed.
	c = Estimate(InfrequentAccess, Usage{StorageBytes: 1.5 * GB, ClassA: 1, ClassB: 1, RetrievedBytes: 0.1 * GB})
	if !near(c.Storage, 0.02) || !near(c.ClassA, 9.0) || !near(c.ClassB, 0.9) || !near(c.Retrieval, 0.01) {
		t.Errorf("IA: %+v", c)
	}
	if c := Estimate(InfrequentAccess, Usage{}); c.Total != 0 {
		t.Errorf("IA zero: %+v", c)
	}
}

func TestFormatting(t *testing.T) {
	if got := HumanBytes(733493037); got != "733.5 MB" {
		t.Errorf("HumanBytes = %q", got)
	}
	if got := HumanBytes(999); got != "999 B" {
		t.Errorf("HumanBytes = %q", got)
	}
	if got := HumanCount(1234567); got != "1,234,567" {
		t.Errorf("HumanCount = %q", got)
	}
	if got := HumanCount(12); got != "12" {
		t.Errorf("HumanCount = %q", got)
	}
	if Ext("a/b/c.PNG") != ".png" || Ext("a/b/Makefile") != "(none)" || Ext("a/.DS_Store") != ".ds_store" {
		t.Errorf("Ext: %q %q %q", Ext("a/b/c.PNG"), Ext("a/b/Makefile"), Ext("a/.DS_Store"))
	}
	cases := []struct {
		key, base string
		depth     int
		want      string
	}{
		{"assets/audio/x.mp3", "", 1, "assets/"},
		{"assets/audio/x.mp3", "", 2, "assets/audio/"},
		{"assets/x.mp3", "", 2, "assets/ (files)"},
		{"x.mp3", "", 1, "(files)"},
		{"assets/audio/x.mp3", "assets/", 1, "assets/audio/"},
	}
	for _, c := range cases {
		if got := PrefixAt(c.key, c.base, c.depth); got != c.want {
			t.Errorf("PrefixAt(%q,%q,%d) = %q, want %q", c.key, c.base, c.depth, got, c.want)
		}
	}
}

func TestListPaginatesAndCollectsPrefixes(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch q.Get("cursor") {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{{"key": "a.txt", "size": 1}},
				"result_info": map[string]any{"cursor": "c1", "is_truncated": true, "delimited": []string{"d1/"}}})
		case "c1":
			// A page with only prefixes (empty result) must not stop the loop.
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{},
				"result_info": map[string]any{"cursor": "c2", "is_truncated": true, "delimited": []string{"d2/"}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{{"key": "b.txt", "size": 2}},
				"result_info": map[string]any{"is_truncated": false}})
		}
	}))
	defer srv.Close()
	c := api.New("t", srv.URL)
	res, err := List(context.Background(), c, "acct", "bkt", ListOptions{Delimiter: "/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(res.Objects) != 2 || strings.Join(res.Prefixes, ",") != "d1/,d2/" {
		t.Errorf("calls=%d objects=%v prefixes=%v", calls, res.Objects, res.Prefixes)
	}
	// Limit stops early.
	calls = 0
	res, err = List(context.Background(), c, "acct", "bkt", ListOptions{Limit: 1}, nil)
	if err != nil || len(res.Objects) != 1 || calls != 1 || !res.Truncated {
		t.Errorf("limit: %v %+v calls=%d", err, res, calls)
	}
}

func TestSyncPlans(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
	write("index.html", "hello")
	write("css/site.css", "body{}")
	write("same.txt", "same")
	write("skip.map", "x")
	write("node_modules/x.js", "x")
	local, err := WalkLocal(dir, []string{"*.map", "node_modules/"})
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, l := range local {
		rels = append(rels, l.Rel)
	}
	if strings.Join(rels, ",") != "css/site.css,index.html,same.txt" {
		t.Fatalf("WalkLocal = %v", rels)
	}
	sameMD5, _ := FileMD5(filepath.Join(dir, "same.txt"))
	remote := []Object{
		{Key: "site/same.txt", Size: 4, ETag: sameMD5},
		{Key: "site/index.html", Size: 5, ETag: "deadbeefdeadbeefdeadbeefdeadbeef"}, // same size, different content
		{Key: "site/old.txt", Size: 3},
	}
	plan := PlanUpload(local, remote, "site/", false, true, nil)
	got := map[string]string{}
	for _, it := range plan.Transfers {
		got[it.Key] = it.Reason
	}
	if got["site/css/site.css"] != "new" || got["site/index.html"] != "changed" || len(got) != 2 || plan.Unchanged != 1 {
		t.Errorf("upload plan: %+v", plan)
	}
	if strings.Join(plan.Deletes, ",") != "site/old.txt" {
		t.Errorf("deletes: %v", plan.Deletes)
	}
	// --size-only treats the same-size index.html as unchanged.
	if p := PlanUpload(local, remote, "site/", true, false, nil); len(p.Transfers) != 1 || p.Deletes != nil {
		t.Errorf("size-only plan: %+v", p)
	}
	// Multipart ETags can't be compared: size decides.
	if p := PlanUpload(local[2:], []Object{{Key: "site/same.txt", Size: 4, ETag: "abc-2"}}, "site/", false, false, nil); len(p.Transfers) != 0 {
		t.Errorf("multipart etag plan: %+v", p)
	}

	dl := PlanDownload([]Object{{Key: "site/a/b.txt", Size: 1}, {Key: "site/dir/", Size: 0}, {Key: "site/../evil", Size: 1}, {Key: "site/same.txt", Size: 4, ETag: sameMD5}},
		local, "site/", dir, false, true, nil)
	if len(dl.Transfers) != 1 || dl.Transfers[0].Path != filepath.Join(dir, "a", "b.txt") || dl.Unchanged != 1 || len(dl.Deletes) != 2 {
		t.Errorf("download plan: %+v", dl)
	}
}

func TestEndpoint(t *testing.T) {
	t.Setenv(S3EndpointEnv, "")
	if got := S3Endpoint("abc", ""); got != "https://abc.r2.cloudflarestorage.com" {
		t.Error(got)
	}
	if got := S3Endpoint("abc", "eu"); got != "https://abc.eu.r2.cloudflarestorage.com" {
		t.Error(got)
	}
	t.Setenv(S3EndpointEnv, "http://127.0.0.1:9/")
	if got := S3Endpoint("abc", ""); got != "http://127.0.0.1:9" {
		t.Error(got)
	}
}
