package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// adminItem is the canned object the admin fake returns for list/get reads.
var adminItem = map[string]any{
	"id": "item-1", "name": "thing-one", "status": "active", "enabled": true, "value": "on", "editable": true,
	"kind": "zone", "phase": "http_request_firewall_custom", "version": "3", "type": "universal",
	"hosts": []any{"example.com", "*.example.com"}, "email": "a@example.com",
	"user":  map[string]any{"email": "member@example.com"},
	"roles": []any{map[string]any{"name": "Administrator"}},
	"rules": []any{map[string]any{"id": "rule-1", "action": "block", "expression": "(ip.src eq 192.0.2.1)", "enabled": true,
		"action_parameters": map[string]any{"from_value": map[string]any{"target_url": map[string]any{"value": "https://example.com/new"}, "status_code": 301}}}},
	"configuration": map[string]any{"target": "ip", "value": "192.0.2.1"},
	"mode":          "block",
}

// adminFake is an apiFake-style server for the admin commands: every GET
// under the account, zone, /user, /certificates, and /memberships returns
// [adminItem] (or adminItem for phase entrypoints), writes echo their body.
func adminFake(t *testing.T) *fakeCF {
	t.Helper()
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)
	f.custom = func(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
		p := req.Path
		inScope := strings.HasPrefix(p, "/accounts/"+acctID+"/") || strings.HasPrefix(p, "/zones/"+zoneID+"/") ||
			p == "/zones/"+zoneID || strings.HasPrefix(p, "/user") || strings.HasPrefix(p, "/certificates") || p == "/memberships" || p == "/graphql"
		if !inScope || strings.HasPrefix(p, "/zones/"+zoneID+"/dns_records") && !strings.Contains(p, "/export") && !strings.Contains(p, "/import") {
			if p == "/zones" && r.Method == "POST" {
				writeJSON(w, 200, ok(map[string]any{"id": "newzone", "name": req.Body["name"], "status": "pending", "name_servers": []any{"a.ns.cloudflare.com"}}))
				return true
			}
			return false
		}
		switch {
		case r.Method == "GET" && strings.Contains(p, "/logpush/jobs"):
			job := map[string]any{"id": "item-1", "name": "job", "dataset": "http_requests",
				"destination_conf": "s3://bucket/logs?region=us-east-1&access-key-id=AKIDEXAMPLE&secret-access-key=SuperSecretValue"}
			if strings.HasSuffix(p, "/jobs") {
				writeJSON(w, 200, okList([]any{job}, 1))
			} else {
				writeJSON(w, 200, ok(job))
			}
		case p == "/graphql":
			serveGraphQL(w, req)
		case p == "/user/tokens/verify":
			writeJSON(w, 403, fail(9109, "Valid user-level authentication not found"))
		case strings.HasSuffix(p, "/dns_records/export"):
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(";; zone file\nexample.com. 1 IN A 192.0.2.1\n"))
		case strings.HasSuffix(p, "/entrypoint") && r.Method == "GET":
			if strings.Contains(p, "http_request_dynamic_redirect") || strings.Contains(p, "http_request_firewall_custom") || strings.Contains(p, "http_response_headers_transform") {
				writeJSON(w, 200, ok(map[string]any{"id": "rs-1", "name": "entry", "version": "3", "rules": adminItem["rules"]}))
			} else {
				writeJSON(w, 404, fail(10003, "could not find entrypoint ruleset"))
			}
		case strings.HasSuffix(p, "/entrypoint") && r.Method == "PUT":
			writeJSON(w, 200, ok(map[string]any{"id": "rs-new", "version": "1", "rules": []any{map[string]any{"id": "rule-new"}}}))
		case strings.HasSuffix(p, "/rules") && r.Method == "POST":
			writeJSON(w, 200, ok(map[string]any{"id": "rs-1", "version": "4", "rules": []any{map[string]any{"id": "rule-1"}, map[string]any{"id": "rule-new"}}}))
		case strings.HasSuffix(p, "/tokens") && r.Method == "POST":
			writeJSON(w, 200, ok(map[string]any{"id": "tok-new", "name": req.Body["name"], "value": "NEW-TOKEN-SECRET-VALUE"}))
		case strings.HasSuffix(p, "/forbidden-thing"):
			writeJSON(w, 403, fail(10000, "Authentication error"))
		case strings.HasSuffix(p, "/spectrum/apps"):
			writeJSON(w, 403, fail(10007, "Forbidden."))
		case r.Method == "GET" && (strings.HasSuffix(p, "/settings/ssl") || strings.HasSuffix(p, "/settings/development_mode")):
			writeJSON(w, 200, ok(map[string]any{"id": "ssl", "value": "full", "editable": true}))
		case r.Method == "GET" && strings.HasSuffix(p, "/alerting/v3/available_alerts"):
			writeJSON(w, 200, ok(map[string]any{"Billing": []any{map[string]any{"type": "billing_budget_alert", "display_name": "Budget"}}}))
		case r.Method == "GET" && (strings.HasSuffix(p, "/universal/settings") || strings.HasSuffix(p, "/dnssec") || strings.HasSuffix(p, "/dns_settings") ||
			strings.HasSuffix(p, "/tiered_caching") || strings.HasSuffix(p, "/cache_reserve") || strings.HasSuffix(p, "/regional_tiered_cache") || strings.HasSuffix(p, "/tiered_cache_smart_topology_enable") ||
			strings.HasSuffix(p, "/tokens/verify") || strings.HasSuffix(p, "/organizations") || p == "/user" || strings.HasSuffix(p, "/billing/profile") || strings.HasSuffix(p, "/subscription") ||
			strings.HasSuffix(p, "/settings/security_level")):
			writeJSON(w, 200, ok(adminItem))
		case r.Method == "GET" && (strings.Contains(p, "/settings") || strings.HasSuffix(p, "s") || strings.HasSuffix(p, "/health") || strings.HasSuffix(p, "/history") || strings.HasSuffix(p, "/fields") || strings.HasSuffix(p, "/audit") || strings.HasSuffix(p, "/audit_logs") || strings.HasSuffix(p, "/verification") || strings.HasSuffix(p, "/status") || strings.HasSuffix(p, "/certificate_packs") || p == "/certificates"):
			writeJSON(w, 200, okList([]any{adminItem}, 1))
		case r.Method == "GET":
			writeJSON(w, 200, ok(adminItem))
		default:
			var body any = req.Body
			if body == nil {
				body = map[string]any{}
			}
			res := map[string]any{"id": "item-1", "echo": body, "method": r.Method, "operation_id": "op-1"}
			if req.Body != nil {
				if v, ok := req.Body["value"]; ok {
					res["value"] = v
				}
			}
			writeJSON(w, 200, ok(res))
		}
		return true
	}
	return f
}

func serveGraphQL(w http.ResponseWriter, req fakeRequest) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(req.RawBody, &body)
	q := body.Query
	var data any
	switch {
	case strings.Contains(q, "groups:"):
		group := func(ts string, req float64) map[string]any {
			return map[string]any{
				"dimensions": map[string]any{"ts": ts},
				"sum": map[string]any{"requests": req, "cachedRequests": req / 2, "bytes": 2048.0, "cachedBytes": 1024.0, "threats": 1.0, "pageViews": 3.0, "encryptedRequests": req,
					"responseStatusMap": []any{map[string]any{"edgeResponseStatus": 200, "requests": req - 1}, map[string]any{"edgeResponseStatus": 404, "requests": 1}},
					"countryMap":        []any{map[string]any{"clientCountryName": "US", "requests": req - 2, "bytes": 1000, "threats": 0}, map[string]any{"clientCountryName": "DE", "requests": 2, "bytes": 48, "threats": 1}}},
				"uniq": map[string]any{"uniques": 5},
			}
		}
		data = map[string]any{"viewer": map[string]any{"zones": []any{map[string]any{"groups": []any{group("2026-10-02T01:00:00Z", 10), group("2026-10-02T02:00:00Z", 30)}}}}}
	case strings.Contains(q, "top:"):
		data = map[string]any{"viewer": map[string]any{"zones": []any{map[string]any{"top": []any{
			map[string]any{"count": 90, "sum": map[string]any{"edgeResponseBytes": 4096}, "dimensions": map[string]any{"key": "/index.html"}},
			map[string]any{"count": 10, "sum": map[string]any{"edgeResponseBytes": 100}, "dimensions": map[string]any{"key": "/robots.txt"}},
		}}}}}
	case strings.Contains(q, "firewallEventsAdaptiveGroups"):
		writeJSON(w, 200, map[string]any{"data": nil, "errors": []any{map[string]any{"message": "zone 'x' does not have access to the path"}}})
		return
	case strings.Contains(q, "events:"):
		data = map[string]any{"viewer": map[string]any{"zones": []any{map[string]any{"events": []any{
			map[string]any{"action": "block", "source": "waf", "clientCountryName": "BE"},
			map[string]any{"action": "block", "source": "waf", "clientCountryName": "BE"},
			map[string]any{"action": "managed_challenge", "source": "bic", "clientCountryName": "ID"},
		}}}}}
	case strings.Contains(q, "w:"):
		data = map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"w": []any{
			map[string]any{"sum": map[string]any{"requests": 100, "errors": 2, "subrequests": 7}, "quantiles": map[string]any{"cpuTimeP50": 1500, "cpuTimeP99": 9000}, "dimensions": map[string]any{"scriptName": "api-worker", "status": "success"}},
		}}}}}
	case strings.Contains(q, "ops:"):
		data = map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{
			"ops": []any{
				map[string]any{"sum": map[string]any{"requests": 5, "responseObjectSize": 0}, "dimensions": map[string]any{"bucketName": "media", "actionType": "PutObject"}},
				map[string]any{"sum": map[string]any{"requests": 50, "responseObjectSize": 1 << 20}, "dimensions": map[string]any{"bucketName": "media", "actionType": "GetObject"}},
				map[string]any{"sum": map[string]any{"requests": 1, "responseObjectSize": 0}, "dimensions": map[string]any{"bucketName": "media", "actionType": "DeleteObject"}},
			},
			"st": []any{
				map[string]any{"max": map[string]any{"payloadSize": 3 << 30, "metadataSize": 10, "objectCount": 42, "uploadCount": 0}, "dimensions": map[string]any{"bucketName": "media", "datetime": "2026-10-02T23:00:00Z"}},
				map[string]any{"max": map[string]any{"payloadSize": 1, "metadataSize": 1, "objectCount": 1, "uploadCount": 0}, "dimensions": map[string]any{"bucketName": "media", "datetime": "2026-10-01T23:00:00Z"}},
			},
		}}}}
	default:
		data = map[string]any{}
	}
	writeJSON(w, 200, map[string]any{"data": data, "errors": nil})
}

// readCase is one read-only command and the request it must make.
type readCase struct {
	args []string
	// method and path of the request that matters (the last one matching).
	method, path string
	query        string // substring of the raw query
	out          string // substring of the human output
}

const zp = "/zones/" + zoneID
const ap = "/accounts/" + acctID

var adminReads = []readCase{
	{[]string{"zones", "settings", "list", "example.com"}, "GET", zp + "/settings", "", "item-1"},
	{[]string{"zones", "settings", "list", "example.com", "--filter", "item"}, "GET", zp + "/settings", "", "item-1"},
	{[]string{"zones", "settings", "get", "example.com", "ssl"}, "GET", zp + "/settings/ssl", "", "full"},
	{[]string{"cache", "settings", "example.com"}, "GET", zp + "/cache/cache_reserve", "", "Cache settings"},
	{[]string{"cache", "dev-mode", "example.com"}, "GET", zp + "/settings/development_mode", "", "Development mode"},
	{[]string{"ssl", "status", "example.com"}, "GET", zp + "/ssl/certificate_packs", "status=all", "SSL/TLS for example.com"},
	{[]string{"ssl", "mode", "example.com"}, "GET", zp + "/settings/ssl", "", "SSL mode: full"},
	{[]string{"ssl", "packs", "list", "example.com"}, "GET", zp + "/ssl/certificate_packs", "", "example.com, *.example.com"},
	{[]string{"ssl", "packs", "get", "example.com", "p1"}, "GET", zp + "/ssl/certificate_packs/p1", "", "item-1"},
	{[]string{"ssl", "universal", "example.com"}, "GET", zp + "/ssl/universal/settings", "", "Universal SSL"},
	{[]string{"ssl", "verification", "example.com"}, "GET", zp + "/ssl/verification", "", "Verification"},
	{[]string{"ssl", "origin", "list", "example.com"}, "GET", "/certificates", "zone_id=" + zoneID, "item-1"},
	{[]string{"ssl", "origin", "get", "c1"}, "GET", "/certificates/c1", "", "item-1"},
	{[]string{"ssl", "custom", "list", "example.com"}, "GET", zp + "/custom_certificates", "", "item-1"},
	{[]string{"ssl", "custom", "get", "example.com", "c1"}, "GET", zp + "/custom_certificates/c1", "", "item-1"},
	{[]string{"rulesets", "list", "--zone", "example.com"}, "GET", zp + "/rulesets", "", "item-1"},
	{[]string{"rulesets", "list"}, "GET", ap + "/rulesets", "", "item-1"},
	{[]string{"rulesets", "list", "--zone", "example.com", "--phase", "waf"}, "GET", zp + "/rulesets", "", "item-1"},
	{[]string{"rulesets", "get", "rs1", "--zone", "example.com"}, "GET", zp + "/rulesets/rs1", "", "rule-1"},
	{[]string{"rulesets", "phase", "redirect", "--zone", "example.com"}, "GET", zp + "/rulesets/phases/http_request_dynamic_redirect/entrypoint", "", "rule-1"},
	{[]string{"rulesets", "phase", "cache", "--zone", "example.com"}, "GET", zp + "/rulesets/phases/http_request_cache_settings/entrypoint", "", "No rules in phase"},
	{[]string{"rulesets", "versions", "rs1"}, "GET", ap + "/rulesets/rs1/versions", "", "Versions"},
	{[]string{"redirects", "list", "example.com"}, "GET", zp + "/rulesets/phases/http_request_dynamic_redirect/entrypoint", "", "https://example.com/new"},
	{[]string{"redirects", "bulk", "lists"}, "GET", ap + "/rules/lists", "", "No bulk redirect lists"},
	{[]string{"redirects", "bulk", "items", "l1"}, "GET", ap + "/rules/lists/l1/items", "", "item-1"},
	{[]string{"redirects", "bulk", "rules"}, "GET", ap + "/rulesets/phases/http_request_redirect/entrypoint", "", "No bulk redirect rules"},
	{[]string{"transform", "list", "example.com"}, "GET", zp + "/rulesets/phases/http_response_headers_transform/entrypoint", "", "rule-1"},
	{[]string{"transform", "list", "example.com", "--type", "url-rewrite"}, "GET", zp + "/rulesets/phases/http_request_transform/entrypoint", "", "No transform rules"},
	{[]string{"waf", "overview", "example.com"}, "GET", zp + "/settings/security_level", "", "Security level: on"},
	{[]string{"waf", "managed", "example.com"}, "GET", zp + "/rulesets", "", "No deployed managed rulesets"},
	{[]string{"waf", "custom", "list", "example.com"}, "GET", zp + "/rulesets/phases/http_request_firewall_custom/entrypoint", "", "rule-1"},
	{[]string{"waf", "rate-limits", "example.com"}, "GET", zp + "/rulesets/phases/http_ratelimit/entrypoint", "", "No rate limiting rules"},
	{[]string{"page-rules", "list", "example.com"}, "GET", zp + "/pagerules", "", "item-1"},
	{[]string{"page-rules", "get", "example.com", "pr1"}, "GET", zp + "/pagerules/pr1", "", "item-1"},
	{[]string{"firewall", "access-rules", "list", "--zone", "example.com", "--mode", "block"}, "GET", zp + "/firewall/access_rules/rules", "mode=block", "192.0.2.1"},
	{[]string{"firewall", "access-rules", "list"}, "GET", ap + "/firewall/access_rules/rules", "", "192.0.2.1"},
	{[]string{"lists", "list"}, "GET", ap + "/rules/lists", "", "item-1"},
	{[]string{"lists", "get", "l1"}, "GET", ap + "/rules/lists/l1", "", "item-1"},
	{[]string{"lists", "items", "l1"}, "GET", ap + "/rules/lists/l1/items", "", "item-1"},
	{[]string{"lists", "operation", "op1"}, "GET", ap + "/rules/lists/bulk_operations/op1", "", "item-1"},
	{[]string{"lb", "list", "example.com"}, "GET", zp + "/load_balancers", "", "item-1"},
	{[]string{"lb", "get", "example.com", "lb1"}, "GET", zp + "/load_balancers/lb1", "", "item-1"},
	{[]string{"lb", "pools", "list"}, "GET", ap + "/load_balancers/pools", "", "item-1"},
	{[]string{"lb", "pools", "get", "p1"}, "GET", ap + "/load_balancers/pools/p1", "", "Pool thing-one"},
	{[]string{"lb", "pools", "health", "p1"}, "GET", ap + "/load_balancers/pools/p1/health", "", "item-1"},
	{[]string{"lb", "monitors", "list"}, "GET", ap + "/load_balancers/monitors", "", "item-1"},
	{[]string{"lb", "monitors", "get", "m1"}, "GET", ap + "/load_balancers/monitors/m1", "", "item-1"},
	{[]string{"accounts", "list"}, "GET", "/accounts", "", "Kyle's Account"},
	{[]string{"accounts", "get"}, "GET", "/accounts/" + acctID, "", "Kyle's Account"},
	{[]string{"members", "list", "--status", "accepted"}, "GET", ap + "/members", "status=accepted", "member@example.com"},
	{[]string{"members", "get", "m1"}, "GET", ap + "/members/m1", "", "Administrator"},
	{[]string{"roles", "list"}, "GET", ap + "/roles", "", "item-1"},
	{[]string{"roles", "get", "r1"}, "GET", ap + "/roles/r1", "", "item-1"},
	{[]string{"tokens", "list"}, "GET", ap + "/tokens", "", "item-1"},
	{[]string{"tokens", "list", "--user"}, "GET", "/user/tokens", "", "item-1"},
	{[]string{"tokens", "get", "t1"}, "GET", ap + "/tokens/t1", "", "Token thing-one"},
	{[]string{"tokens", "verify"}, "GET", ap + "/tokens/verify", "", "account-owned"},
	{[]string{"tokens", "permission-groups", "--filter", "thing"}, "GET", ap + "/tokens/permission_groups", "", "thing-one"},
	{[]string{"audit-logs", "list", "--since", "7d", "--actor", "a@example.com", "--action", "delete", "--product", "dns_records"}, "GET", ap + "/logs/audit", "actor_email=a%40example.com", "Audit log entries"},
	{[]string{"audit-logs", "list", "--v1", "--action", "token_create"}, "GET", ap + "/audit_logs", "action.type=token_create", "Audit log entries"},
	{[]string{"billing", "subscriptions"}, "GET", ap + "/subscriptions", "", "item-1"},
	{[]string{"billing", "zone", "example.com"}, "GET", zp + "/subscription", "", "item-1"},
	{[]string{"billing", "profile"}, "GET", "/user/billing/profile", "", "item-1"},
	{[]string{"billing", "history"}, "GET", "/user/billing/history", "", "item-1"},
	{[]string{"logpush", "jobs", "list", "--zone", "example.com"}, "GET", zp + "/logpush/jobs", "", "access-key-id=REDACTED"},
	{[]string{"logpush", "jobs", "list"}, "GET", ap + "/logpush/jobs", "", "item-1"},
	{[]string{"logpush", "jobs", "get", "j1"}, "GET", ap + "/logpush/jobs/j1", "", "item-1"},
	{[]string{"logpush", "fields", "http_requests", "--zone", "example.com"}, "GET", zp + "/logpush/datasets/http_requests/fields", "", "item-1"},
	{[]string{"notifications", "policies", "list"}, "GET", ap + "/alerting/v3/policies", "", "item-1"},
	{[]string{"notifications", "policies", "get", "p1"}, "GET", ap + "/alerting/v3/policies/p1", "", "item-1"},
	{[]string{"notifications", "destinations"}, "GET", ap + "/alerting/v3/destinations/pagerduty", "", "webhook"},
	{[]string{"notifications", "available"}, "GET", ap + "/alerting/v3/available_alerts", "", "billing_budget_alert"},
	{[]string{"notifications", "history", "--since", "30d"}, "GET", ap + "/alerting/v3/history", "since=", "Alert history"},
	{[]string{"healthchecks", "list", "example.com"}, "GET", zp + "/healthchecks", "", "item-1"},
	{[]string{"healthchecks", "get", "example.com", "h1"}, "GET", zp + "/healthchecks/h1", "", "item-1"},
	{[]string{"waiting-rooms", "list", "example.com"}, "GET", zp + "/waiting_rooms", "", "item-1"},
	{[]string{"waiting-rooms", "get", "example.com", "w1"}, "GET", zp + "/waiting_rooms/w1", "", "item-1"},
	{[]string{"waiting-rooms", "status", "example.com", "w1"}, "GET", zp + "/waiting_rooms/w1/status", "", "item-1"},
	{[]string{"waiting-rooms", "events", "example.com", "w1"}, "GET", zp + "/waiting_rooms/w1/events", "", "item-1"},
	{[]string{"spectrum", "apps", "get", "example.com", "a1"}, "GET", zp + "/spectrum/apps/a1", "", "item-1"},
	{[]string{"access", "apps", "list"}, "GET", ap + "/access/apps", "", "item-1"},
	{[]string{"access", "apps", "list", "--zone", "example.com"}, "GET", zp + "/access/apps", "", "item-1"},
	{[]string{"access", "apps", "get", "a1"}, "GET", ap + "/access/apps/a1", "", "item-1"},
	{[]string{"access", "policies", "list"}, "GET", ap + "/access/policies", "", "item-1"},
	{[]string{"access", "policies", "get", "p1"}, "GET", ap + "/access/policies/p1", "", "item-1"},
	{[]string{"access", "groups", "list"}, "GET", ap + "/access/groups", "", "item-1"},
	{[]string{"access", "idps", "list"}, "GET", ap + "/access/identity_providers", "", "item-1"},
	{[]string{"access", "organization"}, "GET", ap + "/access/organizations", "", "Organization"},
	{[]string{"user", "get"}, "GET", "/user", "", "item-1"},
	{[]string{"user", "invites"}, "GET", "/user/invites", "", "item-1"},
	{[]string{"user", "memberships"}, "GET", "/memberships", "", "item-1"},
	{[]string{"dns", "dnssec", "status", "example.com"}, "GET", zp + "/dnssec", "", "active"},
	{[]string{"dns", "settings", "get", "example.com"}, "GET", zp + "/dns_settings", "", "item-1"},
	{[]string{"dns", "export", "example.com"}, "GET", zp + "/dns_records/export", "", "192.0.2.1"},
}

func lastRequest(f *fakeCF, method, path string) *fakeRequest {
	var out *fakeRequest
	for _, r := range f.Requests() {
		if r.Method == method && r.Path == path {
			r := r
			out = &r
		}
	}
	return out
}

func TestAdminReads(t *testing.T) {
	f := adminFake(t)
	for _, c := range adminReads {
		name := strings.Join(c.args, " ")
		out, stderr, err := runCLI(t, "", c.args...)
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
		if c.query != "" && !strings.Contains(r.Query, c.query) {
			t.Errorf("%s: query %q lacks %q", name, r.Query, c.query)
		}
		if c.out != "" && !strings.Contains(out, c.out) {
			t.Errorf("%s: output lacks %q:\n%s", name, c.out, out)
		}
		// --json is valid JSON (dns export is raw text).
		if c.args[0] == "dns" && c.args[1] == "export" {
			continue
		}
		jout, stderr, err := runCLI(t, "", append(append([]string{}, c.args...), "--json")...)
		if err != nil {
			t.Errorf("%s --json: %v\n%s", name, err, stderr)
			continue
		}
		if !json.Valid([]byte(strings.TrimSpace(jout))) {
			t.Errorf("%s --json: not JSON:\n%s", name, jout)
		}
		if strings.Contains(jout, "SuperSecretValue") || strings.Contains(out, "SuperSecretValue") {
			t.Errorf("%s: leaked a destination secret", name)
		}
	}
}

// Every read works in read-only mode, and nothing but GET (and GraphQL) is sent.
func TestAdminReadsReadOnly(t *testing.T) {
	f := adminFake(t)
	t.Setenv("CFCTL_READONLY", "1")
	before := len(f.Requests())
	for _, c := range adminReads {
		if _, stderr, err := runCLI(t, "", c.args...); err != nil {
			t.Errorf("%s (read-only): %v\n%s", strings.Join(c.args, " "), err, stderr)
		}
	}
	for _, r := range f.Requests()[before:] {
		if r.Method != "GET" && r.Path != "/graphql" {
			t.Errorf("read command sent %s %s", r.Method, r.Path)
		}
	}
}

func TestFriendlyErrors(t *testing.T) {
	adminFake(t)
	_, _, err := runCLI(t, "", "spectrum", "apps", "list", "example.com")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "hint: the token lacks permission for Spectrum") {
		t.Errorf("403: %v", err)
	}
	// /user on an account token.
	f2 := newFakeCF(t)
	testEnv(t, f2)
	login(t)
	f2.custom = func(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
		switch req.Path {
		case "/user":
			writeJSON(w, 403, fail(9109, "Valid user-level authentication not found"))
		case zp + "/pagerules":
			writeJSON(w, 400, fail(1011, "Page Rules endpoint does not support account owned tokens."))
		case ap + "/access/apps":
			writeJSON(w, 403, fail(9999, "access.api.error.not_enabled: Access is not enabled."))
		case zp + "/custom_certificates":
			writeJSON(w, 400, fail(1011, "Plan level does not allow custom certificates"))
		default:
			return false
		}
		return true
	}
	for args, want := range map[string]string{
		"user get":                         "needs a user-owned API token",
		"page-rules list example.com":      "needs a user-owned API token",
		"access apps list":                 "Cloudflare Access is not enabled",
		"ssl custom list example.com":      "not available on this zone's or account's plan",
		"spectrum apps list nosuchzone.io": "not found",
	} {
		_, _, err := runCLI(t, "", strings.Fields(args)...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", args, want, err)
		}
	}
}

func TestParseValueAndSince(t *testing.T) {
	for in, want := range map[string]string{"on": `"on"`, "1.2": `1.2`, "14400": `14400`, "true": `true`, `{"css":"on"}`: `{"css":"on"}`, `"x"`: `"x"`, "strict": `"strict"`} {
		b, _ := json.Marshal(parseValue(in))
		if string(b) != want {
			t.Errorf("parseValue(%q) = %s, want %s", in, b, want)
		}
	}
	for _, bad := range []string{"7x", "-1d", "d"} {
		if _, err := parseSince(bad, nowForTest()); err == nil {
			t.Errorf("parseSince(%q): expected error", bad)
		}
	}
	for in, hours := range map[string]float64{"24h": 24, "7d": 168, "2w": 336, "30m": 0.5} {
		got, err := parseSince(in, nowForTest())
		if err != nil || nowForTest().Sub(got).Hours() != hours {
			t.Errorf("parseSince(%q) = %v %v", in, got, err)
		}
	}
}

func nowForTest() time.Time { return time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC) }
