package cmd

import "testing"

func k2Fake(t *testing.T) *fakeCF {
	s := map[string]any{"id": "st1", "name": "clicks", "retention_seconds": 86400, "http": map[string]any{"enabled": true}, "worker_binding": map[string]any{"enabled": false}}
	return stFake(t, map[string]stHandler{
		"GET /k2/streams":                   stListH([]map[string]any{s}),
		"POST /k2/streams":                  stJSON(s),
		"GET /k2/streams/st1":               stJSON(s),
		"PATCH /k2/streams/st1":             stJSON(s),
		"DELETE /k2/streams/st1":            stJSON(nil),
		"GET /k2/streams/st1/subscriptions": stListH([]map[string]any{{"id": "sub1", "name": "to-r2"}}),
	})
}

func TestK2Streams(t *testing.T) {
	f := k2Fake(t)
	out, _ := stRun(t, "", "k2", "streams", "list")
	stMust(t, out, "clicks", "st1", "86400")
	out, _ = stRun(t, "", "k2", "streams", "get", "clicks")
	stMust(t, out, "retention_seconds:")
	out, _ = stRun(t, "", "k2", "streams", "subscriptions", "clicks")
	stMust(t, out, "to-r2")
	stRun(t, "", "k2", "streams", "create", "new", "--retention", "3600", "--http-auth", "--cors-origins", "https://a.example")
	r := stFind(t, f, "POST", "/k2/streams")
	if r.Body["name"] != "new" || stGet(r.Body, "http.enabled") != true || stGet(r.Body, "http.authentication") != true || r.Body["retention_seconds"] != float64(3600) {
		t.Fatalf("create body %v", r.Body)
	}
	stRun(t, "", "k2", "streams", "update", "clicks", "--http=false")
	if r := stFind(t, f, "PATCH", "/k2/streams/st1"); stGet(r.Body, "http.enabled") != false {
		t.Fatalf("update body %v", r.Body)
	}
	stRunErr(t, "", "k2", "streams", "update", "clicks")
	stRunErr(t, "", "k2", "streams", "delete", "clicks")
	stRun(t, "", "k2", "streams", "delete", "clicks", "--yes")
	stFind(t, f, "DELETE", "/k2/streams/st1")
}

func TestK2ReadOnly(t *testing.T) {
	f := k2Fake(t)
	stRunErr(t, "", "--read-only", "k2", "streams", "create", "x")
	stNoMutation(t, f)
}
