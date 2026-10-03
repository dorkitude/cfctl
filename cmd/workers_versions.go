package cmd

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// wkDeployment is a Worker deployment (a traffic split over versions).
type wkDeployment struct {
	ID          string            `json:"id"`
	Source      string            `json:"source,omitempty"`
	Strategy    string            `json:"strategy,omitempty"`
	AuthorEmail string            `json:"author_email,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Versions    []wkSplit         `json:"versions"`
	CreatedOn   string            `json:"created_on,omitempty"`
}

type wkSplit struct {
	VersionID  string  `json:"version_id"`
	Percentage float64 `json:"percentage"`
}

func (d wkDeployment) Summary() string {
	var parts []string
	for _, v := range d.Versions {
		parts = append(parts, fmt.Sprintf("%s%% %s", strconv.FormatFloat(v.Percentage, 'f', -1, 64), v.VersionID))
	}
	return strings.Join(parts, ", ")
}

// wkVersion is a Worker version (list/view shape).
type wkVersion struct {
	ID       string `json:"id"`
	Number   int    `json:"number"`
	Metadata struct {
		CreatedOn   string `json:"created_on"`
		Source      string `json:"source"`
		AuthorEmail string `json:"author_email"`
		HasPreview  bool   `json:"has_preview"`
	} `json:"metadata"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Resources   map[string]any    `json:"resources,omitempty"`
}

// wkListVersions pages through versions (newest first). limit 0 = all.
func wkListVersions(ctx context.Context, s *apiSession, name string, limit int) ([]map[string]any, error) {
	var out []map[string]any
	per := 100
	if limit > 0 && limit < per {
		per = limit
	}
	for page := 1; page <= 1000; page++ {
		var res struct {
			Items []map[string]any `json:"items"`
		}
		resp, err := wkCall(ctx, s, "GET", wkScriptPath(name, "/versions"), url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(per)}}, nil, &res)
		if err != nil {
			return nil, fmt.Errorf("failed to list versions of %s: %w", name, err)
		}
		out = append(out, res.Items...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
		total := 0
		if resp.Envelope != nil && resp.Envelope.ResultInfo != nil {
			total = int(resp.Envelope.ResultInfo.TotalCount)
		}
		if len(res.Items) == 0 || (total > 0 && len(out) >= total) || (total == 0 && len(res.Items) < per) {
			break
		}
	}
	return out, nil
}

func wkListDeployments(ctx context.Context, s *apiSession, name string) ([]wkDeployment, error) {
	var res struct {
		Deployments []wkDeployment `json:"deployments"`
	}
	if _, err := wkCall(ctx, s, "GET", wkScriptPath(name, "/deployments"), nil, nil, &res); err != nil {
		return nil, fmt.Errorf("failed to list deployments of %s: %w", name, err)
	}
	return res.Deployments, nil
}

func wkStr(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	if cur == nil {
		return ""
	}
	return fmt.Sprint(cur)
}

// ---- versions -------------------------------------------------------------

func newVersionsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "versions",
		Aliases: []string{"version"},
		Short:   "List, view, upload, and deploy Worker versions",
		Long: `Worker versions: upload code without deploying it, then deploy one
version or split traffic between two (gradual rollouts).

Examples:
  cfctl workers versions list my-worker
  cfctl workers versions view 8fa8b1b6 --name my-worker
  cfctl workers versions upload --message "fix login" --tag v1.2.3
  cfctl workers versions deploy 8fa8b1b6@10 7005f4a4@90 --name my-worker`,
	}
	c.AddCommand(newVersionsListCmd(), newVersionsViewCmd(), newVersionsUploadCmd(), newVersionsDeployCmd(), newVersionsSecretCmd())
	return c
}

func newVersionsListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list [name]",
		Short: "List a Worker's versions (newest first)",
		Long: `List a Worker's versions, newest first.

Examples:
  cfctl workers versions list my-worker
  cfctl workers versions list --limit 0 --json    # all of them`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			limit, _ := cmd.Flags().GetInt("limit")
			vs, err := wkListVersions(cmd.Context(), s, name, limit)
			if err != nil {
				return err
			}
			if jsonOutput {
				if vs == nil {
					vs = []map[string]any{}
				}
				return printJSONValue(vs)
			}
			if len(vs) == 0 {
				fmt.Println(ui.Warn("No versions"))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🫧 %d versions of %s", len(vs), name)))
			fmt.Printf("  %-36s %-4s %-20s %-10s %-24s %s\n", "VERSION ID", "#", "CREATED", "SOURCE", "AUTHOR", "MESSAGE / TAG")
			for _, v := range vs {
				note := wkStr(v, "annotations", "workers/message")
				if t := wkStr(v, "annotations", "workers/tag"); t != "" {
					note = strings.TrimSpace(note + " [" + t + "]")
				}
				if note == "" {
					note = ui.SubtleStyle.Render(wkStr(v, "annotations", "workers/triggered_by"))
				}
				fmt.Printf("  %-36s %-4s %-20s %-10s %-24s %s\n", ui.AccentStyle.Render(wkStr(v, "id")), wkStr(v, "number"), wkTime(wkStr(v, "metadata", "created_on")), wkStr(v, "metadata", "source"), truncate(wkStr(v, "metadata", "author_email"), 24), truncate(note, 60))
			}
			return nil
		},
	}
	wkAddScriptFlags(c)
	c.Flags().Int("limit", 10, "How many versions to show (0 = all)")
	return c
}

// wkResolveVersion turns "latest", a full ID, or a unique ID prefix into a
// version ID.
func wkResolveVersion(ctx context.Context, s *apiSession, name, ref string, cache *[]map[string]any) (string, error) {
	if len(ref) == 36 && strings.Count(ref, "-") == 4 {
		return ref, nil
	}
	if *cache == nil {
		vs, err := wkListVersions(ctx, s, name, 0)
		if err != nil {
			return "", err
		}
		*cache = vs
	}
	if ref == "latest" || ref == "current" {
		if len(*cache) == 0 {
			return "", fmt.Errorf("%s has no versions", name)
		}
		return wkStr((*cache)[0], "id"), nil
	}
	var match []string
	for _, v := range *cache {
		id := wkStr(v, "id")
		if strings.HasPrefix(id, ref) {
			match = append(match, id)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return "", fmt.Errorf("no version of %s matches %q", name, ref)
	}
	return "", fmt.Errorf("%q matches %d versions; use more characters", ref, len(match))
}

func newVersionsViewCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "view <version-id>",
		Aliases: []string{"get", "show"},
		Short:   "Show one version: metadata, compatibility, bindings",
		Long: `Show one version of a Worker. The version can be a full ID, a unique
prefix, or "latest".

Examples:
  cfctl workers versions view 8fa8b1b6 --name my-worker
  cfctl workers versions view latest --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var cache []map[string]any
			id, err := wkResolveVersion(ctx, s, name, args[0], &cache)
			if err != nil {
				return err
			}
			var v map[string]any
			if _, err := wkCall(ctx, s, "GET", wkScriptPath(name, "/versions/"+url.PathEscape(id)), nil, nil, &v); err != nil {
				return fmt.Errorf("failed to get version %s: %w", id, err)
			}
			if jsonOutput {
				return printJSONValue(v)
			}
			fmt.Println(ui.TitleStyle.Render("🫧 " + name + " version " + id))
			wkKV("Number", wkStr(v, "number"))
			wkKV("Created", wkTime(wkStr(v, "metadata", "created_on")))
			wkKV("Source", wkStr(v, "metadata", "source"))
			wkKV("Author", wkStr(v, "metadata", "author_email"))
			if m := wkStr(v, "annotations", "workers/message"); m != "" {
				wkKV("Message", m)
			}
			if t := wkStr(v, "annotations", "workers/tag"); t != "" {
				wkKV("Tag", t)
			}
			wkKV("Triggered by", wkStr(v, "annotations", "workers/triggered_by"))
			wkKV("Compat date", wkStr(v, "resources", "script_runtime", "compatibility_date"))
			if res, ok := v["resources"].(map[string]any); ok {
				if rt, ok := res["script_runtime"].(map[string]any); ok {
					if fl, ok := rt["compatibility_flags"].([]any); ok && len(fl) > 0 {
						wkKV("Compat flags", wkJoin(fl))
					}
				}
				if sc, ok := res["script"].(map[string]any); ok {
					if h, ok := sc["handlers"].([]any); ok {
						wkKV("Handlers", wkJoin(h))
					}
				}
				if bs, ok := res["bindings"].([]any); ok && len(bs) > 0 {
					fmt.Println()
					fmt.Println(ui.SubtleStyle.Render("  Bindings:"))
					wkPrintBindings(bs)
				}
			}
			return nil
		},
	}
	wkAddScriptFlags(c)
	return c
}

func newVersionsUploadCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "upload [script]",
		Short: "Upload a new version without deploying it",
		Long: `Upload code, bindings, and assets as a new version, without sending it
any traffic. Deploy it later with 'versions deploy'. Takes the same flags and
wrangler config as 'deploy', but never touches triggers.

Examples:
  cfctl workers versions upload --message "canary" --tag v1.3.0
  cfctl workers versions upload dist/index.js --name my-worker --preview-alias staging`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := wkPrepareUpload(cmd, args)
			if err != nil {
				return err
			}
			defer p.Close()
			if alias, _ := cmd.Flags().GetString("preview-alias"); alias != "" {
				ann, _ := p.Metadata["annotations"].(map[string]any)
				if ann == nil {
					ann = map[string]any{}
				}
				ann["workers/alias"] = alias
				p.Metadata["annotations"] = ann
			}
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				return wkPrintDryRun(p)
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			if err := wkApplyMigrations(ctx, s, p); err != nil {
				return err
			}
			res, err := wkUpload(ctx, s, "POST", wkScriptPath(p.Name, "/versions"), p)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(res)
			}
			fmt.Println(ui.Success(fmt.Sprintf("Uploaded a new version of %s (%s)", p.Name, wkTotalSize(p.Modules))))
			if id := wkStr(res, "id"); id != "" {
				fmt.Println(ui.Info("Version ID: " + id))
				fmt.Println(ui.SubtleStyle.Render(fmt.Sprintf("  Deploy it: %s workers versions deploy %s --name %s", BinName(), id, p.Name)))
			}
			return nil
		},
	}
	wkAddUploadFlags(c)
	c.Flags().String("preview-alias", "", "Preview alias for this version (workers/alias)")
	return c
}

// wkParseSplits parses "id@pct" specs; versions without @pct share what is left.
func wkParseSplits(ctx context.Context, s *apiSession, name string, specs []string) ([]wkSplit, error) {
	var cache []map[string]any
	var out []wkSplit
	var unset []int
	sum := 0.0
	for _, sp := range specs {
		ref, pct, hasPct := strings.Cut(sp, "@")
		id, err := wkResolveVersion(ctx, s, name, ref, &cache)
		if err != nil {
			return nil, err
		}
		v := wkSplit{VersionID: id}
		if hasPct {
			f, err := strconv.ParseFloat(strings.TrimSuffix(pct, "%"), 64)
			if err != nil || f < 0 || f > 100 {
				return nil, fmt.Errorf("bad percentage in %q (want 0-100)", sp)
			}
			v.Percentage = f
			sum += f
		} else {
			unset = append(unset, len(out))
		}
		out = append(out, v)
	}
	if len(unset) > 0 {
		left := (100 - sum) / float64(len(unset))
		for _, i := range unset {
			out[i].Percentage = left
		}
		sum = 100
	}
	if len(out) == 0 || len(out) > 2 {
		return nil, fmt.Errorf("deploy 1 or 2 versions (got %d)", len(out))
	}
	if sum < 99.999 || sum > 100.001 {
		return nil, fmt.Errorf("percentages add up to %v, not 100", sum)
	}
	return out, nil
}

func wkCreateDeployment(ctx context.Context, s *apiSession, name string, splits []wkSplit, message string, force bool) (map[string]any, error) {
	body := map[string]any{"strategy": "percentage", "versions": splits}
	if message != "" {
		body["annotations"] = map[string]string{"workers/message": message}
	}
	var q url.Values
	if force {
		q = url.Values{"force": {"true"}}
	}
	var res map[string]any
	if _, err := wkCall(ctx, s, "POST", wkScriptPath(name, "/deployments"), q, body, &res); err != nil {
		return nil, fmt.Errorf("failed to deploy %s: %w", name, err)
	}
	return res, nil
}

func newVersionsDeployCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "deploy <version[@percent]>...",
		Short: "Deploy one version, or split traffic between two",
		Long: `Create a deployment: one version at 100%, or two versions with a
percentage split (gradual rollout). Versions can be full IDs, unique
prefixes, or "latest"; a version without @percent gets the remainder.

Examples:
  cfctl workers versions deploy latest --name my-worker
  cfctl workers versions deploy 8fa8b1b6@10 7005f4a4@90 --message "10% canary"
  cfctl workers versions deploy 8fa8b1b6@25 7005f4a4      # 25 / 75`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			splits, err := wkParseSplits(ctx, s, name, args)
			if err != nil {
				return err
			}
			msg, _ := cmd.Flags().GetString("message")
			force, _ := cmd.Flags().GetBool("force")
			res, err := wkCreateDeployment(ctx, s, name, splits, msg, force)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(res)
			}
			fmt.Println(ui.Success("Deployed " + name + ": " + wkDeployment{Versions: splits}.Summary()))
			if id := wkStr(res, "id"); id != "" {
				fmt.Println(ui.Info("Deployment ID: " + id))
			}
			return nil
		},
	}
	wkAddScriptFlags(c)
	c.Flags().String("message", "", "Deployment message (workers/message)")
	c.Flags().Bool("force", false, "Deploy even if the API would block it (e.g. secrets changed since the version)")
	return c
}

// ---- deployments ----------------------------------------------------------

func newDeploymentsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "deployments",
		Aliases: []string{"deployment"},
		Short:   "List deployments or show the current one",
		Long: `Deployments say which versions serve traffic, and at what split.

Examples:
  cfctl workers deployments list my-worker
  cfctl workers deployments status my-worker`,
	}
	list := &cobra.Command{
		Use:   "list [name]",
		Short: "List a Worker's deployments (newest first)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			ds, err := wkListDeployments(cmd.Context(), s, name)
			if err != nil {
				return err
			}
			if jsonOutput {
				if ds == nil {
					ds = []wkDeployment{}
				}
				return printJSONValue(ds)
			}
			if len(ds) == 0 {
				fmt.Println(ui.Warn("No deployments"))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🚢 %d deployments of %s", len(ds), name)))
			for i, d := range ds {
				marker := "  "
				if i == 0 {
					marker = ui.SuccessStyle.Render("▶ ")
				}
				msg := d.Annotations["workers/message"]
				if msg == "" {
					msg = d.Annotations["workers/triggered_by"]
				}
				fmt.Printf("%s%s  %s  %-10s %s\n", marker, wkTime(d.CreatedOn), ui.AccentStyle.Render(d.ID), d.Source, ui.SubtleStyle.Render(d.AuthorEmail))
				fmt.Printf("      %s  %s\n", d.Summary(), ui.SubtleStyle.Render(msg))
			}
			return nil
		},
	}
	wkAddScriptFlags(list)
	status := &cobra.Command{
		Use:   "status [name]",
		Short: "Show the deployment currently serving traffic",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			ds, err := wkListDeployments(cmd.Context(), s, name)
			if err != nil {
				return err
			}
			if len(ds) == 0 {
				return fmt.Errorf("%s has no deployments", name)
			}
			d := ds[0]
			if jsonOutput {
				return printJSONValue(d)
			}
			fmt.Println(ui.TitleStyle.Render("🚢 " + name + " — current deployment"))
			wkKV("Deployment ID", d.ID)
			wkKV("Created", wkTime(d.CreatedOn))
			wkKV("Source", d.Source)
			wkKV("Author", d.AuthorEmail)
			if m := d.Annotations["workers/message"]; m != "" {
				wkKV("Message", m)
			}
			for _, v := range d.Versions {
				wkKV("Version", fmt.Sprintf("%s (%s%%)", v.VersionID, strconv.FormatFloat(v.Percentage, 'f', -1, 64)))
			}
			return nil
		},
	}
	wkAddScriptFlags(status)
	c.AddCommand(list, status)
	return c
}

// ---- rollback -------------------------------------------------------------

func newRollbackCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "rollback [version-id]",
		Short: "Roll back to an earlier version (default: the previous deployment)",
		Long: `Deploy an earlier version to 100% of traffic. With no version, rolls back
to the versions of the previous deployment. Asks for confirmation (--yes to
skip). Like wrangler, it deploys even if secrets changed since that version.

Examples:
  cfctl rollback --name my-worker
  cfctl rollback 7005f4a4 --name my-worker --message "bad release" --yes`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var splits []wkSplit
			if len(args) == 1 {
				var cache []map[string]any
				id, err := wkResolveVersion(ctx, s, name, args[0], &cache)
				if err != nil {
					return err
				}
				splits = []wkSplit{{VersionID: id, Percentage: 100}}
			} else {
				ds, err := wkListDeployments(ctx, s, name)
				if err != nil {
					return err
				}
				if len(ds) < 2 {
					return fmt.Errorf("%s has no previous deployment to roll back to; pass a version ID", name)
				}
				splits = ds[1].Versions
			}
			what := fmt.Sprintf("roll back %s to %s", name, wkDeployment{Versions: splits}.Summary())
			if err := confirm(cmd, what); err != nil {
				return err
			}
			msg, _ := cmd.Flags().GetString("message")
			if msg == "" {
				msg = "Rollback via cfctl"
			}
			res, err := wkCreateDeployment(ctx, s, name, splits, msg, true)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(res)
			}
			fmt.Println(ui.Success("Rolled back " + name + ": " + wkDeployment{Versions: splits}.Summary()))
			return nil
		},
	}
	wkAddScriptFlags(c)
	wkAddYesFlag(c)
	c.Flags().StringP("message", "m", "", "Rollback message (workers/message)")
	return c
}

func init() {
	workersCmd.AddCommand(newVersionsCmd(), newDeploymentsCmd(), newRollbackCmd())
	rootCmd.AddCommand(newVersionsCmd(), newDeploymentsCmd(), newRollbackCmd())
}
