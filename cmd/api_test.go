package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dorkitude/cfctl/internal/apispec"
	"github.com/spf13/cobra"
)

// apiFake is a fakeCF with extra routes for the api/graphql tests.
func apiFake(t *testing.T) *fakeCF {
	t.Helper()
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)
	f.custom = func(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
		q := r.URL.Query()
		switch {
		case req.Path == "/accounts/"+acctID+"/r2/buckets" && r.Method == "GET":
			switch q.Get("cursor") {
			case "":
				writeJSON(w, 200, map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": map[string]any{"buckets": []any{map[string]any{"name": "b1"}, map[string]any{"name": "b2"}}}, "result_info": map[string]any{"cursor": "next1", "per_page": 2}})
			case "next1":
				writeJSON(w, 200, map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": map[string]any{"buckets": []any{map[string]any{"name": "b3"}}}, "result_info": map[string]any{"cursor": "", "per_page": 2}})
			}
			return true
		case req.Path == "/accounts/"+acctID+"/r2/buckets/mybucket":
			writeJSON(w, 200, ok(map[string]any{"name": "mybucket", "location": "ENAM"}))
			return true
		case req.Path == "/paged":
			p, _ := strconv.Atoi(q.Get("page"))
			if p == 0 {
				p = 1
			}
			writeJSON(w, 200, map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": []any{p*10 + 1, p*10 + 2}, "result_info": map[string]any{"page": p, "per_page": 2, "total_pages": 3, "count": 2}})
			return true
		case req.Path == "/forbidden":
			writeJSON(w, 403, fail(9109, "Unauthorized to access requested resource"))
			return true
		case req.Path == "/text":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("plain value"))
			return true
		case req.Path == "/graphql" && r.Method == "POST":
			var body struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			_ = json.Unmarshal(req.RawBody, &body)
			if strings.Contains(body.Query, "broken") {
				writeJSON(w, 200, map[string]any{"data": nil, "errors": []any{map[string]any{"message": "unknown field broken"}}})
				return true
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"echo": body.Query, "vars": body.Variables}, "errors": nil})
			return true
		case strings.HasPrefix(req.Path, "/accounts/"+acctID+"/"):
			// Echo anything else under the account.
			writeJSON(w, 200, ok(map[string]any{"method": r.Method, "path": req.Path, "query": req.Query}))
			return true
		case strings.HasPrefix(req.Path, "/zones/"+zoneID+"/") && !strings.HasPrefix(req.Path, "/zones/"+zoneID+"/dns_records"):
			writeJSON(w, 200, ok(map[string]any{"method": r.Method, "path": req.Path, "query": req.Query}))
			return true
		case req.Path == "/zones/"+zoneID+"/dns_records" && r.Method == "POST":
			writeJSON(w, 200, ok(map[string]any{"echo": req.Body}))
			return true
		}
		return false
	}
	return f
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, stderr, err := runCLI(t, "", args...)
	if err != nil {
		t.Fatalf("cfctl %s: %v\nstderr: %s", strings.Join(args, " "), err, stderr)
	}
	assertNoToken(t, "output", out, stderr)
	return out
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, s)
	}
	return v
}

func TestAPIRequestPlaceholdersAndCursorPagination(t *testing.T) {
	f := apiFake(t)
	out := mustRun(t, "api", "request", "GET", "/accounts/{account_id}/r2/buckets")
	got := decode(t, out).(map[string]any)
	if len(got["buckets"].([]any)) != 2 {
		t.Fatalf("single page: %s", out)
	}
	if f.find("GET", "/accounts/"+acctID+"/r2/buckets") == nil {
		t.Fatal("placeholder not filled")
	}

	// --all follows result_info.cursor and merges {"buckets": [...]}.
	out = mustRun(t, "api", "request", "GET", "accounts/{account_id}/r2/buckets", "--all")
	got = decode(t, out).(map[string]any)
	if n := len(got["buckets"].([]any)); n != 3 {
		t.Fatalf("--all cursor: got %d buckets: %s", n, out)
	}
	// --raw keeps the envelope.
	out = mustRun(t, "api", "request", "GET", "/accounts/{account_id}/r2/buckets", "--raw")
	if env := decode(t, out).(map[string]any); env["success"] != true || env["result_info"] == nil {
		t.Fatalf("--raw: %s", out)
	}
}

func TestAPIRequestPagePagination(t *testing.T) {
	f := apiFake(t)
	out := mustRun(t, "api", "request", "GET", "/paged", "--all", "--query", "per_page=2")
	if strings.Join(strings.Fields(out), "") != "[11,12,21,22,31,32]" {
		t.Fatalf("--all pages: %s", out)
	}
	if countRequests(f, "GET", "/paged") != 3 {
		t.Fatalf("expected 3 requests, got %d", countRequests(f, "GET", "/paged"))
	}
	for _, r := range f.Requests() {
		if r.Path == "/paged" && !strings.Contains(r.Query, "per_page=2") {
			t.Errorf("per_page lost: %s", r.Query)
		}
	}
	// --max-pages bounds it.
	_, stderr, err := runCLI(t, "", "api", "request", "GET", "/paged", "--all", "--max-pages", "2")
	if err != nil || !strings.Contains(stderr, "stopped after 2 pages") {
		t.Fatalf("max-pages: %v %s", err, stderr)
	}
}

func TestAPIRequestZoneAndQuery(t *testing.T) {
	f := apiFake(t)
	out := mustRun(t, "api", "request", "GET", "/zones/{zone_id}/settings?x=1", "--zone", "example.com", "--query", "a=b", "--query", "a=c")
	got := decode(t, out).(map[string]any)
	if got["path"] != "/zones/"+zoneID+"/settings" {
		t.Fatalf("zone not resolved: %s", out)
	}
	q, _ := url.ParseQuery(got["query"].(string))
	if q.Get("x") != "1" || strings.Join(q["a"], ",") != "b,c" {
		t.Fatalf("query: %v", q)
	}
	if r := f.find("GET", "/zones"); r == nil || !strings.Contains(r.Query, "name=example.com") {
		t.Fatal("zone lookup by name missing")
	}
	// A zone ID is used as-is (no lookup).
	before := countRequests(f, "GET", "/zones")
	mustRun(t, "api", "request", "GET", "/zones/{zone_id}/settings", "--zone", zoneID)
	if countRequests(f, "GET", "/zones") != before {
		t.Error("zone ID should not be looked up")
	}
	// Missing --zone is a clear error.
	_, _, err := runCLI(t, "", "api", "request", "GET", "/zones/{zone_id}/settings")
	if err == nil || !strings.Contains(err.Error(), "--zone") {
		t.Fatalf("missing zone: %v", err)
	}
	// Unknown placeholder.
	_, _, err = runCLI(t, "", "api", "request", "GET", "/accounts/{account_id}/r2/buckets/{bucket}")
	if err == nil || !strings.Contains(err.Error(), "{bucket}") {
		t.Fatalf("unknown placeholder: %v", err)
	}
}

func TestAPIRequestBodies(t *testing.T) {
	f := apiFake(t)
	mustRun(t, "api", "request", "POST", "/zones/{zone_id}/dns_records", "--zone", "example.com", "--data", `{"type":"A","name":"x","content":"192.0.2.1"}`)
	r := f.find("POST", "/zones/"+zoneID+"/dns_records")
	if r == nil || r.ContentType != "application/json" || r.Body["content"] != "192.0.2.1" {
		t.Fatalf("json body: %+v", r)
	}

	// --data @file and --data - (stdin).
	dir := t.TempDir()
	p := filepath.Join(dir, "body.json")
	os.WriteFile(p, []byte(`{"from":"file"}`), 0o600)
	mustRun(t, "api", "request", "PATCH", "/accounts/{account_id}/thing", "--data", "@"+p)
	if r := f.find("PATCH", "/accounts/"+acctID+"/thing"); r == nil || r.Body["from"] != "file" {
		t.Fatalf("@file body: %+v", r)
	}
	if _, _, err := runCLI(t, `{"from":"stdin"}`, "api", "request", "PUT", "/accounts/{account_id}/thing", "--data", "-"); err != nil {
		t.Fatal(err)
	}
	if r := f.find("PUT", "/accounts/"+acctID+"/thing"); r == nil || r.Body["from"] != "stdin" {
		t.Fatalf("stdin body: %+v", r)
	}

	// --form builds multipart/form-data.
	js := filepath.Join(dir, "index.js")
	os.WriteFile(js, []byte("export default {fetch(){return new Response('hi')}}"), 0o600)
	mustRun(t, "api", "request", "PUT", "/accounts/{account_id}/workers/scripts/hello",
		"--form", `metadata={"main_module":"index.js"};type=application/json`,
		"--form", "index.js=@"+js+";type=application/javascript+module")
	r = f.find("PUT", "/accounts/"+acctID+"/workers/scripts/hello")
	if r == nil {
		t.Fatal("multipart request missing")
	}
	parts := multipartParts(t, r)
	if parts["metadata"] != `application/json|{"main_module":"index.js"}` || !strings.HasPrefix(parts["index.js"], "application/javascript+module|export default") {
		t.Fatalf("parts: %v", parts)
	}
	// --header is sent; Authorization can't be overridden.
	mustRun(t, "api", "request", "GET", "/accounts/{account_id}/hdr", "--header", "X-Test: yes")
	if r := f.find("GET", "/accounts/"+acctID+"/hdr"); r == nil || r.Header.Get("X-Test") != "yes" {
		t.Fatal("header not sent")
	}
	if _, _, err := runCLI(t, "", "api", "request", "GET", "/zones", "--header", "Authorization: Bearer x"); err == nil {
		t.Fatal("Authorization header override should be refused")
	}
	// Invalid JSON is caught locally.
	if _, _, err := runCLI(t, "", "api", "request", "POST", "/accounts/{account_id}/x", "--data", "{nope", "--header", "Content-Type: application/json"); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("invalid json: %v", err)
	}
}

func multipartParts(t *testing.T, r *fakeRequest) map[string]string {
	t.Helper()
	mt, params, err := mime.ParseMediaType(r.ContentType)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("content type %q", r.ContentType)
	}
	mr := multipart.NewReader(bytes.NewReader(r.RawBody), params["boundary"])
	out := map[string]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		out[p.FormName()] = p.Header.Get("Content-Type") + "|" + string(b)
	}
	return out
}

func TestAPIRequestErrorsAndText(t *testing.T) {
	apiFake(t)
	out, _, err := runCLI(t, "", "api", "request", "GET", "/forbidden")
	if err == nil || err.Error() != "HTTP 403: Unauthorized to access requested resource (code 9109)" || out != "" {
		t.Fatalf("403: err=%v out=%q", err, out)
	}
	out, _, err = runCLI(t, "", "api", "request", "GET", "/forbidden", "--raw")
	if err == nil || !strings.Contains(out, `"success": false`) {
		t.Fatalf("--raw on error should print the envelope: %v %q", err, out)
	}
	out = mustRun(t, "api", "request", "GET", "/text")
	if out != "plain value" {
		t.Fatalf("text: %q", out)
	}
	if _, _, err := runCLI(t, "", "api", "request", "FETCH", "/zones"); err == nil {
		t.Fatal("bad method accepted")
	}
	if _, _, err := runCLI(t, "", "api", "request", "POST", "/x", "--all"); err == nil {
		t.Fatal("--all with POST accepted")
	}
}

func TestReadOnlyGuardCLI(t *testing.T) {
	f := apiFake(t)
	before := len(f.Requests())

	// --read-only refuses before sending, raw and generated.
	_, stderr, err := runCLI(t, "", "--read-only", "--debug", "api", "request", "POST", "/accounts/{account_id}/r2/buckets", "--data", `{"name":"x"}`)
	if err == nil || err.Error() != "refusing POST /client/v4/accounts/"+acctID+"/r2/buckets: read-only mode" {
		t.Fatalf("raw: %v", err)
	}
	if !strings.Contains(stderr, "refused, not sent") || strings.Contains(stderr, "POST /client/v4/accounts/"+acctID+"/r2/buckets ->  ") {
		t.Errorf("debug: %s", stderr)
	}
	t.Setenv("CFCTL_READONLY", "1")
	_, _, err = runCLI(t, "", "api", "r2-bucket", "create-bucket", "--data", `{"name":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "read-only mode") {
		t.Fatalf("generated: %v", err)
	}
	// DELETE: no confirmation prompt needed; still refused.
	_, _, err = runCLI(t, "", "api", "dns-records-for-a-zone", "delete-dns-record", recordID, "--zone", zoneID)
	if err == nil || !strings.Contains(err.Error(), "refusing DELETE") {
		t.Fatalf("delete: %v", err)
	}
	// SDK-based hand-written commands are guarded too.
	_, _, err = runCLI(t, "", "records", "delete", "example.com", recordID)
	if err == nil || !strings.Contains(err.Error(), "read-only mode") {
		t.Fatalf("sdk records delete: %v", err)
	}
	for _, r := range f.Requests()[before:] {
		switch r.Method {
		case "GET", "HEAD", "OPTIONS":
		default:
			t.Errorf("guard let %s %s through", r.Method, r.Path)
		}
	}
	// Reads and GraphQL still work.
	mustRun(t, "api", "request", "GET", "/accounts/{account_id}/r2/buckets")
	mustRun(t, "graphql", "{ viewer { __typename } }")
}

func TestGraphQL(t *testing.T) {
	f := apiFake(t)
	out := mustRun(t, "graphql", `query { viewer { accounts(filter:{accountTag:"{account_id}"}) { x } } }`, "--variables", `{"z":"{zone_id}","n":1}`, "--zone", "example.com")
	got := decode(t, out).(map[string]any)["data"].(map[string]any)
	if !strings.Contains(got["echo"].(string), acctID) {
		t.Errorf("account_id not substituted in query: %v", got["echo"])
	}
	if vars := got["vars"].(map[string]any); vars["z"] != zoneID || vars["n"] != float64(1) {
		t.Errorf("variables: %v", vars)
	}
	r := f.find("POST", "/graphql")
	if r == nil || r.ContentType != "application/json" {
		t.Fatal("graphql request missing")
	}

	// stdin, --file, and --variables-file.
	dir := t.TempDir()
	qf := filepath.Join(dir, "q.graphql")
	vf := filepath.Join(dir, "v.json")
	os.WriteFile(qf, []byte("query FromFile { x }"), 0o600)
	os.WriteFile(vf, []byte(`{"a":"b"}`), 0o600)
	out = mustRun(t, "graphql", "--file", qf, "--variables-file", vf)
	if !strings.Contains(out, "FromFile") || !strings.Contains(out, `"a": "b"`) {
		t.Errorf("file: %s", out)
	}
	out, _, err := runCLI(t, "query FromStdin { x }", "graphql")
	if err != nil || !strings.Contains(out, "FromStdin") {
		t.Errorf("stdin: %v %s", err, out)
	}

	// GraphQL errors are printed and fail the command.
	out, _, err = runCLI(t, "", "graphql", "{ broken }")
	if err == nil || !strings.Contains(err.Error(), "unknown field broken") || !strings.Contains(out, "errors") {
		t.Errorf("errors: %v %s", err, out)
	}
	// Input validation.
	if _, _, err := runCLI(t, "", "graphql"); err == nil {
		t.Error("no query accepted")
	}
	if _, _, err := runCLI(t, "", "graphql", "q", "--query", "q2"); err == nil {
		t.Error("two sources accepted")
	}
	if _, _, err := runCLI(t, "", "graphql", "q", "--variables", "[1]"); err == nil {
		t.Error("non-object variables accepted")
	}
}

func TestGeneratedCommands(t *testing.T) {
	f := apiFake(t)

	// Account path param from config, query flags (kebab-case), cursor --all.
	out := mustRun(t, "api", "r2-bucket", "list-buckets", "--per-page", "2", "--direction", "asc", "--all")
	if n := len(decode(t, out).(map[string]any)["buckets"].([]any)); n != 3 {
		t.Fatalf("list-buckets --all: %s", out)
	}
	r := f.find("GET", "/accounts/"+acctID+"/r2/buckets")
	q, _ := url.ParseQuery(r.Query)
	if q.Get("per_page") != "2" || q.Get("direction") != "asc" {
		t.Fatalf("query: %s", r.Query)
	}
	// Enum validation.
	if _, _, err := runCLI(t, "", "api", "r2-bucket", "list-buckets", "--direction", "sideways"); err == nil || !strings.Contains(err.Error(), "asc, desc") {
		t.Fatalf("enum: %v", err)
	}

	// Positional path params.
	out = mustRun(t, "api", "r2-bucket", "get-bucket", "mybucket")
	if decode(t, out).(map[string]any)["name"] != "mybucket" {
		t.Fatalf("get-bucket: %s", out)
	}
	if _, _, err := runCLI(t, "", "api", "r2-bucket", "get-bucket"); err == nil || !strings.Contains(err.Error(), "<bucket_name>") {
		t.Fatalf("missing arg: %v", err)
	}
	// Path params are escaped.
	mustRun(t, "api", "workers-kv-namespace", "get-a-keys-metadata", "ns1", "a/b c")
	var esc string
	for _, r := range f.Requests() {
		if strings.Contains(r.Path, "/metadata/") {
			esc = r.EscapedPath
		}
	}
	if esc != "/client/v4/accounts/"+acctID+"/storage/kv/namespaces/ns1/metadata/a%2Fb%20c" {
		t.Errorf("path param not escaped: %q", esc)
	}

	// Required query param ("query" clashes with --query → --param-query).
	_, _, err := runCLI(t, "", "api", "analytics-engine", "execute-an-analytics-engine-sql-query-via-query-parameter")
	if err == nil || !strings.Contains(err.Error(), "--param-query") {
		t.Fatalf("required query: %v", err)
	}
	out = mustRun(t, "api", "analytics-engine", "execute-an-analytics-engine-sql-query-via-query-parameter", "--param-query", "SELECT 1")
	if q, _ := url.ParseQuery(decode(t, out).(map[string]any)["query"].(string)); q.Get("query") != "SELECT 1" {
		t.Fatalf("param-query: %s", out)
	}

	// Zone ops: --zone by name; JSON body.
	out = mustRun(t, "api", "dns-records-for-a-zone", "create-dns-record", "--zone", "example.com", "--data", `{"type":"A","name":"www","content":"192.0.2.9","ttl":1}`)
	if decode(t, out).(map[string]any)["echo"].(map[string]any)["content"] != "192.0.2.9" {
		t.Fatalf("create-dns-record: %s", out)
	}
	if _, _, err := runCLI(t, "", "api", "dns-records-for-a-zone", "create-dns-record", "--zone", "example.com"); err == nil || !strings.Contains(err.Error(), "request body") {
		t.Fatalf("required body: %v", err)
	}
	if _, _, err := runCLI(t, "", "api", "dns-records-for-a-zone", "get-dns-record-usage"); err == nil || !strings.Contains(err.Error(), "--zone") {
		t.Fatalf("missing zone: %v", err)
	}

	// Multipart op.
	dir := t.TempDir()
	js := filepath.Join(dir, "index.js")
	os.WriteFile(js, []byte("export default {}"), 0o600)
	mustRun(t, "api", "worker-script", "upload-worker-module", "hello",
		"--form", `metadata={"main_module":"index.js"};type=application/json`,
		"--form", "index.js=@"+js+";type=application/javascript+module")
	r = f.find("PUT", "/accounts/"+acctID+"/workers/scripts/hello")
	if r == nil || !strings.HasPrefix(r.ContentType, "multipart/form-data") {
		t.Fatalf("multipart: %+v", r)
	}

	// DELETE needs --yes when stdin isn't a terminal.
	_, _, err = runCLI(t, "", "api", "r2-bucket", "delete-bucket", "mybucket")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("delete without --yes: %v", err)
	}
	if f.find("DELETE", "/accounts/"+acctID+"/r2/buckets/mybucket") != nil {
		t.Fatal("DELETE sent without confirmation")
	}
	mustRun(t, "api", "r2-bucket", "delete-bucket", "mybucket", "--yes")
	if f.find("DELETE", "/accounts/"+acctID+"/r2/buckets/mybucket") == nil {
		t.Fatal("DELETE --yes not sent")
	}

	// --help shows method, path, and params.
	out = mustRun(t, "api", "r2-bucket", "get-bucket", "--help")
	for _, want := range []string{"GET /accounts/{account_id}/r2/buckets/{bucket_name}", "operationId: r2-get-bucket", "<bucket_name>"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
}

func TestAPIDiscovery(t *testing.T) {
	apiFake(t)
	out := mustRun(t, "api", "search", "r2", "list", "buckets")
	if !strings.Contains(out, "r2-bucket list-buckets") {
		t.Errorf("search: %s", out)
	}
	out = mustRun(t, "api", "list", "--tag", "r2-bucket", "--method", "delete", "--json")
	var rows []map[string]any
	_ = json.Unmarshal([]byte(out), &rows)
	if len(rows) == 0 {
		t.Fatalf("list: %s", out)
	}
	for _, r := range rows {
		if r["method"] != "DELETE" {
			t.Errorf("list --method: %v", r)
		}
	}
	out = mustRun(t, "api", "tags")
	if !strings.Contains(out, "dns-records-for-a-zone") {
		t.Error("tags missing dns")
	}
	out = mustRun(t, "api", "describe", "dns-records-for-a-zone", "create-dns-record")
	if !strings.Contains(out, "Request body (application/json") || !strings.Contains(out, "Example (--data)") {
		t.Errorf("describe: %s", out)
	}
	out2 := mustRun(t, "api", "describe", "dns-records-for-a-zone-create-dns-record")
	if out != out2 {
		t.Error("describe by operationId differs")
	}
	out = mustRun(t, "api", "spec-info", "--json")
	if info := decode(t, out).(map[string]any); info["operations"] != float64(len(apispec.Ops())) || info["commit"] != apispec.SpecCommit {
		t.Errorf("spec-info: %s", out)
	}
	SetVersion("")
	out = mustRun(t, "--version")
	if !strings.Contains(out, "0.2.") {
		t.Errorf("version: %s", out)
	}
}

// TestCoverage: every operation in the embedded spec is reachable through
// exactly one generated command, and describe works for all of them.
func TestCoverage(t *testing.T) {
	populateAllTags()
	seen := map[string]int{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if id, ok := c.Annotations[annOperationID]; ok {
			seen[id]++
			o, _ := apispec.ByID(id)
			// The command path must resolve back to this command.
			found, _, err := rootCmd.Find([]string{"api", o.TagSlug, o.Slug})
			if err != nil || found != c {
				t.Errorf("%s: 'api %s %s' does not resolve to its command", id, o.TagSlug, o.Slug)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	missing, dup := 0, 0
	for _, o := range apispec.Ops() {
		switch seen[o.ID] {
		case 0:
			missing++
			t.Errorf("operation %s (%s %s) has no command", o.ID, o.Method, o.Path)
		case 1:
		default:
			dup++
			t.Errorf("operation %s has %d commands", o.ID, seen[o.ID])
		}
		op := o
		if _, err := findOp([]string{op.TagSlug, op.Slug}); err != nil {
			t.Errorf("describe lookup %s: %v", op.ID, err)
		}
		if _, err := apispec.Describe(&op, commandFor(&op)); err != nil {
			t.Errorf("describe %s: %v", op.ID, err)
		}
	}
	if len(seen) != len(apispec.Ops()) {
		t.Errorf("%d commands for %d operations", len(seen), len(apispec.Ops()))
	}
	t.Logf("coverage: %d/%d operations reachable via exactly one command (missing %d, duplicated %d)",
		len(apispec.Ops())-missing-dup, len(apispec.Ops()), missing, dup)
}
