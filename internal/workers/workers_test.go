package workers

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStripJSONC(t *testing.T) {
	in := `{
  // line comment
  "url": "https://example.com/a//b", /* block
  comment */ "s": "has \"quote\" and /* not a comment */",
  "arr": [1, 2, ],
}`
	var v map[string]any
	if err := json.Unmarshal(StripJSONC([]byte(in)), &v); err != nil {
		t.Fatalf("%v\n%s", err, StripJSONC([]byte(in)))
	}
	if v["url"] != "https://example.com/a//b" || v["s"] != `has "quote" and /* not a comment */` || len(v["arr"].([]any)) != 2 {
		t.Fatalf("got %v", v)
	}
}

func TestLoadConfigTOMLAndEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wrangler.toml")
	os.WriteFile(p, []byte(`
name = "app"
main = "src/index.ts"
compatibility_date = 2025-02-03
workers_dev = false
route = { pattern = "example.com/*", zone_name = "example.com" }
[vars]
A = "1"
[ai]
binding = "AI"
[[queues.producers]]
binding = "Q"
queue = "jobs"
[env.prod]
vars = { A = "prod" }
routes = ["prod.example.com/*"]
`), 0o644)
	c, err := LoadConfig(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "app" || c.CompatibilityDate != "2025-02-03" || c.WorkersDev == nil || *c.WorkersDev || c.MainPath() != filepath.Join(dir, "src/index.ts") {
		t.Fatalf("config: %+v", c)
	}
	if len(c.Routes) != 1 || c.Routes[0].ZoneName != "example.com" {
		t.Fatalf("routes: %+v", c.Routes)
	}
	bs, err := c.Bindings()
	if err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(bs)
	for _, want := range []string{`"type":"plain_text"`, `"type":"ai"`, `"queue_name":"jobs"`} {
		if !strings.Contains(string(j), want) {
			t.Errorf("bindings missing %s: %s", want, j)
		}
	}
	e, err := LoadConfig(p, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "app-prod" || e.Vars["A"] != "prod" || len(e.Routes) != 1 || e.Routes[0].Pattern != "prod.example.com/*" || e.Raw["ai"] != nil {
		t.Fatalf("env: %+v", e)
	}
	if _, err := LoadConfig(p, "nope"); err == nil {
		t.Fatal("missing env should fail")
	}
}

func TestParseBindingFlag(t *testing.T) {
	for _, tc := range []struct{ kind, val, want string }{
		{"var", "A=b=c", `"text":"b=c"`},
		{"service", "S=w#E", `"entrypoint":"E"`},
		{"ai", "AI", `"type":"ai"`},
		{"binding", `{"type":"x","name":"y"}`, `"type":"x"`},
	} {
		b, err := ParseBindingFlag(tc.kind, tc.val)
		if err != nil {
			t.Fatal(err)
		}
		j, _ := json.Marshal(b)
		if !strings.Contains(string(j), tc.want) {
			t.Errorf("%s %s: %s", tc.kind, tc.val, j)
		}
	}
	for _, bad := range [][2]string{{"kv", "noequals"}, {"binding", "{"}, {"binding", `{"type":"x"}`}, {"ai", ""}} {
		if _, err := ParseBindingFlag(bad[0], bad[1]); err == nil {
			t.Errorf("%v should fail", bad)
		}
	}
	merged := MergeBindings([]Binding{{"name": "A", "text": "1"}}, []Binding{{"name": "A", "text": "2"}, {"name": "B"}})
	if len(merged) != 2 || merged[0]["text"] != "2" {
		t.Errorf("merge: %v", merged)
	}
}

func TestModulesAndFormat(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "index.js")
	os.WriteFile(main, []byte("import x from './lib/x.wasm'\nexport default {}"), 0o644)
	os.MkdirAll(filepath.Join(dir, "lib"), 0o755)
	os.WriteFile(filepath.Join(dir, "lib/x.wasm"), []byte{0, 1}, 0o644)
	os.WriteFile(main+".map", []byte("{}"), 0o644)
	mods, esm, err := LoadModules(main, []string{filepath.Join(dir, "lib/x.wasm")}, nil, true)
	if err != nil || !esm || len(mods) != 3 {
		t.Fatalf("mods=%v esm=%v err=%v", mods, esm, err)
	}
	if mods[1].Name != "lib/x.wasm" || mods[1].ContentType != "application/wasm" || mods[2].ContentType != "application/source-map" {
		t.Errorf("mods: %+v", mods)
	}
	if !IsModuleSyntax([]byte(`import {x} from "./x.js"; export default {}`)) || !IsModuleSyntax([]byte(`const a=1;export{a}`)) {
		t.Error("ES module not detected")
	}
	if got := RelativeImports([]byte(`import a from "./a.js"; import "./side.js"; const m = await import('../m.js'); import x from "pkg"`)); strings.Join(got, ",") != "./a.js,./side.js,../m.js" {
		t.Errorf("relative imports: %v", got)
	}
	// Relative imports are followed; files outside the main dir are skipped.
	os.WriteFile(filepath.Join(dir, "app.js"), []byte(`import {h} from "./lib/h.js"; import "../outside.js"; export default {}`), 0o644)
	os.WriteFile(filepath.Join(dir, "lib/h.js"), []byte(`import {g} from "./g.js"; export const h = g`), 0o644)
	os.WriteFile(filepath.Join(dir, "lib/g.js"), []byte(`export const g = 1`), 0o644)
	mods, _, err = LoadModules(filepath.Join(dir, "app.js"), nil, nil, false)
	if err != nil || len(mods) != 3 || mods[1].Name != "lib/h.js" || mods[2].Name != "lib/g.js" {
		t.Errorf("followed imports: %+v %v", mods, err)
	}
	if IsModuleSyntax([]byte("addEventListener('fetch', e => {})")) {
		t.Error("service worker detected as module")
	}
	body, ct, err := BuildUpload(map[string]any{"main_module": "index.js"}, mods)
	if err != nil || !strings.HasPrefix(ct, "multipart/form-data; boundary=") || !strings.Contains(string(body), `name="metadata"`) {
		t.Fatalf("upload: %v %s", err, ct)
	}
}

func TestScanAssets(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]string{"index.html": "<p>", "a/b.css": "b{}", "_redirects": "/x /y", ".assetsignore": "secret/\n*.bak", "secret/k.txt": "k", "old.bak": "o"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(c), 0o644)
	}
	a, err := ScanAssets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.Paths(), ","); got != "/a/b.css,/index.html" {
		t.Fatalf("paths: %s", got)
	}
	if a.Redirects != "/x /y" {
		t.Errorf("redirects: %q", a.Redirects)
	}
	e := a.Manifest["/index.html"]
	if len(e.Hash) != 32 || e.Size != 3 || e.Hash != HashAsset([]byte("<p>"), ".html") {
		t.Errorf("entry: %+v", e)
	}
	if _, ct, ok := a.FileFor(e.Hash); !ok || !strings.HasPrefix(ct, "text/html") {
		t.Errorf("FileFor: %v %s", ok, ct)
	}
}

func TestTailFiltersAndPretty(t *testing.T) {
	fs, err := TailFilterOptions{Status: []string{"ok", "canceled"}, Sampling: 0.25}.Filters()
	if err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(fs)
	if string(j) != `[{"sampling_rate":0.25},{"outcome":["ok","canceled"]}]` {
		t.Errorf("filters: %s", j)
	}
	if _, err := (TailFilterOptions{Sampling: 2}).Filters(); err == nil {
		t.Error("sampling 2 should fail")
	}
	empty, _ := TailFilterOptions{}.Filters()
	if empty == nil || len(empty) != 0 {
		t.Error("empty filters should be []")
	}
	out := FormatTailPretty([]byte(`{"outcome":"exceededCpu","eventTimestamp":0,"event":{"queue":"jobs","batchSize":3},"logs":[{"message":[{"a":1}],"level":"warn"}]}`), time.UTC)
	if !strings.Contains(out, "Queue jobs (3 messages) - Exceeded CPU Limit") || !strings.Contains(out, `(warn) {"a":1}`) {
		t.Errorf("pretty: %s", out)
	}
	if FormatTailPretty([]byte("not json"), nil) != "not json" {
		t.Error("non-JSON passthrough")
	}
}

// TestWSClient checks fragmentation, pings, and close handling.
func TestWSClient(t *testing.T) {
	pong := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
		conn, rw, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
		rw.Write([]byte{0x89, 0x00})                // ping
		rw.Write([]byte{0x01, 0x03, 'a', 'b', 'c'}) // text, not final
		rw.Write([]byte{0x80, 0x02, 'd', 'e'})      // continuation, final
		big := strings.Repeat("x", 300)
		rw.Write(append([]byte{0x81, 126, 0x01, 0x2c}, big...))
		rw.Write([]byte{0x88, 0x02, 0x03, 0xe8}) // close
		rw.Flush()
		// Expect a masked pong, then a close reply.
		br := bufio.NewReader(rw)
		h := make([]byte, 2)
		io.ReadFull(br, h)
		pong <- h[0] == 0x8A && h[1]&0x80 != 0
		io.Copy(io.Discard, br)
	}))
	defer srv.Close()
	c, err := DialWS(context.Background(), strings.Replace(srv.URL, "http://", "ws://", 1)+"/x", TailProtocol, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m, err := c.ReadMessage()
	if err != nil || string(m) != "abcde" {
		t.Fatalf("fragmented: %q %v", m, err)
	}
	m, err = c.ReadMessage()
	if err != nil || len(m) != 300 {
		t.Fatalf("extended length: %d %v", len(m), err)
	}
	if _, err := c.ReadMessage(); err != ErrWSClosed {
		t.Fatalf("close: %v", err)
	}
	if !<-pong {
		t.Error("ping not answered with a masked pong")
	}
	if _, err := DialWS(context.Background(), "ftp://x", "", nil, time.Second); err == nil {
		t.Error("bad scheme should fail")
	}
}
