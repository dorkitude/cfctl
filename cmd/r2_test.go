package cmd

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dorkitude/cfctl/internal/r2"
)

// r2FakeStore is an in-memory R2 served over both the REST API (through
// stFake) and the S3 API (its own httptest server).
type r2FakeStore struct {
	mu      sync.Mutex
	objects map[string]map[string][]byte // bucket → key → body
	ctypes  map[string]string            // bucket/key → content type
	parts   map[string]map[int][]byte    // uploadID → part → body
	s3reqs  []string                     // "METHOD /path?query"
	perPage int                          // REST page size cap (to force pagination)
}

func newR2FakeStore() *r2FakeStore {
	return &r2FakeStore{
		objects: map[string]map[string][]byte{
			"bkt": {
				"a.txt":               []byte("hello"),
				"img/logo.png":        []byte("PNGDATA!"),
				"img/icons/x.svg":     []byte("<svg/>"),
				"docs/readme.md":      []byte("# hi"),
				"docs/guide/intro.md": []byte("intro"),
			},
			"empty": {},
		},
		ctypes:  map[string]string{},
		parts:   map[string]map[int][]byte{},
		perPage: 2,
	}
}

func etagOf(b []byte) string {
	s := md5.Sum(b)
	return hex.EncodeToString(s[:])
}

// rest returns stFake routes for buckets and objects.
func (s *r2FakeStore) rest() map[string]stHandler {
	return map[string]stHandler{
		"GET /r2/buckets": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			writeJSON(w, 200, ok(map[string]any{"buckets": []map[string]any{
				{"name": "bkt", "creation_date": "2026-02-28T02:15:23.067Z"},
				{"name": "empty", "creation_date": "2026-02-28T04:23:13.024Z"},
			}}))
		},
		"GET /r2/buckets/*":   stJSON(map[string]any{"name": "bkt", "creation_date": "2026-02-28T02:15:23.067Z", "location": "WNAM", "storage_class": "Standard"}),
		"POST /r2/buckets":    stJSON(map[string]any{"name": "new"}),
		"PATCH /r2/buckets/*": stJSON(nil),
		"DELETE /r2/buckets/*": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			b := strings.Split(req.Path, "/")[5]
			s.mu.Lock()
			n := len(s.objects[b])
			s.mu.Unlock()
			if n > 0 {
				writeJSON(w, 409, fail(10008, "The bucket you tried to delete is not empty."))
				return
			}
			writeJSON(w, 200, ok(nil))
		},
		"GET /r2/buckets/*/objects":    s.restList,
		"DELETE /r2/buckets/*/objects": s.restBulkDelete,
		"GET /r2/buckets/*/objects/*": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			b, k := s.bucketKey(req)
			s.mu.Lock()
			body, found := s.objects[b][k]
			s.mu.Unlock()
			if !found {
				writeJSON(w, 404, fail(10007, "The specified key does not exist."))
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		},
		"PUT /r2/buckets/*/objects/*": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			b, k := s.bucketKey(req)
			s.mu.Lock()
			s.objects[b][k] = req.RawBody
			s.ctypes[b+"/"+k] = req.ContentType
			s.mu.Unlock()
			writeJSON(w, 200, ok(map[string]any{"key": k, "size": len(req.RawBody), "etag": etagOf(req.RawBody)}))
		},
		"DELETE /r2/buckets/*/objects/*": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			b, k := s.bucketKey(req)
			s.mu.Lock()
			delete(s.objects[b], k)
			s.mu.Unlock()
			writeJSON(w, 200, ok(nil))
		},
	}
}

// bucketKey extracts bucket and (unescaped) key from an object path.
func (s *r2FakeStore) bucketKey(req fakeRequest) (string, string) {
	p := strings.TrimPrefix(req.EscapedPath, "/client/v4/accounts/"+acctID+"/r2/buckets/")
	b, rest, _ := strings.Cut(p, "/")
	k, _ := url.PathUnescape(strings.TrimPrefix(rest, "objects/"))
	return b, k
}

func (s *r2FakeStore) restList(w http.ResponseWriter, r *http.Request, req fakeRequest) {
	b, _ := s.bucketKey(req)
	q := r.URL.Query()
	prefix, delim := q.Get("prefix"), q.Get("delimiter")
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per <= 0 || per > s.perPage {
		per = s.perPage
	}
	s.mu.Lock()
	var keys []string
	for k := range s.objects[b] {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	type entry struct {
		key    string
		isPref bool
	}
	var entries []entry
	seen := map[string]bool{}
	for _, k := range keys {
		if delim != "" {
			if i := strings.Index(k[len(prefix):], delim); i >= 0 {
				p := k[:len(prefix)+i+1]
				if !seen[p] {
					seen[p] = true
					entries = append(entries, entry{p, true})
				}
				continue
			}
		}
		entries = append(entries, entry{k, false})
	}
	start, _ := strconv.Atoi(q.Get("cursor"))
	end := min(start+per, len(entries))
	var objs []map[string]any
	var delimited []string
	for _, e := range entries[start:end] {
		if e.isPref {
			delimited = append(delimited, e.key)
			continue
		}
		body := s.objects[b][e.key]
		objs = append(objs, map[string]any{"key": e.key, "size": len(body), "etag": etagOf(body), "last_modified": "2026-03-01T00:00:00.000Z",
			"http_metadata": map[string]any{"contentType": "text/plain"}, "custom_metadata": map[string]any{}, "storage_class": "Standard"})
	}
	s.mu.Unlock()
	if objs == nil {
		objs = []map[string]any{}
	}
	info := map[string]any{"is_truncated": end < len(entries), "per_page": per, "delimited": delimited}
	if end < len(entries) {
		info["cursor"] = strconv.Itoa(end)
	}
	m := ok(objs)
	m["result_info"] = info
	writeJSON(w, 200, m)
}

func (s *r2FakeStore) restBulkDelete(w http.ResponseWriter, r *http.Request, req fakeRequest) {
	b, _ := s.bucketKey(req)
	var keys []string
	_ = json.Unmarshal(req.RawBody, &keys)
	s.mu.Lock()
	for _, k := range keys {
		delete(s.objects[b], k)
	}
	s.mu.Unlock()
	writeJSON(w, 200, ok(nil))
}

// s3 serves a path-style S3 API over the same objects.
func (s *r2FakeStore) s3(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.s3reqs = append(s.s3reqs, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if !strings.Contains(r.Header.Get("Authorization"), "AKTEMP") {
			w.WriteHeader(403)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		b, k, _ := strings.Cut(path, "/")
		q := r.URL.Query()
		switch {
		case r.Method == "POST" && q.Has("delete"):
			var del struct {
				Objects []struct {
					Key string `xml:"Key"`
				} `xml:"Object"`
			}
			_ = xml.Unmarshal(body, &del)
			for _, o := range del.Objects {
				delete(s.objects[b], o.Key)
			}
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><DeleteResult></DeleteResult>`)
		case r.Method == "POST" && q.Has("uploads"):
			id := fmt.Sprintf("up%d", len(s.parts)+1)
			s.parts[id] = map[int][]byte{}
			s.ctypes[b+"/"+k] = r.Header.Get("Content-Type")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, b, k, id)
		case r.Method == "PUT" && q.Has("partNumber"):
			n, _ := strconv.Atoi(q.Get("partNumber"))
			s.parts[q.Get("uploadId")][n] = body
			w.Header().Set("ETag", `"`+etagOf(body)+`"`)
		case r.Method == "POST" && q.Has("uploadId"):
			parts := s.parts[q.Get("uploadId")]
			var all []byte
			for i := 1; i <= len(parts); i++ {
				all = append(all, parts[i]...)
			}
			s.objects[b][k] = all
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><ETag>"x-%d"</ETag></CompleteMultipartUploadResult>`, b, k, len(parts))
		case r.Method == "DELETE" && q.Has("uploadId"):
			delete(s.parts, q.Get("uploadId"))
			w.WriteHeader(204)
		case r.Method == "PUT":
			s.objects[b][k] = body
			s.ctypes[b+"/"+k] = r.Header.Get("Content-Type")
			w.Header().Set("ETag", `"`+etagOf(body)+`"`)
		case r.Method == "GET":
			obj, found := s.objects[b][k]
			if !found {
				w.WriteHeader(404)
				fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(obj)))
			_, _ = w.Write(obj)
		default:
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(r2.S3EndpointEnv, srv.URL)
	return srv
}

// r2Fake starts the API fake with R2 routes plus extra ones.
func r2Fake(t *testing.T, extra map[string]stHandler) (*fakeCF, *r2FakeStore) {
	s := newR2FakeStore()
	routes := s.rest()
	routes["POST /r2/temp-access-credentials"] = stJSON(map[string]any{"accessKeyId": "AKTEMP123", "secretAccessKey": "SECRETTEMP", "sessionToken": "SESSIONTEMP"})
	for k, v := range extra {
		routes[k] = v
	}
	return stFake(t, routes), s
}

func TestR2Buckets(t *testing.T) {
	gql := func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{
			"r2StorageAdaptiveGroups": []any{
				map[string]any{"max": map[string]any{"objectCount": 5, "payloadSize": 1500, "metadataSize": 0, "uploadCount": 0}, "dimensions": map[string]any{"bucketName": "bkt", "storageClass": "Standard", "datetime": "2026-10-03T00:00:00Z"}},
				map[string]any{"max": map[string]any{"objectCount": 4, "payloadSize": 1000, "metadataSize": 0, "uploadCount": 0}, "dimensions": map[string]any{"bucketName": "bkt", "storageClass": "Standard", "datetime": "2026-10-02T00:00:00Z"}},
			}}}}}})
	}
	f, store := r2Fake(t, map[string]stHandler{"POST /graphql": gql})
	out, _ := stRun(t, "", "r2", "buckets", "list")
	stMust(t, out, "2 R2 buckets", "bkt", "empty", "2026-02-28 02:15")
	out, _ = stRun(t, "", "r2", "buckets", "list", "--json")
	stMust(t, out, `"buckets"`, `"name": "bkt"`)

	out, _ = stRun(t, "", "r2", "buckets", "get", "bkt")
	stMust(t, out, "location:", "WNAM", "objects:", "5", "1.5 KB")
	out, _ = stRun(t, "", "r2", "buckets", "info", "bkt", "--json")
	stMust(t, out, `"object_count": 5`, `"payload_bytes": 1500`)

	stRun(t, "", "r2", "buckets", "create", "new", "--location", "weur", "--storage-class", "InfrequentAccess", "--jurisdiction", "eu")
	r := stFind(t, f, "POST", "/r2/buckets")
	if r.Body["name"] != "new" || r.Body["locationHint"] != "weur" || r.Body["storageClass"] != "InfrequentAccess" || r.Header.Get("cf-r2-jurisdiction") != "eu" {
		t.Errorf("create: %v %v", r.Body, r.Header)
	}

	stRun(t, "", "r2", "buckets", "update", "bkt", "--storage-class", "InfrequentAccess")
	r = stFind(t, f, "PATCH", "/r2/buckets/bkt")
	if r.Header.Get("cf-r2-storage-class") != "InfrequentAccess" {
		t.Errorf("update header: %v", r.Header)
	}

	stRun(t, "", "r2", "buckets", "delete", "empty", "--yes")
	if _, err := stRunErr(t, "", "r2", "buckets", "delete", "bkt", "--yes"); !strings.Contains(err.Error(), "--force") {
		t.Errorf("non-empty delete: %v", err)
	}
	if _, err := stRunErr(t, "", "r2", "buckets", "delete", "bkt", "--force"); !strings.Contains(err.Error(), "--yes") {
		t.Errorf("force without --yes: %v", err)
	}
	stRun(t, "", "r2", "buckets", "delete", "bkt", "--force", "--yes")
	if len(store.objects["bkt"]) != 0 {
		t.Errorf("force delete left %d objects", len(store.objects["bkt"]))
	}
}

func TestR2LsStatGet(t *testing.T) {
	f, _ := r2Fake(t, nil)
	out, _ := stRun(t, "", "r2", "ls", "bkt")
	stMust(t, out, "DIR", "docs/", "img/", "a.txt", "2 folders, 1 objects")
	if strings.Contains(out, "logo.png") {
		t.Errorf("folder view listed nested objects:\n%s", out)
	}
	out, _ = stRun(t, "", "r2", "ls", "bkt/img/")
	stMust(t, out, "img/icons/", "img/logo.png")
	out, _ = stRun(t, "", "r2", "ls", "bkt", "docs/", "-r")
	stMust(t, out, "docs/readme.md", "docs/guide/intro.md", "2 objects")
	out, _ = stRun(t, "", "r2", "ls", "bkt", "-r", "--json")
	var lr struct {
		Objects []map[string]any `json:"objects"`
	}
	_ = json.Unmarshal([]byte(out), &lr)
	if len(lr.Objects) != 5 {
		t.Errorf("ls -r --json: %d objects", len(lr.Objects))
	}
	out, _ = stRun(t, "", "r2", "ls", "bkt", "-r", "--limit", "3", "--json")
	_ = json.Unmarshal([]byte(out), &lr)
	if len(lr.Objects) != 3 {
		t.Errorf("--limit 3: %d objects", len(lr.Objects))
	}

	out, _ = stRun(t, "", "r2", "stat", "bkt/img/logo.png")
	stMust(t, out, "8 B (8 bytes)", etagOf([]byte("PNGDATA!")), "contentType:")
	if _, err := stRunErr(t, "", "r2", "stat", "bkt/img/logo"); !strings.Contains(err.Error(), "no object") {
		t.Errorf("stat missing: %v", err)
	}

	out, _ = stRun(t, "", "r2", "get", "bkt/img/logo.png")
	if out != "PNGDATA!" {
		t.Errorf("get stdout = %q", out)
	}
	r := stFind(t, f, "GET", "/r2/buckets/bkt/objects/img/logo.png")
	if !strings.Contains(r.EscapedPath, "img%2Flogo.png") {
		t.Errorf("key not escaped: %s", r.EscapedPath)
	}
	dst := filepath.Join(t.TempDir(), "sub", "logo.png")
	stRun(t, "", "r2", "get", "bkt/img/logo.png", "--file", dst)
	if b, _ := os.ReadFile(dst); string(b) != "PNGDATA!" {
		t.Errorf("get --file wrote %q", b)
	}
	if _, err := stRunErr(t, "", "r2", "get", "bkt/nope"); !strings.Contains(err.Error(), "404") {
		t.Errorf("get missing: %v", err)
	}
	// wrangler-style alias
	out, _ = stRun(t, "", "r2", "object", "get", "bkt/a.txt", "--pipe")
	if out != "hello" {
		t.Errorf("object get = %q", out)
	}
}

func TestR2PutRm(t *testing.T) {
	f, store := r2Fake(t, nil)
	dir := t.TempDir()
	html := filepath.Join(dir, "page.html")
	_ = os.WriteFile(html, []byte("<html></html>"), 0o644)
	stRun(t, "", "r2", "put", "bkt/site/", "--file", html)
	if string(store.objects["bkt"]["site/page.html"]) != "<html></html>" || !strings.HasPrefix(store.ctypes["bkt/site/page.html"], "text/html") {
		t.Errorf("put: %q %q", store.objects["bkt"]["site/page.html"], store.ctypes["bkt/site/page.html"])
	}
	// Sniffed content type for an extensionless file; stdin input.
	stRun(t, "%PDF-1.4 fake", "r2", "put", "bkt/docfile")
	if store.ctypes["bkt/docfile"] != "application/pdf" {
		t.Errorf("sniffed content type = %q", store.ctypes["bkt/docfile"])
	}
	stRun(t, "", "r2", "put", "bkt/x.bin", "--file", html, "--content-type", "application/x-custom", "--storage-class", "InfrequentAccess")
	r := stFind(t, f, "PUT", "/r2/buckets/bkt/objects/x.bin")
	if r.ContentType != "application/x-custom" || r.Header.Get("cf-r2-storage-class") != "InfrequentAccess" {
		t.Errorf("put headers: %v", r.Header)
	}
	if _, err := stRunErr(t, "", "r2", "put", "bkt/d", "--file", dir); !strings.Contains(err.Error(), "directory") {
		t.Errorf("put dir: %v", err)
	}

	if _, err := stRunErr(t, "", "r2", "rm", "bkt/a.txt"); !strings.Contains(err.Error(), "--yes") {
		t.Errorf("rm without --yes: %v", err)
	}
	stRun(t, "", "r2", "rm", "bkt/a.txt", "-y")
	if _, found := store.objects["bkt"]["a.txt"]; found {
		t.Error("rm didn't delete a.txt")
	}
	out, _ := stRun(t, "", "r2", "rm", "bkt/docs/", "-r", "--dry-run")
	stMust(t, out, "would delete bkt/docs/readme.md", "would delete bkt/docs/guide/intro.md", "2 objects (dry run)")
	if len(store.objects["bkt"]) == 0 || store.objects["bkt"]["docs/readme.md"] == nil {
		t.Error("dry run deleted objects")
	}
	stRun(t, "", "r2", "rm", "bkt/docs/", "-r", "--yes")
	if _, found := store.objects["bkt"]["docs/readme.md"]; found {
		t.Error("rm -r left docs/readme.md")
	}
	r = stFind(t, f, "DELETE", "/r2/buckets/bkt/objects")
	if !strings.Contains(string(r.RawBody), "docs/guide/intro.md") {
		t.Errorf("bulk delete body: %s", r.RawBody)
	}
	if _, err := stRunErr(t, "", "r2", "rm", "bkt"); !strings.Contains(err.Error(), "-r") {
		t.Errorf("rm bucket without -r: %v", err)
	}
}

func TestR2Du(t *testing.T) {
	r2Fake(t, nil)
	out, _ := stRun(t, "", "r2", "du", "bkt")
	stMust(t, out, "5 objects, 28 B")
	out, _ = stRun(t, "", "r2", "du", "bkt", "--by", "ext")
	stMust(t, out, ".md", ".png", ".svg", ".txt")
	out, _ = stRun(t, "", "r2", "du", "bkt", "--by", "prefix", "--json")
	var du struct {
		Objects int `json:"objects"`
		Bytes   int `json:"bytes"`
		Groups  []r2DUGroup
	}
	_ = json.Unmarshal([]byte(out), &du)
	got := map[string]int64{}
	for _, g := range du.Groups {
		got[g.Group] = g.Bytes
	}
	if du.Objects != 5 || du.Bytes != 28 || got["img/"] != 14 || got["docs/"] != 9 || got["(files)"] != 5 {
		t.Errorf("du --by prefix: %+v", du)
	}
	out, _ = stRun(t, "", "r2", "du", "bkt", "--by", "ext", "--top", "1", "--json")
	_ = json.Unmarshal([]byte(out), &du)
	if len(du.Groups) != 1 || du.Groups[0].Group != ".md" {
		t.Errorf("--top 1: %+v", du.Groups)
	}
	if _, err := stRunErr(t, "", "r2", "du", "bkt", "--by", "color"); err == nil {
		t.Error("bad --by accepted")
	}
}

func TestR2Usage(t *testing.T) {
	gql := func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		q, _ := req.Body["query"].(string)
		vars, _ := req.Body["variables"].(map[string]any)
		var rows []any
		switch {
		case strings.Contains(q, "r2OperationsAdaptiveGroups") && strings.HasPrefix(vars["start"].(string), "2026-09"):
			rows = []any{
				map[string]any{"sum": map[string]any{"requests": 1_500_000, "responseObjectSize": 0}, "dimensions": map[string]any{"actionType": "PutObject", "bucketName": "bkt", "storageClass": "Standard"}},
				map[string]any{"sum": map[string]any{"requests": 20_000_000, "responseObjectSize": 5e9}, "dimensions": map[string]any{"actionType": "GetObject", "bucketName": "bkt", "storageClass": "Standard"}},
				map[string]any{"sum": map[string]any{"requests": 7, "responseObjectSize": 0}, "dimensions": map[string]any{"actionType": "DeleteObject", "bucketName": "bkt", "storageClass": "Standard"}},
				map[string]any{"sum": map[string]any{"requests": 3, "responseObjectSize": 0}, "dimensions": map[string]any{"actionType": "ListBuckets", "bucketName": "", "storageClass": "Standard"}},
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"r2OperationsAdaptiveGroups": rows}}}}})
		case strings.Contains(q, "r2OperationsAdaptiveGroups"):
			rows = []any{map[string]any{"sum": map[string]any{"requests": 100, "responseObjectSize": 0}, "dimensions": map[string]any{"actionType": "GetObject", "bucketName": "bkt", "storageClass": "Standard"}}}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"r2OperationsAdaptiveGroups": rows}}}}})
		case strings.Contains(q, "date_geq") && vars["start"] == "2026-09-01":
			// 30 days × 20 GB Standard → 20 GB-months.
			for d := 1; d <= 30; d++ {
				rows = append(rows, map[string]any{"max": map[string]any{"payloadSize": 20e9}, "dimensions": map[string]any{"bucketName": "bkt", "storageClass": "Standard", "date": fmt.Sprintf("2026-09-%02d", d)}})
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"r2StorageAdaptiveGroups": rows}}}}})
		case strings.Contains(q, "date_geq"):
			rows = []any{map[string]any{"max": map[string]any{"payloadSize": 20e9}, "dimensions": map[string]any{"bucketName": "bkt", "storageClass": "Standard", "date": "2026-10-01"}}}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"r2StorageAdaptiveGroups": rows}}}}})
		default:
			rows = []any{map[string]any{"max": map[string]any{"objectCount": 1000, "payloadSize": 20e9, "metadataSize": 0, "uploadCount": 0}, "dimensions": map[string]any{"bucketName": "bkt", "storageClass": "Standard", "datetime": "2026-10-03T00:00:00Z"}}}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"r2StorageAdaptiveGroups": rows}}}}})
		}
	}
	f, _ := r2Fake(t, map[string]stHandler{"POST /graphql": gql})
	out, _ := stRun(t, "", "r2", "usage", "--now", "2026-10-03T12:00:00Z")
	stMust(t, out, "bkt", "1,000", "20.0 GB", "1,500,003", "20,000,000", "(account)", "September 2026", "$")
	out, _ = stRun(t, "", "r2", "usage", "--now", "2026-10-03T12:00:00Z", "--json")
	var u struct {
		Estimates []r2PeriodCost `json:"estimates"`
		Buckets   []r2BucketUsage
	}
	if err := json.Unmarshal([]byte(out), &u); err != nil {
		t.Fatal(err)
	}
	// September: 20 GB-mo → 10 billable × 0.015 = 0.15; A 1.5M+3 → 1M billable (rounded up from 0.5M) × 4.50;
	// B 20M → 10M × 0.36 = 3.60. DeleteObject is free.
	sep := u.Estimates[0]
	if sep.Label != "September 2026" || !near2(sep.Costs["Standard"].Storage, 0.15) || !near2(sep.Costs["Standard"].ClassA, 4.5) || !near2(sep.Costs["Standard"].ClassB, 3.6) || !near2(sep.Total, 8.25) {
		t.Errorf("September estimate: %+v", sep)
	}
	for _, b := range u.Buckets {
		if b.Bucket == "bkt" && (b.ClassALast != 1_500_000 || b.ClassBLast != 20_000_000 || b.FreeOpsLast != 7 || b.ClassBThis != 100) {
			t.Errorf("bucket row: %+v", b)
		}
	}
	stNoMutation(t, f)
}

func near2(a, b float64) bool { return a-b < 1e-6 && b-a < 1e-6 }

func TestR2TempCredentials(t *testing.T) {
	f, _ := r2Fake(t, nil)
	out, _ := stRun(t, "", "r2", "temp-credentials", "bkt", "--permission", "object-read-write", "--ttl", "15m", "--prefix", "uploads/")
	stMust(t, out, "AKTEMP123", "SECRETTEMP", "SESSIONTEMP", "r2.cloudflarestorage.com")
	r := stFind(t, f, "POST", "/r2/temp-access-credentials")
	if r.Body["bucket"] != "bkt" || r.Body["parentAccessKeyId"] != "tok-id-123" || r.Body["permission"] != "object-read-write" || r.Body["ttlSeconds"] != float64(900) {
		t.Errorf("temp creds body: %v", r.Body)
	}
	out, _ = stRun(t, "", "r2", "temp-credentials", "bkt", "--env", "--parent-access-key-id", "parent123")
	stMust(t, out, "export AWS_ACCESS_KEY_ID=AKTEMP123", "AWS_SESSION_TOKEN=SESSIONTEMP")
	if _, err := stRunErr(t, "", "--read-only", "r2", "temp-credentials", "bkt"); !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only temp creds: %v", err)
	}
}

func TestR2PutMultipartViaS3(t *testing.T) {
	_, store := r2Fake(t, nil)
	store.s3(t)
	big := make([]byte, 12<<20+123) // 12 MiB + change → 3 parts at 5 MiB
	for i := range big {
		big[i] = byte(i % 251)
	}
	file := filepath.Join(t.TempDir(), "big.bin")
	_ = os.WriteFile(file, big, 0o644)
	out, _ := stRun(t, "", "r2", "put", "bkt/big.bin", "--file", file, "--multipart-threshold", "1", "--part-size", "5", "--concurrency", "2")
	stMust(t, out, "via S3")
	if got := store.objects["bkt"]["big.bin"]; len(got) != len(big) || etagOf(got) != etagOf(big) {
		t.Errorf("multipart upload mismatch: %d bytes", len(got))
	}
	parts := 0
	for _, r := range store.s3reqs {
		if strings.HasPrefix(r, "PUT /bkt/big.bin?") && strings.Contains(r, "partNumber=") {
			parts++
		}
	}
	if parts != 3 {
		t.Errorf("expected 3 parts, got %d: %v", parts, store.s3reqs)
	}
	// Metadata the REST API can't carry forces the S3 path even for small files.
	small := filepath.Join(t.TempDir(), "s.css")
	_ = os.WriteFile(small, []byte("body{}"), 0o644)
	stRun(t, "", "r2", "put", "bkt/s.css", "--file", small, "--cache-control", "max-age=60")
	if string(store.objects["bkt"]["s.css"]) != "body{}" || store.ctypes["bkt/s.css"] != "text/css; charset=utf-8" {
		t.Errorf("s3 small put: %q %q", store.objects["bkt"]["s.css"], store.ctypes["bkt/s.css"])
	}
}

func TestR2Sync(t *testing.T) {
	f, store := r2Fake(t, nil)
	store.s3(t)
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
	write("a.txt", "hello")    // same as bkt/a.txt → unchanged
	write("img/logo.png", "X") // changed
	write("new/file.js", "js") // new
	write("skip.map", "map")   // excluded

	// Dry run works read-only (REST listing only, nothing minted).
	out, _ := stRun(t, "", "--read-only", "r2", "sync", dir, "bkt", "--dry-run", "--exclude", "*.map")
	stMust(t, out, "would upload", "new/file.js (new", "img/logo.png (changed", "2 to upload", "1 unchanged")
	stNoMutation(t, f)

	stRun(t, "", "r2", "sync", dir, "bkt", "--exclude", "*.map")
	if string(store.objects["bkt"]["img/logo.png"]) != "X" || string(store.objects["bkt"]["new/file.js"]) != "js" {
		t.Errorf("sync upload: %v", store.objects["bkt"])
	}
	if _, found := store.objects["bkt"]["skip.map"]; found {
		t.Error("excluded file uploaded")
	}
	if store.ctypes["bkt/new/file.js"] == "" {
		t.Error("sync didn't set a content type")
	}
	r := stFind(t, f, "POST", "/r2/temp-access-credentials")
	if r.Body["permission"] != "object-read-write" {
		t.Errorf("sync minted %v", r.Body)
	}

	// --delete needs --yes when not on a terminal.
	if _, err := stRunErr(t, "", "r2", "sync", dir, "bkt", "--delete", "--exclude", "*.map"); !strings.Contains(err.Error(), "--yes") {
		t.Errorf("sync --delete without --yes: %v", err)
	}
	stRun(t, "", "r2", "sync", dir, "bkt", "--delete", "--exclude", "*.map", "--yes")
	if _, found := store.objects["bkt"]["docs/readme.md"]; found {
		t.Error("sync --delete kept docs/readme.md")
	}
	out, _ = stRun(t, "", "r2", "sync", dir, "bkt", "--exclude", "*.map")
	stMust(t, out, "Already in sync")

	// Download direction.
	down := t.TempDir()
	stRun(t, "", "r2", "sync", "bkt/img/", down)
	if b, _ := os.ReadFile(filepath.Join(down, "logo.png")); string(b) != "X" {
		t.Errorf("sync download: %q", b)
	}
	all := t.TempDir()
	stRun(t, "", "r2", "sync", "bkt", all)
	if b, _ := os.ReadFile(filepath.Join(all, "new", "file.js")); string(b) != "js" {
		t.Errorf("sync download nested: %q", b)
	}
	if _, err := os.Stat(filepath.Join(all, "img", "icons", "x.svg")); err == nil {
		t.Error("x.svg should have been removed by the earlier sync --delete")
	}
}

func TestR2ReadOnly(t *testing.T) {
	f, _ := r2Fake(t, nil)
	file := filepath.Join(t.TempDir(), "x.txt")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	for _, args := range [][]string{
		{"r2", "put", "bkt/x.txt", "--file", file},
		{"r2", "rm", "bkt/a.txt"},
		{"r2", "rm", "bkt/docs/", "-r"},
		{"r2", "buckets", "create", "nb"},
		{"r2", "buckets", "delete", "empty"},
		{"r2", "buckets", "cors", "delete", "bkt"},
	} {
		_, err := stRunErr(t, "", append([]string{"--read-only"}, args...)...)
		if !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%v: %v", args, err)
		}
	}
	stNoMutation(t, f)
}
