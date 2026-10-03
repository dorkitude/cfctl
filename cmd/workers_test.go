package cmd

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wkRun(t *testing.T, stdin string, args ...string) (string, string) {
	t.Helper()
	out, errOut, err := runCLI(t, stdin, args...)
	if err != nil {
		t.Fatalf("cfctl %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, errOut)
	}
	assertNoToken(t, "output", out, errOut)
	return out, errOut
}

func wkRunErr(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, errOut, err := runCLI(t, stdin, args...)
	if err == nil {
		t.Fatalf("cfctl %s: expected an error\nstdout: %s", strings.Join(args, " "), out)
	}
	assertNoToken(t, "output", out, errOut, err.Error())
	return err.Error()
}

const wkScriptURL = "/accounts/" + acctID + "/workers/scripts/my-worker"

func TestWorkersListGetDelete(t *testing.T) {
	f := newWorkersFake(t)
	out, _ := wkRun(t, "", "workers", "list")
	if !strings.Contains(out, "my-worker") || !strings.Contains(out, "2025-01-01") {
		t.Fatalf("list: %s", out)
	}
	out, _ = wkRun(t, "", "workers", "list", "--json")
	if arr, ok := decode(t, out).([]any); !ok || len(arr) != 1 {
		t.Fatalf("list --json: %s", out)
	}

	out, _ = wkRun(t, "", "workers", "get", "my-worker")
	for _, want := range []string{"my-worker", "https://my-worker.team.workers.dev", "0 * * * *", "TOKEN", "(secret)", "100% " + wkVer3} {
		if !strings.Contains(out, want) {
			t.Errorf("get missing %q:\n%s", want, out)
		}
	}
	out, _ = wkRun(t, "", "workers", "get", "my-worker", "--json")
	got := decode(t, out).(map[string]any)
	if got["worker"] == nil || got["settings"] == nil || got["deployment"] == nil {
		t.Fatalf("get --json: %s", out)
	}

	// Delete refuses without --yes when stdin isn't a terminal.
	if msg := wkRunErr(t, "", "workers", "delete", "my-worker"); !strings.Contains(msg, "--yes") {
		t.Fatalf("delete without --yes: %s", msg)
	}
	if f.find("DELETE", wkScriptURL) != nil {
		t.Fatal("delete was sent without confirmation")
	}
	wkRun(t, "", "workers", "delete", "my-worker", "--yes", "--force")
	r := f.find("DELETE", wkScriptURL)
	if r == nil || r.Query != "force=true" {
		t.Fatalf("delete request: %+v", r)
	}
}

func TestWorkersReadOnlyRefusesMutations(t *testing.T) {
	f := newWorkersFake(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.js"), []byte("export default { fetch() { return new Response('hi') } }"), 0o644)
	t.Setenv("CFCTL_READONLY", "1")
	for _, args := range [][]string{
		{"deploy", filepath.Join(dir, "index.js"), "--name", "my-worker", "--compatibility-date", "2025-01-01"},
		{"workers", "delete", "my-worker"},
		{"secret", "put", "K", "--name", "my-worker"},
		{"rollback", "--name", "my-worker"},
		{"tail", "my-worker"},
		{"dispatch-namespace", "create", "x"},
	} {
		msg := wkRunErr(t, "value\n", args...)
		if !strings.Contains(msg, "read-only") {
			t.Errorf("%v: want read-only refusal, got %s", args, msg)
		}
	}
	for _, r := range f.Requests() {
		if r.Method != "GET" {
			t.Errorf("read-only mode sent %s %s", r.Method, r.Path)
		}
	}
	// Reads still work.
	wkRun(t, "", "workers", "versions", "list", "my-worker")
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const wkToml = `
name = "my-worker"
main = "src/index.js"
compatibility_date = 2025-01-01
compatibility_flags = ["nodejs_compat"]
routes = ["example.com/api/*", { pattern = "app.example.com", custom_domain = true }]

[triggers]
crons = ["*/5 * * * *"]

[vars]
API_BASE = "https://api.example.com"
LIMITS = { max = 3 }

[[kv_namespaces]]
binding = "CACHE"
id = "kv-123"

[[d1_databases]]
binding = "DB"
database_name = "app"
database_id = "d1-456"

[[r2_buckets]]
binding = "FILES"
bucket_name = "files"

[[services]]
binding = "AUTH"
service = "auth-worker"

[durable_objects]
bindings = [{ name = "ROOM", class_name = "Room" }]

[[migrations]]
tag = "v1"
new_classes = ["Room"]

[[migrations]]
tag = "v2"
new_sqlite_classes = ["Chat"]

[assets]
directory = "./public"
binding = "ASSETS"
not_found_handling = "single-page-application"

[env.staging]
name = "my-worker-stg"
vars = { API_BASE = "https://staging.example.com" }
`

func TestDeployFromWranglerToml(t *testing.T) {
	f := newWorkersFake(t)
	dir := writeProject(t, map[string]string{
		"wrangler.toml":        wkToml,
		"src/index.js":         "export default { async fetch(req, env) { return env.ASSETS.fetch(req) } }",
		"public/index.html":    "<h1>hi</h1>",
		"public/app.js":        "console.log(1)",
		"public/_headers":      "/*\n  X-Frame-Options: DENY\n",
		"public/.assetsignore": "*.map\n",
		"public/app.js.map":    "{}",
	})
	t.Chdir(dir)
	out, _ := wkRun(t, "", "deploy")
	for _, want := range []string{"Uploaded my-worker", "Cron triggers: */5 * * * *", "Routes: example.com/api/*", "Custom domains: app.example.com", "Current Version ID: " + wkVer3} {
		if !strings.Contains(out, want) {
			t.Errorf("deploy output missing %q:\n%s", want, out)
		}
	}

	// Assets: manifest excludes _headers, .assetsignore and ignored files.
	sess := f.find("POST", wkScriptURL+"/assets-upload-session")
	if sess == nil {
		t.Fatal("no assets upload session")
	}
	man := sess.Body["manifest"].(map[string]any)
	if len(man) != 2 || man["/index.html"] == nil || man["/app.js"] == nil {
		t.Fatalf("manifest: %v", man)
	}
	if len(f.assetUploads) != 1 {
		t.Fatalf("asset uploads: %d", len(f.assetUploads))
	}
	up := f.assetUploads[0]
	if up.Auth != "Bearer "+wkJWT || up.Query != "base64=true" {
		t.Fatalf("asset upload auth/query: %q %q", up.Auth, up.Query)
	}
	parts := wkParts(t, up)
	if len(parts) != 2 {
		t.Fatalf("asset upload parts: %v", parts)
	}
	for _, p := range parts {
		if _, err := base64Decode(p[1]); err != nil {
			t.Errorf("asset part not base64: %q", p[1])
		}
	}

	// Script upload: multipart metadata + module.
	put := f.find("PUT", wkScriptURL)
	if put == nil {
		t.Fatal("no script upload")
	}
	parts = wkParts(t, *put)
	if parts["index.js"][0] != "application/javascript+module" {
		t.Errorf("module part: %v", parts["index.js"])
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(parts["metadata"][1]), &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta["main_module"] != "index.js" || meta["compatibility_date"] != "2025-01-01" {
		t.Errorf("metadata: %v", meta)
	}
	if fl := meta["compatibility_flags"].([]any); len(fl) != 1 || fl[0] != "nodejs_compat" {
		t.Errorf("compat flags: %v", fl)
	}
	kb := meta["keep_bindings"].([]any)
	if len(kb) != 2 || kb[0] != "secret_text" {
		t.Errorf("keep_bindings: %v", kb)
	}
	assets := meta["assets"].(map[string]any)
	if assets["jwt"] != wkDone {
		t.Errorf("assets jwt: %v", assets["jwt"])
	}
	conf := assets["config"].(map[string]any)
	if !strings.Contains(conf["_headers"].(string), "X-Frame-Options") || conf["not_found_handling"] != "single-page-application" {
		t.Errorf("assets config: %v", conf)
	}
	types := map[string]map[string]any{}
	for _, b := range meta["bindings"].([]any) {
		bm := b.(map[string]any)
		types[bm["name"].(string)] = bm
	}
	checks := map[string]string{"API_BASE": "plain_text", "LIMITS": "json", "CACHE": "kv_namespace", "DB": "d1", "FILES": "r2_bucket", "AUTH": "service", "ROOM": "durable_object_namespace", "ASSETS": "assets"}
	for name, typ := range checks {
		if types[name] == nil || types[name]["type"] != typ {
			t.Errorf("binding %s: want %s, got %v", name, typ, types[name])
		}
	}
	if types["CACHE"]["namespace_id"] != "kv-123" || types["DB"]["id"] != "d1-456" {
		t.Errorf("binding fields: %v %v", types["CACHE"], types["DB"])
	}
	// Migrations start after the script's current tag (v1).
	mig := meta["migrations"].(map[string]any)
	if mig["old_tag"] != "v1" || mig["new_tag"] != "v2" || len(mig["steps"].([]any)) != 1 {
		t.Errorf("migrations: %v", mig)
	}

	// Triggers.
	sched := f.find("PUT", wkScriptURL+"/schedules")
	if sched == nil || !strings.Contains(string(sched.RawBody), `"*/5 * * * *"`) {
		t.Errorf("schedules: %+v", sched)
	}
	route := f.find("POST", "/zones/"+zoneID+"/workers/routes")
	if route == nil || route.Body["pattern"] != "example.com/api/*" || route.Body["script"] != "my-worker" {
		t.Errorf("route: %+v", route)
	}
	dom := f.find("PUT", "/accounts/"+acctID+"/workers/domains")
	if dom == nil || dom.Body["hostname"] != "app.example.com" || dom.Body["zone_id"] != zoneID || dom.Body["service"] != "my-worker" {
		t.Errorf("custom domain: %+v", dom)
	}
	// Routes present and workers_dev unset → workers.dev left alone.
	if f.find("POST", wkScriptURL+"/subdomain") != nil {
		t.Error("workers.dev was changed")
	}
}

func base64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func TestDeployEnvDryRunAndFlags(t *testing.T) {
	f := newWorkersFake(t)
	dir := writeProject(t, map[string]string{
		"wrangler.toml": wkToml,
		"src/index.js":  "export default {}",
		"public/a.txt":  "a",
	})
	t.Chdir(dir)
	out, _ := wkRun(t, "", "deploy", "--env", "staging", "--dry-run", "--var", "EXTRA=1")
	got := decode(t, out).(map[string]any)
	if got["name"] != "my-worker-stg" {
		t.Fatalf("env name: %v", got["name"])
	}
	meta := got["metadata"].(map[string]any)
	names := map[string]any{}
	for _, b := range meta["bindings"].([]any) {
		bm := b.(map[string]any)
		names[bm["name"].(string)] = bm["text"]
	}
	// vars are not inherited by [env.staging]; KV etc. aren't either.
	if names["API_BASE"] != "https://staging.example.com" || names["EXTRA"] != "1" || names["CACHE"] != nil {
		t.Errorf("env bindings: %v", names)
	}
	// Inheritable keys (main, compatibility_date, routes) are.
	if meta["main_module"] != "index.js" || meta["compatibility_date"] != "2025-01-01" {
		t.Errorf("env inherited: %v", meta)
	}
	for _, r := range f.Requests() {
		if r.Method != "GET" || strings.Contains(r.Path, "/workers/") {
			t.Errorf("dry run sent %s %s", r.Method, r.Path)
		}
	}

	// Flags only, service-worker script, workers.dev explicitly on.
	sw := filepath.Join(t.TempDir(), "worker.js")
	os.WriteFile(sw, []byte("addEventListener('fetch', e => e.respondWith(new Response('hi')))"), 0o644)
	t.Chdir(t.TempDir())
	wkRun(t, "", "deploy", sw, "--name", "my-worker", "--compatibility-date", "2024-06-01",
		"--kv", "K=ns1", "--r2", "B=bucket", "--d1", "D=db1", "--service", "S=other#Entry", "--queue", "Q=jobs", "--ai", "AI",
		"--binding", `{"type":"browser","name":"BR"}`, "--workers-dev", "--message", "hello", "--tag", "v9")
	put := f.find("PUT", wkScriptURL)
	parts := wkParts(t, *put)
	if parts["worker.js"][0] != "application/javascript" {
		t.Errorf("service-worker part: %v", parts["worker.js"][0])
	}
	var meta2 map[string]any
	json.Unmarshal([]byte(parts["metadata"][1]), &meta2)
	if meta2["body_part"] != "worker.js" || meta2["main_module"] != nil {
		t.Errorf("service-worker metadata: %v", meta2)
	}
	if ann := meta2["annotations"].(map[string]any); ann["workers/message"] != "hello" || ann["workers/tag"] != "v9" {
		t.Errorf("annotations: %v", ann)
	}
	bs, _ := json.Marshal(meta2["bindings"])
	for _, want := range []string{`"namespace_id":"ns1"`, `"bucket_name":"bucket"`, `"id":"db1"`, `"entrypoint":"Entry"`, `"queue_name":"jobs"`, `"type":"ai"`, `"type":"browser"`} {
		if !strings.Contains(string(bs), want) {
			t.Errorf("bindings missing %s: %s", want, bs)
		}
	}
	sub := f.find("POST", wkScriptURL+"/subdomain")
	if sub == nil || sub.Body["enabled"] != true {
		t.Errorf("workers.dev: %+v", sub)
	}
}

func TestVersionsListViewUploadDeploy(t *testing.T) {
	f := newWorkersFake(t)
	out, _ := wkRun(t, "", "workers", "versions", "list", "my-worker", "--limit", "0", "--json")
	if arr := decode(t, out).([]any); len(arr) != 3 {
		t.Fatalf("versions (paginated) = %d: %s", len(arr), out)
	}
	if n := countRequests(f.fakeCF, "GET", wkScriptURL+"/versions"); n != 2 {
		t.Errorf("version pages fetched: %d", n)
	}
	out, _ = wkRun(t, "", "versions", "list", "my-worker", "--limit", "2")
	if !strings.Contains(out, wkVer3) || strings.Contains(out, wkVer1) {
		t.Errorf("--limit 2: %s", out)
	}

	out, _ = wkRun(t, "", "versions", "view", "2222", "--name", "my-worker")
	if !strings.Contains(out, wkVer2) || !strings.Contains(out, "TOKEN") {
		t.Errorf("view by prefix: %s", out)
	}
	if msg := wkRunErr(t, "", "versions", "view", "9999", "--name", "my-worker"); !strings.Contains(msg, "no version") {
		t.Errorf("unknown prefix: %s", msg)
	}

	// Split deploy: prefix@10 + remainder.
	wkRun(t, "", "versions", "deploy", "3333@10", "2222", "--name", "my-worker", "--message", "canary")
	dep := f.find("POST", wkScriptURL+"/deployments")
	if dep == nil {
		t.Fatal("no deployment")
	}
	var body struct {
		Strategy    string            `json:"strategy"`
		Versions    []wkSplit         `json:"versions"`
		Annotations map[string]string `json:"annotations"`
	}
	json.Unmarshal(dep.RawBody, &body)
	if body.Strategy != "percentage" || len(body.Versions) != 2 || body.Versions[0].VersionID != wkVer3 || body.Versions[0].Percentage != 10 || body.Versions[1].Percentage != 90 || body.Annotations["workers/message"] != "canary" {
		t.Errorf("deployment body: %s", dep.RawBody)
	}
	if msg := wkRunErr(t, "", "versions", "deploy", "3333@60", "2222@60", "--name", "my-worker"); !strings.Contains(msg, "add up") {
		t.Errorf("bad split: %s", msg)
	}

	// Upload a version (no triggers touched).
	main := filepath.Join(t.TempDir(), "index.mjs")
	os.WriteFile(main, []byte("export default {}"), 0o644)
	t.Chdir(t.TempDir())
	out, _ = wkRun(t, "", "versions", "upload", main, "--name", "my-worker", "--compatibility-date", "2025-01-01", "--tag", "v2", "--preview-alias", "staging")
	if !strings.Contains(out, "44444444-4444-4444-8444-444444444444") {
		t.Errorf("upload output: %s", out)
	}
	up := f.find("POST", wkScriptURL+"/versions")
	parts := wkParts(t, *up)
	var meta map[string]any
	json.Unmarshal([]byte(parts["metadata"][1]), &meta)
	ann := meta["annotations"].(map[string]any)
	if meta["main_module"] != "index.mjs" || ann["workers/tag"] != "v2" || ann["workers/alias"] != "staging" {
		t.Errorf("version metadata: %v", meta)
	}
	if f.find("PUT", wkScriptURL+"/schedules") != nil {
		t.Error("versions upload touched triggers")
	}
}

func TestDeploymentsAndRollback(t *testing.T) {
	f := newWorkersFake(t)
	out, _ := wkRun(t, "", "deployments", "list", "my-worker")
	if !strings.Contains(out, "dep-2") || !strings.Contains(out, "dep-1") {
		t.Errorf("deployments list: %s", out)
	}
	out, _ = wkRun(t, "", "workers", "deployments", "status", "my-worker", "--json")
	if decode(t, out).(map[string]any)["id"] != "dep-2" {
		t.Errorf("status: %s", out)
	}

	if msg := wkRunErr(t, "", "rollback", "--name", "my-worker"); !strings.Contains(msg, "--yes") {
		t.Fatalf("rollback without --yes: %s", msg)
	}
	if f.find("POST", wkScriptURL+"/deployments") != nil {
		t.Fatal("rollback sent without confirmation")
	}
	wkRun(t, "", "rollback", "--name", "my-worker", "--yes")
	dep := f.find("POST", wkScriptURL+"/deployments")
	if dep == nil || dep.Query != "force=true" || !strings.Contains(string(dep.RawBody), wkVer2) || !strings.Contains(string(dep.RawBody), "Rollback via cfctl") {
		t.Fatalf("rollback request: %+v %s", dep, dep.RawBody)
	}
}

const wkSecretValue = "s3cr3t-VALUE-never-printed"

func TestSecretsNeverEchoed(t *testing.T) {
	f := newWorkersFake(t)
	t.Setenv("CFCTL_DEBUG", "1")
	out, errOut := wkRun(t, wkSecretValue+"\n", "secret", "put", "API_KEY", "--name", "my-worker")
	if strings.Contains(out+errOut, wkSecretValue) {
		t.Fatalf("secret value echoed: %s %s", out, errOut)
	}
	put := f.find("PUT", wkScriptURL+"/secrets")
	if put == nil || put.Body["text"] != wkSecretValue || put.Body["name"] != "API_KEY" || put.Body["type"] != "secret_text" {
		t.Fatalf("secret put body: %+v", put)
	}
	out, errOut = wkRun(t, wkSecretValue, "secret", "put", "API_KEY", "--name", "my-worker", "--json")
	if strings.Contains(out+errOut, wkSecretValue) {
		t.Fatalf("secret value in --json: %s", out)
	}
	if msg := wkRunErr(t, "", "secret", "put", "API_KEY", "--name", "my-worker"); !strings.Contains(msg, "empty") {
		t.Errorf("empty secret: %s", msg)
	}

	out, _ = wkRun(t, "", "secret", "list", "--name", "my-worker")
	if !strings.Contains(out, "EXISTING") {
		t.Errorf("secret list: %s", out)
	}

	if msg := wkRunErr(t, "", "secret", "delete", "EXISTING", "--name", "my-worker"); !strings.Contains(msg, "--yes") {
		t.Errorf("secret delete without --yes: %s", msg)
	}
	wkRun(t, "", "secret", "delete", "EXISTING", "--name", "my-worker", "-y")
	if f.find("DELETE", wkScriptURL+"/secrets/EXISTING") == nil {
		t.Error("secret delete not sent")
	}

	// Bulk from JSON on stdin, with a deletion (needs --yes).
	bulk := `{"A":"` + wkSecretValue + `","OLD":null}`
	if msg := wkRunErr(t, bulk, "secret", "bulk", "--name", "my-worker"); !strings.Contains(msg, "--yes") {
		t.Errorf("bulk with deletes needs --yes: %s", msg)
	}
	out, errOut = wkRun(t, bulk, "secret", "bulk", "--name", "my-worker", "--yes")
	if strings.Contains(out+errOut, wkSecretValue) {
		t.Fatalf("bulk echoed a value: %s", out)
	}
	p := f.find("PATCH", wkScriptURL+"/secrets-bulk")
	secs := p.Body["secrets"].(map[string]any)
	if a := secs["A"].(map[string]any); a["text"] != wkSecretValue || a["type"] != "secret_text" {
		t.Errorf("bulk A: %v", a)
	}
	if v, ok := secs["OLD"]; !ok || v != nil {
		t.Errorf("bulk OLD should be null: %v", secs)
	}
	// .env file.
	env := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(env, []byte("# comment\nexport B=\"two words\"\nC=3\n"), 0o600)
	out, _ = wkRun(t, "", "secret", "bulk", env, "--name", "my-worker", "--json")
	got := decode(t, out).(map[string]any)
	if len(got["set"].([]any)) != 2 {
		t.Errorf("bulk .env: %s", out)
	}
}

func TestTriggersRoutesDomains(t *testing.T) {
	f := newWorkersFake(t)
	out, _ := wkRun(t, "", "triggers", "list", "my-worker", "--json")
	got := decode(t, out).(map[string]any)
	if crons := got["crons"].([]any); len(crons) != 1 || crons[0] != "0 * * * *" {
		t.Errorf("triggers crons: %v", got)
	}

	wkRun(t, "", "workers", "routes", "add", "example.com/blog/*", "--name", "my-worker")
	if r := f.find("POST", "/zones/"+zoneID+"/workers/routes"); r == nil || r.Body["pattern"] != "example.com/blog/*" {
		t.Fatalf("route add: %+v", r)
	}
	out, _ = wkRun(t, "", "workers", "routes", "list")
	if !strings.Contains(out, "example.com/blog/*") {
		t.Errorf("routes list: %s", out)
	}
	if msg := wkRunErr(t, "", "workers", "routes", "add", "nope.org/*", "--name", "my-worker"); !strings.Contains(msg, "no zone") {
		t.Errorf("route on unknown zone: %s", msg)
	}
	wkRunErr(t, "", "workers", "routes", "delete", "example.com/blog/*")
	wkRun(t, "", "workers", "routes", "delete", "example.com/blog/*", "--yes")
	if f.find("DELETE", "/zones/"+zoneID+"/workers/routes/route-1") == nil {
		t.Error("route delete not sent")
	}

	wkRun(t, "", "workers", "domains", "add", "api.example.com", "--name", "my-worker")
	out, _ = wkRun(t, "", "workers", "domains", "list", "--json")
	if !strings.Contains(out, "api.example.com") {
		t.Errorf("domains list: %s", out)
	}
	wkRun(t, "", "workers", "domains", "delete", "api.example.com", "--yes")
	if f.find("DELETE", "/accounts/"+acctID+"/workers/domains/dom-1") == nil {
		t.Error("domain delete not sent")
	}

	wkRun(t, "", "workers", "crons", "set", "my-worker", "--cron", "0 0 * * *")
	if s := f.find("PUT", wkScriptURL+"/schedules"); s == nil || string(s.RawBody) != `[{"cron":"0 0 * * *"}]` {
		t.Errorf("crons set: %+v", s)
	}
	wkRunErr(t, "", "workers", "crons", "clear", "my-worker")
	wkRun(t, "", "workers", "crons", "clear", "my-worker", "--yes")

	out, _ = wkRun(t, "", "workers", "subdomain", "get")
	if strings.TrimSpace(out) != "team.workers.dev" {
		t.Errorf("subdomain get: %q", out)
	}
	wkRunErr(t, "", "workers", "subdomain", "set", "newteam")
	wkRun(t, "", "workers", "subdomain", "set", "newteam", "--yes")
	if s := f.find("PUT", "/accounts/"+acctID+"/workers/subdomain"); s == nil || s.Body["subdomain"] != "newteam" {
		t.Errorf("subdomain set: %+v", s)
	}
	out, _ = wkRun(t, "", "workers", "dev-url", "get", "my-worker")
	if !strings.Contains(out, "https://my-worker.team.workers.dev") {
		t.Errorf("dev-url get: %s", out)
	}
	wkRun(t, "", "workers", "dev-url", "enable", "my-worker", "--previews")
	if s := f.find("POST", wkScriptURL+"/subdomain"); s == nil || s.Body["enabled"] != true || s.Body["previews_enabled"] != true {
		t.Errorf("dev-url enable: %+v", s)
	}

	// triggers deploy from flags.
	t.Chdir(t.TempDir())
	wkRun(t, "", "triggers", "deploy", "my-worker", "--cron", "1 * * * *", "--workers-dev=false")
}

func TestTailStreamsAndDeletes(t *testing.T) {
	f := newWorkersFake(t)
	out, _ := wkRun(t, "", "tail", "my-worker", "--status", "error", "--method", "post", "--search", "boom", "--sampling-rate", "0.5", "--header", "x-debug:1", "--ip", "1.2.3.4")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 JSON lines (piped → json), got: %q", out)
	}
	for _, l := range lines {
		var v map[string]any
		if err := json.Unmarshal([]byte(l), &v); err != nil || v["scriptName"] != "my-worker" {
			t.Errorf("bad line %q", l)
		}
	}
	start := f.find("POST", wkScriptURL+"/tails")
	if start == nil {
		t.Fatal("tail not created")
	}
	fs, _ := json.Marshal(start.Body["filters"])
	for _, want := range []string{`"sampling_rate":0.5`, `"outcome":["exception","exceededCpu","exceededMemory","unknown"]`, `"method":["POST"]`, `"key":"x-debug"`, `"query":"1"`, `"client_ip":["1.2.3.4"]`, `"query":"boom"`} {
		if !strings.Contains(string(fs), want) {
			t.Errorf("filters missing %s: %s", want, fs)
		}
	}
	if f.find("DELETE", wkScriptURL+"/tails/tail-1") == nil {
		t.Error("tail was not deleted")
	}
	f.mu.Lock()
	h, first := f.wsHeader, f.wsFirstMsg
	f.mu.Unlock()
	if h.Get("Sec-WebSocket-Protocol") != "trace-v1" || h.Get("Authorization") != "" {
		t.Errorf("ws headers: proto=%q auth=%q", h.Get("Sec-WebSocket-Protocol"), h.Get("Authorization"))
	}
	if first != `{"debug":false}` {
		t.Errorf("ws first message: %q", first)
	}

	// Pretty format.
	out, _ = wkRun(t, "", "tail", "my-worker", "--format", "pretty")
	for _, want := range []string{"GET https://example.com/hi - 200 Ok", "(log) hello 1", `"*/5 * * * *"`, "Error: boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty missing %q:\n%s", want, out)
		}
	}
	if msg := wkRunErr(t, "", "tail", "my-worker", "--status", "weird"); !strings.Contains(msg, "--status") {
		t.Errorf("bad status: %s", msg)
	}
}

func TestDispatchNamespace(t *testing.T) {
	f := newWorkersFake(t)
	ns := "/accounts/" + acctID + "/workers/dispatch/namespaces"
	out, _ := wkRun(t, "", "dispatch-namespace", "list")
	if !strings.Contains(out, "customers") {
		t.Errorf("list: %s", out)
	}
	out, _ = wkRun(t, "", "workers", "dispatch-namespace", "get", "customers", "--json")
	if decode(t, out).(map[string]any)["namespace_id"] != "ns-1" {
		t.Errorf("get: %s", out)
	}
	wkRun(t, "", "dispatch-namespace", "create", "tenants")
	if r := f.find("POST", ns); r == nil || r.Body["name"] != "tenants" {
		t.Errorf("create: %+v", r)
	}
	wkRun(t, "", "dispatch-namespace", "rename", "customers", "clients")
	if r := f.find("PATCH", ns+"/customers"); r == nil || r.Body["name"] != "clients" {
		t.Errorf("rename: %+v", r)
	}
	out, _ = wkRun(t, "", "dispatch-namespace", "scripts", "customers")
	if !strings.Contains(out, "tenant-a") {
		t.Errorf("scripts: %s", out)
	}
	wkRunErr(t, "", "dispatch-namespace", "delete", "customers")
	wkRun(t, "", "dispatch-namespace", "delete", "customers", "--yes")
	if f.find("DELETE", ns+"/customers") == nil {
		t.Error("delete not sent")
	}
}

func TestPreviews(t *testing.T) {
	f := newWorkersFake(t)
	f.needUpload = false
	pv := "/accounts/" + acctID + "/workers/workers/my-worker/previews"
	main := filepath.Join(t.TempDir(), "index.js")
	os.WriteFile(main, []byte("export default {}"), 0o644)
	t.Chdir(t.TempDir())
	out, _ := wkRun(t, "", "workers", "preview", "deploy", main, "--name", "my-worker", "--preview", "feature-x", "--compatibility-date", "2025-01-01", "--var", "A=1")
	if !strings.Contains(out, "Created preview feature-x") || !strings.Contains(out, "https://feature-x-my-worker.team.workers.dev") {
		t.Errorf("preview deploy: %s", out)
	}
	if r := f.find("POST", pv); r == nil || r.Body["name"] != "feature-x" {
		t.Fatalf("preview create: %+v", r)
	}
	d := f.find("POST", pv+"/pv-1/deployments")
	if d == nil {
		t.Fatal("no preview deployment")
	}
	mods := d.Body["modules"].([]any)
	m0 := mods[0].(map[string]any)
	if m0["name"] != "index.js" || m0["content_type"] != "application/javascript+module" || m0["content_base64"] == "" || d.Body["main_module"] != "index.js" || d.Body["keep_bindings"] != nil {
		t.Errorf("preview deployment body: %v", d.Body)
	}

	out, _ = wkRun(t, "", "preview", "list", "my-worker")
	if !strings.Contains(out, "feature-x") {
		t.Errorf("preview list: %s", out)
	}
	wkRun(t, "", "preview", "get", "--name", "my-worker", "--preview", "feature-x")
	wkRun(t, "", "preview", "deployments", "--name", "my-worker", "--preview", "feature-x")

	out, errOut := wkRun(t, wkSecretValue, "preview", "secret", "put", "PK", "--name", "my-worker", "--preview", "feature-x")
	if strings.Contains(out+errOut, wkSecretValue) {
		t.Fatal("preview secret echoed")
	}
	p := f.find("PATCH", pv+"/feature-x/deployments/latest")
	if p == nil || !strings.Contains(string(p.RawBody), `"PK":{"text":"`+wkSecretValue+`","type":"secret_text"}`) {
		t.Fatalf("preview secret put: %+v", p)
	}
	out, _ = wkRun(t, "", "preview", "secret", "list", "--name", "my-worker", "--preview", "feature-x", "--json")
	if !strings.Contains(out, "PV_SECRET") || strings.Contains(out, `"V"`) {
		t.Errorf("preview secret list: %s", out)
	}

	wkRun(t, wkSecretValue, "preview", "base-config", "secret", "put", "BK", "--name", "my-worker")
	b := f.find("PATCH", "/accounts/"+acctID+"/workers/workers/my-worker")
	if b == nil || !strings.Contains(string(b.RawBody), `"previews_base_config":{"env":{"BK":{"text":"`+wkSecretValue) {
		t.Fatalf("base-config secret put: %+v", b)
	}
	out, _ = wkRun(t, "", "preview", "base-config", "secret", "list", "--name", "my-worker")
	if !strings.Contains(out, "BASE_SECRET") || strings.Contains(out, "PLAIN") {
		t.Errorf("base-config secret list: %s", out)
	}

	wkRunErr(t, "", "preview", "delete", "--name", "my-worker", "--preview", "feature-x")
	wkRun(t, "", "preview", "delete", "--name", "my-worker", "--preview", "feature-x", "--yes")
	if f.find("DELETE", pv+"/feature-x") == nil {
		t.Error("preview delete not sent")
	}
}

func TestWorkersLogs(t *testing.T) {
	f := newWorkersFake(t)
	out, _ := wkRun(t, "", "workers", "logs", "my-worker", "--search", "broke", "--level", "error")
	if !strings.Contains(out, "it broke") || !strings.Contains(out, "ERROR") {
		t.Errorf("logs: %s", out)
	}
	q := f.find("POST", "/accounts/"+acctID+"/workers/observability/telemetry/query")
	b, _ := json.Marshal(q.Body)
	for _, want := range []string{`"view":"events"`, `"value":"my-worker"`, `"needle":{"value":"broke"}`, `"$metadata.level"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("logs query missing %s: %s", want, b)
		}
	}
}

func TestWorkerNameFromConfig(t *testing.T) {
	newWorkersFake(t)
	dir := writeProject(t, map[string]string{"wrangler.jsonc": `{
  // comment
  "name": "my-worker", /* block */
  "main": "index.js",
}`})
	t.Chdir(dir)
	out, _ := wkRun(t, "", "secret", "list")
	if !strings.Contains(out, "my-worker") {
		t.Errorf("name from wrangler.jsonc: %s", out)
	}
	t.Chdir(t.TempDir())
	if msg := wkRunErr(t, "", "secret", "list"); !strings.Contains(msg, "which Worker") {
		t.Errorf("no name: %s", msg)
	}
}

func TestVersionsSecret(t *testing.T) {
	f := newWorkersFake(t)
	latest := "/accounts/" + acctID + "/workers/workers/my-worker/versions/latest"
	out, errOut := wkRun(t, wkSecretValue, "versions", "secret", "put", "NEW", "--name", "my-worker", "--message", "rotate")
	if strings.Contains(out+errOut, wkSecretValue) {
		t.Fatal("versions secret echoed the value")
	}
	if !strings.Contains(out, "55555555-5555-4555-8555-555555555555") || !strings.Contains(out, "not deployed") {
		t.Errorf("versions secret put: %s", out)
	}
	p := f.find("PATCH", latest)
	if p == nil {
		t.Fatal("no PATCH versions/latest")
	}
	want := `{"annotations":{"workers/message":"rotate"},"bindings":[{"name":"API","type":"inherit"},{"name":"TOKEN","type":"inherit"},{"name":"NEW","text":"` + wkSecretValue + `","type":"secret_text"}]}`
	if string(p.RawBody) != want {
		t.Errorf("patch body:\n got %s\nwant %s", p.RawBody, want)
	}
	if f.find("POST", wkScriptURL+"/deployments") != nil || strings.Contains(p.Query, "deploy") {
		t.Error("versions secret put deployed")
	}
	if msg := wkRunErr(t, "", "versions", "secret", "delete", "TOKEN", "--name", "my-worker"); !strings.Contains(msg, "--yes") {
		t.Errorf("delete without --yes: %s", msg)
	}
	if msg := wkRunErr(t, "", "versions", "secret", "delete", "NOPE", "--name", "my-worker", "--yes"); !strings.Contains(msg, "no binding") {
		t.Errorf("delete unknown: %s", msg)
	}
	out, _ = wkRun(t, "", "versions", "secret", "list", "--name", "my-worker", "--json")
	if !strings.Contains(out, "TOKEN") || strings.Contains(out, "API") {
		t.Errorf("versions secret list: %s", out)
	}
}
