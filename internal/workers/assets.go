package workers

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MaxAssetSize is Cloudflare's per-file limit for Workers static assets.
const MaxAssetSize = 25 << 20

// MaxAssetFiles is the per-version file count limit.
const MaxAssetFiles = 100000

// AssetEntry is one manifest entry ({hash, size}) plus where it came from.
type AssetEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	path string
}

// Assets is a scanned static-assets directory.
type Assets struct {
	Dir string
	// Manifest maps "/path/in/site.html" to its entry.
	Manifest map[string]AssetEntry
	// Headers and Redirects hold _headers / _redirects contents, which are
	// sent in the assets config instead of being uploaded.
	Headers, Redirects string
	byHash             map[string]string // hash → local file path
}

// ScanAssets walks dir and builds the upload manifest. It skips _headers,
// _redirects, .assetsignore, and anything .assetsignore matches, plus
// node_modules, .git and .DS_Store.
func ScanAssets(dir string) (*Assets, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("assets directory: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("assets directory %s is not a directory", dir)
	}
	a := &Assets{Dir: dir, Manifest: map[string]AssetEntry{}, byHash: map[string]string{}}
	ignore := readIgnore(filepath.Join(dir, ".assetsignore"))
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == "node_modules" || name == ".git" || ignored(ignore, rel, name) {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case rel == "_headers":
			b, err := os.ReadFile(p)
			a.Headers = string(b)
			return err
		case rel == "_redirects":
			b, err := os.ReadFile(p)
			a.Redirects = string(b)
			return err
		case rel == ".assetsignore", name == ".DS_Store", ignored(ignore, rel, name):
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > MaxAssetSize {
			return fmt.Errorf("asset %s is %d bytes; the limit is 25 MiB per file", rel, info.Size())
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h := HashAsset(data, filepath.Ext(name))
		a.Manifest["/"+rel] = AssetEntry{Hash: h, Size: info.Size(), path: p}
		a.byHash[h] = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(a.Manifest) > MaxAssetFiles {
		return nil, fmt.Errorf("%d asset files; the limit is %d", len(a.Manifest), MaxAssetFiles)
	}
	return a, nil
}

// HashAsset is the content key for a file: the first 32 hex chars of
// SHA-256 over the base64 contents plus the extension (wrangler uses BLAKE3
// over the same input; the API treats the hash as an opaque key).
func HashAsset(data []byte, ext string) string {
	sum := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(data) + strings.TrimPrefix(ext, ".")))
	return hex.EncodeToString(sum[:])[:32]
}

// Payload returns the manifest in the API's {path: {hash, size}} shape.
func (a *Assets) Payload() map[string]AssetEntry { return a.Manifest }

// FileFor returns the local path and content type for a hash the API asked for.
func (a *Assets) FileFor(hash string) (string, string, bool) {
	p, ok := a.byHash[hash]
	if !ok {
		return "", "", false
	}
	ct := mime.TypeByExtension(filepath.Ext(p))
	if ct == "" {
		ct = "application/octet-stream"
	}
	return p, ct, true
}

// Count returns the number of files in the manifest.
func (a *Assets) Count() int { return len(a.Manifest) }

// Paths returns the manifest paths, sorted.
func (a *Assets) Paths() []string {
	out := make([]string, 0, len(a.Manifest))
	for p := range a.Manifest {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func readIgnore(p string) []string {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var pats []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		pats = append(pats, strings.TrimSuffix(strings.TrimPrefix(l, "/"), "/"))
	}
	return pats
}

// ignored is a simple .assetsignore matcher: each pattern is matched
// against the relative path, the base name, and as a directory prefix.
func ignored(pats []string, rel, name string) bool {
	for _, p := range pats {
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
		if strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}
