package cmd

import (
	"encoding/json"
	"testing"
)

// Zone settings whose API value is a string enum of numbers must stay strings;
// numeric settings stay numbers.
func TestZoneSettingStringValues(t *testing.T) {
	f := adminFake(t)
	for _, c := range []struct {
		setting, arg string
		want         any
	}{
		{"min_tls_version", "1.2", "1.2"},
		{"origin_max_http_version", "2", "2"},
		{"browser_cache_ttl", "14400", float64(14400)},
		{"ssl", "strict", "strict"},
		{"minify", `{"css":"on"}`, map[string]any{"css": "on"}},
	} {
		if _, stderr, err := runCLI(t, "", "zones", "settings", "set", "example.com", c.setting, c.arg); err != nil {
			t.Fatalf("%s: %v %s", c.setting, err, stderr)
		}
		r := lastRequest(f, "PATCH", zp+"/settings/"+c.setting)
		if r == nil {
			t.Fatalf("%s: no PATCH", c.setting)
		}
		var body map[string]any
		_ = json.Unmarshal(r.RawBody, &body)
		got, _ := json.Marshal(body["value"])
		want, _ := json.Marshal(c.want)
		if string(got) != string(want) {
			t.Errorf("%s: value %s, want %s", c.setting, got, want)
		}
	}
}

// PATCH on a ruleset rule replaces the rule, so update must send the
// current rule with only the requested fields changed.
func TestRulesetRuleUpdateKeepsFields(t *testing.T) {
	f := adminFake(t)
	if _, stderr, err := runCLI(t, "", "rulesets", "rules", "update", "rs-1", "rule-1", "--zone", "example.com", "--enabled=false"); err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if lastRequest(f, "GET", zp+"/rulesets/rs-1") == nil {
		t.Fatal("did not read the current rule")
	}
	r := lastRequest(f, "PATCH", zp+"/rulesets/rs-1/rules/rule-1")
	if r == nil {
		t.Fatal("no PATCH")
	}
	var body map[string]any
	_ = json.Unmarshal(r.RawBody, &body)
	if body["enabled"] != false || body["action"] != "block" || body["expression"] != "(ip.src eq 192.0.2.1)" {
		t.Fatalf("body %s", r.RawBody)
	}
	for _, k := range []string{"id", "version", "last_updated"} {
		if _, ok := body[k]; ok {
			t.Errorf("sent read-only field %s: %s", k, r.RawBody)
		}
	}
	if _, _, err := runCLI(t, "", "rulesets", "rules", "update", "rs-1", "no-such-rule", "--zone", "example.com", "--action", "log"); err == nil {
		t.Fatal("updating a missing rule should fail")
	}
}

// vpc service update/create accept the whole service from --data.
func TestVPCServiceDataOnly(t *testing.T) {
	p := platFake(t)
	vpc := platA + "/connectivity/directory/services"
	platCheck(t, p, []platCase{
		{args: []string{"vpc", "service", "update", "svc1", "--data", `{"name":"db","type":"tcp","tcp_port":5432,"host":{"ipv4":"10.0.0.5","network":{"tunnel_id":"` + tnID + `"}}}`},
			method: "PUT", path: vpc + "/svc1", body: map[string]any{"name": "db", "tcp_port": 5432}},
	})
}
