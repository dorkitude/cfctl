package cmd

import (
	"strings"
	"testing"
)

const testHDID = "22222222222222222222222222222222"

func hyperdriveFake(t *testing.T) *fakeCF {
	cfg := map[string]any{"id": testHDID, "name": "my-db", "origin": map[string]any{"scheme": "postgres", "host": "db.example.com", "port": 5432, "database": "app", "user": "app"}, "caching": map[string]any{"disabled": false}}
	p := "/hyperdrive/configs/" + testHDID
	return stFake(t, map[string]stHandler{
		"GET /hyperdrive/configs":  stListH([]map[string]any{cfg}),
		"POST /hyperdrive/configs": stJSON(cfg),
		"GET " + p:                 stJSON(cfg),
		"PATCH " + p:               stJSON(cfg),
		"DELETE " + p:              stJSON(nil),
		"POST " + p + "/restart":   stJSON(nil),
		"POST /hyperdrive/integrationsOperations/planetScale/createDatabaseSignature": stJSON(map[string]any{"signature": "sig", "expires_at": "2026-10-03T00:10:00Z"}),
	})
}

func TestHyperdrive(t *testing.T) {
	f := hyperdriveFake(t)
	out, _ := stRun(t, "", "hyperdrive", "list")
	stMust(t, out, "my-db", "db.example.com", "postgres", "on")
	out, _ = stRun(t, "", "hyperdrive", "get", "my-db")
	stMust(t, out, "db.example.com")

	out, _ = stRun(t, "", "hyperdrive", "create", "new-db", "--connection-string", "postgres://u:s3cretpw@h.example.com:6543/d", "--max-age", "90")
	if strings.Contains(out, "s3cretpw") {
		t.Fatal("password echoed")
	}
	r := stFind(t, f, "POST", "/hyperdrive/configs")
	if r.Body["name"] != "new-db" || stGet(r.Body, "origin.host") != "h.example.com" || stGet(r.Body, "origin.port") != float64(6543) ||
		stGet(r.Body, "origin.password") != "s3cretpw" || stGet(r.Body, "origin.database") != "d" || stGet(r.Body, "caching.max_age") != float64(90) {
		t.Fatalf("create body: %v", r.Body)
	}
	if _, err := stRunErr(t, "", "hyperdrive", "create", "x"); !strings.Contains(err.Error(), "missing origin") {
		t.Fatalf("got %v", err)
	}
	stRun(t, "", "hyperdrive", "update", "my-db", "--caching-disabled", "--name", "renamed")
	r = stFind(t, f, "PATCH", "/hyperdrive/configs/"+testHDID)
	if stGet(r.Body, "caching.disabled") != true || r.Body["name"] != "renamed" {
		t.Fatalf("update body: %v", r.Body)
	}
	stRun(t, "", "hyperdrive", "restart", "my-db")
	stFind(t, f, "POST", "/hyperdrive/configs/"+testHDID+"/restart")
	stRunErr(t, "", "hyperdrive", "delete", "my-db")
	stRun(t, "", "hyperdrive", "delete", "my-db", "--yes")
	stFind(t, f, "DELETE", "/hyperdrive/configs/"+testHDID)
	out, _ = stRun(t, "", "hyperdrive", "planetscale", "signature")
	stMust(t, out, "sig")
}

func TestHyperdriveParseConn(t *testing.T) {
	o, err := hyperdriveParseConn("mysql://u@h/db")
	if err != nil || o["port"] != 3306 || o["database"] != "db" {
		t.Fatalf("%v %v", o, err)
	}
	if _, err := hyperdriveParseConn("redis://h"); err == nil {
		t.Fatal("want scheme error")
	}
}

func TestHyperdriveReadOnly(t *testing.T) {
	f := hyperdriveFake(t)
	stRunErr(t, "", "--read-only", "hyperdrive", "create", "x", "--connection-string", "postgres://u:p@h/d")
	stRunErr(t, "", "--read-only", "hyperdrive", "restart", "my-db")
	stNoMutation(t, f)
}
