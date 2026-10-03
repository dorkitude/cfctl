package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const r2TestQueueID = "44444444444444444444444444444444"

func r2ConfigRoutes() map[string]stHandler {
	return map[string]stHandler{
		"GET /r2/buckets/*/cors":    stJSON(map[string]any{"rules": []any{map[string]any{"allowed": map[string]any{"origins": []any{"*"}, "methods": []any{"GET"}}, "maxAgeSeconds": 60}}}),
		"PUT /r2/buckets/*/cors":    stJSON(nil),
		"DELETE /r2/buckets/*/cors": stJSON(nil),
		"GET /r2/buckets/*/lifecycle": stJSON(map[string]any{"rules": []any{map[string]any{"id": "Default Multipart Abort Rule", "enabled": true, "conditions": map[string]any{},
			"abortMultipartUploadsTransition": map[string]any{"condition": map[string]any{"type": "Age", "maxAge": 604800}}}}}),
		"PUT /r2/buckets/*/lifecycle":           stJSON(nil),
		"GET /r2/buckets/*/lock":                stJSON(map[string]any{"rules": []any{}}),
		"PUT /r2/buckets/*/lock":                stJSON(nil),
		"GET /r2/buckets/*/domains/custom":      stJSON(map[string]any{"domains": []any{map[string]any{"domain": "cdn.example.com", "enabled": true, "status": map[string]any{"ssl": "active", "ownership": "active"}, "minTLS": "1.0", "zoneName": "example.com"}}}),
		"POST /r2/buckets/*/domains/custom":     stJSON(map[string]any{"domain": "assets.example.com", "enabled": true}),
		"PUT /r2/buckets/*/domains/custom/*":    stJSON(map[string]any{"domain": "cdn.example.com"}),
		"DELETE /r2/buckets/*/domains/custom/*": stJSON(nil),
		"GET /r2/buckets/*/domains/managed":     stJSON(map[string]any{"enabled": false, "domain": "pub-x.r2.dev"}),
		"PUT /r2/buckets/*/domains/managed":     stJSON(map[string]any{"enabled": true, "domain": "pub-x.r2.dev"}),
		"GET /r2/buckets/*/local-uploads":       stJSON(map[string]any{"enabled": false}),
		"PUT /r2/buckets/*/local-uploads":       stJSON(map[string]any{"enabled": true}),
		"GET /r2/buckets/*/sippy":               stJSON(map[string]any{"enabled": false}),
		"PUT /r2/buckets/*/sippy":               stJSON(map[string]any{"enabled": true}),
		"DELETE /r2/buckets/*/sippy":            stJSON(nil),
		"GET /r2/buckets/*/jobs":                stJSON(map[string]any{"jobs": []any{map[string]any{"id": "job1", "jobType": "prefixDelete", "status": "COMPLETED", "prefix": "tmp/"}}}),
		"GET /r2/buckets/*/jobs/*":              stJSON(map[string]any{"id": "job1", "status": "RUNNING"}),
		"POST /r2/buckets/*/jobs":               stJSON(map[string]any{"id": "job2"}),
		"GET /queues":                           stListH([]map[string]any{{"queue_id": r2TestQueueID, "queue_name": "uploads"}}),
		"GET /event_notifications/r2/*/configuration": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			if strings.Contains(req.Path, "/empty/") {
				writeJSON(w, 404, fail(11015, "No event notification config found"))
				return
			}
			writeJSON(w, 200, ok(map[string]any{"bucketName": "bkt", "queues": []any{map[string]any{"queueId": r2TestQueueID, "queueName": "uploads",
				"rules": []any{map[string]any{"ruleId": "r1", "actions": []any{"PutObject"}, "prefix": "img/"}}}}}))
		},
		"PUT /event_notifications/r2/*/configuration/queues/*":    stJSON(nil),
		"DELETE /event_notifications/r2/*/configuration/queues/*": stJSON(nil),
	}
}

func TestR2BucketCorsLifecycleLock(t *testing.T) {
	f := stFake(t, r2ConfigRoutes())
	out, _ := stRun(t, "", "r2", "buckets", "cors", "list", "bkt")
	stMust(t, out, "1 CORS rules", "GET", "60")

	rules := filepath.Join(t.TempDir(), "cors.json")
	_ = os.WriteFile(rules, []byte(`{"rules":[{"allowed":{"origins":["https://a.com"],"methods":["GET"]}}]}`), 0o644)
	stRun(t, "", "r2", "buckets", "cors", "set", "bkt", "--file", rules, "--yes")
	r := stFind(t, f, "PUT", "/r2/buckets/bkt/cors")
	if !strings.Contains(string(r.RawBody), "https://a.com") {
		t.Errorf("cors set body: %s", r.RawBody)
	}
	stRun(t, "", "r2", "buckets", "cors", "delete", "bkt", "-y")
	stFind(t, f, "DELETE", "/r2/buckets/bkt/cors")

	out, _ = stRun(t, "", "r2", "buckets", "lifecycle", "list", "bkt")
	stMust(t, out, "Default Multipart Abort Rule", "abort uploads after 7 days")
	stRun(t, "", "r2", "buckets", "lifecycle", "add", "bkt", "--id", "exp", "--prefix", "tmp/", "--expire-days", "7", "--ia-transition-days", "2")
	r = stFind(t, f, "PUT", "/r2/buckets/bkt/lifecycle")
	var body struct {
		Rules []map[string]any `json:"rules"`
	}
	_ = json.Unmarshal(r.RawBody, &body)
	if len(body.Rules) != 2 || body.Rules[1]["id"] != "exp" {
		t.Fatalf("lifecycle add body: %s", r.RawBody)
	}
	del := body.Rules[1]["deleteObjectsTransition"].(map[string]any)["condition"].(map[string]any)
	if del["maxAge"] != float64(7*86400) || body.Rules[1]["conditions"].(map[string]any)["prefix"] != "tmp/" {
		t.Errorf("lifecycle rule: %v", body.Rules[1])
	}
	if _, err := stRunErr(t, "", "r2", "buckets", "lifecycle", "add", "bkt", "--id", "x"); !strings.Contains(err.Error(), "action") {
		t.Errorf("lifecycle add without action: %v", err)
	}
	stRun(t, "", "r2", "buckets", "lifecycle", "remove", "bkt", "--id", "Default Multipart Abort Rule", "--yes")
	if _, err := stRunErr(t, "", "r2", "buckets", "lifecycle", "remove", "bkt", "--id", "nope", "--yes"); !strings.Contains(err.Error(), "no lifecycle rule") {
		t.Errorf("remove unknown: %v", err)
	}

	stRun(t, "", "r2", "buckets", "lock", "add", "bkt", "--id", "keep", "--prefix", "backups/", "--retention-days", "30", "--yes")
	r = stFind(t, f, "PUT", "/r2/buckets/bkt/lock")
	if !strings.Contains(string(r.RawBody), `"maxAgeSeconds":2592000`) || !strings.Contains(string(r.RawBody), `"prefix":"backups/"`) {
		t.Errorf("lock add body: %s", r.RawBody)
	}
	out, _ = stRun(t, "", "r2", "buckets", "lock", "list", "bkt")
	stMust(t, out, "No lock rules")
}

func TestR2BucketDomainsDevURLNotifications(t *testing.T) {
	f := stFake(t, r2ConfigRoutes())
	out, _ := stRun(t, "", "r2", "buckets", "domain", "list", "bkt")
	stMust(t, out, "cdn.example.com", "active")

	stRun(t, "", "r2", "buckets", "domain", "add", "bkt", "--domain", "assets.example.com", "--min-tls", "1.2")
	r := stFind(t, f, "POST", "/r2/buckets/bkt/domains/custom")
	if r.Body["zoneId"] != zoneID || r.Body["domain"] != "assets.example.com" || r.Body["minTLS"] != "1.2" || r.Body["enabled"] != true {
		t.Errorf("domain add body: %v", r.Body)
	}
	stRun(t, "", "r2", "buckets", "domain", "update", "bkt", "cdn.example.com", "--enabled=false")
	r = stFind(t, f, "PUT", "/r2/buckets/bkt/domains/custom/cdn.example.com")
	if r.Body["enabled"] != false {
		t.Errorf("domain update: %v", r.Body)
	}
	stRun(t, "", "r2", "buckets", "domain", "remove", "bkt", "cdn.example.com", "--yes")

	out, _ = stRun(t, "", "r2", "buckets", "dev-url", "get", "bkt")
	stMust(t, out, "pub-x.r2.dev")
	stRun(t, "", "r2", "buckets", "dev-url", "enable", "bkt", "--yes")
	r = stFind(t, f, "PUT", "/r2/buckets/bkt/domains/managed")
	if r.Body["enabled"] != true {
		t.Errorf("dev-url enable: %v", r.Body)
	}
	stRun(t, "", "r2", "buckets", "local-uploads", "disable", "bkt", "--yes")
	r = stFind(t, f, "PUT", "/r2/buckets/bkt/local-uploads")
	if r.Body["enabled"] != false {
		t.Errorf("local-uploads disable: %v", r.Body)
	}

	out, _ = stRun(t, "", "r2", "buckets", "notification", "list", "bkt")
	stMust(t, out, "uploads", "r1", "PutObject", "img/")
	out, _ = stRun(t, "", "r2", "buckets", "notification", "list", "empty")
	stMust(t, out, "No event notification rules")
	stRun(t, "", "r2", "buckets", "notification", "create", "bkt", "--queue", "uploads", "--event-types", "object-create", "--suffix", ".png")
	r = stFind(t, f, "PUT", "/event_notifications/r2/bkt/configuration/queues/"+r2TestQueueID)
	if !strings.Contains(string(r.RawBody), `"CompleteMultipartUpload"`) || !strings.Contains(string(r.RawBody), `"suffix":".png"`) {
		t.Errorf("notification create body: %s", r.RawBody)
	}
	stRun(t, "", "r2", "buckets", "notification", "delete", "bkt", "--queue", "uploads", "--rule", "r1", "--yes")
	r = stFind(t, f, "DELETE", "/event_notifications/r2/bkt/configuration/queues/"+r2TestQueueID)
	if !strings.Contains(string(r.RawBody), `"ruleIds":["r1"]`) {
		t.Errorf("notification delete body: %s", r.RawBody)
	}
}

func TestR2BucketSippyJobs(t *testing.T) {
	f := stFake(t, r2ConfigRoutes())
	stRun(t, "", "r2", "buckets", "sippy", "enable", "bkt", "--provider", "aws", "--bucket", "src", "--region", "us-east-1",
		"--access-key-id", "AKIA1", "--secret-access-key", "S1", "--r2-access-key-id", "R2ID", "--r2-secret-access-key", "R2S")
	r := stFind(t, f, "PUT", "/r2/buckets/bkt/sippy")
	src := r.Body["source"].(map[string]any)
	dst := r.Body["destination"].(map[string]any)
	if src["provider"] != "aws" || src["bucket"] != "src" || src["region"] != "us-east-1" || dst["provider"] != "r2" || dst["accessKeyId"] != "R2ID" {
		t.Errorf("sippy body: %v", r.Body)
	}
	key := filepath.Join(t.TempDir(), "key.json")
	_ = os.WriteFile(key, []byte(`{"client_email":"sa@x.iam","private_key":"-----BEGIN"}`), 0o600)
	stRun(t, "", "r2", "buckets", "sippy", "enable", "bkt2", "--provider", "gcs", "--bucket", "g", "--service-account-key-file", key)
	r = stFind(t, f, "PUT", "/r2/buckets/bkt2/sippy")
	if r.Body["source"].(map[string]any)["clientEmail"] != "sa@x.iam" {
		t.Errorf("gcs sippy body: %v", r.Body)
	}
	stRun(t, "", "r2", "buckets", "sippy", "disable", "bkt", "--yes")
	stFind(t, f, "DELETE", "/r2/buckets/bkt/sippy")

	out, _ := stRun(t, "", "r2", "buckets", "jobs", "list", "bkt")
	stMust(t, out, "job1", "prefixDelete", "COMPLETED")
	if _, err := stRunErr(t, "", "r2", "buckets", "jobs", "create", "bkt", "--type", "prefix-delete", "--prefix", "tmp", "--yes"); !strings.Contains(err.Error(), "end in /") {
		t.Errorf("bad prefix: %v", err)
	}
	stRun(t, "", "r2", "buckets", "jobs", "create", "bkt", "--type", "prefix-delete", "--prefix", "tmp/", "--yes")
	r = stFind(t, f, "POST", "/r2/buckets/bkt/jobs")
	if r.Body["jobType"] != "prefixDelete" || r.Body["prefix"] != "tmp/" {
		t.Errorf("job body: %v", r.Body)
	}
}
