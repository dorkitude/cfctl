package cmd

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Worker Previews (open beta): named, long-lived preview environments per
// Worker, each with its own deployments and secrets.

func wkWorkerPath(worker, suffix string) string {
	return "/accounts/{account_id}/workers/workers/" + url.PathEscape(worker) + suffix
}

func wkPreviewPath(worker, preview, suffix string) string {
	return wkWorkerPath(worker, "/previews/"+url.PathEscape(preview)+suffix)
}

// wkPreviewName returns --preview, or the current git branch.
func wkPreviewName(cmd *cobra.Command) (string, error) {
	if p, _ := cmd.Flags().GetString("preview"); p != "" {
		return p, nil
	}
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if b := strings.TrimSpace(string(out)); err == nil && b != "" && b != "HEAD" {
		return b, nil
	}
	return "", fmt.Errorf("which preview? pass --preview <name> (defaults to the current git branch)")
}

func wkAddPreviewFlags(c *cobra.Command) {
	wkAddScriptFlags(c)
	c.Flags().String("preview", "", "Preview name, slug, or ID (default: current git branch)")
}

func newPreviewCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "preview",
		Aliases: []string{"previews"},
		Short:   "Manage Worker Previews (open beta)",
		Long: `Worker Previews are named preview environments of a Worker (one per
branch, say), each with its own URL, deployments, and secrets.

Examples:
  cfctl workers preview deploy --preview feature-x        # upload + deploy to a preview
  cfctl workers preview list --name my-worker
  cfctl workers preview get --preview feature-x
  cfctl workers preview deployments --preview feature-x
  cfctl workers preview secret put API_KEY --preview feature-x
  cfctl workers preview base-config secret list
  cfctl workers preview delete --preview feature-x`,
	}

	deploy := &cobra.Command{
		Use:   "deploy [script]",
		Short: "Upload code to a Preview (created if needed) and deploy it",
		Long: `Upload an ES module Worker (plus bindings and assets, same flags and
wrangler config as 'deploy') as a new deployment of a Preview. The Preview
is created if it doesn't exist.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := wkPrepareUpload(cmd, args)
			if err != nil {
				return err
			}
			defer p.Close()
			preview, err := wkPreviewName(cmd)
			if err != nil {
				return err
			}
			if _, ok := p.Metadata["body_part"]; ok {
				return fmt.Errorf("previews need an ES module Worker (service-worker scripts aren't supported)")
			}
			body := map[string]any{}
			for _, k := range []string{"main_module", "compatibility_date", "compatibility_flags", "bindings", "annotations", "limits", "placement", "usage_model"} {
				if v, ok := p.Metadata[k]; ok {
					body[k] = v
				}
			}
			var mods []map[string]string
			for _, m := range p.Modules {
				mods = append(mods, map[string]string{"name": m.Name, "content_type": m.ContentType, "content_base64": base64.StdEncoding.EncodeToString(m.Data)})
			}
			if mods != nil {
				body["modules"] = mods
			}
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				for _, m := range mods {
					m["content_base64"] = fmt.Sprintf("<%d bytes>", len(m["content_base64"]))
				}
				return wkPrintDryRun(&uploadPlan{Name: p.Name + " (preview " + preview + ")", Metadata: body, Modules: p.Modules, Assets: p.Assets})
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var pv map[string]any
			_, err = wkCall(ctx, s, "GET", wkPreviewPath(p.Name, preview, ""), nil, nil, &pv)
			var apiE *api.Error
			if errors.As(err, &apiE) && apiE.Status == http.StatusNotFound {
				if _, err := wkCall(ctx, s, "POST", wkWorkerPath(p.Name, "/previews"), nil, map[string]any{"name": preview}, &pv); err != nil {
					return fmt.Errorf("failed to create preview %s: %w", preview, err)
				}
				if !jsonOutput {
					fmt.Println(ui.Success("Created preview " + preview))
				}
			} else if err != nil {
				return fmt.Errorf("failed to get preview %s: %w", preview, err)
			}
			if p.Assets != nil {
				jwt, err := wkUploadAssets(ctx, s, wkScriptPath(p.Name, "/assets-upload-session"), p.Assets)
				if err != nil {
					return err
				}
				body["assets"] = map[string]any{"jwt": jwt, "config": wkAssetsConfig(p)}
			}
			pid := wkStr(pv, "id")
			if pid == "" {
				pid = preview
			}
			var dep map[string]any
			if _, err := wkCall(ctx, s, "POST", wkPreviewPath(p.Name, pid, "/deployments"), nil, body, &dep); err != nil {
				return fmt.Errorf("failed to deploy preview %s: %w", preview, err)
			}
			if jsonOutput {
				return printJSONValue(map[string]any{"preview": pv, "deployment": dep})
			}
			fmt.Println(ui.Success(fmt.Sprintf("Deployed %s to preview %s (%s)", p.Name, preview, wkTotalSize(p.Modules))))
			if urls, ok := pv["urls"].([]any); ok {
				for _, u := range urls {
					wkKV("URL", u)
				}
			}
			if urls, ok := dep["urls"].([]any); ok {
				for _, u := range urls {
					wkKV("Deployment URL", u)
				}
			}
			return nil
		},
	}
	wkAddUploadFlags(deploy)
	deploy.Flags().String("preview", "", "Preview name (default: current git branch)")

	list := &cobra.Command{
		Use: "list [name]", Aliases: []string{"ls"}, Short: "List a Worker's Previews", Args: cobra.MaximumNArgs(1),
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
			p, err := s.fillPath(ctx, wkWorkerPath(name, "/previews"), nil)
			if err != nil {
				return err
			}
			res, err := s.c.All(ctx, api.Request{Method: "GET", Path: p}, 0)
			if err != nil {
				return fmt.Errorf("failed to list previews of %s: %w", name, err)
			}
			var pvs []map[string]any
			_ = json.Unmarshal(res.Result, &pvs)
			if jsonOutput {
				if pvs == nil {
					pvs = []map[string]any{}
				}
				return printJSONValue(pvs)
			}
			if len(pvs) == 0 {
				fmt.Println(ui.Warn("No previews for " + name))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("👀 %d previews of %s", len(pvs), name)))
			for _, pv := range pvs {
				u := ""
				if urls, ok := pv["urls"].([]any); ok && len(urls) > 0 {
					u = fmt.Sprint(urls[0])
				}
				fmt.Printf("  %-28s %-20s %s\n", ui.AccentStyle.Render(wkStr(pv, "name")), wkTime(wkStr(pv, "updated_on")), ui.SubtleStyle.Render(u))
			}
			return nil
		},
	}
	wkAddScriptFlags(list)

	get := &cobra.Command{
		Use: "get", Short: "Show a Preview", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, preview, s, err := wkPreviewTarget(cmd)
			if err != nil {
				return err
			}
			var pv map[string]any
			if _, err := wkCall(cmd.Context(), s, "GET", wkPreviewPath(name, preview, ""), nil, nil, &pv); err != nil {
				return fmt.Errorf("failed to get preview %s: %w", preview, err)
			}
			if jsonOutput {
				return printJSONValue(pv)
			}
			fmt.Println(ui.TitleStyle.Render("👀 " + name + " preview " + wkStr(pv, "name")))
			wkKV("ID", wkStr(pv, "id"))
			wkKV("Slug", wkStr(pv, "slug"))
			wkKV("Created", wkTime(wkStr(pv, "created_on")))
			wkKV("Updated", wkTime(wkStr(pv, "updated_on")))
			if urls, ok := pv["urls"].([]any); ok {
				for _, u := range urls {
					wkKV("URL", u)
				}
			}
			return nil
		},
	}
	wkAddPreviewFlags(get)

	del := &cobra.Command{
		Use: "delete", Aliases: []string{"rm"}, Short: "Delete a Preview and all its deployments", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, preview, s, err := wkPreviewTarget(cmd)
			if err != nil {
				return err
			}
			if err := confirm(cmd, "delete preview "+preview+" of "+name+" and all its deployments"); err != nil {
				return err
			}
			if _, err := wkCall(cmd.Context(), s, "DELETE", wkPreviewPath(name, preview, ""), nil, nil, nil); err != nil {
				return fmt.Errorf("failed to delete preview %s: %w", preview, err)
			}
			if printJSON(map[string]any{"worker": name, "preview": preview, "deleted": true}) {
				return nil
			}
			fmt.Println(ui.Success("Deleted preview " + preview))
			return nil
		},
	}
	wkAddPreviewFlags(del)
	wkAddYesFlag(del)

	deps := &cobra.Command{
		Use: "deployments", Short: "List a Preview's deployments", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, preview, s, err := wkPreviewTarget(cmd)
			if err != nil {
				return err
			}
			p, err := s.fillPath(ctx, wkPreviewPath(name, preview, "/deployments"), nil)
			if err != nil {
				return err
			}
			res, err := s.c.All(ctx, api.Request{Method: "GET", Path: p}, 0)
			if err != nil {
				return fmt.Errorf("failed to list deployments of preview %s: %w", preview, err)
			}
			var ds []map[string]any
			_ = json.Unmarshal(res.Result, &ds)
			if jsonOutput {
				if ds == nil {
					ds = []map[string]any{}
				}
				return printJSONValue(ds)
			}
			if len(ds) == 0 {
				fmt.Println(ui.Warn("No deployments on preview " + preview))
				return nil
			}
			for _, d := range ds {
				fmt.Printf("  %-36s %-20s %s\n", ui.AccentStyle.Render(wkStr(d, "id")), wkTime(wkStr(d, "created_on")), ui.SubtleStyle.Render(wkStr(d, "annotations", "workers/message")))
			}
			return nil
		},
	}
	wkAddPreviewFlags(deps)

	c.AddCommand(deploy, list, get, del, deps, newPreviewSecretCmd(false), newPreviewBaseConfigCmd())
	return c
}

func wkPreviewTarget(cmd *cobra.Command) (string, string, *apiSession, error) {
	name, err := wkScriptName(cmd, nil)
	if err != nil {
		return "", "", nil, err
	}
	preview, err := wkPreviewName(cmd)
	if err != nil {
		return "", "", nil, err
	}
	s, err := newAPISession(cmd)
	return name, preview, s, err
}

// secretsFromEnv picks secret bindings out of an env map or bindings list.
func wkSecretNames(v any) []map[string]any {
	out := []map[string]any{}
	switch t := v.(type) {
	case map[string]any:
		for _, k := range wkSortedKeys(t) {
			if b, ok := t[k].(map[string]any); ok && strings.HasPrefix(fmt.Sprint(b["type"]), "secret_") {
				out = append(out, map[string]any{"name": k, "type": b["type"]})
			}
		}
	case []any:
		for _, x := range t {
			if b, ok := x.(map[string]any); ok && strings.HasPrefix(fmt.Sprint(b["type"]), "secret_") {
				out = append(out, map[string]any{"name": b["name"], "type": b["type"]})
			}
		}
		sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]["name"]) < fmt.Sprint(out[j]["name"]) })
	}
	return out
}

// newPreviewSecretCmd manages secrets on a Preview (base=false: patches the
// latest preview deployment's env) or on the Worker's preview base config
// (base=true: patches previews_base_config.env).
func newPreviewSecretCmd(base bool) *cobra.Command {
	where := "a Preview (creates a new preview deployment)"
	if base {
		where = "the Preview base config (used for new Previews)"
	}
	c := &cobra.Command{
		Use:   "secret",
		Short: "Manage secrets on " + where,
	}
	target := func(cmd *cobra.Command) (string, string, *apiSession, error) {
		if base {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return "", "", nil, err
			}
			s, err := newAPISession(cmd)
			return name, "", s, err
		}
		return wkPreviewTarget(cmd)
	}
	apply := func(cmd *cobra.Command, name, preview string, s *apiSession, env map[string]any) error {
		if base {
			_, err := wkCall(cmd.Context(), s, "PATCH", wkWorkerPath(name, ""), nil, map[string]any{"previews_base_config": map[string]any{"env": env}}, nil)
			return err
		}
		_, err := wkCall(cmd.Context(), s, "PATCH", wkPreviewPath(name, preview, "/deployments/latest"), nil, map[string]any{"env": env}, nil)
		return err
	}
	addFlags := func(x *cobra.Command) {
		if base {
			wkAddScriptFlags(x)
		} else {
			wkAddPreviewFlags(x)
		}
	}
	label := func(name, preview string) string {
		if base {
			return name + " preview base config"
		}
		return name + " preview " + preview
	}

	put := &cobra.Command{
		Use: "put <KEY>", Short: "Create or update a secret (value from prompt or stdin)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, preview, s, err := target(cmd)
			if err != nil {
				return err
			}
			val, err := wkReadSecret(args[0])
			if err != nil {
				return err
			}
			if err := apply(cmd, name, preview, s, map[string]any{args[0]: map[string]string{"type": "secret_text", "text": val}}); err != nil {
				return fmt.Errorf("failed to set secret %s on %s: %w", args[0], label(name, preview), err)
			}
			if printJSON(map[string]any{"worker": name, "preview": preview, "secret": args[0], "updated": true}) {
				return nil
			}
			fmt.Println(ui.Success(fmt.Sprintf("Set secret %s on %s", args[0], label(name, preview))))
			return nil
		},
	}
	addFlags(put)
	del := &cobra.Command{
		Use: "delete <KEY>", Aliases: []string{"rm"}, Short: "Delete a secret", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, preview, s, err := target(cmd)
			if err != nil {
				return err
			}
			if err := confirm(cmd, fmt.Sprintf("delete secret %s from %s", args[0], label(name, preview))); err != nil {
				return err
			}
			if err := apply(cmd, name, preview, s, map[string]any{args[0]: nil}); err != nil {
				return fmt.Errorf("failed to delete secret %s from %s: %w", args[0], label(name, preview), err)
			}
			if printJSON(map[string]any{"worker": name, "preview": preview, "secret": args[0], "deleted": true}) {
				return nil
			}
			fmt.Println(ui.Success(fmt.Sprintf("Deleted secret %s from %s", args[0], label(name, preview))))
			return nil
		},
	}
	addFlags(del)
	wkAddYesFlag(del)
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List secret names (never values)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, preview, s, err := target(cmd)
			if err != nil {
				return err
			}
			var secs []map[string]any
			if base {
				var w map[string]any
				if _, err := wkCall(cmd.Context(), s, "GET", wkWorkerPath(name, ""), nil, nil, &w); err != nil {
					return fmt.Errorf("failed to get %s: %w", name, err)
				}
				var env any
				if bc, ok := w["previews_base_config"].(map[string]any); ok {
					env = bc["env"]
				}
				secs = wkSecretNames(env)
			} else {
				var d map[string]any
				if _, err := wkCall(cmd.Context(), s, "GET", wkPreviewPath(name, preview, "/deployments/latest"), nil, nil, &d); err != nil {
					return fmt.Errorf("failed to get the latest deployment of preview %s: %w", preview, err)
				}
				secs = wkSecretNames(d["env"])
				if len(secs) == 0 {
					secs = wkSecretNames(d["bindings"])
				}
			}
			if jsonOutput {
				return printJSONValue(secs)
			}
			if len(secs) == 0 {
				fmt.Println(ui.Warn("No secrets on " + label(name, preview)))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🤫 %d secrets on %s", len(secs), label(name, preview))))
			for _, sec := range secs {
				fmt.Printf("  %-32s %s\n", ui.AccentStyle.Render(fmt.Sprint(sec["name"])), ui.SubtleStyle.Render(fmt.Sprint(sec["type"])))
			}
			return nil
		},
	}
	addFlags(list)
	bulk := &cobra.Command{
		Use: "bulk [file|-]", Short: "Set (or delete, with null) many secrets at once", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, preview, s, err := target(cmd)
			if err != nil {
				return err
			}
			secrets, err := wkReadSecretsInput(args)
			if err != nil {
				return err
			}
			patch, set, dels := wkSecretsPatch(secrets)
			for k, v := range patch {
				if m, ok := v.(map[string]string); ok {
					delete(m, "name") // env entries are keyed by name
					patch[k] = m
				}
			}
			if len(dels) > 0 {
				if err := confirm(cmd, fmt.Sprintf("delete %d secrets (%s) from %s", len(dels), strings.Join(dels, ", "), label(name, preview))); err != nil {
					return err
				}
			}
			if err := apply(cmd, name, preview, s, patch); err != nil {
				return fmt.Errorf("failed to update secrets on %s: %w", label(name, preview), err)
			}
			if printJSON(map[string]any{"worker": name, "preview": preview, "set": wkNonNil(set), "deleted": wkNonNil(dels)}) {
				return nil
			}
			fmt.Println(ui.Success(fmt.Sprintf("Updated %d secrets on %s", len(set)+len(dels), label(name, preview))))
			return nil
		},
	}
	addFlags(bulk)
	wkAddYesFlag(bulk)
	c.AddCommand(put, del, list, bulk)
	return c
}

func newPreviewBaseConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "base-config",
		Short: "Manage the Preview base config shared by new Previews",
		Long: `Examples:
  cfctl workers preview base-config secret put API_KEY --name my-worker
  cfctl workers preview base-config secret list --name my-worker`,
	}
	c.AddCommand(newPreviewSecretCmd(true))
	return c
}

func init() {
	workersCmd.AddCommand(newPreviewCmd())
	rootCmd.AddCommand(newPreviewCmd())
}
