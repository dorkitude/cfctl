package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Purge the cache and view cache settings",
	Long: `Purge Cloudflare's cache and view a zone's cache configuration.

Examples:
  cfctl cache purge example.com --url https://example.com/app.js
  cfctl cache purge example.com --prefix example.com/assets/
  cfctl cache purge example.com --tag product-123 --tag header
  cfctl cache purge example.com --host static.example.com
  cfctl cache purge example.com --everything --yes
  cfctl cache settings example.com
  cfctl cache dev-mode example.com on`,
}

var cachePurgeCmd = &cobra.Command{
	Use:   "purge <zone>",
	Short: "Purge cached content (by URL, tag, host, prefix, or everything)",
	Long: `Purge cached content from Cloudflare's edge.

Exactly one kind of purge per call: --everything, or one or more of --url,
--tag, --host, or --prefix (repeatable or comma-separated). Tag, host, and
prefix purges need an Enterprise plan on some accounts.

--everything asks for confirmation (or --yes): it empties the zone's cache and
sends all traffic to your origin until the cache warms up again.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		everything, _ := cmd.Flags().GetBool("everything")
		kinds := map[string][]string{}
		for _, k := range []string{"url", "tag", "host", "prefix"} {
			v, _ := cmd.Flags().GetStringArray(k)
			if vs := splitList(v); len(vs) > 0 {
				kinds[k] = vs
			}
		}
		if everything && len(kinds) > 0 {
			return fmt.Errorf("--everything can't be combined with --url/--tag/--host/--prefix")
		}
		if !everything && len(kinds) == 0 {
			return fmt.Errorf("say what to purge: --url, --tag, --host, --prefix, or --everything")
		}
		if len(kinds) > 1 {
			return fmt.Errorf("purge one kind at a time (--url, --tag, --host, or --prefix)")
		}
		var body map[string]any
		what := ""
		if everything {
			body = map[string]any{"purge_everything": true}
			what = "purge EVERYTHING from the cache of zone " + args[0]
			if err := confirm(cmd, what); err != nil {
				return err
			}
		} else {
			for k, vs := range kinds {
				field := map[string]string{"url": "files", "tag": "tags", "host": "hosts", "prefix": "prefixes"}[k]
				body = map[string]any{field: vs}
				what = fmt.Sprintf("%d %s(s)", len(vs), k)
			}
		}
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		raw, err := send(context.Background(), s, "POST", "/zones/{zone_id}/purge_cache", nil, nil, body, "cache purge")
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			if everything {
				fmt.Println(ui.Success("Purged everything from " + args[0]))
				return
			}
			fmt.Println(ui.Success("Purged " + what + " from " + args[0]))
		})
	},
}

// cacheSettingNames are the zone settings shown by `cache settings`.
var cacheSettingNames = []string{"cache_level", "browser_cache_ttl", "development_mode", "always_online", "sort_query_string_for_cache", "edge_cache_ttl", "crawl_hints"}

var cacheSettingsCmd = &cobra.Command{
	Use:   "settings <zone>",
	Short: "Show a zone's cache configuration (level, TTLs, dev mode, tiered cache, cache reserve)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		raw, err := fetch(ctx, s, "/zones/{zone_id}/settings", nil, nil, false, "zone settings")
		if err != nil {
			return err
		}
		out := map[string]any{}
		for _, it := range asList(raw, "") {
			id := jstr(it, "id")
			for _, n := range cacheSettingNames {
				if id == n {
					out[id] = jget(it, "value")
				}
			}
		}
		// Extras live on their own endpoints; skip the ones the plan lacks.
		extras := []struct{ key, path string }{
			{"tiered_caching", "/zones/{zone_id}/argo/tiered_caching"},
			{"regional_tiered_cache", "/zones/{zone_id}/cache/regional_tiered_cache"},
			{"tiered_cache_smart_topology", "/zones/{zone_id}/cache/tiered_cache_smart_topology_enable"},
			{"cache_reserve", "/zones/{zone_id}/cache/cache_reserve"},
		}
		for _, e := range extras {
			r, err := fetch(ctx, s, e.path, nil, nil, false, e.key)
			if err != nil {
				out[e.key] = "unavailable"
				continue
			}
			out[e.key] = jget(decodeAny(r), "value")
		}
		b, _ := json.Marshal(out)
		return emit(b, func(v any) {
			var fields []col
			for _, n := range append(append([]string{}, cacheSettingNames...), "tiered_caching", "regional_tiered_cache", "tiered_cache_smart_topology", "cache_reserve") {
				fields = append(fields, col{n, n})
			}
			printDetail("Cache settings for "+args[0], v, fields)
			fmt.Println(ui.SubtleStyle.Render("\n  Change with: cfctl zones settings set " + args[0] + " <setting> <value>"))
		})
	},
}

var cacheDevModeCmd = &cobra.Command{
	Use:   "dev-mode <zone> [on|off]",
	Short: "Show or toggle development mode (bypasses the cache for 3 hours)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		var raw json.RawMessage
		if len(args) == 1 {
			raw, err = fetch(ctx, s, "/zones/{zone_id}/settings/development_mode", nil, nil, false, "zone settings")
		} else {
			v := strings.ToLower(args[1])
			if v != "on" && v != "off" {
				return fmt.Errorf("dev-mode value must be on or off")
			}
			raw, err = send(ctx, s, "PATCH", "/zones/{zone_id}/settings/development_mode", nil, nil, map[string]any{"value": v}, "zone settings")
		}
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			msg := "Development mode: " + jstr(v, "value")
			if rem := jstr(v, "time_remaining"); rem != "" && rem != "0" {
				msg += " (" + rem + "s remaining)"
			}
			fmt.Println(ui.Success(msg))
		})
	},
}

func init() {
	rootCmd.AddCommand(cacheCmd)
	cacheCmd.AddCommand(cachePurgeCmd, cacheSettingsCmd, cacheDevModeCmd)
	for _, k := range []string{"url", "tag", "host", "prefix"} {
		cachePurgeCmd.Flags().StringArray(k, nil, "Purge by "+k+" (repeatable or comma-separated)")
	}
	cachePurgeCmd.Flags().Bool("everything", false, "Purge everything (asks for confirmation)")
	cachePurgeCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt for --everything")
}
