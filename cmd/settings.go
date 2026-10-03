package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Zone settings (`cfctl zones settings ...`) and zone lifecycle operations
// (`cfctl zones create|delete|pause|unpause|activation-check`), attached to
// the existing `zones` command.

var settingCols = []col{{"Setting", "id"}, {"Value", "value"}, {"Editable", "editable"}, {"Modified", "modified_on"}}

var zonesSettingsCmd = group("settings", "View and change zone settings (ssl, always_use_https, min_tls_version, ...)",
	`View and change any zone setting by name.

Common settings: ssl (off|flexible|full|strict), always_use_https (on|off),
min_tls_version (1.0|1.1|1.2|1.3), tls_1_3 (on|off|zrt), automatic_https_rewrites,
security_level, cache_level, browser_cache_ttl, development_mode, brotli,
http3, 0rtt, ipv6, websockets, opportunistic_encryption, early_hints,
email_obfuscation, hotlink_protection, rocket_loader, always_online.

Examples:
  cfctl zones settings list example.com
  cfctl zones settings get example.com ssl
  cfctl zones settings set example.com always_use_https on
  cfctl zones settings set example.com min_tls_version 1.2
  cfctl zones settings set example.com browser_cache_ttl 14400
  cfctl zones settings set example.com minify '{"css":"on","html":"off","js":"on"}'

Generated equivalent: cfctl api zone-settings ...`, []string{"setting"},
	readSpec{
		Use: "list <zone>", Short: "List every setting of a zone", Scope: scopeZone,
		Path: "/zones/{zone_id}/settings", Title: "Zone settings", Cols: settingCols, Feature: "zone settings",
		Flags: func(c *cobra.Command) { c.Flags().String("filter", "", "Only settings whose name contains this") },
		Human: nil,
	}.withFilter(),
	readSpec{
		Use: "get <zone> <setting>", Short: "Show one zone setting", Scope: scopeZone,
		Path: "/zones/{zone_id}/settings/{setting_id}", Args: []string{"setting_id"},
		Title: "Setting", Feature: "zone settings",
	}.build(),
	writeSpec{
		Use: "set <zone> <setting> <value>", Short: "Change one zone setting",
		Long: `Change one zone setting. The value is sent as JSON when it parses as JSON
(numbers, true/false, objects), otherwise as a string ("on", "off", "full").

Examples:
  cfctl zones settings set example.com ssl strict
  cfctl zones settings set example.com always_use_https on
  cfctl zones settings set example.com browser_cache_ttl 14400`,
		Method: "PATCH", Scope: scopeZone, Path: "/zones/{zone_id}/settings/{setting_id}", Args: []string{"setting_id", "value"},
		Body: func(c *cobra.Command, args []string) (any, error) {
			return map[string]any{"value": settingValue(args[1], args[2])}, nil
		},
		Feature: "zone settings",
		Human: func(v any) {
			fmt.Println(ui.Success(fmt.Sprintf("%s = %s", jstr(v, "id"), jstr(v, "value"))))
		},
	}.build(),
)

// stringValueSettings take a string value even when it looks like a number
// (the API rejects min_tls_version 1.2 sent as a JSON number).
var stringValueSettings = map[string]bool{"min_tls_version": true, "origin_max_http_version": true}

// settingValue parses a setting value for the API (see parseValue).
func settingValue(setting, raw string) any {
	v := parseValue(raw)
	if _, isNum := v.(json.Number); isNum && stringValueSettings[setting] {
		return strings.TrimSpace(raw)
	}
	return v
}

// withFilter builds a settings list command with a --filter flag.
func (r readSpec) withFilter() *cobra.Command {
	c := r.build()
	inner := c.RunE
	c.RunE = func(cmd *cobra.Command, args []string) error {
		f, _ := cmd.Flags().GetString("filter")
		if f == "" || jsonOutput {
			return inner(cmd, args)
		}
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		raw, err := fetch(context.Background(), s, r.Path, nil, nil, false, r.Feature)
		if err != nil {
			return err
		}
		var keep []any
		for _, it := range asList(raw, "") {
			if strings.Contains(jstr(it, "id"), f) {
				keep = append(keep, it)
			}
		}
		printTable(r.Title, keep, r.Cols)
		return nil
	}
	return c
}

var zonesCreateCmd = writeSpec{
	Use: "create <domain>", Short: "Add a zone (domain) to the account",
	Long: `Add a zone to the account. Cloudflare returns the nameservers to set at
your registrar.

Examples:
  cfctl zones create example.com
  cfctl zones create example.com --type partial
  cfctl zones create example.com --jump-start`,
	Method: "POST", Path: "/zones",
	Args: []string{"name"},
	Body: func(c *cobra.Command, args []string) (any, error) {
		typ, _ := c.Flags().GetString("type")
		js, _ := c.Flags().GetBool("jump-start")
		s, err := adminSession(c, "")
		if err != nil {
			return nil, err
		}
		acct, err := s.account(context.Background())
		if err != nil {
			return nil, err
		}
		b := map[string]any{"name": strings.TrimSuffix(strings.ToLower(args[0]), "."), "account": map[string]any{"id": acct}, "type": typ}
		if js {
			b["jump_start"] = true
		}
		return b, nil
	},
	Flags: func(c *cobra.Command) {
		c.Flags().String("type", "full", "Zone type: full, partial (CNAME setup), or secondary")
		c.Flags().Bool("jump-start", false, "Scan for existing DNS records")
	},
	Feature: "zone creation",
	Human: func(v any) {
		fmt.Println(ui.Success(fmt.Sprintf("Zone %s created (id %s, status %s)", jstr(v, "name"), jstr(v, "id"), jstr(v, "status"))))
		if ns := jstr(v, "name_servers"); ns != "" {
			fmt.Println("  Set these nameservers at your registrar: " + ns)
		}
	},
}.build()

var zonesDeleteCmd = writeSpec{
	Use: "delete <zone>", Short: "PERMANENTLY delete a zone and all its settings and records",
	Method: "DELETE", Scope: scopeZone, Path: "/zones/{zone_id}",
	Confirm: "PERMANENTLY delete the zone and all its DNS records and settings", Done: "Zone deleted", Feature: "zones",
}.build()

func zonePauseCmd(pause bool) *cobra.Command {
	use, short, done := "pause <zone>", "Pause Cloudflare on a zone (DNS only, no proxy/security)", "Zone paused"
	if !pause {
		use, short, done = "unpause <zone>", "Resume Cloudflare on a paused zone", "Zone unpaused"
	}
	w := writeSpec{
		Use: use, Short: short, Method: "PATCH", Scope: scopeZone, Path: "/zones/{zone_id}",
		Body:    func(*cobra.Command, []string) (any, error) { return map[string]any{"paused": pause}, nil },
		Done:    done,
		Feature: "zones",
	}
	if pause {
		w.Confirm = "pause the zone (traffic bypasses Cloudflare's proxy and security)"
	}
	return w.build()
}

var zonesActivationCheckCmd = writeSpec{
	Use: "activation-check <zone>", Short: "Ask Cloudflare to re-check a pending zone's nameservers now",
	Method: "PUT", Scope: scopeZone, Path: "/zones/{zone_id}/activation_check",
	Done: "Activation check requested", Feature: "zones",
}.build()

func init() {
	zonesCmd.AddCommand(zonesSettingsCmd, zonesCreateCmd, zonesDeleteCmd, zonePauseCmd(true), zonePauseCmd(false), zonesActivationCheckCmd)
}
