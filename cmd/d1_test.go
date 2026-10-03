package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const d1UUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

type d1Fake struct {
	mu        sync.Mutex
	applied   []string
	queries   []string
	exportN   int
	uploaded  []byte
	signedSrv *httptest.Server
}

func newD1Fake(t *testing.T) (*fakeCF, *d1Fake) {
	d := &d1Fake{}
	d.signedSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("pre-signed URL got an Authorization header")
		}
		switch r.Method {
		case "GET":
			_, _ = io.WriteString(w, "CREATE TABLE users(id);\n")
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			d.mu.Lock()
			d.uploaded = b
			d.mu.Unlock()
			w.Header().Set("ETag", `"`+etagOf(b)+`"`)
		}
	}))
	t.Cleanup(d.signedSrv.Close)
	d1PollInterval = time.Millisecond
	routes := map[string]stHandler{
		"GET /d1/database": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			items := []map[string]any{{"uuid": d1UUID, "name": "my-db", "created_at": "2026-01-01T00:00:00Z", "num_tables": 2, "file_size": 12288, "version": "production"}}
			if n := r.URL.Query().Get("name"); n != "" && n != "my-db" {
				items = nil
			}
			stListH(items)(w, r, req)
		},
		"GET /d1/database/*":    stJSON(map[string]any{"uuid": d1UUID, "name": "my-db", "num_tables": 2, "file_size": 12288, "running_in_region": "WNAM"}),
		"POST /d1/database":     stJSON(map[string]any{"uuid": d1UUID, "name": "new-db"}),
		"PATCH /d1/database/*":  stJSON(map[string]any{"uuid": d1UUID}),
		"DELETE /d1/database/*": stJSON(nil),
		"POST /d1/database/*/raw": stJSON([]any{map[string]any{"success": true, "results": map[string]any{"columns": []string{"id", "name"}, "rows": [][]any{{1, "kyle"}, {2, nil}}},
			"meta": map[string]any{"rows_read": 2, "duration": 0.5}}}),
		"POST /d1/database/*/query": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			sql, _ := req.Body["sql"].(string)
			d.mu.Lock()
			defer d.mu.Unlock()
			d.queries = append(d.queries, sql)
			switch {
			case strings.HasPrefix(sql, "SELECT name FROM d1_migrations"):
				var rows []any
				for _, a := range d.applied {
					rows = append(rows, map[string]any{"name": a})
				}
				writeJSON(w, 200, ok([]any{map[string]any{"results": rows, "success": true}}))
			case strings.Contains(sql, "INSERT INTO d1_migrations"):
				i := strings.Index(sql, "VALUES ('")
				name := sql[i+9 : strings.LastIndex(sql, "')")]
				if strings.Contains(sql, "BROKEN") {
					writeJSON(w, 400, fail(7500, "near \"BROKEN\": syntax error"))
					return
				}
				d.applied = append(d.applied, name)
				writeJSON(w, 200, ok([]any{map[string]any{"results": []any{}, "success": true}}))
			default:
				writeJSON(w, 200, ok([]any{map[string]any{"results": []any{map[string]any{"n": 1}}, "success": true, "meta": map[string]any{}}}))
			}
		},
		"POST /d1/database/*/export": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			d.mu.Lock()
			d.exportN++
			n := d.exportN
			d.mu.Unlock()
			if n == 1 {
				writeJSON(w, 200, ok(map[string]any{"success": true, "type": "export", "at_bookmark": "bm1", "status": "active", "messages": []string{"Generating"}}))
				return
			}
			if req.Body["current_bookmark"] != "bm1" {
				t.Errorf("export poll without bookmark: %v", req.Body)
			}
			writeJSON(w, 200, ok(map[string]any{"success": true, "type": "export", "at_bookmark": "bm1", "status": "complete",
				"result": map[string]any{"filename": "x.sql", "signed_url": d.signedSrv.URL + "/x.sql?sig=1"}}))
		},
		"POST /d1/database/*/import": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			switch req.Body["action"] {
			case "init":
				writeJSON(w, 200, ok(map[string]any{"success": true, "upload_url": d.signedSrv.URL + "/up?sig=1", "filename": "f.sql"}))
			case "ingest":
				writeJSON(w, 200, ok(map[string]any{"success": true, "type": "import", "at_bookmark": "bm2", "status": "active"}))
			case "poll":
				writeJSON(w, 200, ok(map[string]any{"success": true, "type": "import", "at_bookmark": "bm2", "status": "complete",
					"result": map[string]any{"num_queries": 3, "final_bookmark": "bm3"}}))
			}
		},
		"GET /d1/database/*/time_travel/bookmark": stJSON(map[string]any{"bookmark": "00000001-abc"}),
		"POST /d1/database/*/time_travel/restore": stJSON(map[string]any{"bookmark": "00000001-abc", "previous_bookmark": "00000002-def", "message": "ok"}),
		"POST /graphql": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			q, _ := req.Body["query"].(string)
			if strings.Contains(q, "d1QueriesAdaptiveGroups") {
				writeJSON(w, 200, map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"d1QueriesAdaptiveGroups": []any{
					map[string]any{"count": 10, "sum": map[string]any{"queryDurationMs": 50, "rowsRead": 100}, "avg": map[string]any{"queryDurationMs": 5}, "dimensions": map[string]any{"query": "SELECT * FROM a"}},
					map[string]any{"count": 1, "sum": map[string]any{"queryDurationMs": 500, "rowsRead": 1}, "avg": map[string]any{"queryDurationMs": 500}, "dimensions": map[string]any{"query": "SELECT * FROM slow"}},
				}}}}}})
				return
			}
			writeJSON(w, 200, map[string]any{"data": map[string]any{"viewer": map[string]any{"accounts": []any{map[string]any{"d1AnalyticsAdaptiveGroups": []any{
				map[string]any{"sum": map[string]any{"readQueries": 7, "writeQueries": 3, "rowsRead": 70, "rowsWritten": 30}}}}}}}})
		},
	}
	return stFake(t, routes), d
}

func TestD1Databases(t *testing.T) {
	f, _ := newD1Fake(t)
	out, _ := stRun(t, "", "d1", "list")
	stMust(t, out, "1 D1 databases", "my-db", d1UUID, "12.3 KB")
	out, _ = stRun(t, "", "d1", "info", "my-db")
	stMust(t, out, "WNAM", "read queries (24h):", "7", "rows written (24h):", "30")
	out, _ = stRun(t, "", "d1", "info", d1UUID, "--json")
	stMust(t, out, `"last_24h"`, `"readQueries": 7`)

	out, _ = stRun(t, "", "d1", "create", "new-db", "--location", "weur", "--read-replication", "auto")
	stMust(t, out, "Created D1 database new-db", "d1_databases")
	r := stFind(t, f, "POST", "/d1/database")
	if r.Body["primary_location_hint"] != "weur" || r.Body["read_replication"].(map[string]any)["mode"] != "auto" {
		t.Errorf("create body: %v", r.Body)
	}
	stRun(t, "", "d1", "update", "my-db", "--read-replication", "disabled")
	stFind(t, f, "PATCH", "/d1/database/"+d1UUID)
	if _, err := stRunErr(t, "", "d1", "delete", "my-db"); !strings.Contains(err.Error(), "--yes") {
		t.Errorf("delete without --yes: %v", err)
	}
	stRun(t, "", "d1", "delete", "my-db", "--yes")
	stFind(t, f, "DELETE", "/d1/database/"+d1UUID)
	if _, err := stRunErr(t, "", "d1", "info", "nope"); !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown db: %v", err)
	}
}

func TestD1Execute(t *testing.T) {
	f, d := newD1Fake(t)
	out, _ := stRun(t, "", "d1", "execute", "my-db", "--command", "SELECT * FROM users WHERE id = ?", "--param", "1")
	stMust(t, out, "id", "name", "kyle", "NULL", "2 rows", "rows read: 2")
	r := stFind(t, f, "POST", "/d1/database/"+d1UUID+"/raw")
	if r.Body["sql"] != "SELECT * FROM users WHERE id = ?" || r.Body["params"].([]any)[0] != "1" {
		t.Errorf("execute body: %v", r.Body)
	}
	out, _ = stRun(t, "", "d1", "execute", "my-db", "--command", "SELECT 1 AS n", "--json")
	stMust(t, out, `"n": 1`)

	file := filepath.Join(t.TempDir(), "schema.sql")
	_ = os.WriteFile(file, []byte("CREATE TABLE t(id);"), 0o644)
	if _, err := stRunErr(t, "", "d1", "execute", "my-db", "--file", file); !strings.Contains(err.Error(), "--yes") {
		t.Errorf("write file without --yes: %v", err)
	}
	stRun(t, "", "d1", "execute", "my-db", "--file", file, "--yes")

	// Large files go through the import API.
	old := d1ImportThreshold
	d1ImportThreshold = 5
	defer func() { d1ImportThreshold = old }()
	out, _ = stRun(t, "", "d1", "execute", "my-db", "--file", file, "--yes")
	stMust(t, out, "Imported", "3 queries", "bm3")
	if string(d.uploaded) != "CREATE TABLE t(id);" {
		t.Errorf("uploaded %q", d.uploaded)
	}
	if _, err := stRunErr(t, "", "d1", "execute", "my-db"); !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("no input: %v", err)
	}
	if _, err := stRunErr(t, "", "--read-only", "d1", "execute", "my-db", "--command", "SELECT 1"); !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only execute: %v", err)
	}
}

func TestD1ExportImportTimeTravelInsights(t *testing.T) {
	f, d := newD1Fake(t)
	out := filepath.Join(t.TempDir(), "backup.sql")
	stRun(t, "", "d1", "export", "my-db", "--output", out, "--no-data", "--table", "users")
	if b, _ := os.ReadFile(out); string(b) != "CREATE TABLE users(id);\n" {
		t.Errorf("export wrote %q", b)
	}
	r := stFind(t, f, "POST", "/d1/database/"+d1UUID+"/export")
	dump := r.Body["dump_options"].(map[string]any)
	if r.Body["output_format"] != "polling" || dump["no_data"] != true || dump["tables"].([]any)[0] != "users" {
		t.Errorf("export body: %v", r.Body)
	}

	file := filepath.Join(t.TempDir(), "data.sql")
	_ = os.WriteFile(file, []byte("INSERT INTO t VALUES (1);"), 0o644)
	o, _ := stRun(t, "", "d1", "import", "my-db", file, "--yes")
	stMust(t, o, "Imported", "bm3")
	if string(d.uploaded) != "INSERT INTO t VALUES (1);" {
		t.Errorf("import uploaded %q", d.uploaded)
	}

	o, _ = stRun(t, "", "d1", "time-travel", "info", "my-db", "--timestamp", "2026-10-01T00:00:00Z")
	stMust(t, o, "00000001-abc", "--bookmark 00000001-abc")
	r = stFind(t, f, "GET", "/d1/database/"+d1UUID+"/time_travel/bookmark")
	if !strings.Contains(r.Query, "timestamp=2026-10-01T00%3A00%3A00Z") {
		t.Errorf("bookmark query: %s", r.Query)
	}
	if _, err := stRunErr(t, "", "d1", "time-travel", "restore", "my-db"); !strings.Contains(err.Error(), "--bookmark") {
		t.Errorf("restore without target: %v", err)
	}
	o, _ = stRun(t, "", "d1", "time-travel", "restore", "my-db", "--bookmark", "00000001-abc", "--yes")
	stMust(t, o, "Restored", "undo with", "00000002-def")

	o, _ = stRun(t, "", "d1", "insights", "my-db")
	if strings.Index(o, "slow") > strings.Index(o, "FROM a") {
		t.Errorf("insights not sorted by time:\n%s", o)
	}
	o, _ = stRun(t, "", "d1", "insights", "my-db", "--sort-by", "count", "--limit", "1", "--json")
	var rows []map[string]any
	_ = json.Unmarshal([]byte(o), &rows)
	if len(rows) != 1 || rows[0]["query"] != "SELECT * FROM a" {
		t.Errorf("insights --sort-by count: %v", rows)
	}
}

func TestD1Migrations(t *testing.T) {
	_, d := newD1Fake(t)
	dir := filepath.Join(t.TempDir(), "migrations")
	o, _ := stRun(t, "", "d1", "migrations", "create", "my-db", "create users", "--migrations-dir", dir)
	stMust(t, o, "0001_create_users.sql")
	stRun(t, "", "d1", "migrations", "create", "my-db", "add-email", "--migrations-dir", dir)
	if _, err := os.Stat(filepath.Join(dir, "0002_add_email.sql")); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "0001_create_users.sql"), []byte("CREATE TABLE users(id)\n-- trailing comment\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "0002_add_email.sql"), []byte("ALTER TABLE users ADD email TEXT;"), 0o644)
	d.applied = []string{"0001_create_users.sql"}

	o, _ = stRun(t, "", "d1", "migrations", "list", "my-db", "--migrations-dir", dir)
	stMust(t, o, "1 migrations to apply", "0002_add_email.sql")
	if strings.Contains(o, "0001") {
		t.Errorf("applied migration listed:\n%s", o)
	}
	if !strings.Contains(d.queries[0], "CREATE TABLE IF NOT EXISTS d1_migrations") {
		t.Errorf("first query: %s", d.queries[0])
	}

	d.applied = nil
	if _, err := stRunErr(t, "", "d1", "migrations", "apply", "my-db", "--migrations-dir", dir); !strings.Contains(err.Error(), "--yes") {
		t.Errorf("apply without --yes: %v", err)
	}
	o, _ = stRun(t, "", "d1", "migrations", "apply", "my-db", "--migrations-dir", dir, "--yes")
	stMust(t, o, "Applied 2 migrations", "00000001-abc")
	if strings.Join(d.applied, ",") != "0001_create_users.sql,0002_add_email.sql" {
		t.Errorf("applied: %v", d.applied)
	}
	var first string
	for _, q := range d.queries {
		if strings.Contains(q, "0001_create_users.sql") {
			first = q
		}
	}
	if !strings.Contains(first, "-- trailing comment\n;\nINSERT INTO d1_migrations (name) VALUES ('0001_create_users.sql');") {
		t.Errorf("migration SQL not terminated:\n%s", first)
	}
	o, _ = stRun(t, "", "d1", "migrations", "apply", "my-db", "--migrations-dir", dir, "--yes")
	stMust(t, o, "No migrations to apply")

	_ = os.WriteFile(filepath.Join(dir, "0003_bad.sql"), []byte("BROKEN;"), 0o644)
	if _, err := stRunErr(t, "", "d1", "migrations", "apply", "my-db", "--migrations-dir", dir, "--yes"); !strings.Contains(err.Error(), "0003_bad.sql failed") {
		t.Errorf("bad migration: %v", err)
	}
}
