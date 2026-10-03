package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dorkitude/cfctl/internal/apispec"
	"github.com/dorkitude/cfctl/internal/output"
)

// Regression tests for bugs found by the live read-only smoke test
// (scripts/smoke.sh, `cfctl api smoke`). See CHANGELOG.md (0.2.006) and docs/smoke/RESULTS.md.

// A list endpoint answering result:null must still print [] with --json.
func TestSmokeNullListPrintsEmptyArray(t *testing.T) {
	f := adminFake(t)
	inner := f.custom
	f.custom = func(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
		if strings.HasSuffix(req.Path, "/alerting/v3/history") {
			writeJSON(w, 200, map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": nil,
				"result_info": map[string]any{"page": 1, "per_page": 25, "count": 0, "total_count": 0}})
			return true
		}
		return inner(w, r, req)
	}
	stdout, stderr, err := runCLI(t, "", "notifications", "history", "--json")
	if err != nil {
		t.Fatalf("err: %v (%s)", err, stderr)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("--json for an empty list = %q, want []", stdout)
	}
	// Text mode still says there's nothing.
	stdout, _, err = runCLI(t, "", "notifications", "history")
	if err != nil || strings.Contains(stdout, "null") {
		t.Errorf("text mode: err=%v out=%q", err, stdout)
	}
}

// dns export --json printed the raw BIND text, which isn't JSON.
func TestSmokeDNSExportJSON(t *testing.T) {
	adminFake(t)
	stdout, stderr, err := runCLI(t, "", "dns", "export", "example.com", "--json")
	if err != nil {
		t.Fatalf("err: %v (%s)", err, stderr)
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("--json output doesn't parse: %v\n%s", err, stdout)
	}
	if !strings.Contains(v["zone"], "192.0.2.1") {
		t.Errorf("zone file missing from JSON: %v", v)
	}
	// Without --json it is still the plain zone file.
	stdout, _, _ = runCLI(t, "", "dns", "export", "example.com")
	if !strings.HasPrefix(stdout, ";; zone file") {
		t.Errorf("text export = %q", stdout)
	}
}

func TestSmokeEmptyListJSON(t *testing.T) {
	type img struct{ Name string }
	var nilSlice []img
	b, _ := json.Marshal(output.EmptyList(nilSlice))
	if string(b) != "[]" {
		t.Errorf("nil slice → %s, want []", b)
	}
	b, _ = json.Marshal(output.EmptyList(json.RawMessage(nil)))
	if string(b) != "null" {
		t.Errorf("nil RawMessage → %s, want null (left alone)", b)
	}
	b, _ = json.Marshal(output.EmptyList([]img{{"a"}}))
	if string(b) != `[{"Name":"a"}]` {
		t.Errorf("non-empty slice changed: %s", b)
	}
}

func TestSmokeClassify(t *testing.T) {
	dbg := func(path, status string) string {
		return "debug: GET /client/v4" + path + " -> " + status + " (10ms)\n"
	}
	cases := []struct {
		name           string
		exit           int
		stdout, stderr string
		want, status   string
	}{
		{"ok", 0, `{"a":1}`, dbg("/zones/z/settings", "200"), "ok", "200"},
		{"ok text body", 0, ";; zone\n", dbg("/zones/z/dns_records/export", "200"), "ok", "200"},
		{"ok ndjson", 0, "{\"a\":1}\n{\"b\":2}\n", dbg("/x", "200"), "ok", "200"},
		{"bad json", 0, `{"a":`, dbg("/x", "200"), "client-bug", "200"},
		{"2xx but failed", 1, "", dbg("/x", "200") + "✗ decode error\n", "client-bug", "200"},
		{"2xx success:false", 1, "", dbg("/x", "200") + "✗ API error: could not find entrypoint ruleset (code 10003)\n", "expected-4xx", "200"},
		{"multi-line json error", 1, "", dbg("/x", "404") + "✗ HTTP 404: {\n  \"code\": 1000\n}\n", "client-bug", "404"},
		{"friendly 403", 1, "", dbg("/x", "403") + "✗ HTTP 403: not entitled (code 10000)\n", "expected-4xx", "403"},
		{"raw json error", 1, "", dbg("/x", "400") + `✗ {"errors":[{"code":1}]}` + "\n", "client-bug", "400"},
		{"silent 4xx", 1, "", dbg("/x", "404"), "client-bug", "404"},
		{"5xx", 1, "", dbg("/x", "502") + "✗ HTTP 502: bad gateway\n", "5xx", "502"},
		{"429", 1, "", dbg("/x", "429") + "✗ HTTP 429: slow down\n", "rate-limited", "429"},
		{"refused", 1, "", "debug: GET /client/v4/x -> refused, not sent (read-only mode)\n✗ refusing GET /x: read-only mode\n", "client-bug", "refused, not sent"},
		{"not sent", 1, "", "✗ missing required flag --since\n", "client-bug", "-"},
		{"panic", 2, "", dbg("/x", "200") + "panic: runtime error: index out of range\n\ngoroutine 1 [running]:\n", "client-bug", "200"},
		{"timeout", -1, "", "", "client-bug", "-"},
	}
	for _, c := range cases {
		r := classifySmoke(c.exit, []byte(c.stdout), []byte(c.stderr), "")
		if r.outcome != c.want || r.status != c.status {
			t.Errorf("%s: got %s/%s (%s), want %s/%s", c.name, r.outcome, r.status, r.detail, c.want, c.status)
		}
	}
	// The op's own request wins over a preceding zone lookup.
	r := classifySmoke(1, nil, []byte(dbg("/zones", "200")+dbg("/zones/z/foo", "403")+"✗ HTTP 403: no\n"), "/foo")
	if r.status != "403" || r.outcome != "expected-4xx" {
		t.Errorf("zone lookup + op: %+v", r)
	}
}

func TestSmokePlan(t *testing.T) {
	env := smokeEnv{accountID: acctID, zoneID: zoneID, zoneName: "example.com", now: time.Date(2026, 10, 3, 5, 30, 0, 0, time.UTC)}
	var n, zoneOps, skipped int
	all := apispec.Ops()
	for i := range all {
		o := &all[i]
		pl, ok := planSmoke(o, env)
		if !ok {
			continue
		}
		n++
		if o.Method != "GET" {
			t.Fatalf("planned a %s: %s", o.Method, o.ID)
		}
		if pl.zone {
			zoneOps++
		}
		if pl.skip != "" {
			skipped++
			continue
		}
		// Every planned invocation must parse against the generated command.
		populateTag(o.TagSlug)
		var cmdFound bool
		for _, c := range tagCommands[o.TagSlug].Commands() {
			if c.Annotations[annOperationID] != o.ID {
				continue
			}
			cmdFound = true
			resetFlags(c)
			if err := c.ParseFlags(pl.args); err != nil {
				t.Errorf("%s: planned args %v don't parse: %v", o.ID, pl.args, err)
			}
			if err := c.Args(c, c.Flags().Args()); err != nil {
				t.Errorf("%s: planned positional args %v: %v", o.ID, c.Flags().Args(), err)
			}
		}
		if !cmdFound {
			t.Errorf("%s: no generated command", o.ID)
		}
	}
	if n < 800 || zoneOps < 150 || skipped > 60 {
		t.Errorf("plan: %d ops, %d zone-level, %d skipped", n, zoneOps, skipped)
	}
}

// A mistyped operation under a real tag, with a flag, used to fail with
// "unknown flag" instead of naming the unknown command.
func TestSmokeUnknownOpWithFlag(t *testing.T) {
	apiFake(t)
	_, _, err := runCLI(t, "", "api", "accounts", "no-such-op", "--raw")
	if err == nil || !strings.Contains(err.Error(), `unknown command "no-such-op"`) {
		t.Errorf("err = %v, want unknown command", err)
	}
}

// A mistyped tag (not just operation), with a flag, used to fail with
// "unknown flag: --per-page" instead of suggesting the right tag.
func TestSmokeUnknownTagWithFlag(t *testing.T) {
	apiFake(t)
	_, _, err := runCLI(t, "", "api", "zonez", "list-zones", "--per-page", "2")
	if err == nil || !strings.Contains(err.Error(), `unknown command "zonez"`) || !strings.Contains(err.Error(), "did you mean") {
		t.Errorf("err = %v, want unknown command with suggestions", err)
	}
}

// billing history's table pointed at fields (action, description, amount)
// that the live API no longer returns, so most columns were blank.
func TestSmokeBillingHistoryColumns(t *testing.T) {
	f := adminFake(t)
	inner := f.custom
	f.custom = func(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
		if req.Path == "/user/billing/history" {
			writeJSON(w, 200, okList([]any{
				map[string]any{"id": "inv-new", "type": "invoice", "occurred_at": "2026-09-06T10:06:21Z", "amount_to_pay": 12.5,
					"currency": "usd", "receipt_id": "IN-78082836", "status": "CLOSED"},
				map[string]any{"id": "inv-old", "type": "charge", "occurred_at": "2025-01-01T00:00:00Z", "action": "subscription",
					"description": "Pro plan", "amount": 20, "currency": "usd"},
			}, 2))
			return true
		}
		return inner(w, r, req)
	}
	stdout, stderr, err := runCLI(t, "", "billing", "history")
	if err != nil {
		t.Fatalf("err: %v (%s)", err, stderr)
	}
	for _, want := range []string{"CLOSED", "IN-78082836", "12.5", "subscription", "Pro plan", "20"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("billing history missing %q:\n%s", want, stdout)
		}
	}
}
