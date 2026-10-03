package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func agentMemoryFake(t *testing.T) *fakeCF {
	p := "/agent-memory/namespaces/bot/profiles/u1"
	return stFake(t, map[string]stHandler{
		"GET /agent-memory/namespaces":              stListH([]map[string]any{{"name": "bot", "id": "n1"}}),
		"POST /agent-memory/namespaces":             stJSON(map[string]any{"name": "bot"}),
		"GET /agent-memory/namespaces/bot":          stJSON(map[string]any{"name": "bot"}),
		"DELETE /agent-memory/namespaces/bot":       stJSON(nil),
		"GET /agent-memory/namespaces/bot/profiles": stListH([]map[string]any{{"name": "u1"}}),
		"DELETE " + p:                  stJSON(nil),
		"POST " + p + "/summary":       stJSON(map[string]any{"summary": "Likes email."}),
		"GET " + p + "/memories":       stListH([]map[string]any{{"id": "m1", "type": "fact", "content": "Prefers email"}}),
		"GET " + p + "/memories/m1":    stJSON(map[string]any{"id": "m1"}),
		"DELETE " + p + "/memories/m1": stJSON(nil),
		"POST " + p + "/remember":      stJSON(map[string]any{"id": "m2"}),
		"POST " + p + "/recall":        stJSON(map[string]any{"answer": "By email."}),
		"POST " + p + "/ingest":        stJSON(map[string]any{"memories": []any{}}),
		"DELETE " + p + "/sessions/s1": stJSON(nil),
	})
}

func TestAgentMemory(t *testing.T) {
	f := agentMemoryFake(t)
	out, _ := stRun(t, "", "agent-memory", "namespace", "list")
	stMust(t, out, "bot", "n1")
	stRun(t, "", "agent-memory", "namespace", "create", "bot")
	stRun(t, "", "agent-memory", "namespace", "get", "bot")
	stRun(t, "", "agent-memory", "namespace", "delete", "bot", "-y")
	out, _ = stRun(t, "", "agent-memory", "profile", "list", "bot")
	stMust(t, out, "u1")
	out, _ = stRun(t, "", "agent-memory", "profile", "summary", "bot", "u1")
	stMust(t, out, "Likes email.")
	out, _ = stRun(t, "", "agent-memory", "memories", "list", "bot", "u1", "--type", "fact")
	stMust(t, out, "Prefers email")
	if r := stFind(t, f, "GET", "/agent-memory/namespaces/bot/profiles/u1/memories"); r.Query != "type=fact" {
		t.Fatalf("query %q", r.Query)
	}
	stRun(t, "", "agent-memory", "memories", "get", "bot", "u1", "m1")
	stRun(t, "", "agent-memory", "memories", "remember", "bot", "u1", "Uses dark mode", "--session", "s1")
	if r := stFind(t, f, "POST", "/agent-memory/namespaces/bot/profiles/u1/remember"); r.Body["content"] != "Uses dark mode" || r.Body["sessionId"] != "s1" {
		t.Fatalf("remember body %v", r.Body)
	}
	out, _ = stRun(t, "", "agent-memory", "memories", "recall", "bot", "u1", "contact?", "--thinking", "high")
	stMust(t, out, "By email.")
	file := filepath.Join(t.TempDir(), "chat.json")
	_ = os.WriteFile(file, []byte(`[{"role":"user","content":"hi"}]`), 0o600)
	stRun(t, "", "agent-memory", "memories", "ingest", "bot", "u1", "--file", file)
	if r := stFind(t, f, "POST", "/agent-memory/namespaces/bot/profiles/u1/ingest"); len(r.Body["messages"].([]any)) != 1 {
		t.Fatalf("ingest body %v", r.Body)
	}
	stRunErr(t, "", "agent-memory", "memories", "ingest", "bot", "u1")
	stRunErr(t, "", "agent-memory", "memories", "delete", "bot", "u1", "m1")
	stRun(t, "", "agent-memory", "memories", "delete", "bot", "u1", "m1", "-y")
	stRun(t, "", "agent-memory", "session", "delete", "bot", "u1", "s1", "-y")
	stRun(t, "", "agent-memory", "profile", "delete", "bot", "u1", "-y")
	stFind(t, f, "DELETE", "/agent-memory/namespaces/bot/profiles/u1")
}

func TestAgentMemoryReadOnly(t *testing.T) {
	f := agentMemoryFake(t)
	stRunErr(t, "", "--read-only", "agent-memory", "memories", "remember", "bot", "u1", "x")
	stNoMutation(t, f)
}
