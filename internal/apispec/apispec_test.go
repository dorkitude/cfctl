package apispec

import (
	"strings"
	"testing"
	"time"
)

func TestTableBasics(t *testing.T) {
	if len(Ops()) < 3000 {
		t.Fatalf("only %d ops", len(Ops()))
	}
	ids := map[string]bool{}
	slugs := map[string]bool{}
	for _, o := range Ops() {
		if ids[o.ID] {
			t.Errorf("duplicate operationId %s", o.ID)
		}
		ids[o.ID] = true
		k := o.TagSlug + " " + o.Slug
		if slugs[k] {
			t.Errorf("duplicate command %s", k)
		}
		slugs[k] = true
		for _, s := range []string{o.TagSlug, o.Slug} {
			if s == "" || strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") || strings.Contains(s, "--") {
				t.Errorf("bad slug %q for %s", s, o.ID)
			}
		}
	}
	if SpecCommit == "" || SpecDate == "" {
		t.Error("spec source not recorded")
	}
}

// TestDescribeAll renders every operation: no panics, no runaway output.
func TestDescribeAll(t *testing.T) {
	start := time.Now()
	for i := range Ops() {
		o := &Ops()[i]
		out, err := Describe(o, "")
		if err != nil {
			t.Fatalf("%s: %v", o.ID, err)
		}
		if !strings.HasPrefix(out, o.Method+" "+o.Path) {
			t.Errorf("%s: unexpected header %q", o.ID, out[:min(len(out), 80)])
		}
		if len(out) > 200<<10 {
			t.Errorf("%s: %d bytes of output", o.ID, len(out))
		}
	}
	t.Logf("described %d ops in %s", len(Ops()), time.Since(start))
}

func TestDescribeDNSCreate(t *testing.T) {
	o, ok := ByID("dns-records-for-a-zone-create-dns-record")
	if !ok {
		t.Fatal("op missing")
	}
	out, err := Describe(o, "cfctl api x y")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"POST /zones/{zone_id}/dns_records", "Request body (application/json", "one of", "Example (--data)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
