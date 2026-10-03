package cmd

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

const recordsPath = "/zones/" + zoneID + "/dns_records"

func TestNameHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"": "example.com", "@": "example.com", "www": "www.example.com",
		"www.example.com": "www.example.com", "www.example.com.": "www.example.com",
		"a.b": "a.b.example.com",
	} {
		if got := fqdnName(in, "example.com"); got != want {
			t.Errorf("fqdnName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"example.com": "@", "www.example.com": "www", "other.org": "other.org",
	} {
		if got := relativeName(in, "example.com"); got != want {
			t.Errorf("relativeName(%q) = %q, want %q", in, got, want)
		}
	}
	if ttlString(1) != "auto" || ttlString(300) != "300" {
		t.Error("ttlString")
	}
}

func TestRecordsList(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	stdout, stderr, err := runCLI(t, "", "records", "list", "example.com", "--type", "a", "--name", "www")
	if err != nil {
		t.Fatal(err)
	}
	assertNoToken(t, "records list", stdout, stderr)

	// Zone resolved by name, scoped to the account.
	z := f.find("GET", "/zones")
	q, _ := url.ParseQuery(z.Query)
	if z == nil || q.Get("name") != "example.com" || q.Get("account.id") != acctID {
		t.Errorf("zone lookup query = %q", z.Query)
	}

	r := f.find("GET", recordsPath)
	if r == nil {
		t.Fatal("records were not listed")
	}
	q, _ = url.ParseQuery(r.Query)
	if q.Get("type") != "A" || q.Get("name.exact") != "www.example.com" {
		t.Errorf("records list query = %q", r.Query)
	}

	for _, want := range []string{"2 records for example.com", "www", "auto", "1.2.3.4", "☁ proxied", "(pri: 10)", "3600"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("records list missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, _ = runCLI(t, "", "records", "list", "example.com", "--json")
	var recs []Record
	if err := json.Unmarshal([]byte(stdout), &recs); err != nil || len(recs) != 2 {
		t.Fatalf("records --json: %v\n%s", err, stdout)
	}
	if recs[0].ID != recordID || !recs[0].Proxied || recs[0].TTL != 1 || recs[0].Priority != nil {
		t.Errorf("unexpected A record: %+v", recs[0])
	}
	if recs[1].Priority == nil || *recs[1].Priority != 10 {
		t.Errorf("unexpected MX record: %+v", recs[1])
	}
}

func TestRecordsCreate(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	stdout, stderr, err := runCLI(t, "", "records", "create", "example.com", "--type", "a", "--name", "api", "--content", "5.6.7.8", "--proxied")
	if err != nil {
		t.Fatal(err)
	}
	assertNoToken(t, "records create", stdout, stderr)
	if !strings.Contains(stdout, "Created A record 'api.example.com' → 5.6.7.8") {
		t.Errorf("unexpected output: %s", stdout)
	}

	r := f.find("POST", recordsPath)
	if r == nil {
		t.Fatal("no POST to dns_records")
	}
	want := map[string]interface{}{"type": "A", "name": "api.example.com", "content": "5.6.7.8", "ttl": float64(1), "proxied": true}
	if len(r.Body) != len(want) {
		t.Errorf("POST body = %v, want %v", r.Body, want)
	}
	for k, v := range want {
		if r.Body[k] != v {
			t.Errorf("POST body[%s] = %v, want %v", k, r.Body[k], v)
		}
	}

	// Apex MX with priority and explicit TTL; proxied omitted when not passed.
	if _, _, err := runCLI(t, "", "records", "create", "example.com", "--type", "MX", "--name", "", "--content", "mx.example.com", "--priority", "5", "--ttl", "600"); err != nil {
		t.Fatal(err)
	}
	var mx *fakeRequest
	for _, req := range f.Requests() {
		if req.Method == "POST" && req.Body["type"] == "MX" {
			req := req
			mx = &req
		}
	}
	if mx == nil || mx.Body["name"] != "example.com" || mx.Body["priority"] != float64(5) || mx.Body["ttl"] != float64(600) {
		t.Errorf("MX POST body = %v", mx)
	}
	if _, ok := mx.Body["proxied"]; ok {
		t.Errorf("proxied should be omitted unless passed: %v", mx.Body)
	}

	if _, _, err := runCLI(t, "", "records", "create", "example.com", "--type", "A"); err == nil {
		t.Error("expected error without --content")
	}
}

func TestRecordsUpdate(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	stdout, _, err := runCLI(t, "", "records", "update", "example.com", recordID, "--content", "9.9.9.9", "--proxied=false", "--json")
	if err != nil {
		t.Fatal(err)
	}
	r := f.find("PATCH", recordsPath+"/"+recordID)
	if r == nil {
		t.Fatal("no PATCH to the record")
	}
	if len(r.Body) != 2 || r.Body["content"] != "9.9.9.9" || r.Body["proxied"] != false {
		t.Errorf("PATCH body = %v (only changed fields expected)", r.Body)
	}
	var rec Record
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil || rec.Content != "9.9.9.9" || rec.Proxied {
		t.Errorf("update --json: %v %+v", err, rec)
	}

	if _, _, err := runCLI(t, "", "records", "update", "example.com", recordID); err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Errorf("expected nothing-to-update error, got %v", err)
	}
}

func TestRecordsDelete(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	stdout, stderr, err := runCLI(t, "", "records", "delete", "example.com", recordID)
	if err != nil {
		t.Fatal(err)
	}
	assertNoToken(t, "records delete", stdout, stderr)
	if f.find("DELETE", recordsPath+"/"+recordID) == nil {
		t.Errorf("no DELETE to %s/%s; got %+v", recordsPath, recordID, f.Requests())
	}
	if !strings.Contains(stdout, "Record "+recordID+" deleted from zone 'example.com'") {
		t.Errorf("unexpected output: %s", stdout)
	}
}

func TestZoneNotFound(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)
	_, _, err := runCLI(t, "", "records", "list", "unknown.org")
	if err == nil || !strings.Contains(err.Error(), "zone 'unknown.org' not found") {
		t.Errorf("expected zone not found, got %v", err)
	}
}

func TestZonesListAndGet(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)

	stdout, _, err := runCLI(t, "", "zones", "list")
	if err != nil || !strings.Contains(stdout, "1 zones") || !strings.Contains(stdout, "example.com") {
		t.Errorf("zones list: %v\n%s", err, stdout)
	}
	stdout, _, err = runCLI(t, "", "zones", "get", "example.com")
	if err != nil || !strings.Contains(stdout, zoneID) || !strings.Contains(stdout, "ada.ns.cloudflare.com") {
		t.Errorf("zones get: %v\n%s", err, stdout)
	}
}

// Every request the CLI made across a full session must have used the token
// only in the Authorization header, never in a URL or body.
func TestTokenOnlyInAuthHeader(t *testing.T) {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)
	for _, args := range [][]string{
		{"whoami", "--json"}, {"domains", "list", "--json"}, {"zones", "list", "--json"},
		{"records", "list", "example.com", "--json"}, {"domains", "autorenew", "mybrand.dev", "on", "--json"},
		{"domains", "get", "nope.com", "--json"},
	} {
		stdout, stderr, err := runCLI(t, "", args...)
		assertNoToken(t, strings.Join(args, " "), stdout, stderr, errString(err))
	}
	for _, r := range f.Requests() {
		b, _ := json.Marshal(r.Body)
		assertNoToken(t, "request "+r.Method+" "+r.Path, r.Path, r.Query, string(b))
	}
}
