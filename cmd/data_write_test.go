package cmd

// Regression tests for the write-path audit of the data products (D1,
// Queues, K2, Basin): request shapes checked against the OpenAPI spec and
// wrangler.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// queues update sends queue_name and keeps the settings it doesn't change,
// like wrangler (a settings object with one field must not reset the rest).
func TestQueuesUpdateKeepsSettingsAndName(t *testing.T) {
	f := queuesFake(t)
	stRun(t, "", "queues", "update", "jobs", "--message-retention-period", "600")
	r := stFind(t, f, "PATCH", "/queues/"+testQueueID)
	if r.Body["queue_name"] != "jobs" {
		t.Errorf("queue_name not sent: %v", r.Body)
	}
	if stGet(r.Body, "settings.message_retention_period") != float64(600) || stGet(r.Body, "settings.delivery_delay") != float64(5) {
		t.Errorf("settings not merged: %v", r.Body)
	}
}

func TestQueuesUpdateRenameKeepsNewName(t *testing.T) {
	f := queuesFake(t)
	stRun(t, "", "queues", "update", "jobs", "--name", "jobs2")
	r := stFind(t, f, "PATCH", "/queues/"+testQueueID)
	if r.Body["queue_name"] != "jobs2" || r.Body["settings"] != nil {
		t.Errorf("rename body: %v", r.Body)
	}
}

func TestQueuesPauseSendsName(t *testing.T) {
	f := queuesFake(t)
	stRun(t, "", "queues", "pause-delivery", "jobs")
	r := stFind(t, f, "PATCH", "/queues/"+testQueueID)
	if r.Body["queue_name"] != "jobs" || stGet(r.Body, "settings.delivery_paused") != true || stGet(r.Body, "settings.delivery_delay") != float64(5) {
		t.Errorf("pause body: %v", r.Body)
	}
}

// An explicit --http=false must not be overridden by --cors-origins.
func TestK2HTTPFalseNotOverridden(t *testing.T) {
	f := k2Fake(t)
	stRun(t, "", "k2", "streams", "update", "clicks", "--http=false", "--cors-origins", "https://a.example")
	r := stFind(t, f, "PATCH", "/k2/streams/st1")
	if stGet(r.Body, "http.enabled") != false {
		t.Errorf("http.enabled overridden: %v", r.Body)
	}
}

// basin stream update fills http.enabled/authentication (both required)
// from the current stream, not from the create defaults.
func TestBasinStreamUpdateKeepsAuth(t *testing.T) {
	f := basinFake(t)
	stRun(t, "", "basin", "pipelines", "streams", "update", "clicks", "--cors-origins", "https://a.example")
	r := stFind(t, f, "PATCH", "/pipelines/v1/streams/st1")
	if stGet(r.Body, "http.authentication") != true || stGet(r.Body, "http.enabled") != true {
		t.Errorf("update body: %v", r.Body)
	}
	if o, ok := stGet(r.Body, "http.cors.origins").([]any); !ok || len(o) != 1 {
		t.Errorf("cors origins: %v", r.Body)
	}
}

// D1 import: an init answer carrying upload_url means "upload", even with a
// status set (wrangler only checks for upload_url). Before the fix cfctl
// kept re-sending init.
func TestD1ImportInitWithStatus(t *testing.T) {
	var uploaded []byte
	signed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		uploaded = b
		w.Header().Set("ETag", `"`+etagOf(b)+`"`)
	}))
	t.Cleanup(signed.Close)
	d1PollInterval = 0
	inits := 0
	f := stFake(t, map[string]stHandler{
		"GET /d1/database": stListH([]map[string]any{{"uuid": d1UUID, "name": "my-db"}}),
		"POST /d1/database/*/import": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			switch req.Body["action"] {
			case "init":
				inits++
				if inits > 1 {
					t.Errorf("init sent %d times", inits)
					writeJSON(w, 400, fail(1, "loop"))
					return
				}
				writeJSON(w, 200, ok(map[string]any{"success": true, "status": "active", "upload_url": signed.URL + "/up?sig=1", "filename": "f.sql"}))
			case "ingest":
				if req.Body["filename"] != "f.sql" || req.Body["etag"] == nil {
					t.Errorf("ingest body %v", req.Body)
				}
				writeJSON(w, 200, ok(map[string]any{"success": true, "status": "complete", "result": map[string]any{"num_queries": 1, "final_bookmark": "bm"}}))
			}
		},
	})
	file := filepath.Join(t.TempDir(), "x.sql")
	_ = os.WriteFile(file, []byte("SELECT 1;"), 0o644)
	stRun(t, "", "d1", "import", "my-db", file, "--yes")
	if string(uploaded) != "SELECT 1;" {
		t.Errorf("uploaded %q", uploaded)
	}
	stFind(t, f, "POST", "/d1/database/"+d1UUID+"/import")
}
