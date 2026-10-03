// Package workers holds the non-Cobra pieces of cfctl's Workers commands:
// reading wrangler config files, turning them into upload metadata and
// bindings, building multipart script uploads, static-asset manifests, and
// the tail WebSocket client.
package workers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ConfigNames are the wrangler config files looked for, in wrangler's order.
var ConfigNames = []string{"wrangler.json", "wrangler.jsonc", "wrangler.toml"}

// FindConfig returns the first wrangler config file in dir, or "".
func FindConfig(dir string) string {
	for _, n := range ConfigNames {
		p := filepath.Join(dir, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// Config is the subset of a wrangler config file cfctl understands. Raw keeps
// the merged (top-level + env) table for anything not modelled here.
type Config struct {
	Path string `json:"-"`
	Dir  string `json:"-"`
	Env  string `json:"-"`

	Name               string                   `json:"name"`
	Main               string                   `json:"main"`
	CompatibilityDate  string                   `json:"compatibility_date"`
	CompatibilityFlags []string                 `json:"compatibility_flags"`
	WorkersDev         *bool                    `json:"workers_dev"`
	PreviewURLs        *bool                    `json:"preview_urls"`
	Routes             []Route                  `json:"-"`
	Triggers           struct{ Crons []string } `json:"triggers"`
	Vars               map[string]any           `json:"vars"`
	UsageModel         string                   `json:"usage_model"`
	Logpush            *bool                    `json:"logpush"`
	Limits             map[string]any           `json:"limits"`
	Placement          map[string]any           `json:"placement"`
	Observability      map[string]any           `json:"observability"`
	TailConsumers      []map[string]any         `json:"tail_consumers"`
	Assets             *AssetsConfig            `json:"assets"`
	Migrations         []Migration              `json:"migrations"`
	KeepVars           bool                     `json:"keep_vars"`

	Raw map[string]any `json:"-"`
}

// AssetsConfig is wrangler's [assets] table.
type AssetsConfig struct {
	Directory        string `json:"directory"`
	Binding          string `json:"binding"`
	HTMLHandling     string `json:"html_handling"`
	NotFoundHandling string `json:"not_found_handling"`
	RunWorkerFirst   any    `json:"run_worker_first"`
}

// Route is one entry of wrangler's routes: a bare pattern, or a table with
// zone_name/zone_id or custom_domain.
type Route struct {
	Pattern      string `json:"pattern"`
	ZoneName     string `json:"zone_name,omitempty"`
	ZoneID       string `json:"zone_id,omitempty"`
	CustomDomain bool   `json:"custom_domain,omitempty"`
}

// Migration is one Durable Object migration step.
type Migration struct {
	Tag              string           `json:"tag"`
	NewClasses       []string         `json:"new_classes,omitempty"`
	NewSqliteClasses []string         `json:"new_sqlite_classes,omitempty"`
	RenamedClasses   []map[string]any `json:"renamed_classes,omitempty"`
	DeletedClasses   []string         `json:"deleted_classes,omitempty"`
	TransferredClass []map[string]any `json:"transferred_classes,omitempty"`
}

// inheritable are the keys an [env.X] section inherits from the top level
// (wrangler's "inheritable keys"); everything else (vars, bindings) must be
// repeated per environment.
var inheritable = map[string]bool{
	"name": true, "main": true, "compatibility_date": true, "compatibility_flags": true,
	"workers_dev": true, "preview_urls": true, "routes": true, "route": true, "triggers": true,
	"usage_model": true, "limits": true, "placement": true, "observability": true,
	"logpush": true, "tail_consumers": true, "assets": true, "minify": true, "keep_vars": true,
	"find_additional_modules": true, "base_dir": true, "no_bundle": true, "rules": true,
	"build": true, "upload_source_maps": true, "migrations": true,
}

// LoadConfig reads a wrangler.toml / wrangler.json / wrangler.jsonc file and
// applies [env.<env>] when env is set.
func LoadConfig(path, env string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw := map[string]any{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		if err := toml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case ".json", ".jsonc":
		if err := json.Unmarshal(StripJSONC(data), &raw); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	default:
		return nil, fmt.Errorf("%s: unsupported config file (want wrangler.toml, wrangler.json, or wrangler.jsonc)", path)
	}

	merged := raw
	if env != "" {
		envs, _ := raw["env"].(map[string]any)
		section, ok := envs[env].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s has no [env.%s] section", path, env)
		}
		merged = map[string]any{}
		for k, v := range raw {
			if inheritable[k] {
				merged[k] = v
			}
		}
		// route and routes are one setting: an env that sets either
		// replaces both.
		_, r1 := section["route"]
		_, r2 := section["routes"]
		if r1 || r2 {
			delete(merged, "route")
			delete(merged, "routes")
		}
		for k, v := range section {
			merged[k] = v
		}
		if _, named := section["name"]; !named {
			if n, _ := raw["name"].(string); n != "" {
				merged["name"] = n + "-" + env
			}
		}
	}
	delete(merged, "env")

	// Round-trip through JSON to fill the typed fields (TOML dates become
	// strings; numbers become float64).
	norm := normalize(merged)
	b, err := json.Marshal(norm)
	if err != nil {
		return nil, err
	}
	c := &Config{Path: path, Dir: filepath.Dir(path), Env: env}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Raw = norm.(map[string]any)
	c.Routes, err = parseRoutes(c.Raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// normalize converts TOML-decoded values (local dates, int64) into plain
// JSON-friendly values.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalize(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalize(x)
		}
		return out
	case []map[string]any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalize(x)
		}
		return out
	case toml.LocalDate:
		return t.String()
	case toml.LocalDateTime:
		return t.String()
	case fmt.Stringer:
		return t.String()
	}
	return v
}

func parseRoutes(raw map[string]any) ([]Route, error) {
	var items []any
	if r, ok := raw["route"]; ok {
		items = append(items, r)
	}
	if rs, ok := raw["routes"].([]any); ok {
		items = append(items, rs...)
	}
	var out []Route
	for _, it := range items {
		switch t := it.(type) {
		case string:
			out = append(out, Route{Pattern: t})
		case map[string]any:
			var r Route
			b, _ := json.Marshal(t)
			if err := json.Unmarshal(b, &r); err != nil || r.Pattern == "" {
				return nil, fmt.Errorf("invalid route %v", t)
			}
			out = append(out, r)
		default:
			return nil, fmt.Errorf("invalid route %v", t)
		}
	}
	return out, nil
}

// MainPath returns the entry point resolved against the config's directory.
func (c *Config) MainPath() string {
	if c == nil || c.Main == "" {
		return ""
	}
	if filepath.IsAbs(c.Main) {
		return c.Main
	}
	return filepath.Join(c.Dir, c.Main)
}

// AssetsDir returns the assets directory resolved against the config's directory.
func (c *Config) AssetsDir() string {
	if c == nil || c.Assets == nil || c.Assets.Directory == "" {
		return ""
	}
	if filepath.IsAbs(c.Assets.Directory) {
		return c.Assets.Directory
	}
	return filepath.Join(c.Dir, c.Assets.Directory)
}

// StripJSONC removes // and /* */ comments and trailing commas from JSONC,
// leaving string contents untouched.
func StripJSONC(in []byte) []byte {
	var out bytes.Buffer
	inStr, esc := false, false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inStr {
			out.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out.WriteByte(c)
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}
			if i < len(in) {
				out.WriteByte('\n')
			}
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			i += 2
			for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
				i++
			}
			i++
		default:
			out.WriteByte(c)
		}
	}
	// Drop trailing commas before } or ].
	b := out.Bytes()
	var res bytes.Buffer
	inStr, esc = false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			res.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
		}
		if c == ',' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
		}
		res.WriteByte(c)
	}
	return res.Bytes()
}
