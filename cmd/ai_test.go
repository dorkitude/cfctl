package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const llama = "@cf/meta/llama-3.1-8b-instruct"

func aiFixtures(p *platFakeT) {
	models := []any{
		map[string]any{"id": "m1", "name": llama, "description": "Llama", "task": map[string]any{"name": "Text Generation"}, "properties": []any{map[string]any{"property_id": "context_window", "value": "8192"}}},
		map[string]any{"id": "m2", "name": "@cf/black-forest-labs/flux-1-schnell", "description": "Flux", "task": map[string]any{"name": "Text-to-Image"}},
	}
	p.routes["GET "+platA+"/ai/models/search"] = models
	p.routes["GET "+platA+"/ai/models/schema"] = map[string]any{"input": map[string]any{"type": "object"}, "output": map[string]any{"type": "object"}}
	p.routes["GET "+platA+"/ai/tasks/search"] = []any{map[string]any{"id": "t1", "name": "Text Generation"}}
	p.routes["GET "+platA+"/ai/authors/search"] = []any{map[string]any{"name": "meta"}}
	p.routes["GET "+platA+"/ai/finetunes"] = []any{map[string]any{"id": "ft1", "name": "mine", "model": llama}}
	p.routes["GET "+platA+"/ai/finetunes/public"] = []any{map[string]any{"id": "ft2", "name": "pub", "model": llama}}
	p.routes["POST "+platA+"/ai/run/"+llama] = func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		if req.Body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"response\":\"Hel\"}\n\ndata: {\"response\":\"lo\"}\n\ndata: [DONE]\n\n"))
			return
		}
		writeJSON(w, 200, ok(map[string]any{"response": "Hello, " + platStr(req.Body["prompt"])}))
	}
	p.routes["POST "+platA+"/ai/run/@cf/black-forest-labs/flux-1-schnell"] = func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNGfake"))
	}
}

func TestAICommands(t *testing.T) {
	p := platFake(t)
	aiFixtures(p)
	platCheck(t, p, []platCase{
		{args: []string{"ai", "models", "list", "--task", "Text Generation"}, method: "GET", path: platA + "/ai/models/search", query: "task=Text+Generation", want: llama},
		{args: []string{"ai", "models", "search", "llama"}, method: "GET", path: platA + "/ai/models/search", query: "search=llama"},
		{args: []string{"ai", "models", "get", llama}, method: "GET", path: platA + "/ai/models/search", want: "context_window"},
		{args: []string{"ai", "models", "schema", llama}, method: "GET", path: platA + "/ai/models/schema", query: "model=%40cf", want: `"input"`},
		{args: []string{"ai", "tasks"}, method: "GET", path: platA + "/ai/tasks/search", want: "Text Generation"},
		{args: []string{"ai", "authors"}, method: "GET", path: platA + "/ai/authors/search", want: "meta"},
		{args: []string{"ai", "finetune", "list"}, method: "GET", path: platA + "/ai/finetunes", want: "ft1"},
		{args: []string{"ai", "finetune", "public"}, method: "GET", path: platA + "/ai/finetunes/public", want: "ft2"},
		{args: []string{"ai", "finetune", "delete", "ft1", "--yes"}, method: "DELETE", path: platA + "/ai/finetunes/ft1"},
		{args: []string{"ai", "run", llama, "--prompt", "Kyle"}, method: "POST", path: platA + "/ai/run/" + llama, body: map[string]any{"prompt": "Kyle"}, want: "Hello, Kyle"},
		{args: []string{"ai", "run", llama, "--prompt", "q", "--system", "be brief", "--max-tokens", "50"}, method: "POST", path: platA + "/ai/run/" + llama,
			body: map[string]any{"messages": []any{map[string]any{"role": "system", "content": "be brief"}, map[string]any{"role": "user", "content": "q"}}, "max_tokens": 50}},
		{args: []string{"ai", "run", llama, "--data", `{"prompt":"x","seed":1}`}, method: "POST", path: platA + "/ai/run/" + llama, body: map[string]any{"seed": 1}},
	})
	if msg := platRunErr(t, "", "ai", "models", "get", "@cf/nope"); !strings.Contains(msg, "not found") {
		t.Fatal(msg)
	}
	if msg := platRunErr(t, "", "ai", "run", llama); !strings.Contains(msg, "exactly one") {
		t.Fatal(msg)
	}
}

func TestAIRunStreamAndBinary(t *testing.T) {
	p := platFake(t)
	aiFixtures(p)
	out := platRunOK(t, "", "ai", "run", llama, "--prompt", "hi", "--stream")
	if strings.TrimSpace(out) != "Hello" {
		t.Fatalf("stream: %q", out)
	}
	r := p.last(t, "POST", platA+"/ai/run/"+llama)
	if r.Body["stream"] != true || r.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("stream request: %s %v", r.RawBody, r.Header)
	}
	if r.Auth != "Bearer "+goodToken {
		t.Fatal("stream request not authenticated with the stored token")
	}
	dest := filepath.Join(t.TempDir(), "out.png")
	platRunOK(t, "", "ai", "run", "@cf/black-forest-labs/flux-1-schnell", "--prompt", "cloud", "-o", dest)
	if b, _ := os.ReadFile(dest); string(b) != "\x89PNGfake" {
		t.Fatalf("binary output: %q", b)
	}
	if msg := platRunErr(t, "", "ai", "run", "@cf/black-forest-labs/flux-1-schnell", "--prompt", "cloud"); !strings.Contains(msg, "--output") {
		t.Fatal(msg)
	}
	in := filepath.Join(t.TempDir(), "a.mp3")
	_ = os.WriteFile(in, []byte("audio"), 0o600)
	platRunOK(t, "", "ai", "run", llama, "--file", in, "--json")
	if r := p.last(t, "POST", platA+"/ai/run/"+llama); string(r.RawBody) != "audio" || r.ContentType != "application/octet-stream" {
		t.Fatalf("--file body: %q %s", r.RawBody, r.ContentType)
	}
}

func TestAIFinetuneCreateAndMarkdown(t *testing.T) {
	p := platFake(t)
	p.routes["POST "+platA+"/ai/finetunes"] = func(fakeRequest) (int, any) { return 200, ok(map[string]any{"id": "ft9", "name": "mine"}) }
	p.routes["POST "+platA+"/ai/tomarkdown"] = []any{map[string]any{"name": "a.html", "data": "# Title", "format": "markdown"}}
	dir := t.TempDir()
	if msg := platRunErr(t, "", "ai", "finetune", "create", llama, "mine", dir); !strings.Contains(msg, "adapter_config.json") {
		t.Fatal(msg)
	}
	for _, f := range aiFinetuneRequired {
		_ = os.WriteFile(filepath.Join(dir, f), []byte(f), 0o600)
	}
	out := platRunOK(t, "", "ai", "finetune", "create", llama, "mine", dir, "--description", "d")
	if !strings.Contains(out, "ft9") {
		t.Fatal(out)
	}
	r := p.last(t, "POST", platA+"/ai/finetunes")
	if r.Body["model"] != llama || r.Body["name"] != "mine" || r.Body["description"] != "d" {
		t.Fatalf("create body: %s", r.RawBody)
	}
	n := 0
	for _, r := range p.Requests() {
		if r.Path == platA+"/ai/finetunes/ft9/finetune-assets" && strings.HasPrefix(r.ContentType, "multipart/form-data") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("uploaded %d assets", n)
	}
	html := filepath.Join(dir, "a.html")
	_ = os.WriteFile(html, []byte("<h1>Title</h1>"), 0o600)
	out = platRunOK(t, "", "ai", "markdown", html)
	if !strings.Contains(out, "# Title") {
		t.Fatal(out)
	}
}

func TestAISearchCommands(t *testing.T) {
	p := platFake(t)
	inst := platA + "/ai-search/namespaces/default/instances"
	p.routes["GET "+inst] = []any{map[string]any{"id": "docs", "type": "r2", "source": "bucket", "status": "ready"}}
	p.routes["GET "+platA+"/ai-search/namespaces"] = []any{map[string]any{"name": "default"}}
	p.routes["POST "+inst+"/docs/search"] = map[string]any{"search_query": "q", "chunks": []any{map[string]any{"score": 0.9, "text": "chunk text", "item": map[string]any{"key": "a.md"}}}}
	platCheck(t, p, []platCase{
		{args: []string{"ai-search", "list"}, method: "GET", path: inst, want: "bucket"},
		{args: []string{"ai-search", "list", "-n", "team"}, method: "GET", path: platA + "/ai-search/namespaces/team/instances"},
		{args: []string{"ai-search", "get", "docs"}, method: "GET", path: inst + "/docs"},
		{args: []string{"ai-search", "create", "docs", "--type", "r2", "--source", "bucket", "--chunk-size", "512", "--reranking"}, method: "POST", path: inst,
			body: map[string]any{"id": "docs", "type": "r2", "source": "bucket", "chunk_size": 512, "reranking": true}},
		{args: []string{"ai-search", "update", "docs", "--generation-model", "@cf/x", "--hybrid-search"}, method: "PUT", path: inst + "/docs",
			body: map[string]any{"ai_search_model": "@cf/x", "hybrid_search_enabled": true}},
		{args: []string{"ai-search", "delete", "docs", "--yes"}, method: "DELETE", path: inst + "/docs"},
		{args: []string{"ai-search", "stats", "docs"}, method: "GET", path: inst + "/docs/stats"},
		{args: []string{"ai-search", "search", "docs", "how?", "--max-num-results", "3", "--filter", "lang=en"}, method: "POST", path: inst + "/docs/search",
			body: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "how?"}}, "ai_search_options": map[string]any{"retrieval": map[string]any{"max_num_results": 3, "filters": map[string]any{"lang": "en"}}}},
			want: "chunk text"},
		{args: []string{"ai-search", "jobs", "list", "docs"}, method: "GET", path: inst + "/docs/jobs"},
		{args: []string{"ai-search", "jobs", "get", "docs", "j1"}, method: "GET", path: inst + "/docs/jobs/j1"},
		{args: []string{"ai-search", "jobs", "create", "docs"}, method: "POST", path: inst + "/docs/jobs"},
		{args: []string{"ai-search", "jobs", "cancel", "docs", "j1", "--yes"}, method: "PATCH", path: inst + "/docs/jobs/j1", body: map[string]any{"action": "cancel"}},
		{args: []string{"ai-search", "jobs", "logs", "docs", "j1"}, method: "GET", path: inst + "/docs/jobs/j1/logs"},
		{args: []string{"ai-search", "items", "list", "docs"}, method: "GET", path: inst + "/docs/items"},
		{args: []string{"ai-search", "items", "get", "docs", "i1"}, method: "GET", path: inst + "/docs/items/i1"},
		{args: []string{"ai-search", "items", "chunks", "docs", "i1"}, method: "GET", path: inst + "/docs/items/i1/chunks"},
		{args: []string{"ai-search", "items", "logs", "docs", "i1"}, method: "GET", path: inst + "/docs/items/i1/logs"},
		{args: []string{"ai-search", "items", "delete", "docs", "i1", "--yes"}, method: "DELETE", path: inst + "/docs/items/i1"},
		{args: []string{"ai-search", "namespace", "list"}, method: "GET", path: platA + "/ai-search/namespaces", want: "default"},
		{args: []string{"ai-search", "namespace", "get", "team"}, method: "GET", path: platA + "/ai-search/namespaces/team"},
		{args: []string{"ai-search", "namespace", "create", "team", "--description", "d"}, method: "POST", path: platA + "/ai-search/namespaces", body: map[string]any{"name": "team", "description": "d"}},
		{args: []string{"ai-search", "namespace", "update", "team", "--description", "e"}, method: "PUT", path: platA + "/ai-search/namespaces/team", body: map[string]any{"description": "e"}},
		{args: []string{"ai-search", "namespace", "delete", "team", "--yes"}, method: "DELETE", path: platA + "/ai-search/namespaces/team"},
		{args: []string{"ai-search", "tokens", "list"}, method: "GET", path: platA + "/ai-search/tokens"},
	})
}

func TestWorkflowsCommands(t *testing.T) {
	p := platFake(t)
	wf := platA + "/workflows"
	p.routes["GET "+wf] = []any{map[string]any{"name": "billing", "script_name": "billing-worker", "class_name": "Billing", "instances": map[string]any{"running": 2, "complete": 5}}}
	p.routes["GET "+wf+"/billing/instances"] = []any{
		map[string]any{"id": "old", "status": "complete", "created_on": "2026-01-01T00:00:00Z"},
		map[string]any{"id": "new", "status": "running", "created_on": "2026-02-01T00:00:00Z"},
	}
	p.routes["GET "+wf+"/billing/instances/new"] = func(fakeRequest) (int, any) {
		return 200, ok(map[string]any{"status": "errored", "params": map[string]any{"a": 1}, "error": map[string]any{"message": "boom"},
			"steps": []any{map[string]any{"name": "charge", "type": "step", "success": false, "attempts": []any{1, 2}, "error": map[string]any{"message": "card declined"}}}})
	}
	p.routes["POST "+wf+"/billing/instances"] = map[string]any{"id": "i9", "status": "queued"}
	platCheck(t, p, []platCase{
		{args: []string{"workflows", "list"}, method: "GET", path: wf, want: "billing-worker"},
		{args: []string{"workflows", "describe", "billing"}, method: "GET", path: wf + "/billing"},
		{args: []string{"workflows", "delete", "billing", "--yes"}, method: "DELETE", path: wf + "/billing"},
		{args: []string{"workflows", "trigger", "billing", `{"order":42}`, "--id", "custom"}, method: "POST", path: wf + "/billing/instances",
			body: map[string]any{"params": map[string]any{"order": 42}, "instance_id": "custom"}, want: "Started instance i9"},
		{args: []string{"workflows", "instances", "list", "billing", "--status", "running"}, method: "GET", path: wf + "/billing/instances", query: "status=running"},
		{args: []string{"workflows", "instances", "describe", "billing", "latest"}, method: "GET", path: wf + "/billing/instances/new", want: "card declined"},
		{args: []string{"workflows", "instances", "pause", "billing", "i1"}, method: "PATCH", path: wf + "/billing/instances/i1/status", body: map[string]any{"status": "pause"}},
		{args: []string{"workflows", "instances", "resume", "billing", "i1"}, method: "PATCH", path: wf + "/billing/instances/i1/status", body: map[string]any{"status": "resume"}},
		{args: []string{"workflows", "instances", "terminate", "billing", "latest", "--rollback", "--yes"}, method: "PATCH", path: wf + "/billing/instances/new/status", body: map[string]any{"status": "terminate", "rollback": true}},
		{args: []string{"workflows", "instances", "restart", "billing", "i1", "--from", "charge", "--yes"}, method: "PATCH", path: wf + "/billing/instances/i1/status", body: map[string]any{"status": "restart", "from": "charge"}},
		{args: []string{"workflows", "instances", "send-event", "billing", "i1", "approved", "--payload", `{"ok":true}`}, method: "POST", path: wf + "/billing/instances/i1/events/approved", body: map[string]any{"ok": true}},
		{args: []string{"workflows", "instances", "delete", "billing", "i1", "i2", "--yes"}, method: "POST", path: wf + "/billing/instances/batch/delete", body: map[string]any{"instances": []any{"i1", "i2"}}},
		{args: []string{"workflows", "versions", "list", "billing"}, method: "GET", path: wf + "/billing/versions"},
		{args: []string{"workflows", "versions", "get", "billing", "v1"}, method: "GET", path: wf + "/billing/versions/v1"},
		{args: []string{"workflows", "versions", "graph", "billing", "v1"}, method: "GET", path: wf + "/billing/versions/v1/graph"},
	})
	out := platRunOK(t, "", "workflows", "trigger", "billing", "--json")
	var v map[string]any
	if json.Unmarshal([]byte(out), &v) != nil || v["id"] != "i9" {
		t.Fatalf("--json: %s", out)
	}
}
