package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const tsBase = "/accounts/{account_id}/challenges/widgets"

func tsFlags(c *cobra.Command) {
	c.Flags().String("name", "", "Widget name")
	c.Flags().StringSlice("domain", nil, "Hostnames the widget may run on (repeatable or comma-separated)")
	c.Flags().String("mode", "", "Widget mode: managed, non-interactive, or invisible")
	c.Flags().String("region", "", "Region: world or china")
	c.Flags().Bool("bot-fight-mode", false, "Enable bot fight mode (Enterprise)")
	c.Flags().Bool("offlabel", false, "Hide Cloudflare branding (Enterprise)")
	c.Flags().String("clearance-level", "", "Clearance level: no_clearance, jschallenge, managed, interactive")
}

func tsApply(c *cobra.Command, b map[string]any) {
	platSetStr(c, b, "name", "name")
	platSetStrs(c, b, "domain", "domains")
	platSetStr(c, b, "mode", "mode")
	platSetStr(c, b, "region", "region")
	platSetBool(c, b, "bot-fight-mode", "bot_fight_mode")
	platSetBool(c, b, "offlabel", "offlabel")
	platSetStr(c, b, "clearance-level", "clearance_level")
}

var tsFields = []platCol{
	{H: "Name", Path: "name"}, {H: "Sitekey", Path: "sitekey"}, {H: "Secret", Path: "secret"}, {H: "Mode", Path: "mode"},
	{H: "Domains", Path: "domains"}, {H: "Region", Path: "region"}, {H: "Clearance", Path: "clearance_level"},
	{H: "Bot fight", Path: "bot_fight_mode"}, {H: "Offlabel", Path: "offlabel"}, {H: "Created", Path: "created_on"}, {H: "Modified", Path: "modified_on"},
}

func init() {
	widget := platGroup("widget", "Manage Turnstile widgets", "", []string{"widgets"},
		append(platSpecs(
			platSpec{Use: "list", Short: "List widgets", Aliases: []string{"ls"}, Path: tsBase, List: true,
				Cols:  []platCol{{H: "sitekey", Path: "sitekey"}, {H: "name", Path: "name"}, {H: "mode", Path: "mode"}, {H: "domains", Path: "domains", W: 50}, {H: "created", Path: "created_on"}},
				Title: "🛡  %d Turnstile widgets", Product: "Turnstile"},
			platSpec{Use: "get <sitekey>", Short: "Show a widget (secret redacted unless --reveal)", Path: tsBase + "/{sitekey}", Fields: tsFields, Title: "🛡  Widget %s",
				Secrets: []string{"secret"}, Product: "Turnstile"},
			platSpec{Use: "create", Short: "Create a widget", Method: "POST", Path: tsBase,
				Example: "  cfctl turnstile widget create --name signup --domain example.com --mode managed",
				Flags:   tsFlags,
				Body: func(c *cobra.Command, _ []string) (any, error) {
					b := map[string]any{"mode": "managed"}
					tsApply(c, b)
					if b["name"] == nil || b["domains"] == nil {
						return nil, fmt.Errorf("--name and --domain are required")
					}
					return b, nil
				},
				Data: true, Fields: tsFields, Title: "🛡  Created widget", Secrets: []string{"secret"}, Product: "Turnstile"},
			platSpec{Use: "delete <sitekey>", Short: "Delete a widget", Aliases: []string{"rm"}, Method: "DELETE", Path: tsBase + "/{sitekey}",
				Confirm: "delete Turnstile widget %s", Done: "Deleted widget %s", Product: "Turnstile"},
			platSpec{Use: "rotate-secret <sitekey>", Short: "Rotate a widget's secret key", Method: "POST", Path: tsBase + "/{sitekey}/rotate_secret",
				Flags: func(c *cobra.Command) {
					c.Flags().Bool("invalidate-immediately", false, "Invalidate the old secret now instead of after a grace period")
				},
				Body: func(c *cobra.Command, _ []string) (any, error) {
					b := map[string]any{}
					platSetBool(c, b, "invalidate-immediately", "invalidate_immediately")
					return b, nil
				},
				Confirm: "rotate the secret of widget %s", Fields: tsFields, Title: "🛡  Rotated secret", Secrets: []string{"secret"}, Product: "Turnstile"},
		), tsUpdateCmd())...)
	rootCmd.AddCommand(platGroup("turnstile", "Manage Turnstile widgets", `Manage Turnstile widgets.

  cfctl turnstile widget list|get|create|update|delete|rotate-secret

Widget secrets are redacted unless --reveal is passed. Generated: 'cfctl api turnstile ...'.`, nil, widget))
}

// tsUpdateCmd merges flags over the current widget and PUTs it (the API
// replaces the whole widget).
func tsUpdateCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "update <sitekey>", Short: "Update a widget (unchanged fields are kept)", Path: tsBase + "/{sitekey}", Flags: tsFlags, Data: true, Secrets: []string{"secret"},
		Run: func(c *cobra.Command, args []string) error {
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			path, _, err := platFill(ctx, s, tsBase+"/{sitekey}", args)
			if err != nil {
				return err
			}
			raw, err := platDo(ctx, s, "GET", path, nil, nil)
			if err != nil {
				return platErr("Turnstile", err)
			}
			var cur map[string]any
			if err := platDecode(raw, &cur); err != nil {
				return err
			}
			b := map[string]any{}
			for _, k := range []string{"name", "domains", "mode", "region", "bot_fight_mode", "offlabel", "clearance_level", "ephemeral_id"} {
				if v, ok := cur[k]; ok {
					b[k] = v
				}
			}
			tsApply(c, b)
			if c.Flags().Changed("data") {
				extra, err := platBuildBody(c, platSpec{Data: true}, args)
				if err != nil {
					return err
				}
				var m map[string]any
				_ = json.Unmarshal(extra, &m)
				for k, v := range m {
					b[k] = v
				}
			}
			raw, err = platDo(ctx, s, "PUT", path, nil, b)
			if err != nil {
				return platErr("Turnstile", err)
			}
			if reveal, _ := c.Flags().GetBool("reveal"); !reveal {
				raw = platRedact(raw, []string{"secret"})
			}
			if jsonOutput {
				return printBody(raw, nil)
			}
			fmt.Println(ui.Success("Updated widget " + args[0]))
			return nil
		},
	})
}
