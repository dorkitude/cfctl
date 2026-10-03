package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

const testQueueID = "11111111111111111111111111111111"

func queuesFake(t *testing.T) *fakeCF {
	queue := map[string]any{"queue_id": testQueueID, "queue_name": "jobs", "created_on": "2026-10-01T10:00:00Z",
		"producers_total_count": 1, "consumers_total_count": 1,
		"settings": map[string]any{"delivery_delay": 5, "delivery_paused": false, "message_retention_period": 345600}}
	consumers := []map[string]any{
		{"consumer_id": "c1", "type": "worker", "script": "my-worker", "settings": map[string]any{"batch_size": 10, "max_retries": 3}},
		{"consumer_id": "c2", "type": "http_pull", "settings": map[string]any{"batch_size": 5}},
	}
	q := "/queues/" + testQueueID
	return stFake(t, map[string]stHandler{
		"GET /queues":                                 stListH([]map[string]any{queue}),
		"POST /queues":                                stJSON(queue),
		"GET " + q:                                    stJSON(queue),
		"PATCH " + q:                                  stJSON(queue),
		"DELETE " + q:                                 stJSON(nil),
		"GET " + q + "/consumers":                     stListH(consumers),
		"POST " + q + "/consumers":                    stJSON(consumers[0]),
		"PUT " + q + "/consumers/*":                   stJSON(consumers[0]),
		"DELETE " + q + "/consumers/*":                stJSON(nil),
		"POST " + q + "/purge":                        stJSON(nil),
		"GET " + q + "/purge":                         stJSON(map[string]any{"completed": "true", "started_at": "2026-10-03T00:00:00Z"}),
		"GET " + q + "/metrics":                       stJSON(map[string]any{"backlog_count": 3, "backlog_bytes": 120}),
		"POST " + q + "/messages":                     stJSON(nil),
		"POST " + q + "/messages/batch":               stJSON(nil),
		"POST " + q + "/messages/ack":                 stJSON(map[string]any{"ackCount": 1, "retryCount": 1}),
		"POST " + q + "/messages/pull":                stJSON(map[string]any{"message_backlog_count": 7, "messages": []any{map[string]any{"id": "m1", "lease_id": "L1", "attempts": 1, "body": "hello"}}}),
		"POST " + q + "/messages/peek":                stJSON(map[string]any{"messages": []any{map[string]any{"id": "m1", "ref": "R1", "attempts": 0, "body": "hi"}}}),
		"GET /event_subscriptions/subscriptions":      stListH([]map[string]any{{"id": "s1", "name": "r2-ev", "enabled": true, "events": []any{"bucket.created"}, "source": map[string]any{"type": "r2"}, "destination": map[string]any{"type": "queues.queue", "queue_id": testQueueID}}, {"id": "s2", "name": "other", "destination": map[string]any{"queue_id": "zzz"}}}),
		"POST /event_subscriptions/subscriptions":     stJSON(map[string]any{"id": "s3", "name": "new"}),
		"PATCH /event_subscriptions/subscriptions/*":  stJSON(map[string]any{"id": "s1"}),
		"DELETE /event_subscriptions/subscriptions/*": stJSON(nil),
		"GET /event_subscriptions/subscriptions/*":    stJSON(map[string]any{"id": "s1", "name": "r2-ev"}),
	})
}

func TestQueuesListGet(t *testing.T) {
	queuesFake(t)
	out, _ := stRun(t, "", "queues", "list")
	stMust(t, out, "1 queues", "jobs", testQueueID, "NAME")
	out, _ = stRun(t, "", "queues", "list", "--json")
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil || items[0]["queue_name"] != "jobs" {
		t.Fatalf("bad json: %v %s", err, out)
	}
	out, _ = stRun(t, "", "queues", "info", "jobs")
	stMust(t, out, "queue_name:", "jobs", "delivery_delay:")
	out, _ = stRun(t, "", "queues", "metrics", "jobs")
	stMust(t, out, "backlog_count:", "3")
}

func TestQueuesCreateUpdateDelete(t *testing.T) {
	f := queuesFake(t)
	stRun(t, "", "queues", "create", "jobs", "--delivery-delay", "10", "--jurisdiction", "eu")
	r := stFind(t, f, "POST", "/queues")
	if r.Body["queue_name"] != "jobs" || r.Body["jurisdiction"] != "eu" || stGet(r.Body, "settings.delivery_delay") != float64(10) {
		t.Fatalf("create body: %v", r.Body)
	}
	stRun(t, "", "queues", "update", "jobs", "--message-retention-period", "600")
	r = stFind(t, f, "PATCH", "/queues/"+testQueueID)
	if stGet(r.Body, "settings.message_retention_period") != float64(600) {
		t.Fatalf("update body: %v", r.Body)
	}
	if _, err := stRunErr(t, "", "queues", "delete", "jobs"); !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("want confirmation error, got %v", err)
	}
	stRun(t, "", "queues", "delete", "jobs", "--yes")
	stFind(t, f, "DELETE", "/queues/"+testQueueID)
}

func TestQueuesPauseResumePurge(t *testing.T) {
	f := queuesFake(t)
	stRun(t, "", "queues", "pause-delivery", "jobs")
	r := stFind(t, f, "PATCH", "/queues/"+testQueueID)
	if stGet(r.Body, "settings.delivery_paused") != true || stGet(r.Body, "settings.delivery_delay") != float64(5) {
		t.Fatalf("pause body should keep settings: %v", r.Body)
	}
	stRun(t, "", "queues", "purge", "jobs", "-y")
	r = stFind(t, f, "POST", "/queues/"+testQueueID+"/purge")
	if r.Body["delete_messages_permanently"] != true {
		t.Fatalf("purge body: %v", r.Body)
	}
	out, _ := stRun(t, "", "queues", "purge", "status", "jobs")
	stMust(t, out, "completed:")
}

func TestQueuesConsumers(t *testing.T) {
	f := queuesFake(t)
	out, _ := stRun(t, "", "queues", "consumer", "list", "jobs")
	stMust(t, out, "my-worker", "http_pull", "c2")
	stRun(t, "", "queues", "consumer", "add", "jobs", "w2", "--batch-size", "50", "--batch-timeout", "5", "--dead-letter-queue", "dlq")
	r := stFind(t, f, "POST", "/queues/"+testQueueID+"/consumers")
	if r.Body["script_name"] != "w2" || r.Body["type"] != "worker" || stGet(r.Body, "settings.max_wait_time_ms") != float64(5000) || r.Body["dead_letter_queue"] != "dlq" {
		t.Fatalf("add body: %v", r.Body)
	}
	stRun(t, "", "queues", "consumer", "update", "jobs", "my-worker", "--batch-size", "99")
	r = stFind(t, f, "PUT", "/queues/"+testQueueID+"/consumers/c1")
	if stGet(r.Body, "settings.batch_size") != float64(99) || stGet(r.Body, "settings.max_retries") != float64(3) || r.Body["script_name"] != "my-worker" {
		t.Fatalf("update body: %v", r.Body)
	}
	stRun(t, "", "queues", "consumer", "remove", "jobs", "my-worker", "--yes")
	stFind(t, f, "DELETE", "/queues/"+testQueueID+"/consumers/c1")
	stRun(t, "", "queues", "consumer", "http", "remove", "jobs", "--yes")
	stFind(t, f, "DELETE", "/queues/"+testQueueID+"/consumers/c2")
	stRun(t, "", "queues", "consumer", "http", "add", "jobs", "--visibility-timeout", "1000")
}

func TestQueuesMessages(t *testing.T) {
	f := queuesFake(t)
	stRun(t, "", "queues", "send", "jobs", `{"a":1}`, "--json-body", "--delay", "3")
	r := stFind(t, f, "POST", "/queues/"+testQueueID+"/messages")
	if r.Body["content_type"] != "json" || stGet(r.Body, "body.a") != float64(1) || r.Body["delay_seconds"] != float64(3) {
		t.Fatalf("send body: %v", r.Body)
	}
	if _, err := stRunErr(t, "", "queues", "send", "jobs", "not json", "--content-type", "json"); err == nil {
		t.Fatal("want error")
	}
	stRun(t, `[{"body":"x"},{"k":2}]`, "queues", "send-batch", "jobs", "-")
	r = stFind(t, f, "POST", "/queues/"+testQueueID+"/messages/batch")
	msgs := r.Body["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["content_type"] != "text" || msgs[1].(map[string]any)["content_type"] != "json" {
		t.Fatalf("batch body: %v", r.Body)
	}
	out, _ := stRun(t, "", "queues", "pull", "jobs", "--batch-size", "5")
	stMust(t, out, "L1", "hello", "Backlog: 7")
	out, _ = stRun(t, "", "queues", "peek", "jobs")
	stMust(t, out, "R1")
	stRun(t, "", "queues", "ack", "jobs", "--ack", "L1", "--retry", "L2", "--retry-delay", "9")
	r = stFind(t, f, "POST", "/queues/"+testQueueID+"/messages/ack")
	if len(r.Body["acks"].([]any)) != 1 || stGet(r.Body["retries"].([]any)[0], "delay_seconds") != float64(9) {
		t.Fatalf("ack body: %v", r.Body)
	}
}

func TestQueuesSubscriptions(t *testing.T) {
	f := queuesFake(t)
	out, _ := stRun(t, "", "queues", "subscription", "list", "jobs")
	stMust(t, out, "r2-ev")
	if strings.Contains(out, "other") {
		t.Fatalf("filter by queue failed: %s", out)
	}
	stRun(t, "", "queues", "subscription", "create", "jobs", "--source", "workflows.workflow", "--workflow-name", "wf", "--events", "a,b", "--name", "n")
	r := stFind(t, f, "POST", "/event_subscriptions/subscriptions")
	if stGet(r.Body, "destination.queue_id") != testQueueID || stGet(r.Body, "source.workflow_name") != "wf" || len(r.Body["events"].([]any)) != 2 {
		t.Fatalf("subscription body: %v", r.Body)
	}
	stRun(t, "", "queues", "subscription", "update", "s1", "--enabled=false")
	r = stFind(t, f, "PATCH", "/event_subscriptions/subscriptions/s1")
	if r.Body["enabled"] != false {
		t.Fatalf("update body: %v", r.Body)
	}
	stRun(t, "", "queues", "subscription", "get", "s1")
	stRun(t, "", "queues", "subscription", "delete", "s1", "-y")
	stFind(t, f, "DELETE", "/event_subscriptions/subscriptions/s1")
}

func TestQueuesReadOnly(t *testing.T) {
	f := queuesFake(t)
	_, err := stRunErr(t, "", "--read-only", "queues", "create", "x")
	if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("want read-only error, got %v", err)
	}
	stRunErr(t, "", "--read-only", "queues", "delete", "jobs")
	stNoMutation(t, f)
}
