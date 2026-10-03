package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func basinFake(t *testing.T) *fakeCF {
	pl := map[string]any{"id": "p1", "name": "pipe", "sql": "INSERT INTO s SELECT * FROM st", "status": "running"}
	st := map[string]any{"id": "st1", "name": "clicks", "http": map[string]any{"enabled": true}, "endpoint": "https://x"}
	sk := map[string]any{"id": "sk1", "name": "clicks-r2", "type": "r2", "config": map[string]any{"bucket": "logs"}}
	cat := "/basin-catalog/lake"
	return stFake(t, map[string]stHandler{
		"GET /pipelines/v1/pipelines":                  stListH([]map[string]any{pl}),
		"POST /pipelines/v1/pipelines":                 stJSON(pl),
		"GET /pipelines/v1/pipelines/p1":               stJSON(pl),
		"DELETE /pipelines/v1/pipelines/p1":            stJSON(nil),
		"POST /pipelines/v1/validate_sql":              stJSON(map[string]any{"tables": map[string]any{"st": "stream"}}),
		"PUT /pipelines/old":                           stJSON(map[string]any{"name": "old"}),
		"GET /pipelines/v1/streams":                    stListH([]map[string]any{st}),
		"POST /pipelines/v1/streams":                   stJSON(st),
		"PATCH /pipelines/v1/streams/st1":              stJSON(st),
		"DELETE /pipelines/v1/streams/st1":             stJSON(nil),
		"GET /pipelines/v1/sinks":                      stListH([]map[string]any{sk}),
		"POST /pipelines/v1/sinks":                     stJSON(sk),
		"GET /pipelines/v1/sinks/sk1":                  stJSON(sk),
		"DELETE /pipelines/v1/sinks/sk1":               stJSON(nil),
		"GET /basin-catalog":                           stJSON(map[string]any{"warehouses": []any{map[string]any{"bucket": "lake", "id": "w1", "status": "active"}}}),
		"GET " + cat:                                   stJSON(map[string]any{"bucket": "lake", "status": "active"}),
		"POST " + cat + "/enable":                      stJSON(map[string]any{"id": "w1"}),
		"POST " + cat + "/disable":                     stJSON(nil),
		"POST " + cat + "/delete":                      stJSON(nil),
		"POST " + cat + "/credential":                  stJSON(nil),
		"GET " + cat + "/credential/status":            stJSON(map[string]any{"credential_status": "present"}),
		"GET " + cat + "/maintenance-configs":          stJSON(map[string]any{"compaction": map[string]any{"state": "enabled"}}),
		"POST " + cat + "/maintenance-configs":         stJSON(map[string]any{"compaction": map[string]any{"state": "enabled"}}),
		"GET " + cat + "/namespaces":                   stJSON(map[string]any{"namespaces": []any{[]any{"default"}}}),
		"GET " + cat + "/namespaces/default/tables":    stJSON(map[string]any{"identifiers": []any{map[string]any{"name": "t1"}}}),
		"GET " + cat + "/namespaces/default/tables/t1": stJSON(map[string]any{"name": "t1"}),
		"POST " + cat + "/namespaces/default/tables/t1/maintenance-configs/compaction/queue": stJSON(nil),
	})
}

func TestBasinPipelines(t *testing.T) {
	f := basinFake(t)
	out, _ := stRun(t, "", "basin", "pipelines", "list")
	stMust(t, out, "pipe", "running")
	stRun(t, "", "basin", "pipelines", "get", "pipe")
	sqlFile := filepath.Join(t.TempDir(), "p.sql")
	_ = os.WriteFile(sqlFile, []byte("INSERT INTO a SELECT * FROM b"), 0o600)
	stRun(t, "", "basin", "pipelines", "create", "p2", "--sql-file", sqlFile)
	if r := stFind(t, f, "POST", "/pipelines/v1/pipelines"); r.Body["sql"] != "INSERT INTO a SELECT * FROM b" || r.Body["name"] != "p2" {
		t.Fatalf("body %v", r.Body)
	}
	stRunErr(t, "", "basin", "pipelines", "create", "p3")
	stRun(t, "", "basin", "pipelines", "validate-sql", "SELECT 1")
	stRunErr(t, "", "basin", "pipelines", "update", "old", "--data", `{}`)
	stRun(t, "", "basin", "pipelines", "update", "old", "--legacy", "--data", `{"source":[]}`)
	stRun(t, "", "basin", "pipelines", "delete", "pipe", "-y")
	stFind(t, f, "DELETE", "/pipelines/v1/pipelines/p1")
}

func TestBasinStreamsSinks(t *testing.T) {
	f := basinFake(t)
	out, _ := stRun(t, "", "basin", "pipelines", "streams", "list")
	stMust(t, out, "clicks", "https://x")
	stRun(t, "", "basin", "pipelines", "streams", "create", "s2", "--http-auth", "--worker-binding", "--format", "json")
	r := stFind(t, f, "POST", "/pipelines/v1/streams")
	if stGet(r.Body, "http.enabled") != true || stGet(r.Body, "http.authentication") != true || stGet(r.Body, "worker_binding.enabled") != true || stGet(r.Body, "format.type") != "json" {
		t.Fatalf("stream body %v", r.Body)
	}
	stRun(t, "", "basin", "pipelines", "streams", "update", "clicks", "--http=false")
	if r := stFind(t, f, "PATCH", "/pipelines/v1/streams/st1"); stGet(r.Body, "http.enabled") != false {
		t.Fatalf("update body %v", r.Body)
	}
	stRun(t, "", "basin", "pipelines", "streams", "delete", "clicks", "-y")

	out, _ = stRun(t, "", "basin", "pipelines", "sinks", "list")
	stMust(t, out, "clicks-r2", "logs")
	out, _ = stRun(t, "", "basin", "pipelines", "sinks", "create", "k", "--bucket", "logs", "--access-key-id", "AK", "--secret-access-key", "SK-secret", "--format", "parquet", "--compression", "zstd")
	if strings.Contains(out, "SK-secret") {
		t.Fatal("secret echoed")
	}
	r = stFind(t, f, "POST", "/pipelines/v1/sinks")
	if stGet(r.Body, "config.account_id") != acctID || stGet(r.Body, "config.credentials.secret_access_key") != "SK-secret" || r.Body["type"] != "r2" || stGet(r.Body, "format.compression") != "zstd" {
		t.Fatalf("sink body %v", r.Body)
	}
	stRunErr(t, "", "basin", "pipelines", "sinks", "create", "k")
	stRun(t, "", "basin", "pipelines", "sinks", "delete", "clicks-r2", "-y")
	stFind(t, f, "DELETE", "/pipelines/v1/sinks/sk1")
}

func TestBasinCatalog(t *testing.T) {
	f := basinFake(t)
	out, _ := stRun(t, "", "basin", "catalog", "list")
	stMust(t, out, "lake", "active")
	stRun(t, "", "basin", "catalog", "get", "lake")
	stRun(t, "", "basin", "catalog", "enable", "lake")
	stRunErr(t, "", "basin", "catalog", "disable", "lake")
	stRun(t, "", "basin", "catalog", "disable", "lake", "-y")
	stRun(t, "", "basin", "catalog", "delete", "lake", "--force", "-y")
	if r := stFind(t, f, "POST", "/basin-catalog/lake/delete"); r.Query != "force=true" {
		t.Fatalf("query %q", r.Query)
	}
	out, errOut := stRun(t, "tok-SECRET\n", "basin", "catalog", "credential", "set", "lake")
	if strings.Contains(out+errOut, "tok-SECRET") {
		t.Fatal("token echoed")
	}
	if r := stFind(t, f, "POST", "/basin-catalog/lake/credential"); r.Body["token"] != "tok-SECRET" {
		t.Fatalf("cred body %v", r.Body)
	}
	stRun(t, "", "basin", "catalog", "credential", "status", "lake")
	stRun(t, "", "basin", "catalog", "maintenance", "get", "lake")
	stRun(t, "", "basin", "catalog", "maintenance", "set", "lake", "--compaction", "enabled", "--target-size", "256")
	stRun(t, "", "basin", "catalog", "snapshot-expiration", "enable", "lake", "--max-age", "7d", "--min-snapshots", "3")
	var found bool
	for _, r := range f.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/maintenance-configs") && stGet(r.Body, "snapshot_expiration.max_snapshot_age") == "7d" && stGet(r.Body, "snapshot_expiration.state") == "enabled" {
			found = true
		}
	}
	if !found {
		t.Fatal("snapshot-expiration body not sent")
	}
	stRunErr(t, "", "basin", "catalog", "maintenance", "set", "lake")
	stRun(t, "", "basin", "catalog", "namespaces", "lake")
	stRun(t, "", "basin", "catalog", "tables", "lake", "default")
	stRun(t, "", "basin", "catalog", "table", "get", "lake", "default", "t1")
	stRun(t, "", "basin", "catalog", "table", "run-maintenance", "lake", "default", "t1", "compaction")
}

func TestBasinReadOnly(t *testing.T) {
	f := basinFake(t)
	stRunErr(t, "", "--read-only", "basin", "catalog", "enable", "lake")
	stRunErr(t, "", "--read-only", "basin", "pipelines", "create", "x", "--sql", "s")
	stNoMutation(t, f)
}
