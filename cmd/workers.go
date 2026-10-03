package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/dorkitude/cfctl/internal/workers"
	"github.com/spf13/cobra"
)

// Workers core: scripts, deploys, versions, deployments, secrets, triggers,
// tail, dispatch namespaces, previews. Everything goes through the raw client
// (newAPISession), so the read-only guard applies.

var workersCmd = &cobra.Command{
	Use:     "workers",
	Aliases: []string{"worker"},
	Short:   "Manage Workers: deploy, versions, secrets, triggers, tail",
	Long: `Manage Cloudflare Workers, the wrangler way.

  list | get | delete            Worker scripts
  deploy                         Upload a built script (+ bindings, assets) and deploy it
  versions                       list | view | upload | deploy (gradual % splits)
  deployments                    list | status
  rollback                       Roll back to an earlier version
  secret                         put | list | delete | bulk
  triggers                       list | deploy (crons, routes, custom domains)
  crons | routes | domains       Individual trigger types
  subdomain | dev-url            workers.dev subdomain and per-Worker workers.dev URL
  tail                           Live logs over a WebSocket
  logs                           Query Workers Logs (observability)
  dispatch-namespace             Workers for Platforms namespaces
  preview                        Worker Previews (open beta)

Top-level shortcuts: cfctl deploy | tail | rollback | secret | versions |
deployments | triggers | dispatch-namespace.

Every command reads the Worker name from an argument, --name, or the
wrangler.toml / wrangler.json(c) in the current directory (--config, --env).
The generated equivalents live under 'cfctl api worker-script', 'cfctl api
worker-versions', 'cfctl api worker-deployments', etc.`,
}

// ---- shared helpers -------------------------------------------------------

// addScriptFlags adds --name / --config / --env, used to find the Worker.
func wkAddScriptFlags(c *cobra.Command) {
	c.Flags().String("name", "", "Worker name (default: from wrangler config)")
	c.Flags().StringP("config", "c", "", "Path to wrangler.toml / wrangler.json(c) (default: found in the current directory)")
	c.Flags().StringP("env", "e", "", "Wrangler environment ([env.<name>] section)")
}

func wkAddYesFlag(c *cobra.Command) {
	c.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
}

// wranglerConfig loads --config, or the wrangler config in the current
// directory. It returns nil (no error) if there is none and none was asked for.
func wkConfig(cmd *cobra.Command) (*workers.Config, error) {
	path, _ := cmd.Flags().GetString("config")
	env, _ := cmd.Flags().GetString("env")
	if path == "" {
		path = workers.FindConfig(".")
		if path == "" {
			if env != "" {
				return nil, fmt.Errorf("--env %s given but no wrangler config found; pass --config", env)
			}
			return nil, nil
		}
	}
	return workers.LoadConfig(path, env)
}

// scriptName resolves the Worker: args[0] if present, else --name, else the
// wrangler config's name.
func wkScriptName(cmd *cobra.Command, args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		return args[0], nil
	}
	if n, _ := cmd.Flags().GetString("name"); n != "" {
		return n, nil
	}
	cfg, err := wkConfig(cmd)
	if err != nil {
		return "", err
	}
	if cfg != nil && cfg.Name != "" {
		return cfg.Name, nil
	}
	return "", fmt.Errorf("which Worker? pass its name, --name, or run in a directory with a wrangler config")
}

// wcall sends one request with {account_id}/{zone_id} filled in, and decodes
// the envelope's result into out (if non-nil).
func wkCall(ctx context.Context, s *apiSession, method, path string, q url.Values, body any, out any) (*api.Response, error) {
	p, err := s.fillPath(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	req := api.Request{Method: method, Path: p, Query: q}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req.Body, req.ContentType = b, "application/json"
	}
	resp, err := s.c.Do(ctx, req)
	if err != nil {
		return resp, err
	}
	if out != nil && resp.Envelope != nil && len(resp.Envelope.Result) > 0 {
		if err := json.Unmarshal(resp.Envelope.Result, out); err != nil {
			return resp, fmt.Errorf("unexpected response from %s %s: %w", method, path, err)
		}
	}
	return resp, nil
}

// scriptPath returns /accounts/{account_id}/workers/scripts/<name><suffix>.
func wkScriptPath(name, suffix string) string {
	return "/accounts/{account_id}/workers/scripts/" + url.PathEscape(name) + suffix
}

func wkKV(label string, v any) {
	fmt.Printf("  %-18s %v\n", label+":", v)
}

func wkTime(s string) string {
	if len(s) >= 19 {
		return strings.Replace(s[:19], "T", " ", 1)
	}
	return s
}

// ---- workers list / get / delete -----------------------------------------

type wkScript struct {
	ID                 string   `json:"id"`
	Tag                string   `json:"tag,omitempty"`
	CreatedOn          string   `json:"created_on,omitempty"`
	ModifiedOn         string   `json:"modified_on,omitempty"`
	Handlers           []string `json:"handlers,omitempty"`
	CompatibilityDate  string   `json:"compatibility_date,omitempty"`
	CompatibilityFlags []string `json:"compatibility_flags,omitempty"`
	UsageModel         string   `json:"usage_model,omitempty"`
	LastDeployedFrom   string   `json:"last_deployed_from,omitempty"`
	HasAssets          bool     `json:"has_assets"`
	HasModules         bool     `json:"has_modules"`
	MigrationTag       string   `json:"migration_tag,omitempty"`
	Logpush            bool     `json:"logpush"`
}

func newWorkersListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List Worker scripts in the account",
		Long: `List every Worker script in the account.

Examples:
  cfctl workers list
  cfctl workers list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			p, err := s.fillPath(ctx, "/accounts/{account_id}/workers/scripts", nil)
			if err != nil {
				return err
			}
			res, err := s.c.All(ctx, api.Request{Method: "GET", Path: p}, 0)
			if err != nil {
				return err
			}
			var list []json.RawMessage
			_ = json.Unmarshal(res.Result, &list)
			if jsonOutput {
				if list == nil {
					list = []json.RawMessage{}
				}
				return printJSONValue(list)
			}
			if len(list) == 0 {
				fmt.Println(ui.Warn("No Workers in this account"))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("⚙️  %d Workers", len(list))))
			fmt.Printf("  %-32s %-20s %-12s %-14s %s\n", "NAME", "MODIFIED", "COMPAT", "HANDLERS", "FROM")
			for _, raw := range list {
				var w wkScript
				_ = json.Unmarshal(raw, &w)
				fmt.Printf("  %-32s %-20s %-12s %-14s %s\n", ui.AccentStyle.Render(truncate(w.ID, 32)), wkTime(w.ModifiedOn), w.CompatibilityDate, strings.Join(w.Handlers, ","), w.LastDeployedFrom)
			}
			return nil
		},
	}
	return c
}

func newWorkersGetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "get [name]",
		Aliases: []string{"info", "show"},
		Short:   "Show a Worker: settings, bindings, workers.dev URL, crons",
		Long: `Show a Worker's settings and bindings (secret values are never returned
by the API), its workers.dev URL, cron triggers, and current deployment.

Examples:
  cfctl workers get my-worker
  cfctl workers get --json            # name from ./wrangler.toml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var worker map[string]any
			if _, err := wkCall(ctx, s, "GET", "/accounts/{account_id}/workers/workers/"+url.PathEscape(name), nil, nil, &worker); err != nil {
				return fmt.Errorf("failed to get Worker %q: %w", name, err)
			}
			var settings map[string]any
			if _, err := wkCall(ctx, s, "GET", wkScriptPath(name, "/settings"), nil, nil, &settings); err != nil {
				return err
			}
			var sched struct {
				Schedules []map[string]any `json:"schedules"`
			}
			_, _ = wkCall(ctx, s, "GET", wkScriptPath(name, "/schedules"), nil, nil, &sched)
			var deps struct {
				Deployments []wkDeployment `json:"deployments"`
			}
			_, _ = wkCall(ctx, s, "GET", wkScriptPath(name, "/deployments"), nil, nil, &deps)
			out := map[string]any{"worker": worker, "settings": settings, "schedules": sched.Schedules}
			if len(deps.Deployments) > 0 {
				out["deployment"] = deps.Deployments[0]
			}
			if jsonOutput {
				return printJSONValue(out)
			}
			fmt.Println(ui.TitleStyle.Render("⚙️  " + name))
			wkKV("ID", worker["id"])
			wkKV("Created", wkTime(fmt.Sprint(worker["created_on"])))
			wkKV("Updated", wkTime(fmt.Sprint(worker["updated_on"])))
			if d, ok := worker["deployed_on"].(string); ok {
				wkKV("Deployed", wkTime(d))
			}
			wkKV("Compat date", settings["compatibility_date"])
			if f, ok := settings["compatibility_flags"].([]any); ok && len(f) > 0 {
				wkKV("Compat flags", wkJoin(f))
			}
			wkKV("Usage model", settings["usage_model"])
			if sd, ok := worker["subdomain"].(map[string]any); ok {
				state := "disabled"
				if sd["enabled"] == true {
					state = fmt.Sprint(sd["url"])
				}
				wkKV("workers.dev", state)
			}
			if len(sched.Schedules) > 0 {
				var cs []string
				for _, sc := range sched.Schedules {
					cs = append(cs, fmt.Sprint(sc["cron"]))
				}
				wkKV("Crons", strings.Join(cs, ", "))
			}
			if len(deps.Deployments) > 0 {
				wkKV("Deployment", deps.Deployments[0].Summary())
			}
			if bs, ok := settings["bindings"].([]any); ok && len(bs) > 0 {
				fmt.Println()
				fmt.Println(ui.SubtleStyle.Render("  Bindings:"))
				wkPrintBindings(bs)
			}
			return nil
		},
	}
	wkAddScriptFlags(c)
	return c
}

func wkJoin(xs []any) string {
	ss := make([]string, len(xs))
	for i, x := range xs {
		ss[i] = fmt.Sprint(x)
	}
	return strings.Join(ss, ", ")
}

// printBindings prints bindings as NAME TYPE DETAIL. Secret values are never
// in API responses; plain_text values are shown.
func wkPrintBindings(bs []any) {
	type row struct{ name, typ, detail string }
	var rows []row
	for _, b := range bs {
		m, _ := b.(map[string]any)
		r := row{name: fmt.Sprint(m["name"]), typ: fmt.Sprint(m["type"])}
		switch r.typ {
		case "plain_text":
			r.detail = truncate(fmt.Sprint(m["text"]), 60)
		case "secret_text", "secret_key":
			r.detail = "(secret)"
		case "kv_namespace":
			r.detail = fmt.Sprint(m["namespace_id"])
		case "r2_bucket":
			r.detail = fmt.Sprint(m["bucket_name"])
		case "d1":
			r.detail = fmt.Sprint(wkFirst(m["id"], m["database_id"]))
		case "service":
			r.detail = fmt.Sprint(m["service"])
		case "durable_object_namespace":
			r.detail = fmt.Sprint(m["class_name"])
		case "queue":
			r.detail = fmt.Sprint(m["queue_name"])
		case "json":
			j, _ := json.Marshal(m["json"])
			r.detail = truncate(string(j), 60)
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	for _, r := range rows {
		fmt.Printf("    %-24s %-26s %s\n", ui.AccentStyle.Render(r.name), r.typ, ui.SubtleStyle.Render(r.detail))
	}
}

func wkFirst(xs ...any) any {
	for _, x := range xs {
		if x != nil {
			return x
		}
	}
	return ""
}

func newWorkersDeleteCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "delete [name]",
		Aliases: []string{"rm"},
		Short:   "Delete a Worker",
		Long: `Delete a Worker script and everything attached to it (versions,
deployments, secrets, routes, custom domains, cron triggers).

Examples:
  cfctl workers delete my-worker            # asks for confirmation
  cfctl workers delete my-worker --yes
  cfctl workers delete my-worker --force    # even if other Workers bind to it
  cfctl workers delete my-worker --dry-run`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				fmt.Fprintln(os.Stderr, ui.Info("dry run: would DELETE Worker "+name))
				return nil
			}
			if err := confirm(cmd, "delete Worker "+name); err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			q := url.Values{}
			if f, _ := cmd.Flags().GetBool("force"); f {
				q.Set("force", "true")
			}
			if _, err := wkCall(ctx, s, "DELETE", wkScriptPath(name, ""), q, nil, nil); err != nil {
				return fmt.Errorf("failed to delete Worker %q: %w", name, err)
			}
			if printJSON(map[string]any{"name": name, "deleted": true}) {
				return nil
			}
			fmt.Println(ui.Success("Deleted Worker " + name))
			return nil
		},
	}
	wkAddScriptFlags(c)
	wkAddYesFlag(c)
	c.Flags().Bool("force", false, "Delete even if other Workers still reference this one")
	c.Flags().Bool("dry-run", false, "Show what would be deleted without deleting")
	return c
}

func init() {
	workersCmd.AddCommand(newWorkersListCmd(), newWorkersGetCmd(), newWorkersDeleteCmd())
	rootCmd.AddCommand(workersCmd)
}
