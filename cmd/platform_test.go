package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// platFakeT is the fake API plus fixtures keyed by "METHOD /path" (path
// under /client/v4, query ignored). A fixture is either a result value
// (wrapped in a success envelope) or a func returning (status, body).
type platFakeT struct {
	*fakeCF
	routes map[string]any
}

const platA = "/accounts/" + acctID

func platFake(t *testing.T) *platFakeT {
	t.Helper()
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)
	p := &platFakeT{fakeCF: f, routes: map[string]any{}}
	f.custom = func(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
		if h, ok := p.routes[r.Method+" "+req.Path]; ok {
			switch v := h.(type) {
			case func(w http.ResponseWriter, r *http.Request, req fakeRequest):
				v(w, r, req)
			case func(req fakeRequest) (int, any):
				st, body := v(req)
				writeJSON(w, st, body)
			default:
				writeJSON(w, 200, okList(v, 1))
			}
			return true
		}
		if strings.HasPrefix(req.Path, platA+"/") || (strings.HasPrefix(req.Path, "/zones/"+zoneID+"/") && !strings.HasPrefix(req.Path, "/zones/"+zoneID+"/dns_records")) {
			writeJSON(w, 200, ok(map[string]any{"method": r.Method, "path": req.Path}))
			return true
		}
		return false
	}
	return p
}

// last returns the last request to method+path (or fails).
func (p *platFakeT) last(t *testing.T, method, path string) fakeRequest {
	t.Helper()
	reqs := p.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == method && reqs[i].Path == path {
			return reqs[i]
		}
	}
	var seen []string
	for _, r := range reqs {
		seen = append(seen, r.Method+" "+r.Path)
	}
	t.Fatalf("no %s %s; saw:\n  %s", method, path, strings.Join(seen, "\n  "))
	return fakeRequest{}
}

// run runs cfctl and fails on error; output is checked for token leaks.
func platRunOK(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, stderr, err := runCLI(t, stdin, args...)
	if err != nil {
		t.Fatalf("cfctl %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, stderr)
	}
	assertNoToken(t, strings.Join(args, " "), out, stderr)
	return out + stderr
}

func platRunErr(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, stderr, err := runCLI(t, stdin, args...)
	if err == nil {
		t.Fatalf("cfctl %s: expected an error\nstdout: %s", strings.Join(args, " "), out)
	}
	assertNoToken(t, strings.Join(args, " "), out, stderr, err.Error())
	return err.Error()
}

// platCase is one table-driven command check.
type platCase struct {
	args   []string
	method string
	path   string
	// body: expected top-level JSON body fields (compared as JSON).
	body map[string]any
	// query substring.
	query string
	// want: substring of the output.
	want string
}

func platCheck(t *testing.T, p *platFakeT, cases []platCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(strings.Join(c.args, "_"), func(t *testing.T) {
			out := platRunOK(t, "", c.args...)
			r := p.last(t, c.method, c.path)
			if c.query != "" && !strings.Contains(r.Query, c.query) {
				t.Errorf("query %q lacks %q", r.Query, c.query)
			}
			for k, want := range c.body {
				got, _ := json.Marshal(r.Body[k])
				w, _ := json.Marshal(want)
				if string(got) != string(w) {
					t.Errorf("body[%s] = %s, want %s (body %s)", k, got, w, r.RawBody)
				}
			}
			if c.want != "" && !strings.Contains(out, c.want) {
				t.Errorf("output lacks %q:\n%s", c.want, out)
			}
		})
	}
}

func TestPlatformKitRedactAndGet(t *testing.T) {
	raw := platRedact(json.RawMessage(`{"a":{"secret":"s3"},"list":[{"secret":"x"},{"secret":""}],"keep":"k"}`), []string{"a.secret", "list.secret"})
	if strings.Contains(string(raw), "s3") || strings.Contains(string(raw), `"x"`) || !strings.Contains(string(raw), `"keep":"k"`) {
		t.Fatalf("redact: %s", raw)
	}
	if got := string(platRedact(json.RawMessage(`"tok"`), []string{""})); strings.Contains(got, "tok") {
		t.Fatalf("bare string not redacted: %s", got)
	}
	var v any
	_ = json.Unmarshal([]byte(`{"a":{"b":[{"c":1}]},"d":"","e":"x"}`), &v)
	if platStr(platGet(v, "a.b.0.c")) != "1" || platStr(platGet(v, "d|e")) != "x" || platGet(v, "a.b.9.c") != nil {
		t.Fatal("platGet paths")
	}
	if platStr("2026-10-03T02:56:01.123Z") != "2026-10-03 02:56" || platStr(true) != "yes" || platStr(2.5) != "2.5" {
		t.Fatal("platStr formatting")
	}
}

func TestPlatformFriendlyErrors(t *testing.T) {
	p := platFake(t)
	p.routes["GET "+platA+"/containers/applications"] = func(req fakeRequest) (int, any) {
		return 401, fail(1000, `{"error":"Unauthorized: You do not have access to Cloudflare Containers."}`)
	}
	p.routes["GET "+platA+"/workflows"] = func(req fakeRequest) (int, any) { return 403, fail(10000, "Authentication error") }
	msg := platRunErr(t, "", "containers", "list")
	if !strings.Contains(msg, "You do not have access to Cloudflare Containers") || strings.Contains(msg, `{"error"`) || !strings.Contains(msg, "plan upgrade") {
		t.Fatalf("401 message: %s", msg)
	}
	msg = platRunErr(t, "", "workflows", "list")
	if !strings.Contains(msg, "Workflows may not be enabled") {
		t.Fatalf("403 message: %s", msg)
	}
}

func TestPlatformConfirmAndReadOnly(t *testing.T) {
	p := platFake(t)
	// Destructive commands refuse without --yes when stdin isn't a terminal.
	msg := platRunErr(t, "", "pages", "project", "delete", "site")
	if !strings.Contains(msg, "--yes") {
		t.Fatalf("confirm: %s", msg)
	}
	for _, r := range p.Requests() {
		if r.Method == "DELETE" {
			t.Fatal("DELETE sent without confirmation")
		}
	}
	platRunOK(t, "", "pages", "project", "delete", "site", "--yes")
	p.last(t, "DELETE", platA+"/pages/projects/site")

	// Read-only mode refuses writes before sending.
	t.Setenv("CFCTL_READONLY", "1")
	n := len(p.Requests())
	for _, args := range [][]string{
		{"pages", "project", "create", "x"},
		{"workflows", "trigger", "wf"},
		{"turnstile", "widget", "delete", "0x4AAA", "--yes"},
		{"ai", "run", "@cf/meta/llama-3.1-8b-instruct", "--prompt", "hi"},
		{"ai", "run", "@cf/meta/llama-3.1-8b-instruct", "--prompt", "hi", "--stream"},
		{"tunnel", "create", "t1"},
	} {
		msg := platRunErr(t, "", args...)
		if !strings.Contains(msg, "read-only") {
			t.Fatalf("%v: %s", args, msg)
		}
	}
	for _, r := range p.Requests()[n:] {
		if r.Method != "GET" {
			t.Fatalf("read-only sent %s %s", r.Method, r.Path)
		}
	}
	// Reads still work.
	platRunOK(t, "", "workflows", "list")
}
