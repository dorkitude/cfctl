package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCase is one mutating command: the request it must send, and
// whether it needs confirmation.
type writeCase struct {
	args         []string
	method, path string
	body         string // substring of the JSON body
	confirm      bool
}

var adminWrites = []writeCase{
	{[]string{"zones", "settings", "set", "example.com", "min_tls_version", "1.2"}, "PATCH", zp + "/settings/min_tls_version", `"value":"1.2"`, false},
	{[]string{"zones", "settings", "set", "example.com", "always_use_https", "on"}, "PATCH", zp + "/settings/always_use_https", `"value":"on"`, false},
	{[]string{"zones", "create", "new.example", "--type", "partial"}, "POST", "/zones", `"type":"partial"`, false},
	{[]string{"zones", "delete", "example.com"}, "DELETE", zp, "", true},
	{[]string{"zones", "pause", "example.com"}, "PATCH", zp, `"paused":true`, true},
	{[]string{"zones", "unpause", "example.com"}, "PATCH", zp, `"paused":false`, false},
	{[]string{"zones", "activation-check", "example.com"}, "PUT", zp + "/activation_check", "", false},
	{[]string{"cache", "purge", "example.com", "--url", "https://example.com/a,https://example.com/b"}, "POST", zp + "/purge_cache", `"files":["https://example.com/a","https://example.com/b"]`, false},
	{[]string{"cache", "purge", "example.com", "--prefix", "example.com/assets/"}, "POST", zp + "/purge_cache", `"prefixes":["example.com/assets/"]`, false},
	{[]string{"cache", "purge", "example.com", "--everything"}, "POST", zp + "/purge_cache", `"purge_everything":true`, true},
	{[]string{"cache", "dev-mode", "example.com", "on"}, "PATCH", zp + "/settings/development_mode", `"value":"on"`, false},
	{[]string{"ssl", "mode", "example.com", "strict"}, "PATCH", zp + "/settings/ssl", `"value":"strict"`, false},
	{[]string{"ssl", "universal", "example.com", "--disable"}, "PATCH", zp + "/ssl/universal/settings", `"enabled":false`, true},
	{[]string{"ssl", "packs", "order", "example.com", "--host", "example.com,*.example.com"}, "POST", zp + "/ssl/certificate_packs/order", `"hosts":["example.com","*.example.com"]`, false},
	{[]string{"ssl", "packs", "delete", "example.com", "p1"}, "DELETE", zp + "/ssl/certificate_packs/p1", "", true},
	{[]string{"ssl", "origin", "revoke", "c1"}, "DELETE", "/certificates/c1", "", true},
	{[]string{"ssl", "custom", "delete", "example.com", "c1"}, "DELETE", zp + "/custom_certificates/c1", "", true},
	{[]string{"rulesets", "rules", "add", "rs-1", "--zone", "example.com", "--expression", "true", "--action", "log"}, "POST", zp + "/rulesets/rs-1/rules", `"action":"log"`, false},
	{[]string{"rulesets", "rules", "add", "--phase", "cache", "--zone", "example.com", "--expression", "true", "--action", "set_cache_settings", "--action-parameters", `{"cache":true}`}, "PUT", zp + "/rulesets/phases/http_request_cache_settings/entrypoint", `"action_parameters":{"cache":true}`, false},
	{[]string{"rulesets", "rules", "add", "--phase", "waf", "--zone", "example.com", "--expression", "true", "--action", "block"}, "POST", zp + "/rulesets/rs-1/rules", `"action":"block"`, false},
	{[]string{"rulesets", "rules", "update", "rs-1", "rule-1", "--enabled=false"}, "PATCH", ap + "/rulesets/rs-1/rules/rule-1", `"enabled":false`, false},
	{[]string{"rulesets", "rules", "delete", "rs-1", "rule-1", "--zone", "example.com"}, "DELETE", zp + "/rulesets/rs-1/rules/rule-1", "", true},
	{[]string{"rulesets", "delete", "rs-1"}, "DELETE", ap + "/rulesets/rs-1", "", true},
	{[]string{"redirects", "add", "example.com", "--from", "https://www.example.com/*", "--to", "https://example.com/${1}"}, "POST", zp + "/rulesets/rs-1/rules", `wildcard_replace(http.request.full_uri`, false},
	{[]string{"redirects", "add", "example.com", "--from", "https://example.com/old", "--to", "https://example.com/new", "--status", "302"}, "POST", zp + "/rulesets/rs-1/rules", `"status_code":302`, false},
	{[]string{"redirects", "delete", "example.com", "rule-1"}, "DELETE", zp + "/rulesets/rs-1/rules/rule-1", "", true},
	{[]string{"redirects", "bulk", "add", "l1", "--from", "example.com/a", "--to", "https://example.com/b"}, "POST", ap + "/rules/lists/l1/items", `"source_url":"example.com/a"`, false},
	{[]string{"transform", "add", "example.com", "--type", "response-headers", "--expression", "true", "--set-header", "X-Frame-Options: DENY", "--remove-header", "X-Debug"}, "POST", zp + "/rulesets/rs-1/rules", `"X-Frame-Options":{"operation":"set","value":"DENY"}`, false},
	{[]string{"transform", "add", "example.com", "--type", "url-rewrite", "--expression", "true", "--path", "/new"}, "PUT", zp + "/rulesets/phases/http_request_transform/entrypoint", `"path":{"value":"/new"}`, false},
	{[]string{"transform", "delete", "example.com", "rule-1"}, "DELETE", zp + "/rulesets/rs-1/rules/rule-1", "", true},
	{[]string{"waf", "custom", "add", "example.com", "--expression", "(ip.src.country eq \"T1\")", "--action", "block"}, "POST", zp + "/rulesets/rs-1/rules", `"action":"block"`, false},
	{[]string{"waf", "custom", "delete", "example.com", "rule-1"}, "DELETE", zp + "/rulesets/rs-1/rules/rule-1", "", true},
	{[]string{"page-rules", "delete", "example.com", "pr1"}, "DELETE", zp + "/pagerules/pr1", "", true},
	{[]string{"firewall", "access-rules", "create", "198.51.100.0/24", "--mode", "challenge", "--zone", "example.com"}, "POST", zp + "/firewall/access_rules/rules", `"target":"ip_range"`, false},
	{[]string{"firewall", "access-rules", "create", "AS64496"}, "POST", ap + "/firewall/access_rules/rules", `"value":"64496"`, false},
	{[]string{"firewall", "access-rules", "delete", "r1"}, "DELETE", ap + "/firewall/access_rules/rules/r1", "", true},
	{[]string{"lists", "create", "bad_ips", "--kind", "ip"}, "POST", ap + "/rules/lists", `"kind":"ip"`, false},
	{[]string{"lists", "delete", "l1"}, "DELETE", ap + "/rules/lists/l1", "", true},
	{[]string{"lists", "items-remove", "l1", "i1", "--item", "i2"}, "DELETE", ap + "/rules/lists/l1/items", `{"items":[{"id":"i1"},{"id":"i2"}]}`, true},
	{[]string{"lb", "create", "example.com", "--data", `{"name":"lb.example.com"}`}, "POST", zp + "/load_balancers", `"name":"lb.example.com"`, false},
	{[]string{"lb", "delete", "example.com", "lb1"}, "DELETE", zp + "/load_balancers/lb1", "", true},
	{[]string{"lb", "pools", "update", "p1", "--data", `{"enabled":false}`}, "PATCH", ap + "/load_balancers/pools/p1", `"enabled":false`, false},
	{[]string{"lb", "pools", "delete", "p1"}, "DELETE", ap + "/load_balancers/pools/p1", "", true},
	{[]string{"lb", "monitors", "delete", "m1"}, "DELETE", ap + "/load_balancers/monitors/m1", "", true},
	{[]string{"members", "remove", "m1"}, "DELETE", ap + "/members/m1", "", true},
	{[]string{"tokens", "delete", "t1"}, "DELETE", ap + "/tokens/t1", "", true},
	{[]string{"logpush", "jobs", "create", "--zone", "example.com", "--dataset", "http_requests", "--destination", "r2://b/{DATE}", "--fields", "ClientIP,EdgeResponseStatus"}, "POST", zp + "/logpush/jobs", `"field_names":["ClientIP","EdgeResponseStatus"]`, false},
	{[]string{"logpush", "jobs", "delete", "j1"}, "DELETE", ap + "/logpush/jobs/j1", "", true},
	{[]string{"notifications", "policies", "delete", "p1"}, "DELETE", ap + "/alerting/v3/policies/p1", "", true},
	{[]string{"healthchecks", "delete", "example.com", "h1"}, "DELETE", zp + "/healthchecks/h1", "", true},
	{[]string{"waiting-rooms", "delete", "example.com", "w1"}, "DELETE", zp + "/waiting_rooms/w1", "", true},
	{[]string{"spectrum", "apps", "delete", "example.com", "a1"}, "DELETE", zp + "/spectrum/apps/a1", "", true},
	{[]string{"dns", "dnssec", "enable", "example.com"}, "PATCH", zp + "/dnssec", `"status":"active"`, false},
	{[]string{"dns", "dnssec", "disable", "example.com"}, "PATCH", zp + "/dnssec", `"status":"disabled"`, true},
	{[]string{"dns", "settings", "set", "example.com", "nameservers.type", "cloudflare.standard"}, "PATCH", zp + "/dns_settings", `{"nameservers":{"type":"cloudflare.standard"}}`, false},
}

func TestAdminWrites(t *testing.T) {
	f := adminFake(t)
	for _, c := range adminWrites {
		name := strings.Join(c.args, " ")
		if c.confirm {
			// No terminal and no --yes: refused before anything is sent.
			before := len(f.Requests())
			_, _, err := runCLI(t, "y\n", c.args...)
			if err == nil || !strings.Contains(err.Error(), "pass --yes") {
				t.Errorf("%s: expected confirmation refusal, got %v", name, err)
			}
			for _, r := range f.Requests()[before:] {
				if r.Method != "GET" {
					t.Errorf("%s: sent %s %s without confirmation", name, r.Method, r.Path)
				}
			}
		}
		args := c.args
		if c.confirm {
			args = append(append([]string{}, c.args...), "--yes")
		}
		out, stderr, err := runCLI(t, "", args...)
		if err != nil {
			t.Errorf("%s: %v\n%s", name, err, stderr)
			continue
		}
		assertNoToken(t, name, out, stderr)
		r := lastRequest(f, c.method, c.path)
		if r == nil {
			t.Errorf("%s: no %s %s", name, c.method, c.path)
			continue
		}
		if c.body != "" && !strings.Contains(string(r.RawBody), c.body) {
			t.Errorf("%s: body %s lacks %s", name, r.RawBody, c.body)
		}
	}
}

// In read-only mode every write is refused by the guard, without prompting.
func TestAdminWritesReadOnly(t *testing.T) {
	f := adminFake(t)
	t.Setenv("CFCTL_READONLY", "1")
	before := len(f.Requests())
	for _, c := range adminWrites {
		_, _, err := runCLI(t, "", c.args...)
		if err == nil || !strings.Contains(err.Error(), "read-only mode") {
			t.Errorf("%s: expected read-only refusal, got %v", strings.Join(c.args, " "), err)
		}
	}
	for _, r := range f.Requests()[before:] {
		if r.Method != "GET" {
			t.Errorf("guard let %s %s through", r.Method, r.Path)
		}
	}
}

func TestAdminValidation(t *testing.T) {
	adminFake(t)
	for args, want := range map[string]string{
		"cache purge example.com":                                                "say what to purge",
		"cache purge example.com --everything --url x":                           "can't be combined",
		"cache purge example.com --url a --tag b":                                "one kind at a time",
		"ssl mode example.com medium":                                            "mode must be",
		"redirects add example.com --to https://x":                               "either --from",
		"redirects add example.com --from https://a --to https://b --status 200": "--status must be",
		"transform add example.com --type nope --expression true":                "--type must be",
		"transform add example.com --type url-rewrite --expression true":         "needs --path",
		"rulesets rules add --zone example.com --phase waf":                      "needs --expression and --action",
		"rulesets rules add rs-1 --phase waf --expression true --action log":     "either a ruleset ID or --phase",
		"rulesets rules update rs-1 rule-1":                                      "nothing to update",
		"firewall access-rules create not-a-thing":                               "can't tell what",
		"lb create example.com":                                                  "pass the request body with --data",
		"tokens create --name x":                                                 "pass --name, --policies, and --value-out",
		"ssl origin create example.com":                                          "--key-out",
	} {
		_, _, err := runCLI(t, "", strings.Fields(args)...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", args, want, err)
		}
	}
}

// Token values and Origin CA keys go to files and never to output.
func TestSecretsStayInFiles(t *testing.T) {
	f := adminFake(t)
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "new.token")
	out, stderr, err := runCLI(t, "", "tokens", "create", "--name", "ci", "--policies", `[{"effect":"allow"}]`, "--value-out", tokFile, "--json")
	if err != nil {
		t.Fatalf("tokens create: %v\n%s", err, stderr)
	}
	if strings.Contains(out+stderr, "NEW-TOKEN-SECRET-VALUE") {
		t.Fatalf("token value printed: %s", out)
	}
	b, _ := os.ReadFile(tokFile)
	if strings.TrimSpace(string(b)) != "NEW-TOKEN-SECRET-VALUE" {
		t.Fatalf("token file: %q", b)
	}
	if st, _ := os.Stat(tokFile); st.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %v", st.Mode().Perm())
	}
	if _, _, err := runCLI(t, "", "tokens", "create", "--name", "ci", "--policies", `[]`, "--value-out", tokFile); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("overwrite: %v", err)
	}

	keyFile, certFile := filepath.Join(dir, "origin.key"), filepath.Join(dir, "origin.pem")
	out, stderr, err = runCLI(t, "", "ssl", "origin", "create", "example.com", "--key-out", keyFile, "--cert-out", certFile)
	if err != nil {
		t.Fatalf("origin create: %v\n%s", err, stderr)
	}
	key, _ := os.ReadFile(keyFile)
	if !strings.Contains(string(key), "PRIVATE KEY") || strings.Contains(out+stderr, "PRIVATE KEY") {
		t.Fatalf("key handling: file=%d bytes, output=%s", len(key), out)
	}
	if st, _ := os.Stat(keyFile); st.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v", st.Mode().Perm())
	}
	r := lastRequest(f, "POST", "/certificates")
	if r == nil || !strings.Contains(string(r.RawBody), "BEGIN CERTIFICATE REQUEST") || strings.Contains(string(r.RawBody), "PRIVATE KEY") {
		t.Fatalf("CSR request: %+v", r)
	}
	var body map[string]any
	_ = json.Unmarshal(r.RawBody, &body)
	if hs, _ := body["hostnames"].([]any); len(hs) != 2 || hs[1] != "*.example.com" {
		t.Errorf("default hostnames: %v", body["hostnames"])
	}
}

func TestAccessRuleTarget(t *testing.T) {
	for in, want := range map[string]string{"192.0.2.1": "ip", "2001:db8::1": "ip6", "10.0.0.0/8": "ip_range", "AS13335": "asn", "13335": "asn", "us": "country", "nope!": ""} {
		if got, _ := accessRuleTarget(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestRedactDestination(t *testing.T) {
	got := redactDestination("s3://b/p?region=us&access-key-id=AK&secret-access-key=SK&sas-token=x")
	if strings.Contains(got, "SK") || strings.Contains(got, "=x") || !strings.Contains(got, "region=us") {
		t.Errorf("got %s", got)
	}
	if got := redactDestination("https://user:pass@example.com/logs"); strings.Contains(got, "pass") {
		t.Errorf("userinfo: %s", got)
	}
}
