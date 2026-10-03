package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlagshipCommands(t *testing.T) {
	p := platFake(t)
	fs := platA + "/flagship/apps"
	flag := map[string]any{"key": "new-ui", "description": "d", "enabled": true, "default_variation": "off",
		"variations": map[string]any{"on": true, "off": false, "beta": "b"}, "rules": []any{}, "updated_at": "2026-01-01T00:00:00Z", "version": 3}
	p.routes["GET "+fs] = []any{map[string]any{"id": "app1", "name": "web"}}
	p.routes["GET "+fs+"/app1/flags"] = []any{flag}
	p.routes["GET "+fs+"/app1/flags/new-ui"] = func(fakeRequest) (int, any) { return 200, ok(flag) }
	platCheck(t, p, []platCase{
		{args: []string{"flagship", "apps", "list"}, method: "GET", path: fs, want: "app1"},
		{args: []string{"flagship", "apps", "get", "app1"}, method: "GET", path: fs + "/app1"},
		{args: []string{"flagship", "apps", "create", "web"}, method: "POST", path: fs, body: map[string]any{"name": "web"}},
		{args: []string{"flagship", "apps", "update", "app1", "web2"}, method: "PUT", path: fs + "/app1", body: map[string]any{"name": "web2"}},
		{args: []string{"flagship", "apps", "delete", "app1", "--yes"}, method: "DELETE", path: fs + "/app1"},
		{args: []string{"flagship", "flags", "list", "app1"}, method: "GET", path: fs + "/app1/flags", want: "new-ui"},
		{args: []string{"flagship", "flags", "get", "app1", "new-ui"}, method: "GET", path: fs + "/app1/flags/new-ui", want: `beta = "b"`},
		{args: []string{"flagship", "flags", "create", "app1", "bool-flag"}, method: "POST", path: fs + "/app1/flags",
			body: map[string]any{"key": "bool-flag", "enabled": true, "default_variation": "off", "variations": map[string]any{"on": true, "off": false}, "rules": []any{}}},
		{args: []string{"flagship", "flags", "create", "app1", "model", "--variation", "stable=gpt-a", "--variation", "n=5", "--variation", "obj={\"a\":1}", "--disabled",
			"--rule-json", `{"conditions":[],"serve_variation":"n"}`}, method: "POST", path: fs + "/app1/flags",
			body: map[string]any{"enabled": false, "default_variation": "stable", "variations": map[string]any{"stable": "gpt-a", "n": 5, "obj": map[string]any{"a": 1}},
				"rules": []any{map[string]any{"conditions": []any{}, "serve_variation": "n", "priority": 1}}}},
		{args: []string{"flagship", "flags", "update", "app1", "new-ui", "--data", `{"key":"new-ui","enabled":false}`}, method: "PUT", path: fs + "/app1/flags/new-ui", body: map[string]any{"enabled": false}},
		{args: []string{"flagship", "flags", "delete", "app1", "new-ui", "--yes"}, method: "DELETE", path: fs + "/app1/flags/new-ui"},
		{args: []string{"flagship", "flags", "changelog", "app1", "new-ui"}, method: "GET", path: fs + "/app1/flags/new-ui/changelog"},
		{args: []string{"flagship", "flags", "evaluate", "app1", "new-ui", "--context", "user_id=42"}, method: "GET", path: fs + "/app1/evaluate", query: "flagKey=new-ui"},
		{args: []string{"flagship", "flags", "enable", "app1", "new-ui"}, method: "PUT", path: fs + "/app1/flags/new-ui", body: map[string]any{"enabled": true, "default_variation": "off"}},
		{args: []string{"flagship", "flags", "disable", "app1", "new-ui"}, method: "PUT", path: fs + "/app1/flags/new-ui", body: map[string]any{"enabled": false}},
		{args: []string{"flagship", "flags", "set", "app1", "new-ui", "beta"}, method: "PUT", path: fs + "/app1/flags/new-ui", body: map[string]any{"default_variation": "beta"}},
		{args: []string{"flagship", "flags", "rollout", "app1", "new-ui", "--to", "on", "--percentage", "25", "--by", "user_id"}, method: "PUT", path: fs + "/app1/flags/new-ui",
			body: map[string]any{"default_variation": "off", "rules": []any{map[string]any{"priority": 1, "conditions": []any{}, "serve_variation": "on", "rollout": map[string]any{"percentage": 25, "attribute": "user_id"}}}}},
		{args: []string{"flagship", "flags", "split", "app1", "new-ui", "-w", "on=1", "-w", "beta=3"}, method: "PUT", path: fs + "/app1/flags/new-ui",
			body: map[string]any{"rules": []any{
				map[string]any{"priority": 1, "conditions": []any{}, "serve_variation": "on", "rollout": map[string]any{"percentage": 25}},
				map[string]any{"priority": 2, "conditions": []any{}, "serve_variation": "beta", "rollout": map[string]any{"percentage": 100}}}}},
		{args: []string{"flagship", "flags", "rules", "app1", "new-ui"}, method: "PUT", path: fs + "/app1/flags/new-ui", body: map[string]any{"rules": []any{}}},
	})
	// The PUT body is the flag input only (no read-only fields like version).
	if r := p.last(t, "PUT", fs+"/app1/flags/new-ui"); r.Body["version"] != nil || r.Body["updated_at"] != nil {
		t.Fatalf("PUT carried read-only fields: %s", r.RawBody)
	}
	if msg := platRunErr(t, "", "flagship", "flags", "set", "app1", "new-ui", "nope"); !strings.Contains(msg, "unknown variation") {
		t.Fatal(msg)
	}
	if msg := platRunErr(t, "", "flagship", "flags", "create", "app1", "x", "--variation", "bad"); !strings.Contains(msg, "name=value") {
		t.Fatal(msg)
	}

	dest := filepath.Join(t.TempDir(), "flags.json")
	out := platRunOK(t, "", "flagship", "flags", "pull", "app1", "-o", dest)
	if !strings.Contains(out, "Pulled 1 flags") {
		t.Fatal(out)
	}
	var doc struct {
		AppID string           `json:"app_id"`
		Flags []map[string]any `json:"flags"`
	}
	b, _ := os.ReadFile(dest)
	if json.Unmarshal(b, &doc) != nil || doc.AppID != "app1" || len(doc.Flags) != 1 || doc.Flags[0]["key"] != "new-ui" || doc.Flags[0]["version"] != nil {
		t.Fatalf("pulled file: %s", b)
	}
}
