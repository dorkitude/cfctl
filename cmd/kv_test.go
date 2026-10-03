package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const kvNS = "11111111111111111111111111111111"

func kvRoutes() map[string]stHandler {
	return map[string]stHandler{
		"GET /storage/kv/namespaces": stListH([]map[string]any{
			{"id": kvNS, "title": "my-cache", "supports_url_encoding": true},
			{"id": "22222222222222222222222222222222", "title": "other", "supports_url_encoding": true},
		}),
		"GET /storage/kv/namespaces/*":    stJSON(map[string]any{"id": kvNS, "title": "my-cache"}),
		"POST /storage/kv/namespaces":     stJSON(map[string]any{"id": "33333333333333333333333333333333", "title": "new-ns"}),
		"PUT /storage/kv/namespaces/*":    stJSON(nil),
		"DELETE /storage/kv/namespaces/*": stJSON(nil),
		"GET /storage/kv/namespaces/*/keys": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			if r.URL.Query().Get("cursor") == "" {
				m := ok([]map[string]any{{"name": "a", "expiration": 1798675200}, {"name": "b", "metadata": map[string]any{"v": 1}}})
				m["result_info"] = map[string]any{"count": 2, "cursor": "c2"}
				writeJSON(w, 200, m)
				return
			}
			m := ok([]map[string]any{{"name": "c"}})
			m["result_info"] = map[string]any{"count": 1, "cursor": ""}
			writeJSON(w, 200, m)
		},
		"GET /storage/kv/namespaces/*/values/*": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("hello world"))
		},
		"GET /storage/kv/namespaces/*/metadata/*":   stJSON(map[string]any{"plan": "pro"}),
		"PUT /storage/kv/namespaces/*/values/*":     stJSON(nil),
		"DELETE /storage/kv/namespaces/*/values/*":  stJSON(nil),
		"PUT /storage/kv/namespaces/*/bulk":         stJSON(map[string]any{"successful_key_count": 2}),
		"POST /storage/kv/namespaces/*/bulk/delete": stJSON(map[string]any{"successful_key_count": 2, "unsuccessful_keys": []string{}}),
		"POST /storage/kv/namespaces/*/bulk/get": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			vals := map[string]any{}
			for _, k := range req.Body["keys"].([]any) {
				vals[k.(string)] = "v-" + k.(string)
			}
			writeJSON(w, 200, ok(map[string]any{"values": vals}))
		},
	}
}

func TestKVNamespaceListCreateRenameDelete(t *testing.T) {
	f := stFake(t, kvRoutes())
	out, _ := stRun(t, "", "kv", "namespace", "list")
	stMust(t, out, "2 KV namespaces", "my-cache", kvNS)

	out, _ = stRun(t, "", "kv", "namespace", "list", "--json")
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 2 {
		t.Fatalf("--json: %v %s", err, out)
	}

	out, _ = stRun(t, "", "kv", "namespace", "create", "new-ns", "--jurisdiction", "eu")
	stMust(t, out, "Created KV namespace new-ns", "NEW_NS")
	r := stFind(t, f, "POST", "/storage/kv/namespaces")
	if r.Body["title"] != "new-ns" || r.Body["jurisdiction"] != "eu" {
		t.Errorf("create body = %v", r.Body)
	}

	stRun(t, "", "kv", "namespace", "rename", "my-cache", "renamed")
	r = stFind(t, f, "PUT", "/storage/kv/namespaces/"+kvNS)
	if r.Body["title"] != "renamed" {
		t.Errorf("rename body = %v", r.Body)
	}

	if _, err := stRunErr(t, "", "kv", "namespace", "delete", "my-cache"); !strings.Contains(err.Error(), "--yes") {
		t.Errorf("delete without --yes: %v", err)
	}
	stRun(t, "", "kv", "namespace", "delete", "my-cache", "--yes")
	stFind(t, f, "DELETE", "/storage/kv/namespaces/"+kvNS)

	if _, err := stRunErr(t, "", "kv", "namespace", "get", "nope"); !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown namespace: %v", err)
	}
}

func TestKVKeys(t *testing.T) {
	f := stFake(t, kvRoutes())
	out, _ := stRun(t, "", "kv", "key", "list", "my-cache")
	stMust(t, out, "3 keys", "a", "b", "c", "2026-12-31")

	out, _ = stRun(t, "", "kv", "key", "list", "my-cache", "--limit", "1", "--json")
	var keys []map[string]any
	_ = json.Unmarshal([]byte(out), &keys)
	if len(keys) != 1 {
		t.Errorf("--limit 1 returned %d keys", len(keys))
	}

	out, _ = stRun(t, "", "kv", "key", "get", "my-cache", "some/key")
	if out != "hello world" {
		t.Errorf("get = %q", out)
	}
	r := stFind(t, f, "GET", "/storage/kv/namespaces/"+kvNS+"/values/some/key")
	if !strings.Contains(r.EscapedPath, "some%2Fkey") {
		t.Errorf("key not escaped: %s", r.EscapedPath)
	}
	dst := filepath.Join(t.TempDir(), "v.bin")
	stRun(t, "", "kv", "key", "get", "my-cache", "k", "-o", dst)
	if b, _ := os.ReadFile(dst); string(b) != "hello world" {
		t.Errorf("file = %q", b)
	}
	out, _ = stRun(t, "", "kv", "key", "get", "my-cache", "k", "--metadata")
	stMust(t, out, `"plan": "pro"`)

	stRun(t, "", "kv", "key", "put", "my-cache", "greeting", "hi", "--ttl", "3600")
	r = stFind(t, f, "PUT", "/storage/kv/namespaces/"+kvNS+"/values/greeting")
	if string(r.RawBody) != "hi" || !strings.Contains(r.Query, "expiration_ttl=3600") || r.ContentType != "application/octet-stream" {
		t.Errorf("put: body=%q query=%q ct=%q", r.RawBody, r.Query, r.ContentType)
	}

	stRun(t, "", "kv", "key", "put", "my-cache", "m", "val", "--metadata", `{"v":2}`, "--expiration", "2027-01-01")
	r = stFind(t, f, "PUT", "/storage/kv/namespaces/"+kvNS+"/values/m")
	if !strings.HasPrefix(r.ContentType, "multipart/form-data") || !strings.Contains(string(r.RawBody), `{"v":2}`) || !strings.Contains(r.Query, "expiration=1798761600") {
		t.Errorf("put with metadata: ct=%q query=%q body=%q", r.ContentType, r.Query, r.RawBody)
	}

	stRun(t, "piped value", "kv", "key", "put", "my-cache", "p", "-")
	r = stFind(t, f, "PUT", "/storage/kv/namespaces/"+kvNS+"/values/p")
	if string(r.RawBody) != "piped value" {
		t.Errorf("stdin put body = %q", r.RawBody)
	}

	if _, err := stRunErr(t, "", "kv", "key", "put", "my-cache", "x", "v", "--ttl", "5"); !strings.Contains(err.Error(), "60") {
		t.Errorf("ttl validation: %v", err)
	}
	stRun(t, "", "kv", "key", "delete", "my-cache", "greeting", "-y")
	stFind(t, f, "DELETE", "/storage/kv/namespaces/"+kvNS+"/values/greeting")
}

func TestKVBulk(t *testing.T) {
	f := stFake(t, kvRoutes())
	dir := t.TempDir()
	file := filepath.Join(dir, "data.json")
	_ = os.WriteFile(file, []byte(`[{"key":"a","value":"1"},{"key":"b","value":"2","expiration_ttl":120}]`), 0o644)

	old := kvBulkPutLimit
	kvBulkPutLimit = 1
	defer func() { kvBulkPutLimit = old }()
	out, _ := stRun(t, "", "kv", "bulk", "put", "my-cache", file)
	stMust(t, out, "Wrote 2 keys")
	n := 0
	for _, r := range f.Requests() {
		if r.Method == "PUT" && strings.HasSuffix(r.Path, "/bulk") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("expected 2 batches, got %d", n)
	}

	stRun(t, "", "kv", "bulk", "delete", "my-cache", file, "--yes")
	r := stFind(t, f, "POST", "/storage/kv/namespaces/"+kvNS+"/bulk/delete")
	if string(r.RawBody) != `["a","b"]` {
		t.Errorf("bulk delete body = %s", r.RawBody)
	}

	out, _ = stRun(t, "", "kv", "bulk", "get", "my-cache", "x", "y")
	stMust(t, out, `"x": "v-x"`, `"y": "v-y"`)

	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`[{"key":"a"}]`), 0o644)
	if _, err := stRunErr(t, "", "kv", "bulk", "put", "my-cache", bad); !strings.Contains(err.Error(), "value") {
		t.Errorf("bad bulk file: %v", err)
	}
}

func TestKVReadOnly(t *testing.T) {
	f := stFake(t, kvRoutes())
	_, err := stRunErr(t, "", "--read-only", "kv", "key", "put", "my-cache", "k", "v")
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only put: %v", err)
	}
	_, err = stRunErr(t, "", "--read-only", "kv", "namespace", "delete", "my-cache")
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only delete: %v", err)
	}
	stNoMutation(t, f)
}
