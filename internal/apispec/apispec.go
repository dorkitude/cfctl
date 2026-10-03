// Package apispec embeds Cloudflare's official OpenAPI spec and a compact,
// generated table of every API operation in it (ops_gen.go).
//
// Regenerate the table with `go generate ./...`; refresh the vendored spec
// with `make spec` (scripts/update-spec.sh).
package apispec

//go:generate go run ./gen

import (
	"sort"
	"strings"
)

// Param is one operation parameter.
type Param struct {
	Name string
	// In is "path", "query", "header", or "cookie".
	In string
	// Type is the JSON schema type: string, integer, number, boolean,
	// object, or array<T>.
	Type     string
	Required bool
	Enum     []string
	Default  string
	// Desc is the first line of the parameter's description.
	Desc string
}

// IsArray reports whether the parameter takes repeated values.
func (p Param) IsArray() bool { return strings.HasPrefix(p.Type, "array") }

// Op is one Cloudflare API operation (an OpenAPI path + method).
type Op struct {
	// ID is the OpenAPI operationId (unique).
	ID     string
	Method string
	// Path is the templated path under /client/v4, e.g. /zones/{zone_id}/dns_records.
	Path string
	// Tag is the operation's first OpenAPI tag; TagSlug its CLI name.
	Tag, TagSlug string
	// Slug is the operation's CLI name, unique within TagSlug.
	Slug    string
	Summary string
	// Desc is the first line of the description.
	Desc       string
	Deprecated bool
	// Confirm is set when the spec marks the operation as needing confirmation
	// (x-forge-require-confirmation).
	Confirm bool
	// Params: path params in path order, then query, header, cookie.
	Params []Param
	// BodyTypes lists request body content types, most CLI-friendly first.
	BodyTypes    []string
	BodyRequired bool
}

// PathParams returns the path parameters in path order.
func (o *Op) PathParams() []Param { return o.paramsIn("path") }

// QueryParams returns the query parameters.
func (o *Op) QueryParams() []Param { return o.paramsIn("query") }

// HeaderParams returns the header parameters.
func (o *Op) HeaderParams() []Param { return o.paramsIn("header") }

func (o *Op) paramsIn(in string) []Param {
	var out []Param
	for _, p := range o.Params {
		if p.In == in {
			out = append(out, p)
		}
	}
	return out
}

// HasBody reports whether the operation takes a request body.
func (o *Op) HasBody() bool { return len(o.BodyTypes) > 0 }

// Ops returns every operation, in path then method order.
func Ops() []Op { return ops }

// ByID returns the operation with the given operationId.
func ByID(id string) (*Op, bool) {
	for i := range ops {
		if ops[i].ID == id {
			return &ops[i], true
		}
	}
	return nil, false
}

// Find returns the operation with the given tag slug and op slug.
func Find(tagSlug, slug string) (*Op, bool) {
	for i := range ops {
		if ops[i].TagSlug == tagSlug && ops[i].Slug == slug {
			return &ops[i], true
		}
	}
	return nil, false
}

// Tag is a tag with its operation count.
type Tag struct {
	Name, Slug string
	Ops        int
}

// Tags returns every tag, sorted by slug.
func Tags() []Tag {
	idx := map[string]*Tag{}
	for _, o := range ops {
		t, ok := idx[o.TagSlug]
		if !ok {
			t = &Tag{Name: o.Tag, Slug: o.TagSlug}
			idx[o.TagSlug] = t
		}
		t.Ops++
	}
	out := make([]Tag, 0, len(idx))
	for _, t := range idx {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// Search returns operations matching every word (case-insensitive) in
// operationId, summary, path, tag, or slugs.
func Search(words []string) []Op {
	var out []Op
	for _, o := range ops {
		hay := strings.ToLower(strings.Join([]string{o.ID, o.Summary, o.Path, o.Tag, o.TagSlug, o.Slug, o.Method}, " "))
		match := true
		for _, w := range words {
			if !strings.Contains(hay, strings.ToLower(w)) {
				match = false
				break
			}
		}
		if match {
			out = append(out, o)
		}
	}
	return out
}
