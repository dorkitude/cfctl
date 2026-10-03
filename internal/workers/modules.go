package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Module is one file uploaded with a Worker.
type Module struct {
	Name        string // part name / module specifier, e.g. "index.js"
	ContentType string
	Data        []byte
}

var (
	esmExportRE = regexp.MustCompile(`(?m)(^|[;{}])\s*export\s*(default\b|\{|const\b|let\b|var\b|function\b|async\b|class\b|\*)`)
	esmImportRE = regexp.MustCompile(`(?m)(^|;)\s*import\s*[\w{*'"]`)
	// relImportRE finds static and dynamic relative imports.
	relImportRE = regexp.MustCompile(`(?:\bfrom\s*|\bimport\s*\(?\s*)['"](\.{1,2}/[^'"]+)['"]`)
)

// IsModuleSyntax reports whether a script looks like an ES module (has an
// import or export statement) rather than a service-worker script.
func IsModuleSyntax(src []byte) bool { return esmExportRE.Match(src) || esmImportRE.Match(src) }

// RelativeImports returns the relative module specifiers a JS module imports.
func RelativeImports(src []byte) []string {
	var out []string
	for _, m := range relImportRE.FindAllSubmatch(src, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

// ContentTypeFor returns the upload content type for a module file name.
func ContentTypeFor(name string, esm bool) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".js", ".mjs":
		if esm {
			return "application/javascript+module"
		}
		return "application/javascript"
	case ".cjs":
		return "application/javascript"
	case ".py":
		return "text/x-python"
	case ".wasm":
		return "application/wasm"
	case ".map":
		return "application/source-map"
	case ".txt", ".html", ".sql":
		return "text/plain"
	case ".json":
		return "application/json"
	}
	return "application/octet-stream"
}

// LoadModules reads the main script plus extra module files. Extra module
// names are their paths relative to the main script's directory.
func LoadModules(main string, extra []string, esm *bool, sourceMaps bool) (mods []Module, isESM bool, err error) {
	src, err := os.ReadFile(main)
	if err != nil {
		return nil, false, err
	}
	isESM = IsModuleSyntax(src) || strings.EqualFold(filepath.Ext(main), ".py")
	if esm != nil {
		isESM = *esm
	}
	mainName := filepath.Base(main)
	if !isESM {
		if len(extra) > 0 {
			return nil, false, fmt.Errorf("additional modules need an ES module Worker (use --esm)")
		}
		return []Module{{Name: mainName, ContentType: "application/javascript", Data: src}}, false, nil
	}
	mods = append(mods, Module{Name: mainName, ContentType: ContentTypeFor(mainName, true), Data: src})
	base := filepath.Dir(main)
	seen := map[string]bool{mainName: true}
	add := func(p string) error {
		rel, err := filepath.Rel(base, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(p)
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			return nil
		}
		seen[rel] = true
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mods = append(mods, Module{Name: rel, ContentType: ContentTypeFor(rel, true), Data: data})
		return nil
	}
	for _, p := range extra {
		if err := add(p); err != nil {
			return nil, true, err
		}
	}
	// Follow relative imports from JS modules (unbundled multi-file Workers).
	queue := []string{main}
	visited := map[string]bool{main: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ext := strings.ToLower(filepath.Ext(cur))
		if ext != ".js" && ext != ".mjs" {
			continue
		}
		data, err := os.ReadFile(cur)
		if err != nil {
			return nil, true, err
		}
		for _, spec := range RelativeImports(data) {
			p := filepath.Join(filepath.Dir(cur), filepath.FromSlash(spec))
			if visited[p] {
				continue
			}
			visited[p] = true
			if rel, err := filepath.Rel(base, p); err != nil || strings.HasPrefix(rel, "..") {
				continue // outside the main module's directory: needs bundling
			}
			if st, err := os.Stat(p); err != nil || st.IsDir() {
				continue
			}
			if err := add(p); err != nil {
				return nil, true, err
			}
			queue = append(queue, p)
		}
	}
	if sourceMaps {
		if _, err := os.Stat(main + ".map"); err == nil {
			if err := add(main + ".map"); err != nil {
				return nil, true, err
			}
		}
	}
	return mods, true, nil
}

// BuildUpload encodes metadata plus modules as the multipart/form-data body
// the script and version upload endpoints take.
func BuildUpload(metadata map[string]any, mods []Module) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	meta, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", err
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="metadata"`)
	h.Set("Content-Type", "application/json")
	pw, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	pw.Write(meta)
	for _, m := range mods {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(m.Name), escapeQuotes(m.Name)))
		h.Set("Content-Type", m.ContentType)
		pw, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		pw.Write(m.Data)
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func escapeQuotes(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// Bundle runs esbuild (which must be on PATH) to bundle entry into a single
// ES module in outDir, and returns the output path.
func Bundle(ctx context.Context, entry, outDir string, minify, nodeCompat, sourcemap bool) (string, error) {
	bin, err := exec.LookPath("esbuild")
	if err != nil {
		return "", fmt.Errorf("--bundle needs esbuild on PATH (npm i -g esbuild), or pass a pre-built file without --bundle")
	}
	out := filepath.Join(outDir, strings.TrimSuffix(filepath.Base(entry), filepath.Ext(entry))+".js")
	args := []string{entry, "--bundle", "--format=esm", "--target=es2022", "--platform=neutral",
		"--main-fields=workerd,worker,browser,module,main", "--conditions=workerd,worker,browser",
		"--external:cloudflare:*", "--outfile=" + out, "--log-level=warning"}
	if nodeCompat {
		args = append(args, "--external:node:*")
	}
	if minify {
		args = append(args, "--minify")
	}
	if sourcemap {
		args = append(args, "--sourcemap=external")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("esbuild failed: %w", err)
	}
	return out, nil
}
