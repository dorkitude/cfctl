package cmd

// Regression tests for the Pages Direct Upload write path, checked against
// wrangler's pages deploy (deploy-helpers upload + createUploadWorkerBundleContents).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// pagesDeployFake wires upload-token, the JWT asset endpoints, and the
// deployment endpoint; it returns a func that yields the last deploy form.
func pagesDeployFake(t *testing.T, jwt string) (*platFakeT, func() *multipart.Form) {
	t.Helper()
	p := platFake(t)
	pagesFixtures(p)
	pagesPollInterval = time.Millisecond
	p.routes["GET "+pagesP+"/site/upload-token"] = func(fakeRequest) (int, any) { return 200, ok(map[string]any{"jwt": jwt}) }
	var form *multipart.Form
	p.routes["POST "+pagesP+"/site/deployments"] = func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		_, params, _ := mime.ParseMediaType(req.ContentType)
		form, _ = multipart.NewReader(bytes.NewReader(req.RawBody), params["boundary"]).ReadForm(10 << 20)
		writeJSON(w, 200, ok(map[string]any{"id": "d9", "url": "https://d9.site.pages.dev", "latest_stage": map[string]any{"name": "deploy", "status": "success"}}))
	}
	inner := p.srv.Config.Handler
	p.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/client/v4/pages/assets/") {
			inner.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/client/v4/pages/assets/check-missing" {
			var in struct{ Hashes []string }
			_ = json.Unmarshal(body, &in)
			writeJSON(w, 200, ok(in.Hashes))
			return
		}
		writeJSON(w, 200, ok(nil))
	})
	return p, func() *multipart.Form { return form }
}

func fakeJWT(claims string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(claims)) + ".sig"
}

// A _worker.js directory (the output of 'wrangler pages functions build
// --outdir dist/_worker.js') is uploaded as index.js + its other ES modules,
// as 'wrangler pages deploy --no-bundle' does; before, it was silently dropped.
func TestPagesDeployWorkerDirectory(t *testing.T) {
	_, form := pagesDeployFake(t, fakeJWT(`{"max_file_count_allowed":100}`))
	dir := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(content), 0o644)
	}
	write("index.html", "hi")
	write("_worker.js/index.js", "import x from './chunks/a.mjs'; export default {}")
	write("_worker.js/chunks/a.mjs", "export default 1")
	write("_worker.js/README.md", "not a module")
	write("_routes.json", `{"version":1,"include":["/*"],"exclude":[]}`)

	platRunOK(t, "", "pages", "deploy", dir, "--project-name", "site", "--branch", "main", "--commit-hash", "abc", "--commit-message", "m")
	f := form()
	if f == nil || len(f.File["_worker.bundle"]) != 1 || len(f.File["_routes.json"]) != 1 {
		t.Fatalf("deploy form missing _worker.bundle/_routes.json: %+v", f)
	}
	var manifest map[string]string
	_ = json.Unmarshal([]byte(f.Value["manifest"][0]), &manifest)
	if len(manifest) != 1 || manifest["/index.html"] == "" {
		t.Fatalf("_worker.js/ must not be uploaded as assets: %v", manifest)
	}
	fh, _ := f.File["_worker.bundle"][0].Open()
	raw, _ := io.ReadAll(fh)
	// The bundle is itself a multipart form; find its boundary from the first line.
	boundary := strings.TrimPrefix(strings.SplitN(string(raw), "\r\n", 2)[0], "--")
	inner, err := multipart.NewReader(bytes.NewReader(raw), boundary).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := inner.Value["metadata"]; len(got) != 1 || got[0] != `{"main_module":"index.js"}` {
		t.Fatalf("metadata = %v", got)
	}
	for _, name := range []string{"index.js", "chunks/a.mjs"} {
		parts := inner.File[name]
		if len(parts) != 1 || parts[0].Header.Get("Content-Type") != "application/javascript+module" {
			t.Fatalf("module %s: %v", name, parts)
		}
	}
	if len(inner.File["README.md"]) != 0 {
		t.Fatal("non-JS file uploaded as a module")
	}
}

// The file-count limit comes from the upload JWT's max_file_count_allowed
// claim (paid plans allow more than 20,000).
func TestPagesDeployFileLimitFromJWT(t *testing.T) {
	if got := pagesMaxFilesFromJWT(fakeJWT(`{"max_file_count_allowed":100000}`)); got != 100000 {
		t.Fatalf("claim limit = %d", got)
	}
	if got := pagesMaxFilesFromJWT("not-a-jwt"); got != pagesMaxFiles {
		t.Fatalf("fallback = %d", got)
	}
	_, _ = pagesDeployFake(t, fakeJWT(`{"max_file_count_allowed":1}`))
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.html"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.html"), []byte("b"), 0o644)
	if msg := platRunErr(t, "", "pages", "deploy", dir, "--project-name", "site", "--branch", "main", "--commit-hash", "x"); !strings.Contains(msg, "more than 1 files") {
		t.Fatal(msg)
	}
}

// commit_message is truncated to 384 bytes without splitting a UTF-8 rune.
func TestPagesDeployCommitMessageUTF8(t *testing.T) {
	_, form := pagesDeployFake(t, fakeJWT(`{}`))
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("x"), 0o644)
	msg := strings.Repeat("a", 383) + "é and more"
	platRunOK(t, "", "pages", "deploy", dir, "--project-name", "site", "--branch", "main", "--commit-hash", "abc", "--commit-message", msg)
	got := form().Value["commit_message"][0]
	if !utf8.ValidString(got) || len(got) > 384 || got != strings.Repeat("a", 383) {
		t.Fatalf("commit_message = %q (%d bytes)", got, len(got))
	}
}
