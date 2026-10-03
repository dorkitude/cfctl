package cmd

// pages deploy: Direct Upload, the same flow as `wrangler pages deploy`:
//
//  1. GET  /accounts/{a}/pages/projects/{p}/upload-token   → JWT
//  2. hash every file: blake3(base64(content) + ext)[:32 hex]
//  3. POST /pages/assets/check-missing {hashes}  (JWT auth) → missing hashes
//  4. POST /pages/assets/upload [{key,value(base64),metadata,base64}] in buckets
//  5. POST /pages/assets/upsert-hashes {hashes}
//  6. POST /accounts/{a}/pages/projects/{p}/deployments  multipart:
//     manifest {"/path": hash}, branch, commit_*, _headers, _redirects,
//     _routes.json, _worker.bundle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
	"lukechampine.com/blake3"
)

const (
	pagesMaxAssetSize   = 25 << 20
	pagesMaxBucketSize  = 40 << 20
	pagesMaxBucketFiles = 2000
	pagesMaxFiles       = 20000
	pagesUploadWorkers  = 3
)

// pagesFile is one static asset to upload.
type pagesFile struct {
	Name        string // forward-slash path relative to the directory
	Path        string
	Size        int64
	ContentType string
	Hash        string
}

// pagesHash is Pages' asset hash: blake3(base64(content) + extension),
// first 32 hex chars.
func pagesHash(content []byte, name string) string {
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	sum := blake3.Sum256([]byte(base64.StdEncoding.EncodeToString(content) + ext))
	return hex.EncodeToString(sum[:])[:32]
}

// pagesIgnored mirrors wrangler's ignore list: the special files at the
// root (sent separately), functions/, and junk anywhere.
func pagesIgnored(rel string, isDir bool) bool {
	switch rel {
	case "_worker.js", "_redirects", "_headers", "_routes.json", "functions", ".wrangler":
		return true
	}
	base := path.Base(rel)
	return base == ".DS_Store" || base == "node_modules" || base == ".git"
}

func pagesContentType(name string) string {
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		return "application/octet-stream"
	}
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// pagesMaxFilesFromJWT reads the plan's file-count limit from the upload
// token's max_file_count_allowed claim (wrangler does the same), falling back
// to the default.
func pagesMaxFilesFromJWT(jwt string) int {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return pagesMaxFiles
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return pagesMaxFiles
	}
	var claims struct {
		Max *float64 `json:"max_file_count_allowed"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Max == nil || *claims.Max <= 0 {
		return pagesMaxFiles
	}
	return int(*claims.Max)
}

// pagesTruncateUTF8 cuts s to at most n bytes without splitting a rune.
func pagesTruncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// pagesWalk collects and hashes the files to upload (at most limit files).
func pagesWalk(dir string, limit int) ([]pagesFile, error) {
	var files []pagesFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if pagesIgnored(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > pagesMaxAssetSize {
			return fmt.Errorf("%s is %d bytes; Pages only supports files up to 25 MiB", rel, info.Size())
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, pagesFile{Name: rel, Path: p, Size: info.Size(), ContentType: pagesContentType(rel), Hash: pagesHash(data, rel)})
		if len(files) > limit {
			return fmt.Errorf("more than %d files (your plan's limit); check that %s is your build output directory", limit, dir)
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, err
}

// pagesBuckets groups files (largest first) into upload batches.
func pagesBuckets(files []pagesFile) [][]pagesFile {
	sorted := append([]pagesFile(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Size > sorted[j].Size })
	type bucket struct {
		files []pagesFile
		left  int64
	}
	var bs []*bucket
	for _, f := range sorted {
		placed := false
		for _, b := range bs {
			if b.left >= f.Size && len(b.files) < pagesMaxBucketFiles {
				b.files = append(b.files, f)
				b.left -= f.Size
				placed = true
				break
			}
		}
		if !placed {
			bs = append(bs, &bucket{files: []pagesFile{f}, left: pagesMaxBucketSize - f.Size})
		}
	}
	out := make([][]pagesFile, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.files)
	}
	return out
}

// pagesGit returns commit details from the working directory's git repo.
func pagesGit(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func pagesDeployCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "deploy <directory>",
		Aliases: []string{"publish"},
		Short:   "Deploy a directory of static assets to Pages (Direct Upload)",
		Long: `Deploy a directory of static assets as a Pages deployment, using the same
Direct Upload flow as 'wrangler pages deploy': files are hashed, only the ones
Pages doesn't already have are uploaded, then a deployment is created.

Included: every file in the directory (except node_modules, .git, .DS_Store,
.wrangler), plus _headers, _redirects, and _routes.json. A pre-built
_worker.js is uploaded as the Worker as-is (cfctl doesn't bundle): either a
single ES module file, or a directory whose index.js is the entry point and
whose other .js/.mjs files are uploaded as modules (like wrangler --no-bundle).
A functions/ directory needs bundling: build it first with
'npx wrangler pages functions build --outdir <dir>/_worker.js' or deploy
with wrangler.

Branch, commit hash, message, and dirty flag default to the current git repo.`,
		Example: `  cfctl pages deploy ./dist --project-name my-site
  cfctl pages deploy ./dist --project-name my-site --branch preview-x
  cfctl pages deploy ./dist --project-name my-site --json`,
		Args: cobra.ExactArgs(1),
		RunE: runPagesDeploy,
	}
	c.Flags().String("project-name", "", "Pages project to deploy to (required)")
	c.Flags().String("branch", "", "Branch name (production if it's the project's production branch; default: current git branch)")
	c.Flags().String("commit-hash", "", "Commit SHA to attach (default: git HEAD)")
	c.Flags().String("commit-message", "", "Commit message to attach (default: git HEAD subject)")
	c.Flags().Bool("commit-dirty", false, "Mark the deployment as built from a dirty working tree (default: from git status)")
	c.Flags().Bool("skip-caching", false, "Upload every file, even ones Pages already has")
	c.Flags().Bool("no-wait", false, "Don't wait for the deployment to finish")
	c.Flags().Bool("dry-run", false, "Hash and list the files, but don't contact the API")
	return c
}

func runPagesDeploy(cmd *cobra.Command, args []string) error {
	dir := args[0]
	project, _ := cmd.Flags().GetString("project-name")
	dry, _ := cmd.Flags().GetBool("dry-run")
	if project == "" && !dry {
		return fmt.Errorf("--project-name is required (see 'cfctl pages project list')")
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if fi, err := os.Stat("functions"); err == nil && fi.IsDir() {
		if _, err := os.Stat(filepath.Join(dir, "_worker.js")); err != nil {
			fmt.Fprintln(os.Stderr, ui.Warn("./functions is not deployed: cfctl doesn't bundle Pages Functions (use 'npx wrangler pages functions build --outdir "+filepath.Join(dir, "_worker.js")+"' first, or wrangler pages deploy)"))
		}
	}
	if dry {
		files, err := pagesWalk(dir, pagesMaxFiles)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printJSONValue(files)
		}
		fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("📦 %d files in %s", len(files), dir)))
		for _, f := range files {
			fmt.Printf("  %s  %s  %d  %s\n", ui.SubtleStyle.Render(f.Hash), f.Name, f.Size, ui.SubtleStyle.Render(f.ContentType))
		}
		return nil
	}

	ctx := context.Background()
	s, err := newAPISession(cmd)
	if err != nil {
		return err
	}
	projPath, _, err := platFill(ctx, s, pagesBase+"/{project_name}", []string{project})
	if err != nil {
		return err
	}
	if _, err := platDo(ctx, s, "GET", projPath, nil, nil); err != nil {
		var ae *api.Error
		if asAPIError(err, &ae) && ae.Status == 404 {
			return fmt.Errorf("Pages project %q not found; create it with 'cfctl pages project create %s'", project, project)
		}
		return platErr("Pages", err)
	}

	raw, err := platDo(ctx, s, "GET", projPath+"/upload-token", nil, nil)
	if err != nil {
		return platErr("Pages", err)
	}
	var tok struct {
		JWT string `json:"jwt"`
	}
	if err := platDecode(raw, &tok); err != nil || tok.JWT == "" {
		return fmt.Errorf("Pages didn't return an upload token")
	}
	files, err := pagesWalk(dir, pagesMaxFilesFromJWT(tok.JWT))
	if err != nil {
		return err
	}
	// The asset endpoints authenticate with the upload JWT, not the API token.
	up := &apiSession{c: api.New(tok.JWT, config.APIBaseURL())}

	if err := pagesUpload(ctx, cmd, up, files); err != nil {
		return err
	}

	body, ct, err := pagesDeploymentForm(cmd, dir, files)
	if err != nil {
		return err
	}
	resp, err := s.c.Do(ctx, api.Request{Method: "POST", Path: projPath + "/deployments", Body: body, ContentType: ct})
	if err != nil {
		return platErr("Pages", err)
	}
	var dep map[string]any
	if resp.Envelope != nil {
		_ = json.Unmarshal(resp.Envelope.Result, &dep)
	}
	if noWait, _ := cmd.Flags().GetBool("no-wait"); !noWait {
		dep = pagesWait(ctx, s, projPath, dep)
	}
	if jsonOutput {
		return printJSONValue(dep)
	}
	status := platStr(platGet(dep, "latest_stage.status"))
	if status == "failure" {
		return fmt.Errorf("deployment %s failed at stage %s", platStr(dep["id"]), platStr(platGet(dep, "latest_stage.name")))
	}
	fmt.Println(ui.Success(fmt.Sprintf("Deployment complete: %s", platStr(dep["url"]))))
	fmt.Printf("  %s  %s\n", ui.SubtleStyle.Render("ID:         "), platStr(dep["id"]))
	fmt.Printf("  %s  %s\n", ui.SubtleStyle.Render("Environment:"), platStr(dep["environment"]))
	if a := platStr(dep["aliases"]); a != "" {
		fmt.Printf("  %s  %s\n", ui.SubtleStyle.Render("Aliases:    "), a)
	}
	return nil
}

func asAPIError(err error, target **api.Error) bool {
	return errors.As(err, target)
}

// pagesUpload checks which hashes are missing, uploads them, and upserts.
func pagesUpload(ctx context.Context, cmd *cobra.Command, up *apiSession, files []pagesFile) error {
	hashes := make([]string, 0, len(files))
	for _, f := range files {
		hashes = append(hashes, f.Hash)
	}
	missing := map[string]bool{}
	if skip, _ := cmd.Flags().GetBool("skip-caching"); skip {
		for _, h := range hashes {
			missing[h] = true
		}
	} else {
		raw, err := platDo(ctx, up, "POST", "/pages/assets/check-missing", nil, map[string]any{"hashes": hashes})
		if err != nil {
			return platErr("Pages", err)
		}
		var m []string
		if err := platDecode(raw, &m); err != nil {
			return err
		}
		for _, h := range m {
			missing[h] = true
		}
	}
	var todo []pagesFile
	seen := map[string]bool{}
	for _, f := range files {
		if missing[f.Hash] && !seen[f.Hash] {
			todo = append(todo, f)
			seen[f.Hash] = true
		}
	}
	start := time.Now()
	buckets := pagesBuckets(todo)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		done     int
	)
	sem := make(chan struct{}, pagesUploadWorkers)
	for _, b := range buckets {
		wg.Add(1)
		sem <- struct{}{}
		go func(b []pagesFile) {
			defer wg.Done()
			defer func() { <-sem }()
			payload := make([]map[string]any, 0, len(b))
			for _, f := range b {
				data, err := os.ReadFile(f.Path)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				payload = append(payload, map[string]any{
					"key": f.Hash, "value": base64.StdEncoding.EncodeToString(data),
					"metadata": map[string]any{"contentType": f.ContentType}, "base64": true,
				})
			}
			_, err := platDo(ctx, up, "POST", "/pages/assets/upload", nil, payload)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("failed to upload files: %w", err)
			}
			done += len(b)
		}(b)
	}
	wg.Wait()
	if firstErr != nil {
		return platErr("Pages", firstErr)
	}
	if !jsonOutput {
		fmt.Fprintln(os.Stderr, ui.Success(fmt.Sprintf("Uploaded %d files (%d already uploaded) in %s", len(todo), len(files)-len(todo), time.Since(start).Round(10*time.Millisecond))))
	}
	if _, err := platDo(ctx, up, "POST", "/pages/assets/upsert-hashes", nil, map[string]any{"hashes": hashes}); err != nil {
		fmt.Fprintln(os.Stderr, ui.Warn("Failed to update file hashes; the next deploy may re-upload files ("+err.Error()+")"))
	}
	return nil
}

// pagesDeploymentForm builds the multipart body for creating a deployment.
func pagesDeploymentForm(cmd *cobra.Command, dir string, files []pagesFile) ([]byte, string, error) {
	manifest := map[string]string{}
	for _, f := range files {
		manifest["/"+f.Name] = f.Hash
	}
	mj, _ := json.Marshal(manifest)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	field := func(name, val string) { _ = w.WriteField(name, val) }
	file := func(name string, data []byte) error {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, name))
		h.Set("Content-Type", "application/octet-stream")
		pw, err := w.CreatePart(h)
		if err != nil {
			return err
		}
		_, err = pw.Write(data)
		return err
	}
	field("manifest", string(mj))

	branch, _ := cmd.Flags().GetString("branch")
	if branch == "" {
		branch = pagesGit("rev-parse", "--abbrev-ref", "HEAD")
		if branch == "HEAD" {
			branch = ""
		}
	}
	if branch != "" {
		field("branch", branch)
	}
	hash, _ := cmd.Flags().GetString("commit-hash")
	if hash == "" {
		hash = pagesGit("rev-parse", "HEAD")
	}
	if hash != "" {
		field("commit_hash", hash)
	}
	msg, _ := cmd.Flags().GetString("commit-message")
	if msg == "" && hash != "" {
		msg = pagesGit("log", "-1", "--format=%s", hash)
	}
	if msg != "" {
		field("commit_message", pagesTruncateUTF8(msg, 384))
	}
	if cmd.Flags().Changed("commit-dirty") {
		d, _ := cmd.Flags().GetBool("commit-dirty")
		field("commit_dirty", fmt.Sprint(d))
	} else if hash != "" && pagesGit("rev-parse", "--is-inside-work-tree") == "true" {
		field("commit_dirty", fmt.Sprint(pagesGit("status", "--porcelain") != ""))
	}

	for _, special := range []string{"_headers", "_redirects"} {
		if data, err := os.ReadFile(filepath.Join(dir, special)); err == nil {
			if err := file(special, data); err != nil {
				return nil, "", err
			}
			if !jsonOutput {
				fmt.Fprintln(os.Stderr, ui.Info("Uploading "+special))
			}
		}
	}
	workerPath := filepath.Join(dir, "_worker.js")
	if fi, err := os.Stat(workerPath); err == nil {
		var bundle []byte
		if fi.IsDir() {
			bundle, err = pagesWorkerDirBundle(workerPath)
		} else {
			var src []byte
			if src, err = os.ReadFile(workerPath); err == nil {
				bundle, err = pagesWorkerBundle(src)
			}
		}
		if err != nil {
			return nil, "", err
		}
		if err := file("_worker.bundle", bundle); err != nil {
			return nil, "", err
		}
		if !jsonOutput {
			fmt.Fprintln(os.Stderr, ui.Info("Uploading _worker.js (as-is, not bundled)"))
		}
		if routes, err := os.ReadFile(filepath.Join(dir, "_routes.json")); err == nil {
			if !json.Valid(routes) {
				return nil, "", fmt.Errorf("invalid _routes.json in %s", dir)
			}
			if err := file("_routes.json", routes); err != nil {
				return nil, "", err
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// pagesWorkerBundle wraps a pre-built _worker.js (ES module) in the
// multipart "worker bundle" Pages expects.
func pagesWorkerBundle(src []byte) ([]byte, error) {
	return pagesModulesBundle("_worker.js", []pagesModule{{Name: "_worker.js", Data: src}})
}

type pagesModule struct {
	Name string
	Data []byte
}

// pagesWorkerDirBundle bundles a _worker.js/ directory the way
// 'wrangler pages deploy --no-bundle' does: index.js is the main module and
// every other **/*.js and **/*.mjs file is an additional ES module, named by
// its path relative to the directory.
func pagesWorkerDirBundle(dir string) ([]byte, error) {
	main, err := os.ReadFile(filepath.Join(dir, "index.js"))
	if err != nil {
		return nil, fmt.Errorf("%s is a directory but has no index.js entry point", dir)
	}
	mods := []pagesModule{{Name: "index.js", Data: main}}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "index.js" || (path.Ext(rel) != ".js" && path.Ext(rel) != ".mjs") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mods = append(mods, pagesModule{Name: rel, Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pagesModulesBundle("index.js", mods)
}

// pagesModulesBundle writes the worker-upload multipart form (metadata +
// one application/javascript+module part per module).
func pagesModulesBundle(mainModule string, mods []pagesModule) ([]byte, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	meta, _ := json.Marshal(map[string]string{"main_module": mainModule})
	if err := w.WriteField("metadata", string(meta)); err != nil {
		return nil, err
	}
	for _, m := range mods {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, m.Name, m.Name))
		h.Set("Content-Type", "application/javascript+module")
		pw, err := w.CreatePart(h)
		if err != nil {
			return nil, err
		}
		if _, err := pw.Write(m.Data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// pagesWait polls a deployment until its deploy stage finishes (max ~60s).
func pagesWait(ctx context.Context, s *apiSession, projPath string, dep map[string]any) map[string]any {
	id := platStr(dep["id"])
	for i := 0; i < 30 && id != ""; i++ {
		stage := platStr(platGet(dep, "latest_stage.name"))
		status := platStr(platGet(dep, "latest_stage.status"))
		if status == "failure" || status == "canceled" || (stage == "deploy" && status == "success") {
			return dep
		}
		time.Sleep(pagesPollInterval)
		raw, err := platDo(ctx, s, "GET", projPath+"/deployments/"+id, nil, nil)
		if err != nil {
			return dep
		}
		var next map[string]any
		if json.Unmarshal(raw, &next) == nil && next != nil {
			dep = next
		}
	}
	return dep
}

var pagesPollInterval = 2 * time.Second
