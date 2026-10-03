package cmd

import "testing"

// AI Search write bodies, checked against the spec and wrangler's ai-search client.
func TestAISearchWriteBodies(t *testing.T) {
	p := platFake(t)
	inst := platA + "/ai-search/namespaces/default/instances"

	// jobs create always sends a JSON object (wrangler sends {} / {description}).
	platRunOK(t, "", "ai-search", "jobs", "create", "docs")
	r := p.last(t, "POST", inst+"/docs/jobs")
	if string(r.RawBody) != "{}" || r.ContentType != "application/json" {
		t.Fatalf("jobs create body %q (%s), want {}", r.RawBody, r.ContentType)
	}
	platRunOK(t, "", "ai-search", "jobs", "create", "docs", "--description", "nightly")
	if r := p.last(t, "POST", inst+"/docs/jobs"); r.Body["description"] != "nightly" {
		t.Fatalf("jobs create body %s", r.RawBody)
	}

	// --type builtin omits "type" (the API enum is r2 | web-crawler).
	platRunOK(t, "", "ai-search", "create", "notes", "--type", "builtin")
	r = p.last(t, "POST", inst)
	if _, has := r.Body["type"]; has || r.Body["id"] != "notes" {
		t.Fatalf("create builtin body %s", r.RawBody)
	}
	platRunOK(t, "", "ai-search", "create", "site", "--type", "web-crawler", "--source", "https://example.com")
	if r := p.last(t, "POST", inst); r.Body["type"] != "web-crawler" {
		t.Fatalf("create web-crawler body %s", r.RawBody)
	}
}
