package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/dns"
	"github.com/cloudflare/cloudflare-go/v7/zones"
	"github.com/dorkitude/cfctl/internal/client"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var zonesCmd = &cobra.Command{
	Use:     "zones",
	Aliases: []string{"zone"},
	Short:   "Manage zones",
	Long:    `List, view, and export Cloudflare zones.`,
}

var zonesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all zones",
	Long: `List all zones in your account.

Examples:
  cfctl zones list
  cfctl zones list --filter example
  cfctl zones list --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		filter, _ := cmd.Flags().GetString("filter")
		page, _ := cmd.Flags().GetInt("page")
		perPage, _ := cmd.Flags().GetInt("per-page")

		params := zones.ZoneListParams{
			Account: cloudflare.F(zones.ZoneListParamsAccount{ID: cloudflare.F(app.AccountID)}),
		}
		if filter != "" {
			params.Name = cloudflare.F("contains:" + filter)
		}
		if perPage > 0 {
			params.PerPage = cloudflare.F(float64(perPage))
		}

		var list []zones.Zone
		if page > 0 {
			params.Page = cloudflare.F(float64(page))
			resp, err := app.Client.Zones.List(ctx, params)
			if err != nil {
				return apiErr("failed to list zones", err)
			}
			list = resp.Result
		} else {
			iter := app.Client.Zones.ListAutoPaging(ctx, params)
			for iter.Next() {
				list = append(list, iter.Current())
			}
			if err := iter.Err(); err != nil {
				return apiErr("failed to list zones", err)
			}
		}

		if printJSON(list) {
			return nil
		}

		if len(list) == 0 {
			fmt.Println(ui.Warn("No zones found"))
			return nil
		}

		fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🗂️  %d zones", len(list))))
		fmt.Println()

		for _, z := range list {
			activeMarker := ui.SuccessStyle.Render("●")
			if z.Status != zones.ZoneStatusActive {
				activeMarker = ui.SubtleStyle.Render("○")
			}

			extra := ""
			if z.Status != zones.ZoneStatusActive {
				extra = ui.SubtleStyle.Render(" (" + string(z.Status) + ")")
			}
			if z.Paused {
				extra += ui.SubtleStyle.Render(" (paused)")
			}

			plan := z.Plan.Name
			fmt.Printf("  %s %-30s %-22s %s%s\n", activeMarker, ui.AccentStyle.Render(z.Name), plan, ui.SubtleStyle.Render(z.ID), extra)
		}

		return nil
	},
}

var zonesGetCmd = &cobra.Command{
	Use:   "get [zone]",
	Short: "Get zone details",
	Long: `Display detailed information about a zone (by name or ID).

Examples:
  cfctl zones get example.com`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		z, err := client.ResolveZone(ctx, app, args[0])
		if err != nil {
			return fmt.Errorf("failed to get zone: %w", err)
		}

		if printJSON(z) {
			return nil
		}

		fmt.Println(ui.TitleStyle.Render("🗂️  " + z.Name))
		fmt.Println()
		fmt.Printf("  %-14s %s\n", "ID:", z.ID)
		fmt.Printf("  %-14s %s\n", "Name:", z.Name)
		fmt.Printf("  %-14s %s\n", "Status:", z.Status)
		fmt.Printf("  %-14s %v\n", "Paused:", z.Paused)
		if z.Plan.Name != "" {
			fmt.Printf("  %-14s %s\n", "Plan:", z.Plan.Name)
		}
		if len(z.NameServers) > 0 {
			fmt.Printf("  %-14s %s\n", "Nameservers:", strings.Join(z.NameServers, ", "))
		}
		if z.OriginalRegistrar != "" {
			fmt.Printf("  %-14s %s\n", "Orig. Reg.:", z.OriginalRegistrar)
		}
		fmt.Printf("  %-14s %s\n", "Created:", timestamp(z.CreatedOn))
		fmt.Printf("  %-14s %s\n", "Modified:", timestamp(z.ModifiedOn))

		return nil
	},
}

var zonesFileCmd = &cobra.Command{
	Use:   "file [zone]",
	Short: "Export zone file",
	Long: `Export the zone's DNS records as a BIND zone file.

Examples:
  cfctl zones file example.com`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		z, err := client.ResolveZone(ctx, app, args[0])
		if err != nil {
			return err
		}

		resp, err := app.Client.DNS.Records.Export(ctx, dns.RecordExportParams{ZoneID: cloudflare.F(z.ID)})
		if err != nil {
			return apiErr("failed to export zone file", err)
		}
		body := ""
		if resp != nil {
			body = *resp
		}

		if printJSON(map[string]string{"zone": body}) {
			return nil
		}

		fmt.Println(ui.TitleStyle.Render("📄 Zone file: " + z.Name))
		fmt.Println()
		fmt.Println(body)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(zonesCmd)

	zonesCmd.AddCommand(zonesListCmd)
	zonesListCmd.Flags().StringP("filter", "f", "", "Filter zones by name")
	zonesListCmd.Flags().Int("page", 0, "Page number (default: fetch all pages)")
	zonesListCmd.Flags().Int("per-page", 0, "Results per page")

	zonesCmd.AddCommand(zonesGetCmd)
	zonesCmd.AddCommand(zonesFileCmd)
}
