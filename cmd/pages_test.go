package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const pagesP = platA + "/pages/projects"

func pagesFixtures(p *platFakeT) {
	project := map[string]any{
		"name": "site", "id": "p1", "subdomain": "site.pages.dev", "domains": []any{"site.pages.dev", "www.example.com"},
		"production_branch": "main", "source": map[string]any{"type": "github"}, "created_on": "2026-01-02T03:04:05Z",
		"build_config":      map[string]any{"destination_dir": "dist"},
		"latest_deployment": map[string]any{"id": "d1", "url": "https://d1.site.pages.dev", "created_on": "2026-02-01T00:00:00Z"},
		"deployment_configs": map[string]any{
			"production": map[string]any{
				"compatibility_date": "2026-01-01", "compatibility_flags": []any{"nodejs_compat"},
				"env_vars": map[string]any{
					"API_KEY": map[string]any{"type": "secret_text"},
					"MODE":    map[string]any{"type": "plain_text", "value": "prod"},
				},
				"kv_namespaces": map[string]any{"CACHE": map[string]any{"namespace_id": "kv1"}},
				"d1_databases":  map[string]any{"DB": map[string]any{"id": "db1"}},
			},
			"preview": map[string]any{"compatibility_date": "2026-01-01"},
		},
	}
	p.routes["GET "+pagesP] = []any{project}
	p.routes["GET "+pagesP+"/site"] = func(fakeRequest) (int, any) { return 200, ok(project) }
	dep := map[string]any{"id": "d1", "project_name": "site", "environment": "production", "url": "https://d1.site.pages.dev",
		"deployment_trigger": map[string]any{"type": "ad_hoc", "metadata": map[string]any{"branch": "main", "commit_hash": "abcdef1234567"}},
		"latest_stage":       map[string]any{"name": "deploy", "status": "success"}, "created_on": "2026-02-01T00:00:00Z"}
	p.routes["GET "+pagesP+"/site/deployments"] = []any{dep}
	p.routes["GET "+pagesP+"/site/deployments/d1"] = func(fakeRequest) (int, any) { return 200, ok(dep) }
	p.routes["GET "+pagesP+"/site/deployments/d1/history/logs"] = func(fakeRequest) (int, any) {
		return 200, ok(map[string]any{"total": 1, "data": []any{map[string]any{"line": "Building...", "ts": "2026-02-01T00:00:01Z"}}})
	}
	p.routes["GET "+pagesP+"/site/domains"] = []any{map[string]any{"name": "www.example.com", "status": "active", "certificate_authority": "google", "created_on": "2026-01-01T00:00:00Z"}}
}

func TestPagesCommands(t *testing.T) {
	p := platFake(t)
	pagesFixtures(p)
	platCheck(t, p, []platCase{
		{args: []string{"pages", "project", "list"}, method: "GET", path: pagesP, want: "www.example.com"},
		{args: []string{"pages", "project", "get", "site"}, method: "GET", path: pagesP + "/site", want: "API_KEY = (secret)"},
		{args: []string{"pages", "project", "create", "new-site"}, method: "POST", path: pagesP, body: map[string]any{"name": "new-site", "production_branch": "main"}, want: "Created Pages project new-site"},
		{args: []string{"pages", "project", "create", "s2", "--production-branch", "prod", "--compatibility-date", "2026-05-05"}, method: "POST", path: pagesP,
			body: map[string]any{"production_branch": "prod", "deployment_configs": map[string]any{"production": map[string]any{"compatibility_date": "2026-05-05"}, "preview": map[string]any{"compatibility_date": "2026-05-05"}}}},
		{args: []string{"pages", "project", "edit", "site", "--production-branch", "trunk"}, method: "PATCH", path: pagesP + "/site", body: map[string]any{"production_branch": "trunk"}},
		{args: []string{"pages", "project", "delete", "site", "--yes"}, method: "DELETE", path: pagesP + "/site", want: "Deleted"},
		{args: []string{"pages", "project", "purge-build-cache", "site"}, method: "POST", path: pagesP + "/site/purge_build_cache"},
		{args: []string{"pages", "deployment", "list", "site", "--env", "preview"}, method: "GET", path: pagesP + "/site/deployments", query: "env=preview", want: "abcdef1"},
		{args: []string{"pages", "deployment", "get", "site", "d1"}, method: "GET", path: pagesP + "/site/deployments/d1", want: "https://d1.site.pages.dev"},
		{args: []string{"pages", "deployment", "delete", "site", "d1", "--force", "-y"}, method: "DELETE", path: pagesP + "/site/deployments/d1", query: "force=true"},
		{args: []string{"pages", "deployment", "retry", "site", "d1"}, method: "POST", path: pagesP + "/site/deployments/d1/retry"},
		{args: []string{"pages", "deployment", "rollback", "site", "d1", "--yes"}, method: "POST", path: pagesP + "/site/deployments/d1/rollback"},
		{args: []string{"pages", "deployment", "logs", "site", "d1"}, method: "GET", path: pagesP + "/site/deployments/d1/history/logs", want: "Building..."},
		{args: []string{"pages", "domains", "list", "site"}, method: "GET", path: pagesP + "/site/domains", want: "google"},
		{args: []string{"pages", "domains", "get", "site", "www.example.com"}, method: "GET", path: pagesP + "/site/domains/www.example.com"},
		{args: []string{"pages", "domains", "add", "site", "app.example.com"}, method: "POST", path: pagesP + "/site/domains", body: map[string]any{"name": "app.example.com"}},
		{args: []string{"pages", "domains", "retry", "site", "app.example.com"}, method: "PATCH", path: pagesP + "/site/domains/app.example.com"},
		{args: []string{"pages", "domains", "delete", "site", "app.example.com", "--yes"}, method: "DELETE", path: pagesP + "/site/domains/app.example.com"},
		{args: []string{"pages", "secret", "list", "site"}, method: "GET", path: pagesP + "/site", want: "API_KEY"},
		{args: []string{"pages", "secret", "delete", "site", "API_KEY", "--env", "preview", "--yes"}, method: "PATCH", path: pagesP + "/site",
			body: map[string]any{"deployment_configs": map[string]any{"preview": map[string]any{"env_vars": map[string]any{"API_KEY": nil}}}}},
	})

	// JSON output is the raw API result.
	out := platRunOK(t, "", "pages", "project", "list", "--json")
	if v := decode(t, out).([]any); len(v) != 1 {
		t.Fatalf("--json: %s", out)
	}
	// secret list --json never includes values (MODE is plain text, not listed).
	out = platRunOK(t, "", "pages", "secret", "list", "site", "--json")
	if strings.Contains(out, "MODE") || !strings.Contains(out, "API_KEY") {
		t.Fatalf("secret list: %s", out)
	}
}

func TestPagesSecretPutAndBulk(t *testing.T) {
	p := platFake(t)
	pagesFixtures(p)
	out := platRunOK(t, "s3cr3t-value\n", "pages", "secret", "put", "site", "TOKEN")
	if strings.Contains(out, "s3cr3t-value") {
		t.Fatal("secret value echoed")
	}
	r := p.last(t, "PATCH", pagesP+"/site")
	if !strings.Contains(string(r.RawBody), `"TOKEN":{"type":"secret_text","value":"s3cr3t-value"}`) || !strings.Contains(string(r.RawBody), `"production"`) {
		t.Fatalf("put body: %s", r.RawBody)
	}
	f := filepath.Join(t.TempDir(), "s.json")
	_ = os.WriteFile(f, []byte(`{"A":"1","B":"2"}`), 0o600)
	out = platRunOK(t, "", "pages", "secret", "bulk", "site", f, "--env", "preview")
	if !strings.Contains(out, "A, B") {
		t.Fatalf("bulk: %s", out)
	}
	r = p.last(t, "PATCH", pagesP+"/site")
	if !strings.Contains(string(r.RawBody), `"preview"`) || !strings.Contains(string(r.RawBody), `"B":{"type":"secret_text","value":"2"}`) {
		t.Fatalf("bulk body: %s", r.RawBody)
	}
	if msg := platRunErr(t, `{"A":1}`, "pages", "secret", "bulk", "site"); !strings.Contains(msg, "must be a string") {
		t.Fatal(msg)
	}
}

func TestPagesDownloadConfig(t *testing.T) {
	p := platFake(t)
	pagesFixtures(p)
	dir := t.TempDir()
	dest := filepath.Join(dir, "wrangler.jsonc")
	platRunOK(t, "", "pages", "download", "config", "site", "-o", dest)
	b, _ := os.ReadFile(dest)
	s := string(b)
	for _, want := range []string{`"name": "site"`, `"pages_build_output_dir": "dist"`, `"compatibility_date": "2026-01-01"`, `"MODE": "prod"`, `"binding": "CACHE"`, `"id": "kv1"`, `"database_id": "db1"`, "secrets (not downloaded): API_KEY"} {
		if !strings.Contains(s, want) {
			t.Errorf("config lacks %s:\n%s", want, s)
		}
	}
	if msg := platRunErr(t, "", "pages", "download", "config", "site", "-o", dest); !strings.Contains(msg, "--force") {
		t.Fatal(msg)
	}
}

func TestPagesHashMatchesWrangler(t *testing.T) {
	// blake3-wasm: hash(base64("hello world\n") + "txt").hex.slice(0,32)
	if got := pagesHash([]byte("hello world\n"), "a.txt"); got != "64e86c1f4ec071b997f1a6af69931db3" {
		t.Fatalf("hash = %s", got)
	}
}

// pagesAssetFake serves the JWT-authenticated /pages/assets/* endpoints
// (the stock fake only accepts the API token).
type pagesAssetFake struct {
	mu       sync.Mutex
	auth     []string
	uploaded map[string]string // hash -> contentType
	checked  []string
	upserted int
}

func TestPagesDeploy(t *testing.T) {
	p := platFake(t)
	pagesFixtures(p)
	pagesPollInterval = time.Millisecond
	const jwt = "pages-upload-JWT-SECRET"
	p.routes["GET "+pagesP+"/site/upload-token"] = func(fakeRequest) (int, any) { return 200, ok(map[string]any{"jwt": jwt}) }
	var deployForm *multipart.Form
	p.routes["POST "+pagesP+"/site/deployments"] = func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		_, params, _ := mime.ParseMediaType(req.ContentType)
		mr := multipart.NewReader(bytes.NewReader(req.RawBody), params["boundary"])
		deployForm, _ = mr.ReadForm(10 << 20)
		writeJSON(w, 200, ok(map[string]any{"id": "d9", "url": "https://d9.site.pages.dev", "environment": "preview", "latest_stage": map[string]any{"name": "queued", "status": "idle"}}))
	}
	p.routes["GET "+pagesP+"/site/deployments/d9"] = func(fakeRequest) (int, any) {
		return 200, ok(map[string]any{"id": "d9", "url": "https://d9.site.pages.dev", "environment": "preview", "latest_stage": map[string]any{"name": "deploy", "status": "success"}})
	}
	af := &pagesAssetFake{uploaded: map[string]string{}}
	inner := p.srv.Config.Handler
	p.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/client/v4/pages/assets/") {
			inner.ServeHTTP(w, r)
			return
		}
		af.mu.Lock()
		defer af.mu.Unlock()
		af.auth = append(af.auth, r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/client/v4/pages/assets/check-missing":
			var in struct{ Hashes []string }
			_ = json.Unmarshal(body, &in)
			af.checked = in.Hashes
			// Pretend the first hash is already uploaded.
			writeJSON(w, 200, ok(in.Hashes[1:]))
		case "/client/v4/pages/assets/upload":
			var in []struct {
				Key      string
				Value    string
				Base64   bool
				Metadata struct{ ContentType string }
			}
			_ = json.Unmarshal(body, &in)
			for _, f := range in {
				af.uploaded[f.Key] = f.Metadata.ContentType
			}
			writeJSON(w, 200, ok(nil))
		case "/client/v4/pages/assets/upsert-hashes":
			af.upserted++
			writeJSON(w, 200, ok(nil))
		}
	})

	dir := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(content), 0o644)
	}
	write("index.html", "<h1>hi</h1>")
	write("css/app.css", "body{}")
	write("img/logo.svg", "<svg/>")
	write("_headers", "/*\n  X-Test: 1\n")
	write("_redirects", "/old /new 301\n")
	write("_worker.js", "export default { fetch() { return new Response('w') } }")
	write("_routes.json", `{"version":1,"include":["/api/*"],"exclude":[]}`)
	write("node_modules/x.js", "ignored")
	write(".DS_Store", "ignored")

	out := platRunOK(t, "", "pages", "deploy", dir, "--project-name", "site", "--branch", "feature", "--commit-hash", "abc123", "--commit-message", "msg", "--commit-dirty")
	if strings.Contains(out, jwt) {
		t.Fatal("upload JWT printed")
	}
	if !strings.Contains(out, "https://d9.site.pages.dev") || !strings.Contains(out, "Uploaded 2 files (1 already uploaded)") {
		t.Fatalf("deploy output: %s", out)
	}
	for _, a := range af.auth {
		if a != "Bearer "+jwt {
			t.Fatalf("asset request auth = %q, want the upload JWT", a)
		}
	}
	if len(af.checked) != 3 || len(af.uploaded) != 2 || af.upserted != 1 {
		t.Fatalf("checked %d, uploaded %d, upserted %d", len(af.checked), len(af.uploaded), af.upserted)
	}
	for _, ct := range af.uploaded {
		if ct == "" || strings.Contains(ct, "charset") {
			t.Fatalf("content type %q", ct)
		}
	}
	if deployForm == nil {
		t.Fatal("no deployment created")
	}
	var manifest map[string]string
	_ = json.Unmarshal([]byte(deployForm.Value["manifest"][0]), &manifest)
	if len(manifest) != 3 || manifest["/index.html"] != pagesHash([]byte("<h1>hi</h1>"), "index.html") || manifest["/css/app.css"] == "" {
		t.Fatalf("manifest: %v", manifest)
	}
	for k, want := range map[string]string{"branch": "feature", "commit_hash": "abc123", "commit_message": "msg", "commit_dirty": "true"} {
		if got := deployForm.Value[k]; len(got) != 1 || got[0] != want {
			t.Fatalf("form %s = %v", k, got)
		}
	}
	for _, f := range []string{"_headers", "_redirects", "_worker.bundle", "_routes.json"} {
		if len(deployForm.File[f]) != 1 {
			t.Fatalf("form file %s missing", f)
		}
	}
	fh, _ := deployForm.File["_worker.bundle"][0].Open()
	bundle, _ := io.ReadAll(fh)
	if !bytes.Contains(bundle, []byte(`{"main_module":"_worker.js"}`)) || !bytes.Contains(bundle, []byte("application/javascript+module")) {
		t.Fatalf("worker bundle: %s", bundle)
	}

	// --dry-run lists files without the API.
	n := len(p.Requests())
	out = platRunOK(t, "", "pages", "deploy", dir, "--dry-run")
	if !strings.Contains(out, "3 files") || strings.Contains(out, "node_modules") || len(p.Requests()) != n {
		t.Fatalf("dry run: %s", out)
	}
	if msg := platRunErr(t, "", "pages", "deploy", dir); !strings.Contains(msg, "--project-name") {
		t.Fatal(msg)
	}
	p.routes["GET "+pagesP+"/nope"] = func(fakeRequest) (int, any) { return 404, fail(8000007, "Project not found") }
	if msg := platRunErr(t, "", "pages", "deploy", dir, "--project-name", "nope"); !strings.Contains(msg, "pages project create nope") {
		t.Fatal(msg)
	}
}

func TestPagesTail(t *testing.T) {
	p := platFake(t)
	pagesFixtures(p)
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"trace-v1"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, _, _ = c.Read(ctx) // {"debug":false}
		ev := `{"outcome":"ok","eventTimestamp":1700000000000,"event":{"request":{"method":"GET","url":"https://site.pages.dev/api/x"}},"logs":[{"level":"log","message":["hello from functions"]}],"exceptions":[]}`
		_ = c.Write(ctx, websocket.MessageText, []byte(ev))
		_, _, _ = c.Read(ctx)
	}))
	defer ws.Close()
	p.routes["POST "+pagesP+"/site/deployments/d1/tails"] = func(fakeRequest) (int, any) {
		return 200, ok(map[string]any{"id": "tail1", "url": "ws" + strings.TrimPrefix(ws.URL, "http"), "expires_at": "2026-12-01T00:00:00Z"})
	}
	p.routes["DELETE "+pagesP+"/site/deployments/d1/tails/tail1"] = map[string]any{}
	out := platRunOK(t, "", "pages", "deployment", "tail", "site", "--status", "error", "--method", "GET", "--once")
	if !strings.Contains(out, "hello from functions") || !strings.Contains(out, "GET https://site.pages.dev/api/x") {
		t.Fatalf("tail output: %s", out)
	}
	r := p.last(t, "POST", pagesP+"/site/deployments/d1/tails")
	if !strings.Contains(string(r.RawBody), `"outcome":["exception","exceededCpu","exceededMemory","unknown"]`) || !strings.Contains(string(r.RawBody), `"method":["GET"]`) {
		t.Fatalf("filters: %s", r.RawBody)
	}
	p.last(t, "GET", pagesP+"/site/deployments") // latest deployment lookup
	p.last(t, "DELETE", pagesP+"/site/deployments/d1/tails/tail1")

	out = platRunOK(t, "", "pages", "deployment", "tail", "site", "d1", "--format", "json", "--once")
	if !strings.HasPrefix(strings.TrimSpace(out), `{"outcome":"ok"`) {
		t.Fatalf("json tail: %s", out)
	}
}

func TestPagesLocalOnly(t *testing.T) {
	platFake(t)
	for _, args := range [][]string{{"pages", "dev", "./dist"}, {"pages", "functions", "build"}, {"dev"}, {"types"}, {"setup"}} {
		if msg := platRunErr(t, "", args...); !strings.Contains(msg, "not applicable") || !strings.Contains(msg, "npx wrangler") {
			t.Fatalf("%v: %s", args, msg)
		}
	}
}
