package apispec

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

//go:embed openapi.json.gz
var specGz []byte

var (
	specOnce sync.Once
	specRoot map[string]any
	specErr  error
)

// Spec returns the parsed embedded OpenAPI document (parsed once, lazily;
// ~13 MB of JSON, so only describe-style commands should need it).
func Spec() (map[string]any, error) {
	specOnce.Do(func() {
		zr, err := gzip.NewReader(bytes.NewReader(specGz))
		if err != nil {
			specErr = err
			return
		}
		dec := json.NewDecoder(zr)
		dec.UseNumber()
		specErr = dec.Decode(&specRoot)
	})
	return specRoot, specErr
}

// SpecSize returns the compressed size of the embedded spec.
func SpecSize() int { return len(specGz) }

// SpecJSON writes the decompressed embedded spec to w.
func SpecJSON(w io.Writer) error {
	zr, err := gzip.NewReader(bytes.NewReader(specGz))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, zr)
	return err
}

// Limits for schema rendering.
const (
	maxSchemaDepth = 6
	maxVariants    = 30
	maxProps       = 200
)

// maxRenderBytes bounds a rendered schema tree; maxExampleNodes bounds an
// example skeleton. Some Cloudflare schemas are huge unions.
const (
	maxRenderBytes  = 48 << 10
	maxExampleNodes = 1500
)

type schemaWalker struct {
	root      map[string]any
	nodes     int
	truncated bool
}

func (w *schemaWalker) full(b *strings.Builder) bool {
	if b.Len() > maxRenderBytes {
		w.truncated = true
		return true
	}
	return false
}

func (w *schemaWalker) lookup(ref string) map[string]any {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var cur any = w.root
	for _, p := range strings.Split(ref[2:], "/") {
		p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	m, _ := cur.(map[string]any)
	return m
}

// resolve follows $refs. It returns the schema and the last ref name seen
// (used for cycle detection), bounded to 20 hops.
func (w *schemaWalker) resolve(v any) (map[string]any, string) {
	m, _ := v.(map[string]any)
	name := ""
	for i := 0; i < 20 && m != nil; i++ {
		ref, ok := m["$ref"].(string)
		if !ok {
			return m, name
		}
		name = ref
		m = w.lookup(ref)
	}
	return m, name
}

func jstr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case nil:
		return ""
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func oneLine(s string, max int) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		if len(line) > max {
			line = line[:max] + "…"
		}
		return line
	}
	return ""
}

// typeName gives a short type label for a schema.
func (w *schemaWalker) typeName(s map[string]any) string {
	if s == nil {
		return "any"
	}
	t := jstr(s["type"])
	if strings.HasPrefix(t, "[") {
		var ts []string
		_ = json.Unmarshal([]byte(t), &ts)
		t = strings.Join(ts, "|")
	}
	switch t {
	case "array":
		items, _ := w.resolve(s["items"])
		return "array<" + w.typeName(items) + ">"
	case "":
		switch {
		case s["properties"] != nil:
			return "object"
		case s["oneOf"] != nil:
			return "oneOf"
		case s["anyOf"] != nil:
			return "anyOf"
		case s["allOf"] != nil:
			return "object"
		case s["enum"] != nil:
			return "string"
		}
		return "any"
	}
	if f := jstr(s["format"]); f != "" && t == "string" {
		return "string(" + f + ")"
	}
	return t
}

// flatten merges allOf members into one object schema (properties and
// required), recursively, bounded by depth and ref cycles.
func (w *schemaWalker) flatten(s map[string]any, seen map[string]bool, depth int) map[string]any {
	all, ok := s["allOf"].([]any)
	if !ok || depth > maxSchemaDepth {
		return s
	}
	out := map[string]any{}
	for k, v := range s {
		if k != "allOf" {
			out[k] = v
		}
	}
	props := map[string]any{}
	if p, ok := s["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	req := append([]any(nil), asSlice(s["required"])...)
	for _, m := range all {
		sub, ref := w.resolve(m)
		if sub == nil || (ref != "" && seen[ref]) {
			continue
		}
		next := withSeen(seen, ref)
		sub = w.flatten(sub, next, depth+1)
		if p, ok := sub["properties"].(map[string]any); ok {
			for k, v := range p {
				if prev, dup := props[k]; dup {
					// Both members define k: merge them (deeply, via allOf).
					props[k] = map[string]any{"allOf": []any{prev, v}}
				} else {
					props[k] = v
				}
			}
		}
		req = append(req, asSlice(sub["required"])...)
		for _, k := range []string{"type", "description", "oneOf", "anyOf", "items", "enum"} {
			if _, has := out[k]; !has && sub[k] != nil {
				out[k] = sub[k]
			}
		}
	}
	if len(props) > 0 {
		out["properties"] = props
		if out["type"] == nil {
			out["type"] = "object"
		}
	}
	out["required"] = req
	return out
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func withSeen(seen map[string]bool, ref string) map[string]bool {
	if ref == "" {
		return seen
	}
	next := make(map[string]bool, len(seen)+1)
	for k := range seen {
		next[k] = true
	}
	next[ref] = true
	return next
}

// render writes a readable tree of a schema.
func (w *schemaWalker) render(b *strings.Builder, v any, indent string, seen map[string]bool, depth int) {
	if w.full(b) {
		return
	}
	s, ref := w.resolve(v)
	if s == nil {
		return
	}
	if ref != "" && seen[ref] {
		fmt.Fprintf(b, "%s(recursive: %s)\n", indent, refName(ref))
		return
	}
	seen = withSeen(seen, ref)
	if depth > maxSchemaDepth {
		fmt.Fprintf(b, "%s…\n", indent)
		return
	}
	s = w.flatten(s, seen, depth)

	if props, ok := s["properties"].(map[string]any); ok && len(props) > 0 {
		required := map[string]bool{}
		for _, r := range asSlice(s["required"]) {
			required[jstr(r)] = true
		}
		names := make([]string, 0, len(props))
		for k := range props {
			names = append(names, k)
		}
		sort.Slice(names, func(i, j int) bool {
			if required[names[i]] != required[names[j]] {
				return required[names[i]]
			}
			return names[i] < names[j]
		})
		for i, name := range names {
			if w.full(b) {
				return
			}
			if i >= maxProps {
				fmt.Fprintf(b, "%s… %d more\n", indent, len(names)-i)
				break
			}
			ps, pref := w.resolve(props[name])
			if ps == nil {
				fmt.Fprintf(b, "%s%s\n", indent, name)
				continue
			}
			ps = w.flatten(ps, withSeen(seen, pref), depth+1)
			line := indent + name + "  " + w.typeName(ps)
			if required[name] {
				line += "  (required)"
			}
			if enum := asSlice(ps["enum"]); len(enum) > 0 {
				line += "  one of: " + joinEnum(enum)
			}
			if d := oneLine(jstr(ps["description"]), 100); d != "" {
				line += "  — " + d
			}
			b.WriteString(line + "\n")
			if pref != "" && seen[pref] {
				fmt.Fprintf(b, "%s  (recursive: %s)\n", indent, refName(pref))
				continue
			}
			w.renderChildren(b, ps, indent+"  ", withSeen(seen, pref), depth+1)
		}
	}
	w.renderVariants(b, s, indent, seen, depth)
	if t := jstr(s["type"]); t == "array" && s["properties"] == nil {
		items, _ := w.resolve(s["items"])
		if items != nil {
			fmt.Fprintf(b, "%sitems: %s\n", indent, w.typeName(items))
			w.render(b, s["items"], indent+"  ", seen, depth+1)
		}
	}
}

// renderChildren renders nested object properties / array items / variants
// of a property schema.
func (w *schemaWalker) renderChildren(b *strings.Builder, ps map[string]any, indent string, seen map[string]bool, depth int) {
	if depth > maxSchemaDepth {
		return
	}
	if _, ok := ps["properties"]; ok {
		w.render(b, ps, indent, seen, depth)
		return
	}
	if jstr(ps["type"]) == "array" {
		items, iref := w.resolve(ps["items"])
		if items == nil || (iref != "" && seen[iref]) {
			return
		}
		items = w.flatten(items, withSeen(seen, iref), depth+1)
		if items["properties"] != nil || items["oneOf"] != nil || items["anyOf"] != nil {
			w.render(b, ps["items"], indent, seen, depth+1)
		}
		return
	}
	w.renderVariants(b, ps, indent, seen, depth)
}

func (w *schemaWalker) renderVariants(b *strings.Builder, s map[string]any, indent string, seen map[string]bool, depth int) {
	for _, kind := range []string{"oneOf", "anyOf"} {
		variants := asSlice(s[kind])
		if len(variants) == 0 {
			continue
		}
		label := "one of"
		if kind == "anyOf" {
			label = "any of"
		}
		fmt.Fprintf(b, "%s%s %d variants:\n", indent, label, len(variants))
		for i, v := range variants {
			if w.full(b) {
				return
			}
			if i >= maxVariants {
				fmt.Fprintf(b, "%s  … %d more\n", indent, len(variants)-i)
				break
			}
			vs, vref := w.resolve(v)
			if vs == nil {
				continue
			}
			title := jstr(vs["title"])
			if title == "" && vref != "" {
				title = refName(vref)
			}
			fmt.Fprintf(b, "%s  [%d] %s %s\n", indent, i+1, w.typeName(w.flatten(vs, seen, depth+1)), title)
			if vref != "" && seen[vref] {
				fmt.Fprintf(b, "%s      (recursive)\n", indent)
				continue
			}
			w.render(b, v, indent+"      ", seen, depth+1)
		}
	}
}

// decodeStringExample unwraps examples the spec stores as JSON-encoded
// strings ("{\"name\": \"x\"}") for objects and arrays.
func decodeStringExample(ex any) any {
	str, ok := ex.(string)
	if !ok {
		return ex
	}
	t := strings.TrimSpace(str)
	if (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
		var v any
		dec := json.NewDecoder(strings.NewReader(t))
		dec.UseNumber()
		if dec.Decode(&v) == nil {
			return v
		}
	}
	return ex
}

func refName(ref string) string {
	return ref[strings.LastIndex(ref, "/")+1:]
}

func joinEnum(enum []any) string {
	var parts []string
	for i, e := range enum {
		if i >= 12 {
			parts = append(parts, fmt.Sprintf("…(+%d)", len(enum)-i))
			break
		}
		parts = append(parts, jstr(e))
	}
	return strings.Join(parts, ", ")
}

// example builds an example value for a schema: the schema's own example
// when present, otherwise a skeleton from types (first oneOf variant,
// required properties first, depth-limited, cycle-safe).
func (w *schemaWalker) example(v any, seen map[string]bool, depth int) any {
	s, ref := w.resolve(v)
	if s == nil {
		return nil
	}
	if ref != "" && seen[ref] {
		return nil
	}
	w.nodes++
	if w.nodes > maxExampleNodes {
		return nil
	}
	seen = withSeen(seen, ref)
	if ex, ok := s["example"]; ok && ex != nil {
		return decodeStringExample(ex)
	}
	if depth > 4 {
		return nil
	}
	s = w.flatten(s, seen, depth)
	for _, k := range []string{"oneOf", "anyOf"} {
		if vs := asSlice(s[k]); len(vs) > 0 && s["properties"] == nil {
			return w.example(vs[0], seen, depth+1)
		}
	}
	if enum := asSlice(s["enum"]); len(enum) > 0 {
		return enum[0]
	}
	if d, ok := s["default"]; ok {
		return d
	}
	t := jstr(s["type"])
	if props, ok := s["properties"].(map[string]any); ok && (t == "object" || t == "") {
		out := map[string]any{}
		for name, pv := range props {
			ps, _ := w.resolve(pv)
			if ps != nil && (ps["readOnly"] == true) {
				continue
			}
			if x := w.example(pv, seen, depth+1); x != nil {
				out[name] = x
			}
			if len(out) >= 40 {
				break
			}
		}
		return out
	}
	switch t {
	case "array":
		if x := w.example(s["items"], seen, depth+1); x != nil {
			return []any{x}
		}
		return []any{}
	case "string":
		if f := jstr(s["format"]); f != "" {
			return "<" + f + ">"
		}
		return "string"
	case "integer", "number":
		return 0
	case "boolean":
		return false
	case "object":
		return map[string]any{}
	}
	return nil
}

// Describe renders a human-readable description of an operation: method,
// path, params, and request body schema with an example skeleton.
func Describe(o *Op, command string) (string, error) {
	root, err := Spec()
	if err != nil {
		return "", fmt.Errorf("failed to load the embedded OpenAPI spec: %w", err)
	}
	w := &schemaWalker{root: root}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n%s\n\n", o.Method, o.Path, o.Summary)
	fmt.Fprintf(&b, "  Tag:          %s\n  operationId:  %s\n", o.Tag, o.ID)
	if command != "" {
		fmt.Fprintf(&b, "  Command:      %s\n", command)
	}
	if o.Deprecated {
		b.WriteString("  Deprecated:   yes\n")
	}

	paths, _ := root["paths"].(map[string]any)
	item, _ := paths[o.Path].(map[string]any)
	raw, _ := item[strings.ToLower(o.Method)].(map[string]any)

	if d := strings.TrimSpace(jstr(raw["description"])); d != "" {
		b.WriteString("\n" + d + "\n")
	}

	writeParams := func(title string, ps []Param) {
		if len(ps) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", title)
		for _, p := range ps {
			line := "  " + p.Name + "  " + p.Type
			if p.Required {
				line += "  (required)"
			}
			if p.Default != "" {
				line += "  default: " + p.Default
			}
			if len(p.Enum) > 0 {
				line += "  one of: " + strings.Join(p.Enum, ", ")
			}
			if p.Desc != "" {
				line += "  — " + p.Desc
			}
			b.WriteString(line + "\n")
		}
	}
	writeParams("Path parameters", o.PathParams())
	writeParams("Query parameters", o.QueryParams())
	writeParams("Header parameters", o.HeaderParams())

	if raw != nil && o.HasBody() {
		rb, _ := w.resolve(raw["requestBody"])
		content, _ := rb["content"].(map[string]any)
		for _, ct := range o.BodyTypes {
			media, _ := content[ct].(map[string]any)
			req := ""
			if o.BodyRequired {
				req = ", required"
			}
			fmt.Fprintf(&b, "\nRequest body (%s%s):\n", ct, req)
			if media == nil || media["schema"] == nil {
				b.WriteString("  (no schema)\n")
				continue
			}
			var tree strings.Builder
			w.render(&tree, media["schema"], "  ", map[string]bool{}, 0)
			if tree.Len() == 0 {
				s, _ := w.resolve(media["schema"])
				fmt.Fprintf(&tree, "  %s\n", w.typeName(s))
			}
			b.WriteString(tree.String())
			if w.truncated {
				b.WriteString("  … (schema truncated; run `cfctl api spec` for the full OpenAPI document)\n")
				w.truncated = false
			}
			if strings.Contains(ct, "json") {
				ex := media["example"]
				if ex == nil {
					ex = w.example(media["schema"], map[string]bool{}, 0)
				}
				ex = decodeStringExample(ex)
				if ex != nil {
					js, err := json.MarshalIndent(ex, "  ", "  ")
					if err == nil && len(js) < 8000 {
						fmt.Fprintf(&b, "\n  Example (--data):\n  %s\n", js)
					}
				}
			}
			if ct == "multipart/form-data" {
				b.WriteString("\n  Send with --form name=value or --form name=@file[;type=mime].\n")
			}
		}
	}
	return b.String(), nil
}
