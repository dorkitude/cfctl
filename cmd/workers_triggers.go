package cmd

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Triggers: cron schedules, zone routes, custom domains, workers.dev.

type wkZoneRoute struct {
	wkRoute
	Zone   string `json:"zone"`
	ZoneID string `json:"zone_id"`
}

// wkAllRoutes lists Worker routes on one zone (--zone) or every zone.
func wkAllRoutes(ctx context.Context, s *apiSession, zoneArg string) ([]wkZoneRoute, error) {
	zones := map[string]string{}
	if zoneArg != "" {
		s.zoneArg = zoneArg
		id, err := s.zone(ctx)
		if err != nil {
			return nil, err
		}
		zones[zoneArg] = id
	} else {
		var err error
		if zones, err = wkListZones(ctx, s); err != nil {
			return nil, err
		}
	}
	var out []wkZoneRoute
	for _, zn := range wkSortedKeys(zones) {
		var rs []wkRoute
		if _, err := wkCall(ctx, s, "GET", "/zones/"+zones[zn]+"/workers/routes", nil, nil, &rs); err != nil {
			return nil, fmt.Errorf("failed to list routes on %s: %w", zn, err)
		}
		for _, r := range rs {
			out = append(out, wkZoneRoute{wkRoute: r, Zone: zn, ZoneID: zones[zn]})
		}
	}
	return out, nil
}

type wkDomain struct {
	ID          string `json:"id"`
	Hostname    string `json:"hostname"`
	Service     string `json:"service"`
	Environment string `json:"environment,omitempty"`
	ZoneID      string `json:"zone_id,omitempty"`
	ZoneName    string `json:"zone_name,omitempty"`
}

func wkListDomains(ctx context.Context, s *apiSession, service string) ([]wkDomain, error) {
	q := url.Values{}
	if service != "" {
		q.Set("service", service)
	}
	var ds []wkDomain
	if _, err := wkCall(ctx, s, "GET", "/accounts/{account_id}/workers/domains", q, nil, &ds); err != nil {
		return nil, fmt.Errorf("failed to list custom domains: %w", err)
	}
	if service != "" {
		var f []wkDomain
		for _, d := range ds {
			if d.Service == service {
				f = append(f, d)
			}
		}
		ds = f
	}
	return ds, nil
}

func wkCrons(ctx context.Context, s *apiSession, name string) ([]string, error) {
	var res struct {
		Schedules []struct {
			Cron string `json:"cron"`
		} `json:"schedules"`
	}
	if _, err := wkCall(ctx, s, "GET", wkScriptPath(name, "/schedules"), nil, nil, &res); err != nil {
		return nil, fmt.Errorf("failed to get cron triggers of %s: %w", name, err)
	}
	out := []string{}
	for _, sc := range res.Schedules {
		out = append(out, sc.Cron)
	}
	return out, nil
}

func newTriggersCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "triggers",
		Aliases: []string{"trigger"},
		Short:   "Show or apply a Worker's triggers (crons, routes, custom domains, workers.dev)",
		Long: `Show everything that sends traffic or events to a Worker, or apply the
triggers from the wrangler config (like 'wrangler triggers deploy', for use
after 'versions upload').

Examples:
  cfctl triggers list my-worker
  cfctl triggers deploy                      # from ./wrangler.toml
  cfctl triggers deploy --name my-worker --cron "0 * * * *" --route "example.com/api/*"`,
	}
	list := &cobra.Command{
		Use:   "list [name]",
		Short: "Show crons, routes, custom domains, and workers.dev for a Worker",
		Args:  cobra.MaximumNArgs(1),
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
			crons, err := wkCrons(ctx, s, name)
			if err != nil {
				return err
			}
			zoneArg, _ := cmd.Flags().GetString("zone")
			all, err := wkAllRoutes(ctx, s, zoneArg)
			if err != nil {
				return err
			}
			routes := []wkZoneRoute{}
			for _, r := range all {
				if r.Script == name {
					routes = append(routes, r)
				}
			}
			domains, err := wkListDomains(ctx, s, name)
			if err != nil {
				return err
			}
			var sub map[string]any
			_, _ = wkCall(ctx, s, "GET", wkScriptPath(name, "/subdomain"), nil, nil, &sub)
			if jsonOutput {
				return printJSONValue(map[string]any{"crons": crons, "routes": routes, "custom_domains": wkNonNilDomains(domains), "workers_dev": sub})
			}
			fmt.Println(ui.TitleStyle.Render("🎯 Triggers for " + name))
			wkKV("Crons", wkOrNone(crons))
			var rp []string
			for _, r := range routes {
				rp = append(rp, r.Pattern)
			}
			wkKV("Routes", wkOrNone(rp))
			var dh []string
			for _, d := range domains {
				dh = append(dh, d.Hostname)
			}
			wkKV("Custom domains", wkOrNone(dh))
			if sub != nil {
				wkKV("workers.dev", wkOnOff(sub["enabled"]))
				wkKV("Preview URLs", wkOnOff(sub["previews_enabled"]))
			}
			return nil
		},
	}
	wkAddScriptFlags(list)
	list.Flags().String("zone", "", "Only look for routes on this zone (default: every zone)")

	deploy := &cobra.Command{
		Use:   "deploy [name]",
		Short: "Apply triggers from the wrangler config and flags",
		Long: `Apply cron triggers, routes, custom domains, and the workers.dev setting
from the wrangler config (and --cron/--route/--domain/--workers-dev). Crons
are replaced; routes and domains are added or pointed at this Worker, never
removed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			cfg, err := wkConfig(cmd)
			if err != nil {
				return err
			}
			t, err := wkTriggersFrom(cmd, cfg)
			if err != nil {
				return err
			}
			if t.empty() {
				return fmt.Errorf("no triggers to apply: add triggers/routes to the wrangler config or pass --cron/--route/--domain/--workers-dev")
			}
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				return printJSONValue(t)
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			applied, err := wkApplyTriggers(cmd.Context(), s, name, t)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(applied)
			}
			return nil
		},
	}
	wkAddScriptFlags(deploy)
	deploy.Flags().StringArray("route", nil, "Route pattern (repeatable)")
	deploy.Flags().StringArray("domain", nil, "Custom domain hostname (repeatable)")
	deploy.Flags().StringArray("cron", nil, "Cron expression (repeatable; replaces the config's crons)")
	deploy.Flags().Bool("workers-dev", true, "Enable or disable workers.dev (--workers-dev=false)")
	deploy.Flags().String("zone", "", "Zone for --route patterns (default: inferred from the hostname)")
	deploy.Flags().Bool("dry-run", false, "Print the triggers that would be applied")
	c.AddCommand(list, deploy)
	return c
}

func wkOrNone(xs []string) string {
	if len(xs) == 0 {
		return ui.SubtleStyle.Render("none")
	}
	return strings.Join(xs, ", ")
}

func wkOnOff(v any) string {
	if v == true {
		return "enabled"
	}
	return "disabled"
}

func wkNonNilDomains(d []wkDomain) []wkDomain {
	if d == nil {
		return []wkDomain{}
	}
	return d
}

// ---- crons ----------------------------------------------------------------

func newCronsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "crons",
		Aliases: []string{"cron", "schedules"},
		Short:   "Get or replace a Worker's cron triggers",
		Long: `Examples:
  cfctl workers crons list my-worker
  cfctl workers crons set my-worker --cron "*/15 * * * *" --cron "0 0 * * MON"
  cfctl workers crons clear my-worker`,
	}
	list := &cobra.Command{
		Use: "list [name]", Aliases: []string{"get"}, Short: "List cron triggers", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			crons, err := wkCrons(cmd.Context(), s, name)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(crons)
			}
			if len(crons) == 0 {
				fmt.Println(ui.Warn("No cron triggers on " + name))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render("⏰ Cron triggers for " + name))
			for _, c := range crons {
				fmt.Println("  " + c)
			}
			return nil
		},
	}
	wkAddScriptFlags(list)
	setCrons := func(cmd *cobra.Command, name string, crons []string) error {
		s, err := newAPISession(cmd)
		if err != nil {
			return err
		}
		_, err = wkApplyTriggers(cmd.Context(), s, name, wkTriggers{Crons: crons, SetCrons: true})
		if err == nil && jsonOutput {
			return printJSONValue(map[string]any{"worker": name, "crons": wkNonNil(crons)})
		}
		return err
	}
	set := &cobra.Command{
		Use: "set [name]", Short: "Replace the cron triggers (--cron, repeatable)", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			crons, _ := cmd.Flags().GetStringArray("cron")
			if len(crons) == 0 {
				return fmt.Errorf("pass at least one --cron (use 'crons clear' to remove them all)")
			}
			return setCrons(cmd, name, crons)
		},
	}
	wkAddScriptFlags(set)
	set.Flags().StringArray("cron", nil, "Cron expression (repeatable)")
	clear := &cobra.Command{
		Use: "clear [name]", Short: "Remove every cron trigger", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			if err := confirm(cmd, "remove every cron trigger from "+name); err != nil {
				return err
			}
			return setCrons(cmd, name, []string{})
		},
	}
	wkAddScriptFlags(clear)
	wkAddYesFlag(clear)
	c.AddCommand(list, set, clear)
	return c
}

// ---- routes ---------------------------------------------------------------

func newRoutesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "routes",
		Aliases: []string{"route"},
		Short:   "List, add, and delete zone routes to Workers",
		Long: `Examples:
  cfctl workers routes list                       # every zone
  cfctl workers routes list --zone example.com
  cfctl workers routes add "example.com/api/*" --name my-worker
  cfctl workers routes delete "example.com/api/*"`,
	}
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List Worker routes", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			zoneArg, _ := cmd.Flags().GetString("zone")
			rs, err := wkAllRoutes(cmd.Context(), s, zoneArg)
			if err != nil {
				return err
			}
			if n, _ := cmd.Flags().GetString("name"); n != "" {
				var f []wkZoneRoute
				for _, r := range rs {
					if r.Script == n {
						f = append(f, r)
					}
				}
				rs = f
			}
			if jsonOutput {
				if rs == nil {
					rs = []wkZoneRoute{}
				}
				return printJSONValue(rs)
			}
			if len(rs) == 0 {
				fmt.Println(ui.Warn("No Worker routes"))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🛣  %d Worker routes", len(rs))))
			for _, r := range rs {
				script := r.Script
				if script == "" {
					script = ui.SubtleStyle.Render("(no worker)")
				}
				fmt.Printf("  %-44s %-28s %s\n", ui.AccentStyle.Render(r.Pattern), script, ui.SubtleStyle.Render(r.Zone+" "+r.ID))
			}
			return nil
		},
	}
	list.Flags().String("zone", "", "Only this zone (name or ID)")
	list.Flags().String("name", "", "Only routes to this Worker")
	add := &cobra.Command{
		Use: "add <pattern>", Short: "Route a pattern to a Worker", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			zoneArg, _ := cmd.Flags().GetString("zone")
			_, err = wkApplyTriggers(cmd.Context(), s, name, wkTriggers{Routes: []wkRouteSpec{{Pattern: args[0]}}, Zone: zoneArg})
			if err == nil && jsonOutput {
				return printJSONValue(map[string]any{"worker": name, "pattern": args[0]})
			}
			return err
		},
	}
	wkAddScriptFlags(add)
	add.Flags().String("zone", "", "Zone (default: inferred from the pattern's hostname)")
	del := &cobra.Command{
		Use: "delete <pattern|route-id>", Aliases: []string{"rm"}, Short: "Delete a Worker route", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			zoneArg, _ := cmd.Flags().GetString("zone")
			if zoneArg == "" && !hexID.MatchString(args[0]) {
				// Search only the zone the pattern belongs to.
				zones, err := wkListZones(ctx, s)
				if err != nil {
					return err
				}
				if zn, _, ok := wkZoneFor(zones, wkRouteHost(args[0])); ok {
					zoneArg = zn
				}
			}
			rs, err := wkAllRoutes(ctx, s, zoneArg)
			if err != nil {
				return err
			}
			var target *wkZoneRoute
			for i := range rs {
				if rs[i].ID == args[0] || rs[i].Pattern == args[0] {
					target = &rs[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("no Worker route matches %q", args[0])
			}
			if err := confirm(cmd, fmt.Sprintf("delete route %s (%s) on %s", target.Pattern, target.Script, target.Zone)); err != nil {
				return err
			}
			if _, err := wkCall(ctx, s, "DELETE", "/zones/"+target.ZoneID+"/workers/routes/"+url.PathEscape(target.ID), nil, nil, nil); err != nil {
				return fmt.Errorf("failed to delete route: %w", err)
			}
			if printJSON(map[string]any{"id": target.ID, "pattern": target.Pattern, "deleted": true}) {
				return nil
			}
			fmt.Println(ui.Success("Deleted route " + target.Pattern))
			return nil
		},
	}
	del.Flags().String("zone", "", "Zone (default: inferred from the pattern's hostname)")
	wkAddYesFlag(del)
	c.AddCommand(list, add, del)
	return c
}

// ---- custom domains -------------------------------------------------------

func newDomainsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "domains",
		Aliases: []string{"domain", "custom-domains"},
		Short:   "List, attach, and detach Worker custom domains",
		Long: `Examples:
  cfctl workers domains list
  cfctl workers domains list --name my-worker
  cfctl workers domains add api.example.com --name my-worker
  cfctl workers domains delete api.example.com`,
	}
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List custom domains", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			n, _ := cmd.Flags().GetString("name")
			ds, err := wkListDomains(cmd.Context(), s, n)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(wkNonNilDomains(ds))
			}
			if len(ds) == 0 {
				fmt.Println(ui.Warn("No Worker custom domains"))
				return nil
			}
			sort.Slice(ds, func(i, j int) bool { return ds[i].Hostname < ds[j].Hostname })
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🌐 %d custom domains", len(ds))))
			for _, d := range ds {
				fmt.Printf("  %-40s %-28s %s\n", ui.AccentStyle.Render(d.Hostname), d.Service, ui.SubtleStyle.Render(d.ID))
			}
			return nil
		},
	}
	list.Flags().String("name", "", "Only domains for this Worker")
	add := &cobra.Command{
		Use: "add <hostname>", Short: "Attach a custom domain to a Worker", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			zoneArg, _ := cmd.Flags().GetString("zone")
			_, err = wkApplyTriggers(cmd.Context(), s, name, wkTriggers{Domains: []wkRouteSpec{{Pattern: args[0], CustomDomain: true}}, Zone: zoneArg})
			if err == nil && jsonOutput {
				return printJSONValue(map[string]any{"worker": name, "hostname": args[0]})
			}
			return err
		},
	}
	wkAddScriptFlags(add)
	add.Flags().String("zone", "", "Zone (default: inferred from the hostname)")
	del := &cobra.Command{
		Use: "delete <hostname|domain-id>", Aliases: []string{"rm", "detach"}, Short: "Detach a custom domain", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			ds, err := wkListDomains(ctx, s, "")
			if err != nil {
				return err
			}
			var target *wkDomain
			for i := range ds {
				if ds[i].ID == args[0] || strings.EqualFold(ds[i].Hostname, args[0]) {
					target = &ds[i]
				}
			}
			if target == nil {
				return fmt.Errorf("no Worker custom domain matches %q", args[0])
			}
			if err := confirm(cmd, fmt.Sprintf("detach %s from %s", target.Hostname, target.Service)); err != nil {
				return err
			}
			if _, err := wkCall(ctx, s, "DELETE", "/accounts/{account_id}/workers/domains/"+url.PathEscape(target.ID), nil, nil, nil); err != nil {
				return fmt.Errorf("failed to detach %s: %w", target.Hostname, err)
			}
			if printJSON(map[string]any{"id": target.ID, "hostname": target.Hostname, "deleted": true}) {
				return nil
			}
			fmt.Println(ui.Success("Detached " + target.Hostname))
			return nil
		},
	}
	wkAddYesFlag(del)
	c.AddCommand(list, add, del)
	return c
}

// ---- workers.dev ----------------------------------------------------------

func newSubdomainCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "subdomain",
		Short: "Get or set the account's workers.dev subdomain",
		Long: `Examples:
  cfctl workers subdomain get
  cfctl workers subdomain set my-team       # <worker>.my-team.workers.dev`,
	}
	get := &cobra.Command{
		Use: "get", Short: "Show the account's workers.dev subdomain", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var res map[string]any
			if _, err := wkCall(cmd.Context(), s, "GET", "/accounts/{account_id}/workers/subdomain", nil, nil, &res); err != nil {
				return fmt.Errorf("failed to get the workers.dev subdomain: %w", err)
			}
			if jsonOutput {
				return printJSONValue(res)
			}
			fmt.Printf("%v.workers.dev\n", res["subdomain"])
			return nil
		},
	}
	set := &cobra.Command{
		Use: "set <subdomain>", Short: "Create or change the workers.dev subdomain", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirm(cmd, "set the workers.dev subdomain to "+args[0]+" (changes every workers.dev URL)"); err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var res map[string]any
			if _, err := wkCall(cmd.Context(), s, "PUT", "/accounts/{account_id}/workers/subdomain", nil, map[string]string{"subdomain": args[0]}, &res); err != nil {
				return fmt.Errorf("failed to set the workers.dev subdomain: %w", err)
			}
			if jsonOutput {
				return printJSONValue(res)
			}
			fmt.Println(ui.Success("workers.dev subdomain: " + args[0] + ".workers.dev"))
			return nil
		},
	}
	wkAddYesFlag(set)
	c.AddCommand(get, set)
	return c
}

func newDevURLCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "dev-url",
		Aliases: []string{"workers-dev"},
		Short:   "Show, enable, or disable a Worker's workers.dev URL",
		Long: `Examples:
  cfctl workers dev-url get my-worker
  cfctl workers dev-url enable my-worker --previews
  cfctl workers dev-url disable my-worker`,
	}
	get := &cobra.Command{
		Use: "get [name]", Short: "Show whether workers.dev and preview URLs are on", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var res map[string]any
			if _, err := wkCall(cmd.Context(), s, "GET", wkScriptPath(name, "/subdomain"), nil, nil, &res); err != nil {
				return fmt.Errorf("failed to get workers.dev status of %s: %w", name, err)
			}
			var acct map[string]any
			_, _ = wkCall(cmd.Context(), s, "GET", "/accounts/{account_id}/workers/subdomain", nil, nil, &acct)
			if sub, ok := acct["subdomain"].(string); ok && sub != "" {
				res["url"] = "https://" + name + "." + sub + ".workers.dev"
			}
			if jsonOutput {
				return printJSONValue(res)
			}
			wkKV("workers.dev", wkOnOff(res["enabled"]))
			wkKV("Preview URLs", wkOnOff(res["previews_enabled"]))
			if u, ok := res["url"]; ok {
				wkKV("URL", u)
			}
			return nil
		},
	}
	wkAddScriptFlags(get)
	toggle := func(on bool) *cobra.Command {
		use, short := "enable [name]", "Serve the Worker on workers.dev"
		if !on {
			use, short = "disable [name]", "Stop serving the Worker on workers.dev"
		}
		t := &cobra.Command{
			Use: use, Short: short, Args: cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				name, err := wkScriptName(cmd, args)
				if err != nil {
					return err
				}
				s, err := newAPISession(cmd)
				if err != nil {
					return err
				}
				body := map[string]bool{"enabled": on}
				if cmd.Flags().Changed("previews") {
					v, _ := cmd.Flags().GetBool("previews")
					body["previews_enabled"] = v
				}
				var res map[string]any
				if _, err := wkCall(cmd.Context(), s, "POST", wkScriptPath(name, "/subdomain"), nil, body, &res); err != nil {
					return fmt.Errorf("failed to update workers.dev for %s: %w", name, err)
				}
				if jsonOutput {
					return printJSONValue(res)
				}
				fmt.Println(ui.Success(fmt.Sprintf("workers.dev %s for %s", map[bool]string{true: "enabled", false: "disabled"}[on], name)))
				return nil
			},
		}
		wkAddScriptFlags(t)
		t.Flags().Bool("previews", false, "Also turn preview URLs on (or off with --previews=false)")
		return t
	}
	c.AddCommand(get, toggle(true), toggle(false))
	return c
}

func init() {
	workersCmd.AddCommand(newTriggersCmd(), newCronsCmd(), newRoutesCmd(), newDomainsCmd(), newSubdomainCmd(), newDevURLCmd())
	rootCmd.AddCommand(newTriggersCmd())
}
