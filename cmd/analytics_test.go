package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyticsPresets(t *testing.T) {
	f := adminFake(t)
	cases := []struct {
		args []string
		out  []string
	}{
		{[]string{"analytics", "zone", "example.com"}, []string{"Requests:        40", "Cached:          20 (50.0%)", "404", "US"}},
		{[]string{"analytics", "zone", "example.com", "--since", "30d"}, []string{"Traffic for example.com"}},
		{[]string{"analytics", "countries", "example.com", "--limit", "1"}, []string{"US", "Top countries"}},
		{[]string{"analytics", "paths", "example.com", "--status", "404"}, []string{"/index.html", "90.0%"}},
		{[]string{"analytics", "firewall", "example.com"}, []string{"block", "BE", "2"}},
		{[]string{"analytics", "workers", "--since", "7d"}, []string{"api-worker", "2.0%", "9.0 ms"}},
		{[]string{"analytics", "r2", "--since", "30d"}, []string{"media", "3.0 GB", "42"}},
	}
	for _, c := range cases {
		name := strings.Join(c.args, " ")
		out, stderr, err := runCLI(t, "", c.args...)
		if err != nil {
			t.Errorf("%s: %v\n%s", name, err, stderr)
			continue
		}
		assertNoToken(t, name, out, stderr)
		for _, w := range c.out {
			if !strings.Contains(out, w) {
				t.Errorf("%s: output lacks %q:\n%s", name, w, out)
			}
		}
		jout, _, err := runCLI(t, "", append(append([]string{}, c.args...), "--json")...)
		if err != nil || !json.Valid([]byte(jout)) {
			t.Errorf("%s --json: %v\n%s", name, err, jout)
		}
	}
	// Hourly data for short ranges, daily for long ones; variables carry the zone ID.
	var hourly, daily bool
	for _, r := range f.Requests() {
		if r.Path != "/graphql" {
			continue
		}
		q := string(r.RawBody)
		hourly = hourly || strings.Contains(q, "httpRequests1hGroups") && strings.Contains(q, zoneID)
		daily = daily || strings.Contains(q, "httpRequests1dGroups")
	}
	if !hourly || !daily {
		t.Errorf("hourly=%v daily=%v", hourly, daily)
	}
	// Works in read-only mode (GraphQL queries are allowed).
	t.Setenv("CFCTL_READONLY", "1")
	if _, _, err := runCLI(t, "", "analytics", "zone", "example.com"); err != nil {
		t.Errorf("read-only: %v", err)
	}
	if _, _, err := runCLI(t, "", "analytics", "zone", "example.com", "--since", "bogus"); err == nil {
		t.Error("bad --since accepted")
	}
}

func TestDNSImportExport(t *testing.T) {
	f := adminFake(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "z.zone")
	if _, stderr, err := runCLI(t, "", "dns", "export", "example.com", "--out", out); err != nil {
		t.Fatalf("export: %v %s", err, stderr)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "192.0.2.1") {
		t.Fatalf("export file: %s", b)
	}
	if _, stderr, err := runCLI(t, "", "dns", "import", "example.com", out, "--proxied"); err != nil {
		t.Fatalf("import: %v %s", err, stderr)
	}
	r := lastRequest(f, "POST", zp+"/dns_records/import")
	if r == nil || !strings.HasPrefix(r.ContentType, "multipart/form-data") || !strings.Contains(string(r.RawBody), "192.0.2.1") || !strings.Contains(string(r.RawBody), `name="proxied"`) {
		t.Fatalf("import request: %+v", r)
	}
}

// Generated commands page through offset/limit endpoints with --all.
func TestGeneratedAllOffset(t *testing.T) {
	f := apiFake(t)
	inner := f.custom
	f.custom = func(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
		if req.Path == "/accounts/"+acctID+"/ai/finetunes/public" {
			off := r.URL.Query().Get("offset")
			switch off {
			case "", "0":
				writeJSON(w, 200, ok([]any{"a", "b"}))
			case "2":
				writeJSON(w, 200, ok([]any{"c"}))
			default:
				writeJSON(w, 200, ok([]any{}))
			}
			return true
		}
		return inner(w, r, req)
	}
	out := mustRun(t, "api", "request", "GET", "/accounts/{account_id}/ai/finetunes/public", "--all", "--query", "limit=2")
	if strings.Join(strings.Fields(out), "") != `["a","b","c"]` {
		t.Fatalf("got %s", out)
	}
}
