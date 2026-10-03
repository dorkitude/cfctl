package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/registrar"
	"github.com/dorkitude/cfctl/internal/client"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var domainsCmd = &cobra.Command{
	Use:     "domains",
	Aliases: []string{"domain", "dom"},
	Short:   "Manage Registrar domains",
	Long:    `List and view domains registered with Cloudflare Registrar, and toggle auto-renew.`,
}

// listRegistrations returns every Registrar registration in the account.
func listRegistrations(ctx context.Context, app *client.App) ([]registrar.Registration, error) {
	var out []registrar.Registration
	iter := app.Client.Registrar.Registrations.ListAutoPaging(ctx, registrar.RegistrationListParams{
		AccountID: cloudflare.F(app.AccountID),
		PerPage:   cloudflare.F(int64(50)),
	})
	for iter.Next() {
		out = append(out, iter.Current())
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

var domainsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all domains",
	Long: `List all domains registered with Cloudflare Registrar in your account.

Examples:
  cfctl domains list
  cfctl domains list --filter example
  cfctl domains list --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		filter, _ := cmd.Flags().GetString("filter")

		all, err := listRegistrations(ctx, app)
		if err != nil {
			return apiErr("failed to list domains", err)
		}

		domains := make([]registrar.Registration, 0, len(all))
		for _, d := range all {
			if filter == "" || strings.Contains(strings.ToLower(d.DomainName), strings.ToLower(filter)) {
				domains = append(domains, d)
			}
		}

		if printJSON(domains) {
			return nil
		}

		if len(domains) == 0 {
			fmt.Println(ui.Warn("No domains found"))
			return nil
		}

		fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🌐 %d domains", len(domains))))
		fmt.Println()

		for _, d := range domains {
			stateColor := ui.SuccessStyle
			if d.Status != registrar.RegistrationStatusActive {
				stateColor = ui.WarningStyle
			}

			fmt.Printf("  %-30s %s",
				ui.AccentStyle.Render(d.DomainName),
				stateColor.Render(string(d.Status)),
			)
			if !d.ExpiresAt.IsZero() {
				fmt.Printf("  %s", ui.SubtleStyle.Render("expires: "+date(d.ExpiresAt)))
			}
			if d.AutoRenew {
				fmt.Printf("  %s", ui.SubtleStyle.Render("↻"))
			}
			fmt.Println()
		}

		return nil
	},
}

var domainsGetCmd = &cobra.Command{
	Use:   "get [domain]",
	Short: "Get domain details",
	Long:  `Display detailed information about a domain registered with Cloudflare Registrar.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		d, err := getRegistration(ctx, app.Client, app.AccountID, args[0])
		if err != nil {
			return err
		}

		if printJSON(d) {
			return nil
		}

		fmt.Println(ui.TitleStyle.Render("🌐 " + d.DomainName))
		fmt.Println()
		fmt.Printf("  %-14s %s\n", "Name:", d.DomainName)
		fmt.Printf("  %-14s %s\n", "Status:", d.Status)
		fmt.Printf("  %-14s %v\n", "Auto-Renew:", d.AutoRenew)
		fmt.Printf("  %-14s %v\n", "Locked:", d.Locked)
		fmt.Printf("  %-14s %s\n", "Privacy:", d.PrivacyMode)
		if !d.ExpiresAt.IsZero() {
			fmt.Printf("  %-14s %s\n", "Expires:", timestamp(d.ExpiresAt))
		}
		fmt.Printf("  %-14s %s\n", "Created:", timestamp(d.CreatedAt))

		return nil
	},
}

// getRegistration fetches one Registrar registration with a friendly 404.
func getRegistration(ctx context.Context, c *cloudflare.Client, accountID, domain string) (*registrar.Registration, error) {
	domain = normalizeDomain(domain)
	d, err := c.Registrar.Registrations.Get(ctx, domain, registrar.RegistrationGetParams{
		AccountID: cloudflare.F(accountID),
	})
	if err != nil {
		if client.IsNotFound(err) {
			return nil, fmt.Errorf("domain '%s' is not registered with Cloudflare Registrar in this account (API: %s)", domain, client.Messages(err))
		}
		return nil, apiErr("failed to get domain", err)
	}
	return d, nil
}

// normalizeDomain lowercases a domain and strips a trailing dot.
func normalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

func init() {
	rootCmd.AddCommand(domainsCmd)

	domainsCmd.AddCommand(domainsListCmd)
	domainsListCmd.Flags().StringP("filter", "f", "", "Filter domains by name")

	domainsCmd.AddCommand(domainsGetCmd)
}
