package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const fsBase = "/accounts/{account_id}/flagship/apps"

var fsFlagCols = []platCol{
	{H: "key", Path: "key"}, {H: "enabled", Path: "enabled"}, {H: "default", Path: "default_variation"},
	{H: "variations", Path: "variations"}, {H: "description", Path: "description", W: 40}, {H: "updated", Path: "updated_at"},
}

// fsScalar infers a variation value like wrangler: true/false, numbers,
// JSON objects/arrays, else a string.
func fsScalar(raw string) any {
	switch raw {
	case "true":
		return true
	case "false":
		return false
	}
	t := strings.TrimSpace(raw)
	if t != "" {
		if n, err := strconv.ParseFloat(t, 64); err == nil && !math.IsInf(n, 0) && strconv.FormatFloat(n, 'f', -1, 64) == t {
			return n
		}
	}
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		var v any
		if json.Unmarshal([]byte(t), &v) == nil {
			return v
		}
	}
	return raw
}

func fsParseKV(entries []string, what string) (map[string]any, []string, error) {
	out := map[string]any{}
	var order []string
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, nil, fmt.Errorf("invalid %s %q: want name=value", what, e)
		}
		if _, dup := out[k]; dup {
			return nil, nil, fmt.Errorf("duplicate %s %q", what, k)
		}
		out[k] = fsScalar(v)
		order = append(order, k)
	}
	return out, order, nil
}

func fsRuleJSON(c *cobra.Command) ([]any, error) {
	rs, _ := c.Flags().GetStringArray("rule-json")
	var rules []any
	for i, r := range rs {
		var v map[string]any
		if err := json.Unmarshal([]byte(r), &v); err != nil {
			return nil, fmt.Errorf("--rule-json #%d is not a JSON object: %w", i+1, err)
		}
		if _, ok := v["priority"]; !ok {
			v["priority"] = i + 1
		}
		rules = append(rules, v)
	}
	return rules, nil
}

// fsMutate gets a flag, lets fn change it, and PUTs it back.
func fsMutate(c *cobra.Command, args []string, fn func(flag map[string]any) error, done string) error {
	ctx := context.Background()
	s, err := newAPISession(c)
	if err != nil {
		return err
	}
	path, _, err := platFill(ctx, s, fsBase+"/{app_id}/flags/{flag_key}", args[:2])
	if err != nil {
		return err
	}
	raw, err := platDo(ctx, s, "GET", path, nil, nil)
	if err != nil {
		return platErr("Flagship", err)
	}
	var flag map[string]any
	if err := platDecode(raw, &flag); err != nil {
		return err
	}
	if err := fn(flag); err != nil {
		return err
	}
	input := map[string]any{}
	for _, k := range []string{"key", "description", "enabled", "default_variation", "variations", "rules"} {
		if v, ok := flag[k]; ok {
			input[k] = v
		}
	}
	raw, err = platDo(ctx, s, "PUT", path, nil, input)
	if err != nil {
		return platErr("Flagship", err)
	}
	if jsonOutput {
		return printBody(raw, nil)
	}
	fmt.Println(ui.Success(fmt.Sprintf(done, args[1])))
	return nil
}

func fsHasVariation(flag map[string]any, v string) error {
	vars, _ := flag["variations"].(map[string]any)
	if _, ok := vars[v]; !ok {
		names := make([]string, 0, len(vars))
		for k := range vars {
			names = append(names, k)
		}
		sort.Strings(names)
		return fmt.Errorf("unknown variation %q; available: %s", v, strings.Join(names, ", "))
	}
	return nil
}

func fsSimple(use, short string, fn func(c *cobra.Command, args []string, flag map[string]any) error, done string, flags func(c *cobra.Command), extra int) *cobra.Command {
	return platCommand(platSpec{
		Use: use, Short: short, Path: fsBase + "/{app_id}/flags/{flag_key}", ExtraArgs: extra, Flags: flags, Product: "Flagship",
		Run: func(c *cobra.Command, args []string) error {
			return fsMutate(c, args, func(f map[string]any) error { return fn(c, args, f) }, done)
		},
	})
}

func init() {
	apps := platGroup("apps", "Manage Flagship apps", "", []string{"app"},
		platSpecs(
			platSpec{Use: "list", Short: "List apps", Aliases: []string{"ls"}, Path: fsBase, List: true,
				Cols: []platCol{{H: "id", Path: "id"}, {H: "name", Path: "name"}, {H: "created", Path: "created_at"}, {H: "updated", Path: "updated_at"}}, Title: "🚩 %d Flagship apps", Product: "Flagship"},
			platSpec{Use: "get <app-id>", Short: "Show an app", Path: fsBase + "/{app_id}", Product: "Flagship"},
			platSpec{Use: "create <name>", Short: "Create an app", Method: "POST", Path: fsBase, ExtraArgs: 1,
				Body:    func(_ *cobra.Command, args []string) (any, error) { return map[string]any{"name": args[0]}, nil },
				Product: "Flagship", Title: "🚩 Created app"},
			platSpec{Use: "update <app-id> <new-name>", Short: "Rename an app", Method: "PUT", Path: fsBase + "/{app_id}", ExtraArgs: 1,
				Body: func(_ *cobra.Command, args []string) (any, error) { return map[string]any{"name": args[1]}, nil },
				Done: "Updated app", Product: "Flagship"},
			platSpec{Use: "delete <app-id>", Short: "Delete an app and its flags", Aliases: []string{"rm"}, Method: "DELETE", Path: fsBase + "/{app_id}",
				Confirm: "delete Flagship app %s and all its flags", Done: "Deleted app %s", Product: "Flagship"},
		)...)

	flags := platGroup("flags", "Manage feature flags", "", []string{"flag"},
		append(platSpecs(
			platSpec{Use: "list <app-id>", Short: "List flags", Aliases: []string{"ls"}, Path: fsBase + "/{app_id}/flags", List: true,
				Cols: fsFlagCols, Title: "🚩 %d flags", Product: "Flagship"},
			platSpec{Use: "get <app-id> <key>", Short: "Show a flag", Aliases: []string{"inspect"}, Path: fsBase + "/{app_id}/flags/{flag_key}", Print: fsPrintFlag, Product: "Flagship"},
			platSpec{Use: "create <app-id> <key>", Short: "Create a flag", Method: "POST", Path: fsBase + "/{app_id}/flags", ExtraArgs: 1,
				Long: `Create a feature flag. With no --variation it's a boolean flag (on=true, off=false,
default off). Variation values are inferred: true/false, numbers, JSON, else strings.`,
				Example: `  cfctl flagship flags create <APP_ID> new-ui
  cfctl flagship flags create <APP_ID> model --variation stable=gpt-a --variation candidate=gpt-b
  cfctl flagship flags create <APP_ID> beta --rule-json '{"conditions":[{"attribute":"plan","operator":"equals","value":"pro"}],"serve_variation":"on"}'`,
				Flags: func(c *cobra.Command) {
					c.Flags().StringArray("variation", nil, "Variation name=value (repeatable)")
					c.Flags().String("default-variation", "", "Variation served by default")
					c.Flags().String("description", "", "Description")
					c.Flags().Bool("disabled", false, "Create the flag disabled")
					c.Flags().StringArray("rule-json", nil, "Targeting rule as a JSON object (repeatable)")
				},
				Body: func(c *cobra.Command, args []string) (any, error) {
					vs, _ := c.Flags().GetStringArray("variation")
					variations := map[string]any{"on": true, "off": false}
					def := "off"
					if len(vs) > 0 {
						var order []string
						var err error
						if variations, order, err = fsParseKV(vs, "--variation"); err != nil {
							return nil, err
						}
						def = order[0]
					}
					if d, _ := c.Flags().GetString("default-variation"); d != "" {
						def = d
					}
					if _, ok := variations[def]; !ok {
						return nil, fmt.Errorf("default variation %q isn't one of the variations", def)
					}
					rules, err := fsRuleJSON(c)
					if err != nil {
						return nil, err
					}
					if rules == nil {
						rules = []any{}
					}
					dis, _ := c.Flags().GetBool("disabled")
					desc, _ := c.Flags().GetString("description")
					return map[string]any{"key": args[1], "description": desc, "enabled": !dis, "default_variation": def, "variations": variations, "rules": rules}, nil
				},
				Data: true, Done: "Created flag", Product: "Flagship"},
			platSpec{Use: "update <app-id> <key>", Short: "Replace a flag's definition (--data, the full flag)", Method: "PUT", Path: fsBase + "/{app_id}/flags/{flag_key}",
				Data: true, Done: "Updated flag", Product: "Flagship"},
			platSpec{Use: "delete <app-id> <key>", Short: "Delete a flag", Aliases: []string{"rm"}, Method: "DELETE", Path: fsBase + "/{app_id}/flags/{flag_key}",
				Confirm: "delete flag %s", Done: "Deleted flag", Product: "Flagship"},
			platSpec{Use: "changelog <app-id> <key>", Short: "Show a flag's change history", Aliases: []string{"history"}, Path: fsBase + "/{app_id}/flags/{flag_key}/changelog", List: true,
				Cols:  []platCol{{H: "when", Path: "created_at|timestamp"}, {H: "actor", Path: "actor.email|actor|user"}, {H: "action", Path: "action|type"}, {H: "summary", Path: "summary|description", W: 70}},
				Title: "🚩 %d changes", Product: "Flagship"},
			platSpec{Use: "evaluate <app-id> <key>", Short: "Evaluate a flag for a context", Aliases: []string{"eval"}, Path: fsBase + "/{app_id}/evaluate", ExtraArgs: 1,
				Flags: func(c *cobra.Command) {
					c.Flags().StringArray("context", nil, "Context attribute name=value (repeatable)")
				},
				Query: func(c *cobra.Command, args []string, q url.Values) error {
					ctxs, _ := c.Flags().GetStringArray("context")
					for _, e := range ctxs {
						k, v, ok := strings.Cut(e, "=")
						if !ok || strings.TrimSpace(k) == "" {
							return fmt.Errorf("invalid --context %q: want name=value", e)
						}
						q.Set(strings.TrimSpace(k), v)
					}
					q.Set("flagKey", args[1])
					return nil
				},
				Product: "Flagship"},
		),
			fsSimple("enable <app-id> <key>", "Enable a flag", func(_ *cobra.Command, _ []string, f map[string]any) error { f["enabled"] = true; return nil }, "Enabled %s", nil, 0),
			fsSimple("disable <app-id> <key>", "Disable a flag", func(_ *cobra.Command, _ []string, f map[string]any) error { f["enabled"] = false; return nil }, "Disabled %s", nil, 0),
			fsSimple("set <app-id> <key> <variation>", "Set the default variation", func(_ *cobra.Command, args []string, f map[string]any) error {
				if err := fsHasVariation(f, args[2]); err != nil {
					return err
				}
				f["default_variation"] = args[2]
				return nil
			}, "Updated default variation of %s", nil, 1),
			fsSimple("rollout <app-id> <key>", "Roll one variation out to a percentage of traffic (replaces rules)", fsRollout, "Updated rollout for %s", func(c *cobra.Command) {
				c.Flags().String("to", "", "Variation to roll out (required)")
				c.Flags().Float64("percentage", -1, "Percentage of traffic (0-100; 0 clears the rollout)")
				c.Flags().String("by", "", "Context attribute for sticky bucketing (e.g. user_id)")
				c.Flags().String("from-variation", "", "Fallback variation for the rest (default: current default)")
			}, 0),
			fsSimple("split <app-id> <key>", "Split traffic across variations by weight (replaces rules)", fsSplit, "Updated split for %s", func(c *cobra.Command) {
				c.Flags().StringArrayP("weight", "w", nil, "variation=weight (repeatable)")
				c.Flags().String("by", "", "Context attribute for sticky bucketing (e.g. user_id)")
				c.Flags().String("default-variation", "", "Fallback variation when bucketing can't run")
			}, 0),
			fsSimple("rules <app-id> <key>", "Replace a flag's targeting rules (--rule-json, repeatable; none clears)", func(c *cobra.Command, _ []string, f map[string]any) error {
				rules, err := fsRuleJSON(c)
				if err != nil {
					return err
				}
				if rules == nil {
					rules = []any{}
				}
				f["rules"] = rules
				return nil
			}, "Updated rules for %s", func(c *cobra.Command) {
				c.Flags().StringArray("rule-json", nil, "Targeting rule as a JSON object (repeatable)")
			}, 0),
			fsPullCmd(),
		)...)

	fsCmd := platGroup("flagship", "Flagship feature flags: apps and flags", `Manage Flagship apps and feature flags.

  cfctl flagship apps list|get|create|update|delete
  cfctl flagship flags list|get|create|update|delete|changelog|evaluate <app-id> ...
  cfctl flagship flags enable|disable|set|rollout|split|rules <app-id> <key>
  cfctl flagship flags pull <app-id> [-o flags.json]`, nil, apps, flags)
	rootCmd.AddCommand(fsCmd)
}

func fsRollout(c *cobra.Command, _ []string, f map[string]any) error {
	to, _ := c.Flags().GetString("to")
	pct, _ := c.Flags().GetFloat64("percentage")
	by, _ := c.Flags().GetString("by")
	if to == "" || pct < 0 || pct > 100 {
		return fmt.Errorf("--to and --percentage (0-100) are required")
	}
	if err := fsHasVariation(f, to); err != nil {
		return err
	}
	if pct == 0 {
		f["rules"] = []any{}
		return nil
	}
	fb, _ := c.Flags().GetString("from-variation")
	if fb == "" {
		fb = platStr(f["default_variation"])
	}
	if err := fsHasVariation(f, fb); err != nil {
		return err
	}
	f["default_variation"] = fb
	rollout := map[string]any{"percentage": pct}
	if by != "" {
		rollout["attribute"] = by
	}
	f["rules"] = []any{map[string]any{"priority": 1, "conditions": []any{}, "serve_variation": to, "rollout": rollout}}
	return nil
}

func fsSplit(c *cobra.Command, _ []string, f map[string]any) error {
	ws, _ := c.Flags().GetStringArray("weight")
	if len(ws) == 0 {
		return fmt.Errorf("at least one --weight variation=weight is required")
	}
	by, _ := c.Flags().GetString("by")
	type w struct {
		v string
		n float64
	}
	var weights []w
	total := 0.0
	for _, e := range ws {
		k, v, ok := strings.Cut(e, "=")
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if !ok || strings.TrimSpace(k) == "" || err != nil || n < 0 {
			return fmt.Errorf("invalid --weight %q: want variation=non-negative-number", e)
		}
		if err := fsHasVariation(f, strings.TrimSpace(k)); err != nil {
			return err
		}
		weights = append(weights, w{strings.TrimSpace(k), n})
		total += n
	}
	if total == 0 {
		return fmt.Errorf("weights add up to 0")
	}
	var rules []any
	cum := 0.0
	for _, x := range weights {
		if x.n == 0 {
			continue
		}
		cum += x.n / total * 100
		r := map[string]any{"percentage": math.Min(100, math.Round(cum*1e6)/1e6)}
		if by != "" {
			r["attribute"] = by
		}
		rules = append(rules, map[string]any{"priority": len(rules) + 1, "conditions": []any{}, "serve_variation": x.v, "rollout": r})
	}
	f["rules"] = rules
	if d, _ := c.Flags().GetString("default-variation"); d != "" {
		if err := fsHasVariation(f, d); err != nil {
			return err
		}
		f["default_variation"] = d
	}
	return nil
}

func fsPrintFlag(_ *cobra.Command, args []string, raw json.RawMessage) error {
	var f map[string]any
	if err := platDecode(raw, &f); err != nil {
		return err
	}
	platDetail("🚩 "+args[1], f, []platCol{{H: "Key", Path: "key"}, {H: "Enabled", Path: "enabled"}, {H: "Default", Path: "default_variation"},
		{H: "Description", Path: "description"}, {H: "Updated", Path: "updated_at"}})
	if vars, ok := f["variations"].(map[string]any); ok {
		names := make([]string, 0, len(vars))
		for k := range vars {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Println("  " + ui.AccentStyle.Render("variations"))
		for _, n := range names {
			b, _ := json.Marshal(vars[n])
			fmt.Printf("    %s = %s\n", n, b)
		}
	}
	if rules, ok := f["rules"].([]any); ok && len(rules) > 0 {
		fmt.Println("  " + ui.AccentStyle.Render("rules"))
		for _, r := range rules {
			b, _ := json.Marshal(r)
			fmt.Println("    " + string(b))
		}
	}
	return nil
}

// fsPullCmd writes every flag of an app to a local JSON file. (wrangler
// imports into its miniflare flag store; cfctl writes a portable file.)
func fsPullCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "pull <app-id>", Short: "Download every flag of an app to a local JSON file",
		Long: `Download all flags of a Flagship app (key, description, enabled, default
variation, variations, rules) into a JSON file, e.g. for local development or
review in git. wrangler imports into its local (miniflare) flag store instead;
this file is the portable equivalent.`,
		Path: fsBase + "/{app_id}/flags",
		Flags: func(c *cobra.Command) {
			c.Flags().StringP("output", "o", "flagship-flags.json", "File to write (- for stdout)")
		},
		Run: func(c *cobra.Command, args []string) error {
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			path, _, err := platFill(ctx, s, fsBase+"/{app_id}/flags", args)
			if err != nil {
				return err
			}
			raw, err := platAll(ctx, s, path, nil)
			if err != nil {
				return platErr("Flagship", err)
			}
			var flags []map[string]any
			if err := platDecode(raw, &flags); err != nil {
				return err
			}
			out := make([]map[string]any, 0, len(flags))
			keys := make([]string, 0, len(flags))
			for _, f := range flags {
				in := map[string]any{}
				for _, k := range []string{"key", "description", "enabled", "default_variation", "variations", "rules"} {
					if v, ok := f[k]; ok {
						in[k] = v
					}
				}
				out = append(out, in)
				keys = append(keys, platStr(f["key"]))
			}
			doc := map[string]any{"app_id": args[0], "pulled_at": time.Now().UTC().Format(time.RFC3339), "flags": out}
			b, _ := json.MarshalIndent(doc, "", "  ")
			dest, _ := c.Flags().GetString("output")
			if dest == "-" {
				fmt.Println(string(b))
				return nil
			}
			if err := os.WriteFile(dest, append(b, '\n'), 0o644); err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(map[string]any{"app_id": args[0], "path": dest, "pulled": keys})
			}
			fmt.Println(ui.Success(fmt.Sprintf("Pulled %d flags from %s into %s", len(out), args[0], dest)))
			return nil
		},
	})
}
