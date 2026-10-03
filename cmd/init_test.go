package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestInitScaffolds(t *testing.T) {
	platFake(t)
	for _, tpl := range []string{"worker", "scheduled", "assets"} {
		dir := filepath.Join(t.TempDir(), "w")
		out := platRunOK(t, "", "init", "my-worker", "--template", tpl, "--dir", dir, "--compatibility-date", "2026-10-01")
		if !strings.Contains(out, "Created my-worker") {
			t.Fatal(out)
		}
		cfg, err := os.ReadFile(filepath.Join(dir, "wrangler.jsonc"))
		if err != nil || !strings.Contains(string(cfg), `"name": "my-worker"`) || !strings.Contains(string(cfg), `"compatibility_date": "2026-10-01"`) {
			t.Fatalf("%s wrangler.jsonc: %s", tpl, cfg)
		}
		// The config must be valid JSON (no comments are emitted).
		var v map[string]any
		if err := json.Unmarshal(cfg, &v); err != nil {
			t.Fatalf("%s config isn't JSON: %v", tpl, err)
		}
		for _, f := range []string{"src/index.ts", "package.json", "tsconfig.json", ".gitignore"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
				t.Fatalf("%s: missing %s", tpl, f)
			}
		}
		if tpl == "assets" {
			if _, err := os.Stat(filepath.Join(dir, "public/index.html")); err != nil || v["assets"] == nil {
				t.Fatal("assets template")
			}
		}
		if tpl == "scheduled" && v["triggers"] == nil {
			t.Fatal("scheduled template has no triggers")
		}
		if msg := platRunErr(t, "", "init", "my-worker", "--template", tpl, "--dir", dir); !strings.Contains(msg, "--force") {
			t.Fatal(msg)
		}
	}
	if msg := platRunErr(t, "", "init", "Bad_Name"); !strings.Contains(msg, "invalid Worker name") {
		t.Fatal(msg)
	}
	if msg := platRunErr(t, "", "init", "x", "--template", "nope", "--dir", t.TempDir()); !strings.Contains(msg, "unknown template") {
		t.Fatal(msg)
	}
}

func TestDocsAndCompletion(t *testing.T) {
	platFake(t)
	old := docsFS
	t.Cleanup(func() { docsFS = old })
	SetDocsFS(fstest.MapFS{
		"README.md":                 {Data: []byte("# cfctl\n\nSee `cfctl docs` and **bold** [link](https://x.y).\n\n```bash\ncfctl pages project list\n```\n")},
		"CHANGELOG.md":              {Data: []byte("# Changelog\n")},
		"docs/ARCHITECTURE.md":      {Data: []byte("# Architecture\nrequest layer\n")},
		"docs/commands/platform.md": {Data: []byte("# Platform\n## Pages\ncfctl pages deploy\n")},
	})
	out := platRunOK(t, "", "docs")
	if !strings.Contains(out, "# cfctl") { // piped: plain Markdown
		t.Fatal(out)
	}
	out = platRunOK(t, "", "docs", "--list")
	for _, want := range []string{"readme", "changelog", "architecture", "platform"} {
		if !strings.Contains(out, want) {
			t.Fatalf("--list lacks %s: %s", want, out)
		}
	}
	if out := platRunOK(t, "", "docs", "platform"); !strings.Contains(out, "cfctl pages deploy") {
		t.Fatal(out)
	}
	if out := platRunOK(t, "", "docs", "--search", "request layer"); !strings.Contains(out, "architecture") {
		t.Fatal(out)
	}
	if msg := platRunErr(t, "", "docs", "nope"); !strings.Contains(msg, "available") {
		t.Fatal(msg)
	}
	r := docsRender("# Title\n- item `code` **b** [l](https://u)\n```\nx\n```\n> quote\n")
	for _, want := range []string{"TITLE", "• item", "code", "(https://u)", "  │ quote", "    "} {
		if !strings.Contains(r, want) {
			t.Fatalf("render lacks %q:\n%s", want, r)
		}
	}
	for _, sh := range []string{"bash", "zsh", "fish", "powershell"} {
		if out := platRunOK(t, "", "completion", sh); len(out) < 200 {
			t.Fatalf("%s completion too short", sh)
		}
	}
	if msg := platRunErr(t, "", "completion", "tcsh"); !strings.Contains(msg, "unsupported") {
		t.Fatal(msg)
	}
}
