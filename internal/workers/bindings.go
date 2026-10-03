package workers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Binding is one entry of the upload metadata's "bindings" array.
type Binding = map[string]any

// tableBindings maps wrangler array-of-tables keys to binding types and how
// their fields translate: wrangler field → API field.
var tableBindings = []struct {
	key, typ string
	nameKey  string
	fields   map[string]string
}{
	{"kv_namespaces", "kv_namespace", "binding", map[string]string{"id": "namespace_id"}},
	{"r2_buckets", "r2_bucket", "binding", map[string]string{"bucket_name": "bucket_name", "jurisdiction": "jurisdiction"}},
	{"d1_databases", "d1", "binding", map[string]string{"database_id": "id"}},
	{"services", "service", "binding", map[string]string{"service": "service", "entrypoint": "entrypoint", "environment": "environment"}},
	{"analytics_engine_datasets", "analytics_engine", "binding", map[string]string{"dataset": "dataset"}},
	{"hyperdrive", "hyperdrive", "binding", map[string]string{"id": "id"}},
	{"vectorize", "vectorize", "binding", map[string]string{"index_name": "index_name"}},
	{"dispatch_namespaces", "dispatch_namespace", "binding", map[string]string{"namespace": "namespace", "outbound": "outbound"}},
	{"mtls_certificates", "mtls_certificate", "binding", map[string]string{"certificate_id": "certificate_id"}},
	{"send_email", "send_email", "name", map[string]string{"destination_address": "destination_address", "allowed_destination_addresses": "allowed_destination_addresses", "allowed_sender_addresses": "allowed_sender_addresses"}},
	{"pipelines", "pipelines", "binding", map[string]string{"pipeline": "pipeline"}},
	{"workflows", "workflow", "binding", map[string]string{"name": "workflow_name", "class_name": "class_name", "script_name": "script_name"}},
	{"secrets_store_secrets", "secrets_store_secret", "binding", map[string]string{"store_id": "store_id", "secret_name": "secret_name"}},
}

// singleBindings are wrangler tables with just a binding name.
var singleBindings = []struct{ key, typ string }{
	{"ai", "ai"}, {"browser", "browser"}, {"images", "images"}, {"version_metadata", "version_metadata"}, {"media", "media"},
}

// Bindings returns the upload bindings described by the config (vars, KV,
// R2, D1, services, Durable Objects, queues, ...). Unknown binding tables are
// ignored; pass them with --binding instead.
func (c *Config) Bindings() ([]Binding, error) {
	if c == nil {
		return nil, nil
	}
	var out []Binding
	keys := make([]string, 0, len(c.Vars))
	for k := range c.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, VarBinding(k, c.Vars[k]))
	}
	for _, tb := range tableBindings {
		items, err := tableList(c.Raw[tb.key])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tb.key, err)
		}
		for _, it := range items {
			name, _ := it[tb.nameKey].(string)
			if name == "" {
				return nil, fmt.Errorf("%s entry is missing %q", tb.key, tb.nameKey)
			}
			b := Binding{"type": tb.typ, "name": name}
			for from, to := range tb.fields {
				if v, ok := it[from]; ok {
					b[to] = v
				}
			}
			out = append(out, b)
		}
	}
	for _, sb := range singleBindings {
		if t, ok := c.Raw[sb.key].(map[string]any); ok {
			if name, _ := t["binding"].(string); name != "" {
				out = append(out, Binding{"type": sb.typ, "name": name})
			}
		}
	}
	if do, ok := c.Raw["durable_objects"].(map[string]any); ok {
		items, err := tableList(do["bindings"])
		if err != nil {
			return nil, fmt.Errorf("durable_objects.bindings: %w", err)
		}
		for _, it := range items {
			b := Binding{"type": "durable_object_namespace", "name": it["name"], "class_name": it["class_name"]}
			if s, ok := it["script_name"]; ok {
				b["script_name"] = s
			}
			if e, ok := it["environment"]; ok {
				b["environment"] = e
			}
			out = append(out, b)
		}
	}
	if q, ok := c.Raw["queues"].(map[string]any); ok {
		items, err := tableList(q["producers"])
		if err != nil {
			return nil, fmt.Errorf("queues.producers: %w", err)
		}
		for _, it := range items {
			out = append(out, Binding{"type": "queue", "name": it["binding"], "queue_name": it["queue"]})
		}
	}
	if c.Assets != nil && c.Assets.Binding != "" {
		out = append(out, Binding{"type": "assets", "name": c.Assets.Binding})
	}
	return out, nil
}

func tableList(v any) ([]map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("want an array of tables")
	}
	out := make([]map[string]any, 0, len(arr))
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("want an array of tables")
		}
		out = append(out, m)
	}
	return out, nil
}

// VarBinding turns a [vars] entry into a plain_text (strings) or json binding.
func VarBinding(name string, v any) Binding {
	if s, ok := v.(string); ok {
		return Binding{"type": "plain_text", "name": name, "text": s}
	}
	return Binding{"type": "json", "name": name, "json": v}
}

// ParseBindingFlag parses the shorthand binding flags:
//
//	--var NAME=value          plain_text
//	--kv NAME=namespace_id    kv_namespace
//	--r2 NAME=bucket          r2_bucket
//	--d1 NAME=database_id     d1
//	--service NAME=worker[#entrypoint]
//	--queue NAME=queue_name
//	--ai NAME
//	--binding '{"type":...}'  any binding as JSON
func ParseBindingFlag(kind, val string) (Binding, error) {
	if kind == "binding" {
		var b Binding
		if err := json.Unmarshal([]byte(val), &b); err != nil {
			return nil, fmt.Errorf("--binding %q: not a JSON object: %v", val, err)
		}
		if b["type"] == nil || b["name"] == nil {
			return nil, fmt.Errorf("--binding needs \"type\" and \"name\"")
		}
		return b, nil
	}
	if kind == "ai" || kind == "browser" || kind == "version-metadata" {
		if val == "" {
			return nil, fmt.Errorf("--%s needs a binding name", kind)
		}
		return Binding{"type": strings.ReplaceAll(kind, "-", "_"), "name": val}, nil
	}
	name, v, ok := strings.Cut(val, "=")
	if !ok || name == "" {
		return nil, fmt.Errorf("--%s %q: want NAME=value", kind, val)
	}
	switch kind {
	case "var":
		return Binding{"type": "plain_text", "name": name, "text": v}, nil
	case "kv":
		return Binding{"type": "kv_namespace", "name": name, "namespace_id": v}, nil
	case "r2":
		return Binding{"type": "r2_bucket", "name": name, "bucket_name": v}, nil
	case "d1":
		return Binding{"type": "d1", "name": name, "id": v}, nil
	case "queue":
		return Binding{"type": "queue", "name": name, "queue_name": v}, nil
	case "service":
		svc, ep, _ := strings.Cut(v, "#")
		b := Binding{"type": "service", "name": name, "service": svc}
		if ep != "" {
			b["entrypoint"] = ep
		}
		return b, nil
	}
	return nil, fmt.Errorf("unknown binding flag --%s", kind)
}

// MergeBindings appends extra to base, replacing any base binding with the
// same name.
func MergeBindings(base, extra []Binding) []Binding {
	idx := map[any]int{}
	out := append([]Binding(nil), base...)
	for i, b := range out {
		idx[b["name"]] = i
	}
	for _, b := range extra {
		if i, ok := idx[b["name"]]; ok {
			out[i] = b
			continue
		}
		idx[b["name"]] = len(out)
		out = append(out, b)
	}
	return out
}
