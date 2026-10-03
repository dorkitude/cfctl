package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseOnOff(t *testing.T) {
	cases := map[string]bool{
		"on": true, "ON": true, "enable": true, "true": true, "yes": true,
		"off": false, "Off": false, "disable": false, "false": false, "no": false,
	}
	for in, want := range cases {
		got, err := parseOnOff(in)
		if err != nil {
			t.Fatalf("parseOnOff(%q) error: %v", in, err)
		}
		if got != want {
			t.Errorf("parseOnOff(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseOnOff("maybe"); err == nil {
		t.Error("parseOnOff(\"maybe\") expected error")
	}
}

const registrationsPath = "/accounts/" + acctID + "/registrar/registrations"

func patches(f *fakeCF) []fakeRequest {
	var out []fakeRequest
	for _, r := range f.Requests() {
		if r.Method == "PATCH" {
			out = append(out, r)
		}
	}
	return out
}

func TestAutoRenewShow(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	stdout, stderr, err := runCLI(t, "", "domains", "autorenew", "example.com", "--json")
	if err != nil {
		t.Fatal(err)
	}
	assertNoToken(t, "autorenew", stdout, stderr)
	var s AutoRenewStatus
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, stdout)
	}
	if !s.AutoRenew || s.State != "active" || s.Domain != "example.com" || s.Changed != nil {
		t.Errorf("unexpected status: %+v", s)
	}
	if f.find("GET", registrationsPath+"/example.com") == nil {
		t.Error("registration was not fetched")
	}
	if len(patches(f)) != 0 {
		t.Error("show must not PATCH")
	}
}

func TestAutoRenewOnOff(t *testing.T) {
	for _, tc := range []struct {
		domain string
		arg    string
		want   bool
	}{
		{"mybrand.dev", "on", true},
		{"example.com", "off", false},
	} {
		f := newFakeCF(t)
		testEnv(t, f)
		login(t)

		stdout, _, err := runCLI(t, "", "domains", "autorenew", tc.domain, tc.arg, "--json")
		if err != nil {
			t.Fatalf("%s %s: %v", tc.domain, tc.arg, err)
		}
		var s AutoRenewStatus
		if err := json.Unmarshal([]byte(stdout), &s); err != nil {
			t.Fatalf("bad JSON: %v\n%s", err, stdout)
		}
		if s.AutoRenew != tc.want || s.Changed == nil || !*s.Changed {
			t.Errorf("%s %s: unexpected status %+v", tc.domain, tc.arg, s)
		}

		p := patches(f)
		if len(p) != 1 || p[0].Path != registrationsPath+"/"+tc.domain {
			t.Fatalf("%s %s: expected one PATCH to the registration, got %+v", tc.domain, tc.arg, p)
		}
		if v, ok := p[0].Body["auto_renew"].(bool); !ok || v != tc.want || len(p[0].Body) != 1 {
			t.Errorf("%s %s: PATCH body = %v, want {auto_renew: %v}", tc.domain, tc.arg, p[0].Body, tc.want)
		}
	}
}

func TestAutoRenewNoop(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	// example.com already has auto-renew on.
	stdout, _, err := runCLI(t, "", "domains", "autorenew", "example.com", "on", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"changed": false`) {
		t.Errorf("expected changed:false, got %s", stdout)
	}
	if len(patches(f)) != 0 {
		t.Error("no-op must not PATCH")
	}

	stdout, _, _ = runCLI(t, "", "domains", "autorenew", "example.com", "enable")
	if !strings.Contains(stdout, "already on") {
		t.Errorf("expected 'already on', got %s", stdout)
	}
}

func TestAutoRenewNotRegistered(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	for _, args := range [][]string{
		{"domains", "autorenew", "nope.com"},
		{"domains", "autorenew", "nope.com", "off"},
	} {
		_, _, err := runCLI(t, "", args...)
		if err == nil || !strings.Contains(err.Error(), "not registered with Cloudflare Registrar") || !strings.Contains(err.Error(), "Domain not found") {
			t.Errorf("%v: expected not-registered error, got %v", args, err)
		}
	}
	if len(patches(f)) != 0 {
		t.Error("must not PATCH an unknown domain")
	}
}

func TestAutoRenewBadArg(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	if _, _, err := runCLI(t, "", "domains", "autorenew", "example.com", "maybe"); err == nil || !strings.Contains(err.Error(), "use on or off") {
		t.Errorf("expected on/off error, got %v", err)
	}
}

func TestDomainsList(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	stdout, stderr, err := runCLI(t, "", "domains", "list")
	if err != nil {
		t.Fatal(err)
	}
	assertNoToken(t, "domains list", stdout, stderr)
	if !strings.Contains(stdout, "2 domains") || !strings.Contains(stdout, "expires: 2027-01-15") {
		t.Errorf("unexpected list:\n%s", stdout)
	}
	// ↻ only on the auto-renewing domain.
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "example.com") && !strings.Contains(line, "↻") {
			t.Errorf("example.com should show ↻: %q", line)
		}
		if strings.Contains(line, "mybrand.dev") && strings.Contains(line, "↻") {
			t.Errorf("mybrand.dev should not show ↻: %q", line)
		}
	}

	stdout, _, _ = runCLI(t, "", "domains", "list", "--filter", "brand", "--json")
	var got []map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || len(got) != 1 || got[0]["domain_name"] != "mybrand.dev" {
		t.Errorf("filtered list: %v %v", err, got)
	}
}
