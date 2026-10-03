package r2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
)

// Object is one R2 object as the REST API lists it.
type Object struct {
	Key            string            `json:"key"`
	ETag           string            `json:"etag,omitempty"`
	LastModified   string            `json:"last_modified,omitempty"`
	Size           int64             `json:"size"`
	HTTPMetadata   map[string]any    `json:"http_metadata,omitempty"`
	CustomMetadata map[string]string `json:"custom_metadata,omitempty"`
	StorageClass   string            `json:"storage_class,omitempty"`
}

// ListOptions selects objects to list.
type ListOptions struct {
	Prefix    string
	Delimiter string // "/" for a folder view; "" lists recursively
	PerPage   int    // ≤0 → 1000 (the maximum)
	// Limit stops after this many objects (≤0 → no limit).
	Limit int
	// MaxPages bounds the loop (≤0 → api.DefaultMaxPages).
	MaxPages int
	// Jurisdiction sets cf-r2-jurisdiction ("" or "default" → none).
	Jurisdiction string
}

// ListResult is the outcome of List.
type ListResult struct {
	Objects []Object `json:"objects"`
	// Prefixes are the common prefixes ("folders") when Delimiter is set.
	Prefixes  []string `json:"prefixes,omitempty"`
	Pages     int      `json:"-"`
	Truncated bool     `json:"truncated,omitempty"`
}

type listInfo struct {
	Cursor      string   `json:"cursor"`
	IsTruncated bool     `json:"is_truncated"`
	Delimited   []string `json:"delimited"`
}

// JurisdictionHeader returns the cf-r2-jurisdiction header for j, or nil.
func JurisdictionHeader(j string) http.Header {
	if j == "" || j == "default" {
		return nil
	}
	return http.Header{"Cf-R2-Jurisdiction": {j}}
}

// BucketPath is /accounts/{acct}/r2/buckets/{bucket} plus optional suffix
// segments (each path-escaped).
func BucketPath(acct, bucket string, segs ...string) string {
	p := "/accounts/" + url.PathEscape(acct) + "/r2/buckets/" + url.PathEscape(bucket)
	for _, s := range segs {
		p += "/" + url.PathEscape(s)
	}
	return p
}

// ObjectPath is the REST path of one object. The key is escaped as a single
// segment (slashes become %2F), as the API expects.
func ObjectPath(acct, bucket, key string) string {
	return BucketPath(acct, bucket) + "/objects/" + url.PathEscape(key)
}

// List lists objects through the REST API, following the cursor until the
// listing isn't truncated. fn, if set, is called with each page's objects
// (useful for progress); the objects are also accumulated unless fn returns
// false for "don't keep".
func List(ctx context.Context, c *api.Client, acct, bucket string, o ListOptions, fn func(page []Object)) (*ListResult, error) {
	perPage := o.PerPage
	if perPage <= 0 || perPage > 1000 {
		perPage = 1000
	}
	maxPages := o.MaxPages
	if maxPages <= 0 {
		maxPages = api.DefaultMaxPages
	}
	q := url.Values{"per_page": {strconv.Itoa(perPage)}}
	if o.Prefix != "" {
		q.Set("prefix", o.Prefix)
	}
	if o.Delimiter != "" {
		q.Set("delimiter", o.Delimiter)
	}
	out := &ListResult{Objects: []Object{}}
	seen := map[string]bool{}
	seenPrefix := map[string]bool{}
	for {
		if o.Limit > 0 {
			if left := o.Limit - len(out.Objects); left < perPage {
				q.Set("per_page", strconv.Itoa(max(left, 1)))
			}
		}
		resp, err := c.Do(ctx, api.Request{Method: "GET", Path: BucketPath(acct, bucket) + "/objects", Query: q, Header: JurisdictionHeader(o.Jurisdiction)})
		out.Pages++
		if err != nil {
			return nil, err
		}
		if resp.Envelope == nil {
			return nil, fmt.Errorf("unexpected response listing objects in %s", bucket)
		}
		var page []Object
		if err := json.Unmarshal(resp.Envelope.Result, &page); err != nil {
			return nil, fmt.Errorf("decoding object list: %w", err)
		}
		// api.ResultInfo doesn't carry is_truncated/delimited; read them from the body.
		var full struct {
			ResultInfo listInfo `json:"result_info"`
		}
		_ = json.Unmarshal(resp.Body, &full)
		info := full.ResultInfo
		if fn != nil && len(page) > 0 {
			fn(page)
		}
		out.Objects = append(out.Objects, page...)
		for _, p := range info.Delimited {
			if !seenPrefix[p] {
				seenPrefix[p] = true
				out.Prefixes = append(out.Prefixes, p)
			}
		}
		if o.Limit > 0 && len(out.Objects) >= o.Limit {
			out.Objects = out.Objects[:o.Limit]
			out.Truncated = info.IsTruncated
			return out, nil
		}
		if !info.IsTruncated || info.Cursor == "" || seen[info.Cursor] {
			return out, nil
		}
		if out.Pages >= maxPages {
			out.Truncated = true
			return out, nil
		}
		seen[info.Cursor] = true
		q.Set("cursor", info.Cursor)
	}
}

// Stat returns one object's listing entry (size, etag, metadata), or
// (nil, nil) if no object has exactly that key. The REST API has no HEAD;
// listing with prefix=key returns the key itself first when it exists.
func Stat(ctx context.Context, c *api.Client, acct, bucket, key, jurisdiction string) (*Object, error) {
	res, err := List(ctx, c, acct, bucket, ListOptions{Prefix: key, PerPage: 1, Limit: 1, Jurisdiction: jurisdiction}, nil)
	if err != nil {
		return nil, err
	}
	for _, o := range res.Objects {
		if o.Key == key {
			o := o
			return &o, nil
		}
	}
	return nil, nil
}

// Ext returns a key's extension for grouping ("(none)" when there isn't one).
func Ext(key string) string {
	e := strings.ToLower(path.Ext(path.Base(key)))
	if e == "" || e == "." {
		return "(none)"
	}
	return e
}

// PrefixAt groups key by the first `depth` path segments below base
// ("a/b/" for key "base/a/b/c.png", depth 2). Objects with fewer directory
// levels group under their own directory plus " (files)"; objects directly
// under base group as "(files)".
func PrefixAt(key, base string, depth int) string {
	rel := strings.TrimPrefix(key, base)
	parts := strings.Split(rel, "/")
	dirs := parts[:len(parts)-1]
	if len(dirs) == 0 {
		return "(files)"
	}
	if depth < 1 {
		depth = 1
	}
	if len(dirs) < depth {
		return base + strings.Join(dirs, "/") + "/ (files)"
	}
	return base + strings.Join(dirs[:depth], "/") + "/"
}
