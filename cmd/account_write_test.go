package cmd

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dns import must send the zone file as a real file part (with a filename),
// like curl -F file=@zone.txt; a part without one is a plain form value.
func TestDNSImportSendsFilePart(t *testing.T) {
	f := adminFake(t)
	zf := filepath.Join(t.TempDir(), "example.com.zone")
	const zone = "www 300 IN A 192.0.2.1\n"
	if err := os.WriteFile(zf, []byte(zone), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := runCLI(t, "", "dns", "import", "example.com", zf, "--proxied"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	r := lastRequest(f, "POST", zp+"/dns_records/import")
	if r == nil {
		t.Fatal("no POST to dns_records/import")
	}
	mt, params, err := mime.ParseMediaType(r.ContentType)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("content type %q", r.ContentType)
	}
	mr := multipart.NewReader(bytes.NewReader(r.RawBody), params["boundary"])
	parts := map[string]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		parts[p.FormName()] = string(b)
		if p.FormName() == "file" && p.FileName() != "example.com.zone" {
			t.Errorf("file part filename %q, want example.com.zone", p.FileName())
		}
	}
	if parts["file"] != zone {
		t.Errorf("file part %q", parts["file"])
	}
	if parts["proxied"] != "true" {
		t.Errorf("proxied part %q", parts["proxied"])
	}
}

// IP Access Rules take ASNs as "AS<number>" (the spec's example is "AS12345").
func TestAccessRuleASNValue(t *testing.T) {
	f := adminFake(t)
	for _, in := range []string{"AS64496", "as64496", "64496"} {
		if _, stderr, err := runCLI(t, "", "firewall", "access-rules", "create", in); err != nil {
			t.Fatalf("%s: %v\n%s", in, err, stderr)
		}
		r := lastRequest(f, "POST", ap+"/firewall/access_rules/rules")
		if r == nil || !strings.Contains(string(r.RawBody), `"configuration":{"target":"asn","value":"AS64496"}`) {
			t.Errorf("%s: body %s", in, r.RawBody)
		}
	}
}
