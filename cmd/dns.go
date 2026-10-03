package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// DNS extras that `cfctl records` doesn't cover: DNSSEC, zone DNS settings,
// and BIND zone file import/export.

var dnsCmd = &cobra.Command{
	Use:   "dns",
	Short: "DNS extras: DNSSEC, DNS settings, zone file import/export",
	Long: `DNS features beyond individual records (see 'cfctl records' for those).

Examples:
  cfctl dns dnssec status example.com
  cfctl dns dnssec enable example.com
  cfctl dns dnssec disable example.com --yes
  cfctl dns settings get example.com
  cfctl dns settings set example.com flatten_all_cnames true
  cfctl dns settings set example.com nameservers.type cloudflare.standard
  cfctl dns export example.com > example.com.zone
  cfctl dns export example.com --out example.com.zone
  cfctl dns import example.com example.com.zone [--proxied]`,
}

var dnssecFields = []col{{"Status", "status"}, {"Algorithm", "algorithm"}, {"Key tag", "key_tag"}, {"Flags", "flags"}, {"Digest type", "digest_type"}, {"Digest", "digest"}, {"DS record", "ds"}, {"Public key", "public_key"}, {"Multi-signer", "dnssec_multi_signer"}, {"Presigned", "dnssec_presigned"}, {"Modified", "modified_on"}}

func dnssecSet(status string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if status == "disabled" {
			if err := confirm(cmd, "disable DNSSEC on "+args[0]+" (remove the DS record at your registrar FIRST, or the zone will stop resolving)"); err != nil {
				return err
			}
		}
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		raw, err := send(context.Background(), s, "PATCH", "/zones/{zone_id}/dnssec", nil, nil, map[string]any{"status": status}, "DNSSEC")
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			fmt.Println(ui.Success("DNSSEC is now " + jstr(v, "status") + " on " + args[0]))
			if ds := jstr(v, "ds"); ds != "" && status == "active" {
				fmt.Println("  Add this DS record at your registrar: " + ds)
			}
		})
	}
}

var dnsDnssecCmd = group("dnssec", "DNSSEC status and on/off", "", nil,
	readSpec{Use: "status <zone>", Short: "Show DNSSEC status and the DS record", Scope: scopeZone, Path: "/zones/{zone_id}/dnssec", Title: "DNSSEC", Fields: dnssecFields, Feature: "DNSSEC"}.build(),
	&cobra.Command{Use: "enable <zone>", Short: "Turn DNSSEC on (then add the DS record at your registrar)", Args: cobra.ExactArgs(1), RunE: dnssecSet("active")},
	&cobra.Command{Use: "disable <zone>", Short: "Turn DNSSEC off (remove the DS record at the registrar first)", Args: cobra.ExactArgs(1), RunE: dnssecSet("disabled")},
)

// nestedBody turns "a.b" = v into {"a": {"b": v}}.
func nestedBody(path string, v any) map[string]any {
	parts := strings.Split(path, ".")
	out := map[string]any{}
	cur := out
	for i, p := range parts {
		if i == len(parts)-1 {
			cur[p] = v
			break
		}
		next := map[string]any{}
		cur[p] = next
		cur = next
	}
	return out
}

var dnsSettingsCmd = group("settings", "Zone DNS settings (CNAME flattening, nameservers, SOA, ...)", "", nil,
	readSpec{Use: "get <zone>", Short: "Show the zone's DNS settings", Scope: scopeZone, Path: "/zones/{zone_id}/dns_settings", Title: "DNS settings", Feature: "DNS settings"}.build(),
	writeSpec{
		Use: "set <zone> <field> <value>", Short: "Change one DNS setting (dotted field names for nested ones)",
		Method: "PATCH", Scope: scopeZone, Path: "/zones/{zone_id}/dns_settings", Args: []string{"field", "value"},
		Body: func(c *cobra.Command, args []string) (any, error) {
			return nestedBody(args[1], parseValue(args[2])), nil
		},
		Feature: "DNS settings",
		Human:   func(v any) { fmt.Println(ui.Success("DNS settings updated")) },
	}.build(),
)

var dnsExportCmd = &cobra.Command{
	Use:   "export <zone>",
	Short: "Export the zone's records as a BIND zone file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		raw, err := fetch(context.Background(), s, "/zones/{zone_id}/dns_records/export", nil, nil, false, "DNS export")
		if err != nil {
			return err
		}
		if out, _ := cmd.Flags().GetString("out"); out != "" {
			if err := os.WriteFile(out, raw, 0o644); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, ui.Success(fmt.Sprintf("Wrote %s (%d bytes)", out, len(raw))))
			return nil
		}
		if jsonOutput {
			// The export is BIND text; --json wraps it (same shape as `zones file`).
			return printJSONValue(map[string]string{"zone": string(raw)})
		}
		_, err = os.Stdout.Write(raw)
		return err
	},
}

var dnsImportCmd = &cobra.Command{
	Use:   "import <zone> <zone-file>",
	Short: "Import records from a BIND zone file",
	Long: `Import DNS records from a BIND-format zone file (as exported by 'cfctl dns
export' or another provider). Existing records are kept; duplicates are
skipped by Cloudflare.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := os.Stat(args[1]); err != nil {
			return err
		}
		fields := []api.FormField{{Name: "file", File: args[1]}}
		if p, _ := cmd.Flags().GetBool("proxied"); p {
			fields = append(fields, api.FormField{Name: "proxied", Value: "true"})
		}
		body, ct, err := api.BuildMultipart(fields)
		if err != nil {
			return err
		}
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		path, err := s.fillPath(ctx, "/zones/{zone_id}/dns_records/import", nil)
		if err != nil {
			return err
		}
		resp, err := s.c.Do(ctx, api.Request{Method: "POST", Path: path, Body: body, ContentType: ct})
		if err != nil {
			return friendly("DNS import", err)
		}
		var raw json.RawMessage
		if resp.Envelope != nil {
			raw = resp.Envelope.Result
		}
		return emit(raw, func(v any) {
			fmt.Println(ui.Success(fmt.Sprintf("Imported %s of %s records into %s", jstr(v, "recs_added"), jstr(v, "total_records_parsed"), args[0])))
		})
	},
}

func init() {
	rootCmd.AddCommand(dnsCmd)
	dnsCmd.AddCommand(dnsDnssecCmd, dnsSettingsCmd, dnsExportCmd, dnsImportCmd)
	for _, c := range dnsDnssecCmd.Commands() {
		if c.Name() == "disable" {
			c.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
		}
	}
	dnsExportCmd.Flags().String("out", "", "Write to this file instead of stdout")
	dnsImportCmd.Flags().Bool("proxied", false, "Proxy imported A/AAAA/CNAME records")
}
