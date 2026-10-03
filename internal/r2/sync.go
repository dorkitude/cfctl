package r2

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// LocalFile is one file under a sync source directory.
type LocalFile struct {
	Rel  string // slash-separated path relative to the root
	Path string // OS path
	Size int64
}

// SyncItem is one transfer in a sync plan.
type SyncItem struct {
	Key    string `json:"key"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Reason string `json:"reason"` // "new" or "changed"
}

// SyncPlan is what a sync would do.
type SyncPlan struct {
	Transfers []SyncItem `json:"transfers"`
	// Deletes are remote keys (upload) or local paths (download) to remove.
	Deletes   []string `json:"deletes,omitempty"`
	Unchanged int      `json:"unchanged"`
	Bytes     int64    `json:"bytes"`
}

// Excluded reports whether rel matches any glob (matched against the full
// relative path and against the base name).
func Excluded(rel string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, rel); ok {
			return true
		}
		if ok, _ := path.Match(g, path.Base(rel)); ok {
			return true
		}
		if strings.HasSuffix(g, "/") && strings.HasPrefix(rel, g) {
			return true
		}
	}
	return false
}

// WalkLocal lists regular files under root (symlinks to files are followed;
// directories named in excludes are skipped).
func WalkLocal(root string, excludes []string) ([]LocalFile, error) {
	var out []LocalFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if Excluded(rel+"/", excludes) || Excluded(rel, excludes) {
				return filepath.SkipDir
			}
			return nil
		}
		if Excluded(rel, excludes) {
			return nil
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		out = append(out, LocalFile{Rel: rel, Path: p, Size: info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, err
}

// FileMD5 returns a file's hex MD5 (the ETag of a single-part upload).
func FileMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sameContent compares a local file and a remote object: sizes must match,
// and unless sizeOnly, a single-part ETag must equal the file's MD5
// (multipart ETags, "...-N", can't be compared, so size decides).
func sameContent(lf LocalFile, o Object, sizeOnly bool) bool {
	if lf.Size != o.Size {
		return false
	}
	etag := strings.Trim(o.ETag, `"`)
	if sizeOnly || etag == "" || strings.Contains(etag, "-") {
		return true
	}
	sum, err := FileMD5(lf.Path)
	return err == nil && strings.EqualFold(sum, etag)
}

// PlanUpload compares local files with remote objects under prefix.
func PlanUpload(local []LocalFile, remote []Object, prefix string, sizeOnly, deleteExtra bool, excludes []string) SyncPlan {
	byKey := make(map[string]Object, len(remote))
	for _, o := range remote {
		byKey[o.Key] = o
	}
	plan := SyncPlan{Transfers: []SyncItem{}}
	seen := map[string]bool{}
	for _, lf := range local {
		key := prefix + lf.Rel
		seen[key] = true
		o, exists := byKey[key]
		switch {
		case !exists:
			plan.Transfers = append(plan.Transfers, SyncItem{Key: key, Path: lf.Path, Size: lf.Size, Reason: "new"})
			plan.Bytes += lf.Size
		case !sameContent(lf, o, sizeOnly):
			plan.Transfers = append(plan.Transfers, SyncItem{Key: key, Path: lf.Path, Size: lf.Size, Reason: "changed"})
			plan.Bytes += lf.Size
		default:
			plan.Unchanged++
		}
	}
	if deleteExtra {
		for _, o := range remote {
			if !seen[o.Key] && !Excluded(strings.TrimPrefix(o.Key, prefix), excludes) {
				plan.Deletes = append(plan.Deletes, o.Key)
			}
		}
		sort.Strings(plan.Deletes)
	}
	return plan
}

// PlanDownload compares remote objects under prefix with files under root.
// Keys ending in "/" (folder markers) are skipped.
func PlanDownload(remote []Object, local []LocalFile, prefix, root string, sizeOnly, deleteExtra bool, excludes []string) SyncPlan {
	byRel := make(map[string]LocalFile, len(local))
	for _, lf := range local {
		byRel[lf.Rel] = lf
	}
	plan := SyncPlan{Transfers: []SyncItem{}}
	seen := map[string]bool{}
	for _, o := range remote {
		rel := strings.TrimPrefix(o.Key, prefix)
		if rel == "" || strings.HasSuffix(rel, "/") || Excluded(rel, excludes) || !safeRel(rel) {
			continue
		}
		seen[rel] = true
		dst := filepath.Join(root, filepath.FromSlash(rel))
		lf, exists := byRel[rel]
		switch {
		case !exists:
			plan.Transfers = append(plan.Transfers, SyncItem{Key: o.Key, Path: dst, Size: o.Size, Reason: "new"})
			plan.Bytes += o.Size
		case !sameContent(lf, o, sizeOnly):
			plan.Transfers = append(plan.Transfers, SyncItem{Key: o.Key, Path: dst, Size: o.Size, Reason: "changed"})
			plan.Bytes += o.Size
		default:
			plan.Unchanged++
		}
	}
	if deleteExtra {
		for _, lf := range local {
			if !seen[lf.Rel] {
				plan.Deletes = append(plan.Deletes, lf.Path)
			}
		}
		sort.Strings(plan.Deletes)
	}
	return plan
}

// safeRel rejects keys that would escape the destination directory.
func safeRel(rel string) bool {
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
