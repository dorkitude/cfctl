package cmd

import (
	"net/http"
	"sort"
	"strings"
	"testing"
)

// stHandler answers one fake API route for the storage command tests.
type stHandler func(w http.ResponseWriter, r *http.Request, req fakeRequest)

// stFake starts the fake API, logs in, and routes requests to handlers keyed
// "METHOD /path" relative to /accounts/{acctID}. A "*" segment matches one
// path segment; a trailing "**" matches the rest. Unmatched requests fall
// through to the built-in routes (and usually 404).
func stFake(t *testing.T, routes map[string]stHandler) *fakeCF {
	t.Helper()
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)
	patterns := make([]string, 0, len(routes))
	for k := range routes {
		patterns = append(patterns, k)
	}
	// Exact and longer patterns first.
	sort.Slice(patterns, func(i, j int) bool {
		wi, wj := strings.Count(patterns[i], "*"), strings.Count(patterns[j], "*")
		if wi != wj {
			return wi < wj
		}
		return len(patterns[i]) > len(patterns[j])
	})
	f.custom = func(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
		// Match on the escaped path so a key with "/" (sent as %2F) stays one segment.
		path := strings.TrimPrefix(strings.TrimPrefix(req.EscapedPath, "/client/v4"), "/accounts/"+acctID)
		key := req.Method + " " + path
		for _, p := range patterns {
			if stMatch(p, key) {
				routes[p](w, r, req)
				return true
			}
		}
		return false
	}
	return f
}

func stMatch(pattern, key string) bool {
	pm, pp, _ := strings.Cut(pattern, " ")
	km, kp, _ := strings.Cut(key, " ")
	if pm != km {
		return false
	}
	ps := strings.Split(strings.Trim(pp, "/"), "/")
	ks := strings.Split(strings.Trim(kp, "/"), "/")
	for i, s := range ps {
		if s == "**" {
			return len(ks) >= i
		}
		if i >= len(ks) || (s != "*" && s != ks[i]) {
			return false
		}
	}
	return len(ps) == len(ks)
}

// stJSON returns a handler that writes v as a successful envelope.
func stJSON(v any) stHandler {
	return func(w http.ResponseWriter, r *http.Request, req fakeRequest) { writeJSON(w, 200, ok(v)) }
}

// stList returns a handler that writes a one-page list.
func stListH(items []map[string]any) stHandler {
	return func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
			writeJSON(w, 200, okList([]any{}, 0))
			return
		}
		writeJSON(w, 200, okList(items, len(items)))
	}
}

// stRun runs the CLI and fails the test on error; it also checks the token
// never leaks.
func stRun(t *testing.T, stdin string, args ...string) (string, string) {
	t.Helper()
	out, errOut, err := runCLI(t, stdin, args...)
	assertNoToken(t, strings.Join(args, " "), out, errOut, errString(err))
	if err != nil {
		t.Fatalf("cfctl %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, errOut)
	}
	return out, errOut
}

// stRunErr runs the CLI and expects an error.
func stRunErr(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	out, errOut, err := runCLI(t, stdin, args...)
	assertNoToken(t, strings.Join(args, " "), out, errOut, errString(err))
	if err == nil {
		t.Fatalf("cfctl %s: expected an error\nstdout: %s", strings.Join(args, " "), out)
	}
	return out, err
}

// stMust fails unless s contains every want.
func stMust(t *testing.T, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("output missing %q:\n%s", w, s)
		}
	}
}

// stFind returns the first request with method and path (relative to the
// account) or fails.
func stFind(t *testing.T, f *fakeCF, method, path string) fakeRequest {
	t.Helper()
	for _, r := range f.Requests() {
		if r.Method == method && strings.TrimPrefix(r.Path, "/accounts/"+acctID) == path {
			return r
		}
	}
	var seen []string
	for _, r := range f.Requests() {
		seen = append(seen, r.Method+" "+r.Path)
	}
	t.Fatalf("no %s %s request; saw:\n%s", method, path, strings.Join(seen, "\n"))
	return fakeRequest{}
}

// stNone fails if any request with this method was sent to an account path.
func stNoMutation(t *testing.T, f *fakeCF) {
	t.Helper()
	for _, r := range f.Requests() {
		if r.Method != "GET" && !strings.HasSuffix(r.Path, "/graphql") {
			t.Errorf("unexpected %s %s", r.Method, r.Path)
		}
	}
}

func TestStMatch(t *testing.T) {
	cases := []struct {
		p, k string
		want bool
	}{
		{"GET /a/b", "GET /a/b", true},
		{"GET /a/*", "GET /a/b", true},
		{"GET /a/*", "GET /a/b/c", false},
		{"GET /a/**", "GET /a/b/c", true},
		{"POST /a/b", "GET /a/b", false},
	}
	for _, c := range cases {
		if got := stMatch(c.p, c.k); got != c.want {
			t.Errorf("stMatch(%q, %q) = %v", c.p, c.k, got)
		}
	}
}
