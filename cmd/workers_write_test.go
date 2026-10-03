package cmd

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for Workers write paths checked against the API spec and
// wrangler (see docs/write-paths.md).

// versions upload must not send per-script settings, and needs ES modules.
func TestVersionsUploadOmitsScriptSettings(t *testing.T) {
	f := newWorkersFake(t)
	dir := writeProject(t, map[string]string{
		"wrangler.toml": `name = "my-worker"
main = "index.js"
compatibility_date = "2025-01-01"
logpush = true
[observability]
enabled = true
[placement]
mode = "smart"
`,
		"index.js": "export default {}",
		"sw.js":    "addEventListener('fetch', e => e.respondWith(new Response('hi')))",
	})
	t.Chdir(dir)
	wkRun(t, "", "versions", "upload")
	up := f.find("POST", wkScriptURL+"/versions")
	if up == nil {
		t.Fatal("no version upload")
	}
	var meta map[string]any
	json.Unmarshal([]byte(wkParts(t, *up)["metadata"][1]), &meta)
	if _, ok := meta["logpush"]; ok {
		t.Errorf("versions upload sent logpush: %v", meta)
	}
	if _, ok := meta["observability"]; ok {
		t.Errorf("versions upload sent observability: %v", meta)
	}
	if meta["placement"] == nil || meta["main_module"] != "index.js" {
		t.Errorf("versions upload metadata: %v", meta)
	}
	// deploy keeps them (PUT script takes both).
	wkRun(t, "", "deploy", "--no-triggers")
	put := f.find("PUT", wkScriptURL)
	json.Unmarshal([]byte(wkParts(t, *put)["metadata"][1]), &meta)
	if meta["logpush"] != true || meta["observability"] == nil {
		t.Errorf("deploy metadata: %v", meta)
	}
	if msg := wkRunErr(t, "", "versions", "upload", "sw.js"); !strings.Contains(msg, "ES module") {
		t.Errorf("service-worker versions upload: %s", msg)
	}
}

// Config bindings are translated to the API's shapes.
func TestDeployBindingShapes(t *testing.T) {
	newWorkersFake(t)
	dir := writeProject(t, map[string]string{
		"wrangler.toml": `name = "my-worker"
main = "index.js"
compatibility_date = "2025-01-01"
[[dispatch_namespaces]]
binding = "DISPATCHER"
namespace = "customers"
outbound = { service = "outbound-worker", environment = "production", parameters = ["customer", "plan"] }
[[queues.producers]]
binding = "JOBS"
queue = "jobs"
delivery_delay = 30
[[services]]
binding = "AUTH"
service = "auth"
props = { tier = "gold" }
[[pipelines]]
binding = "EVENTS"
stream = "stream-1"
`,
		"index.js": "export default {}",
	})
	t.Chdir(dir)
	out, _ := wkRun(t, "", "deploy", "--dry-run")
	got := decode(t, out).(map[string]any)
	byName := map[string]map[string]any{}
	for _, b := range got["metadata"].(map[string]any)["bindings"].([]any) {
		bm := b.(map[string]any)
		byName[bm["name"].(string)] = bm
	}
	ob, _ := json.Marshal(byName["DISPATCHER"]["outbound"])
	if string(ob) != `{"params":[{"name":"customer"},{"name":"plan"}],"worker":{"environment":"production","service":"outbound-worker"}}` {
		t.Errorf("dispatch outbound: %s", ob)
	}
	if byName["JOBS"]["delivery_delay"] != float64(30) || byName["JOBS"]["queue_name"] != "jobs" {
		t.Errorf("queue binding: %v", byName["JOBS"])
	}
	if p, _ := byName["AUTH"]["props"].(map[string]any); p["tier"] != "gold" {
		t.Errorf("service props: %v", byName["AUTH"])
	}
	if byName["EVENTS"]["stream"] != "stream-1" || byName["EVENTS"]["type"] != "pipelines" {
		t.Errorf("pipelines binding: %v", byName["EVENTS"])
	}
}

// preview_urls in the config is sent as previews_enabled.
func TestDeployPreviewURLs(t *testing.T) {
	f := newWorkersFake(t)
	dir := writeProject(t, map[string]string{
		"wrangler.toml": `name = "my-worker"
main = "index.js"
compatibility_date = "2025-01-01"
workers_dev = true
preview_urls = true
`,
		"index.js": "export default {}",
	})
	t.Chdir(dir)
	wkRun(t, "", "deploy")
	r := f.find("POST", wkScriptURL+"/subdomain")
	if r == nil || r.Body["enabled"] != true || r.Body["previews_enabled"] != true {
		t.Fatalf("subdomain body: %+v", r)
	}
}

// Assets with unknown extensions are uploaded as application/null (like
// wrangler), and a session JWT with wrangler_single_asset_uploads switches
// to one raw request per file.
func TestAssetsUploadContentTypeAndSingleMode(t *testing.T) {
	f := newWorkersFake(t)
	dir := writeProject(t, map[string]string{
		"index.js":            "export default {}",
		"public/blob.zzunk":   "raw-bytes",
		"public/index.html":   "<p>hi</p>",
		"public/sub/data.css": "a{}",
	})
	t.Chdir(dir)
	wkRun(t, "", "deploy", "index.js", "--name", "my-worker", "--compatibility-date", "2025-01-01", "--assets", "public", "--no-triggers")
	if len(f.assetUploads) != 1 {
		t.Fatalf("bulk uploads: %d", len(f.assetUploads))
	}
	cts := map[string]bool{}
	for _, p := range wkParts(t, f.assetUploads[0]) {
		cts[p[0]] = true
	}
	if !cts["application/null"] || !cts["text/html; charset=utf-8"] {
		t.Errorf("asset part content types: %v", cts)
	}

	// Single-asset mode.
	claims, _ := json.Marshal(map[string]any{"wrangler_single_asset_uploads": true})
	single := "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	var singles []fakeRequest
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/workers/assets/upload/") {
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			singles = append(singles, fakeRequest{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/client/v4"), Auth: r.Header.Get("Authorization"), ContentType: r.Header.Get("Content-Type"), RawBody: b})
			f.mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer "+single {
				writeJSON(w, 401, fail(10000, "bad upload jwt"))
				return
			}
			writeJSON(w, 201, ok(map[string]any{"jwt": wkDone}))
			return
		}
		inner.ServeHTTP(w, r)
	})
	prev := f.custom
	f.custom = func(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
		if req.Method == "POST" && req.Path == wkScriptURL+"/assets-upload-session" {
			var body struct {
				Manifest map[string]struct{ Hash string } `json:"manifest"`
			}
			_ = json.Unmarshal(req.RawBody, &body)
			var hs []string
			for _, e := range body.Manifest {
				hs = append(hs, e.Hash)
			}
			writeJSON(w, 200, ok(map[string]any{"jwt": single, "buckets": [][]string{hs}}))
			return true
		}
		return prev(w, r, req)
	}
	wkRun(t, "", "deploy", "index.js", "--name", "my-worker", "--compatibility-date", "2025-01-01", "--assets", "public", "--no-triggers")
	if len(singles) != 3 {
		t.Fatalf("single uploads: %d", len(singles))
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "public", "blob.zzunk"))
	found := false
	for _, s := range singles {
		if s.Method != "POST" || !strings.HasPrefix(s.Path, "/accounts/"+acctID+"/workers/assets/upload/") {
			t.Errorf("single upload request: %s %s", s.Method, s.Path)
		}
		if string(s.RawBody) == string(raw) {
			found = true
			if s.ContentType != "application/null" {
				t.Errorf("single upload content type: %q", s.ContentType)
			}
		}
	}
	if !found {
		t.Error("raw file body not sent in single mode")
	}
	var meta map[string]any
	reqs := f.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == "PUT" && reqs[i].Path == wkScriptURL {
			json.Unmarshal([]byte(wkParts(t, reqs[i])["metadata"][1]), &meta)
			break
		}
	}
	if a, _ := meta["assets"].(map[string]any); a["jwt"] != wkDone {
		t.Errorf("assets jwt after single mode: %v", meta["assets"])
	}
}
