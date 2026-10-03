package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Distinctive token values so tests can assert they never leak into output.
const (
	goodToken = "cfctl-test-SECRET-token-0123456789abcdef"
	badToken  = "cfctl-test-BAD-token-fedcba9876543210"
	// Account-owned tokens: with the cfat_ prefix, without it, and disabled.
	acctToken         = "cfat_cfctl-test-SECRET-account-token-0123"
	acctTokenNoPrefix = "cfctl-test-SECRET-account-token-noprefix"
	disabledAcctToken = "cfat_cfctl-test-SECRET-disabled-token-99"

	acctID   = "0123456789abcdef0123456789abcdef"
	acctID2  = "fedcba9876543210fedcba9876543210"
	zoneID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	recordID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeRequest is one request seen by the fake Cloudflare API.
type fakeRequest struct {
	Method string
	Path   string
	Query  string
	Body   map[string]interface{}
	Auth   string
}

// fakeCF is a tiny in-memory Cloudflare API v4.
type fakeCF struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []fakeRequest
	accounts []map[string]interface{}
	domains  map[string]map[string]interface{} // registrar registrations by name
	records  []map[string]interface{}
}

func newFakeCF(t *testing.T) *fakeCF {
	t.Helper()
	f := &fakeCF{
		t: t,
		accounts: []map[string]interface{}{
			{"id": acctID, "name": "Kyle's Account", "type": "standard"},
		},
		domains: map[string]map[string]interface{}{
			"example.com": {"domain_name": "example.com", "status": "active", "auto_renew": true, "locked": true, "privacy_mode": "redaction", "created_at": "2025-01-15T10:00:00Z", "expires_at": "2027-01-15T10:00:00Z"},
			"mybrand.dev": {"domain_name": "mybrand.dev", "status": "active", "auto_renew": false, "locked": true, "privacy_mode": "redaction", "created_at": "2025-03-20T14:30:00Z", "expires_at": "2026-03-20T14:30:00Z"},
		},
		records: []map[string]interface{}{
			{"id": recordID, "type": "A", "name": "www.example.com", "content": "1.2.3.4", "ttl": 1, "proxied": true, "proxiable": true},
			{"id": "cccccccccccccccccccccccccccccccc", "type": "MX", "name": "example.com", "content": "mail.example.com", "ttl": 3600, "priority": 10, "proxied": false, "proxiable": false},
		},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// BaseURL mimics the real https://api.cloudflare.com/client/v4.
func (f *fakeCF) BaseURL() string { return f.srv.URL + "/client/v4" }

func (f *fakeCF) Requests() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.requests...)
}

// find returns the first request matching method and path.
func (f *fakeCF) find(method, path string) *fakeRequest {
	for _, r := range f.Requests() {
		if r.Method == method && r.Path == path {
			r := r
			return &r
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ok(result interface{}) map[string]interface{} {
	return map[string]interface{}{"success": true, "errors": []interface{}{}, "messages": []interface{}{}, "result": result}
}

func okList(result interface{}, n int) map[string]interface{} {
	m := ok(result)
	m["result_info"] = map[string]interface{}{"page": 1, "per_page": 50, "count": n, "total_count": n, "total_pages": 1, "cursor": ""}
	return m
}

func fail(code int, msg string) map[string]interface{} {
	return map[string]interface{}{"success": false, "errors": []interface{}{map[string]interface{}{"code": code, "message": msg}}, "messages": []interface{}{}, "result": nil}
}

func (f *fakeCF) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	req := fakeRequest{Method: r.Method, Path: path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &req.Body)
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	token := strings.TrimPrefix(req.Auth, "Bearer ")
	accountToken := token == acctToken || token == acctTokenNoPrefix || token == disabledAcctToken
	if token != goodToken && !accountToken {
		writeJSON(w, http.StatusUnauthorized, fail(1000, "Invalid API Token"))
		return
	}
	// V4 page pagination keeps asking until it gets an empty page.
	lastPage := r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1"

	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == "GET" && path == "/user/tokens/verify":
		if accountToken {
			writeJSON(w, http.StatusUnauthorized, fail(1000, "Invalid API Token"))
			return
		}
		writeJSON(w, 200, ok(map[string]interface{}{"id": "tok-id-123", "status": "active"}))

	case r.Method == "GET" && len(parts) == 4 && parts[0] == "accounts" && parts[2] == "tokens" && parts[3] == "verify":
		// Account-owned tokens verify only against their own account.
		if !accountToken || parts[1] != acctID {
			writeJSON(w, http.StatusUnauthorized, fail(1000, "Invalid API Token"))
			return
		}
		status := "active"
		if token == disabledAcctToken {
			status = "disabled"
		}
		writeJSON(w, 200, ok(map[string]interface{}{"id": "acct-tok-id-456", "status": status}))

	case r.Method == "GET" && path == "/user":
		writeJSON(w, 403, fail(9109, "Unauthorized to access requested resource"))

	case r.Method == "GET" && path == "/accounts":
		if lastPage {
			writeJSON(w, 200, okList([]interface{}{}, 0))
			return
		}
		if accountToken {
			// An account token only sees its owning account.
			writeJSON(w, 200, okList(f.accounts[:1], 1))
			return
		}
		writeJSON(w, 200, okList(f.accounts, len(f.accounts)))

	case r.Method == "GET" && len(parts) == 2 && parts[0] == "accounts":
		for _, a := range f.accounts {
			if a["id"] == parts[1] {
				writeJSON(w, 200, ok(a))
				return
			}
		}
		writeJSON(w, 404, fail(7003, "Could not route"))

	case len(parts) >= 4 && parts[0] == "accounts" && parts[2] == "registrar" && parts[3] == "registrations":
		f.serveRegistrar(w, r, req, parts)

	case r.Method == "GET" && path == "/zones":
		if lastPage {
			writeJSON(w, 200, okList([]interface{}{}, 0))
			return
		}
		name := r.URL.Query().Get("name")
		var out []interface{}
		if name == "" || name == "example.com" || strings.HasPrefix(name, "contains:") {
			out = append(out, map[string]interface{}{"id": zoneID, "name": "example.com", "status": "active", "paused": false, "name_servers": []string{"ada.ns.cloudflare.com", "bob.ns.cloudflare.com"}})
		}
		writeJSON(w, 200, okList(out, len(out)))

	case len(parts) >= 3 && parts[0] == "zones" && parts[1] == zoneID && parts[2] == "dns_records":
		f.serveRecords(w, r, req, parts, lastPage)

	default:
		writeJSON(w, 404, fail(7003, "No route for that URI"))
	}
}

func (f *fakeCF) serveRegistrar(w http.ResponseWriter, r *http.Request, req fakeRequest, parts []string) {
	if len(parts) == 4 && r.Method == "GET" {
		var out []interface{}
		for _, name := range []string{"example.com", "mybrand.dev"} {
			out = append(out, f.domains[name])
		}
		writeJSON(w, 200, okList(out, len(out)))
		return
	}
	if len(parts) != 5 {
		writeJSON(w, 404, fail(7003, "No route"))
		return
	}
	d, found := f.domains[parts[4]]
	if !found {
		writeJSON(w, 404, fail(10000, "Domain not found"))
		return
	}
	switch r.Method {
	case "GET":
		writeJSON(w, 200, ok(d))
	case "PATCH":
		if v, ok := req.Body["auto_renew"].(bool); ok {
			d["auto_renew"] = v
		}
		writeJSON(w, 200, ok(map[string]interface{}{
			"completed": true, "state": "succeeded",
			"created_at": "2026-10-03T00:00:00Z", "updated_at": "2026-10-03T00:00:02Z",
			"links":   map[string]interface{}{"self": "/x/update-status", "resource": "/x"},
			"context": map[string]interface{}{"domain_name": parts[4], "registration": d},
		}))
	default:
		writeJSON(w, 405, fail(10000, "Method not allowed"))
	}
}

func (f *fakeCF) serveRecords(w http.ResponseWriter, r *http.Request, req fakeRequest, parts []string, lastPage bool) {
	switch {
	case len(parts) == 3 && r.Method == "GET":
		if lastPage {
			writeJSON(w, 200, okList([]interface{}{}, 0))
			return
		}
		writeJSON(w, 200, okList(f.records, len(f.records)))
	case len(parts) == 3 && r.Method == "POST":
		rec := map[string]interface{}{"id": "dddddddddddddddddddddddddddddddd", "proxiable": true}
		for k, v := range req.Body {
			rec[k] = v
		}
		if _, ok := rec["proxied"]; !ok {
			rec["proxied"] = false
		}
		writeJSON(w, 200, ok(rec))
	case len(parts) == 4 && r.Method == "DELETE":
		writeJSON(w, 200, ok(map[string]interface{}{"id": parts[3]}))
	case len(parts) == 4 && r.Method == "PATCH":
		rec := map[string]interface{}{}
		for k, v := range f.records[0] {
			rec[k] = v
		}
		for k, v := range req.Body {
			rec[k] = v
		}
		writeJSON(w, 200, ok(rec))
	default:
		writeJSON(w, 404, fail(81044, "Record does not exist."))
	}
}

// testEnv isolates config dir and env vars, and points cfctl at the fake.
func testEnv(t *testing.T, f *fakeCF) string {
	t.Helper()
	dir := t.TempDir() + "/cfctl"
	t.Setenv("CFCTL_CONFIG_DIR", dir)
	t.Setenv("CFCTL_API_BASE_URL", f.BaseURL())
	for _, k := range []string{"CFCTL_TOKEN", "CLOUDFLARE_API_TOKEN", "CFCTL_ACCOUNT_ID", "CLOUDFLARE_API_KEY", "CLOUDFLARE_EMAIL", "CLOUDFLARE_BASE_URL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return dir
}

// resetFlags puts every flag back to its default between in-process runs.
func resetFlags(c *cobra.Command) {
	reset := func(fl *pflag.Flag) {
		_ = fl.Value.Set(fl.DefValue)
		fl.Changed = false
	}
	c.Flags().VisitAll(reset)
	c.PersistentFlags().VisitAll(reset)
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
}

// runCLI runs cfctl in-process with the given stdin, capturing stdout/stderr.
func runCLI(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	resetFlags(rootCmd)

	in, _ := os.CreateTemp(t.TempDir(), "stdin")
	_, _ = in.WriteString(stdin)
	_, _ = in.Seek(0, 0)
	defer in.Close()

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, outW, errW

	var wg sync.WaitGroup
	var outB, errB strings.Builder
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outB, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errB, errR) }()

	rootCmd.SetArgs(args)
	err = rootCmd.Execute()

	outW.Close()
	errW.Close()
	wg.Wait()
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	return outB.String(), errB.String(), err
}

// login stores goodToken via piped stdin.
func login(t *testing.T) {
	t.Helper()
	if _, _, err := runCLI(t, goodToken+"\n", "auth", "login"); err != nil {
		t.Fatalf("login failed: %v", err)
	}
}

// assertNoToken fails if any token value appears in the given strings.
func assertNoToken(t *testing.T, where string, ss ...string) {
	t.Helper()
	for _, s := range ss {
		for _, tok := range []string{goodToken, badToken, acctToken, acctTokenNoPrefix, disabledAcctToken} {
			if strings.Contains(s, tok) {
				t.Errorf("%s leaked the token: %q", where, s)
			}
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprint(err)
}
