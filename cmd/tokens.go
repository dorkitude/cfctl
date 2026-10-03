package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var tokenCols = []col{{"ID", "id"}, {"Name", "name"}, {"Status", "status"}, {"Policies", "#len:policies"}, {"Issued", "issued_on"}, {"Expires", "expires_on"}, {"Last used", "last_used_on"}}

// tokensBase is /user/tokens with --user, else the account's tokens.
func tokensBase(cmd *cobra.Command) string {
	if u, _ := cmd.Flags().GetBool("user"); u {
		return "/user/tokens"
	}
	return "/accounts/{account_id}/tokens"
}

var tokensCmd = &cobra.Command{
	Use:     "tokens",
	Aliases: []string{"token", "api-tokens"},
	Short:   "API tokens: list, inspect, verify, permission groups, create, delete",
	Long: `Manage API tokens. Account-owned tokens by default; pass --user for tokens
owned by your user (needs a user-owned token).

Token values are secrets: 'tokens create' writes the new value to --value-out
(mode 0600) and never prints it.

Examples:
  cfctl tokens list
  cfctl tokens get <token-id>
  cfctl tokens verify
  cfctl tokens permission-groups --filter dns
  cfctl tokens create --name ci-deploy --policies @policies.json --value-out ci.token
  cfctl tokens delete <token-id>`,
}

var tokensListCmd = &cobra.Command{
	Use:   "list",
	Short: "List API tokens",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		raw, err := fetch(context.Background(), s, tokensBase(cmd), nil, nil, true, "API tokens")
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			b, _ := json.Marshal(v)
			printTable("API tokens", asList(b, ""), tokenCols)
		})
	},
}

var tokensGetCmd = &cobra.Command{
	Use:   "get <token-id>",
	Short: "Show a token and its policies",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		raw, err := fetch(context.Background(), s, tokensBase(cmd)+"/{token_id}", map[string]string{"token_id": args[0]}, nil, false, "API tokens")
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			printDetail("Token "+jstr(v, "name"), v, []col{{"ID", "id"}, {"Name", "name"}, {"Status", "status"}, {"Issued", "issued_on"}, {"Modified", "modified_on"}, {"Expires", "expires_on"}, {"Not before", "not_before"}, {"Last used", "last_used_on"}, {"Allowed IPs", "condition.request_ip.in"}, {"Denied IPs", "condition.request_ip.not_in"}})
			pols, _ := jget(v, "policies").([]any)
			fmt.Println()
			printTable("Policies", pols, []col{{"Effect", "effect"}, {"Permissions", "#names:permission_groups"}, {"Resources", "resources"}})
		})
	},
}

var tokensVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify the stored token (user- or account-owned)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		owner := "user"
		raw, err := fetch(ctx, s, "/user/tokens/verify", nil, nil, false, "API tokens")
		if err != nil {
			var ae *api.Error
			if !errors.As(err, &ae) || (ae.Status != 401 && ae.Status != 403) {
				return err
			}
			owner = "account"
			raw, err = fetch(ctx, s, "/accounts/{account_id}/tokens/verify", nil, nil, false, "API tokens")
			if err != nil {
				return err
			}
		}
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		if v != nil {
			v["owner"] = owner
		}
		b, _ := json.Marshal(v)
		return emit(b, func(v any) {
			msg := fmt.Sprintf("Token %s is %s (%s-owned)", jstr(v, "id"), jstr(v, "status"), owner)
			if exp := jstr(v, "expires_on"); exp != "" {
				msg += ", expires " + exp
			}
			if jstr(v, "status") == "active" {
				fmt.Println(ui.Success(msg))
			} else {
				fmt.Println(ui.Warn(msg))
			}
		})
	},
}

var tokensPermGroupsCmd = &cobra.Command{
	Use:   "permission-groups",
	Short: "List the permission groups a token policy can grant",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		raw, err := fetch(context.Background(), s, tokensBase(cmd)+"/permission_groups", nil, nil, true, "API tokens")
		if err != nil {
			return err
		}
		filter, _ := cmd.Flags().GetString("filter")
		filter = strings.ToLower(filter)
		var keep []any
		for _, it := range asList(raw, "") {
			if filter == "" || strings.Contains(strings.ToLower(jstr(it, "name")), filter) || strings.Contains(strings.ToLower(jstr(it, "scopes")), filter) {
				keep = append(keep, it)
			}
		}
		if keep == nil {
			keep = []any{}
		}
		b, _ := json.Marshal(keep)
		return emit(b, func(any) {
			printTable("Permission groups", keep, []col{{"ID", "id"}, {"Name", "name"}, {"Scopes", "scopes"}})
		})
	},
}

var tokensCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an API token (the value goes to --value-out, never to the screen)",
	Long: `Create an API token. --policies takes the policies array as JSON (or @file),
as in the API: [{"effect":"allow","resources":{"com.cloudflare.api.account.<id>":"*"},
"permission_groups":[{"id":"<group-id>"}]}]. Find group IDs with
'cfctl tokens permission-groups'.

The new token's value is written to --value-out (mode 0600) and redacted from
all output, including --json.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		pol, _ := cmd.Flags().GetString("policies")
		out, _ := cmd.Flags().GetString("value-out")
		if name == "" || pol == "" || out == "" {
			return fmt.Errorf("pass --name, --policies, and --value-out")
		}
		if _, err := os.Stat(out); err == nil {
			return fmt.Errorf("%s exists; refusing to overwrite a token file", out)
		}
		praw, err := api.ReadData(pol, os.Stdin)
		if err != nil {
			return err
		}
		var policies any
		if err := json.Unmarshal(praw, &policies); err != nil {
			return fmt.Errorf("--policies is not valid JSON: %w", err)
		}
		body := map[string]any{"name": name, "policies": policies}
		if exp, _ := cmd.Flags().GetString("expires"); exp != "" {
			body["expires_on"] = exp
		}
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		raw, err := send(context.Background(), s, "POST", tokensBase(cmd), nil, nil, body, "API tokens")
		if err != nil {
			return err
		}
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("unexpected response creating the token")
		}
		if val, ok := v["value"].(string); ok && val != "" {
			if err := os.WriteFile(out, []byte(val+"\n"), 0o600); err != nil {
				return fmt.Errorf("token created (id %v) but writing %s failed: %w; roll it with the API", v["id"], out, err)
			}
			v["value"] = "<written to " + out + ">"
		}
		b, _ := json.Marshal(v)
		return emit(b, func(v any) {
			fmt.Println(ui.Success(fmt.Sprintf("Token %s created (id %s); value written to %s", jstr(v, "name"), jstr(v, "id"), out)))
		})
	},
}

var tokensDeleteCmd = &cobra.Command{
	Use:   "delete <token-id>",
	Short: "Delete (revoke) an API token",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := confirm(cmd, "delete API token "+args[0]+" (anything using it stops working)"); err != nil {
			return err
		}
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		raw, err := send(context.Background(), s, "DELETE", tokensBase(cmd)+"/{token_id}", map[string]string{"token_id": args[0]}, nil, nil, "API tokens")
		if err != nil {
			return err
		}
		return emit(raw, func(any) { fmt.Println(ui.Success("Token " + args[0] + " deleted")) })
	},
}

func init() {
	rootCmd.AddCommand(tokensCmd)
	tokensCmd.AddCommand(tokensListCmd, tokensGetCmd, tokensVerifyCmd, tokensPermGroupsCmd, tokensCreateCmd, tokensDeleteCmd)
	for _, c := range []*cobra.Command{tokensListCmd, tokensGetCmd, tokensPermGroupsCmd, tokensCreateCmd, tokensDeleteCmd} {
		c.Flags().Bool("user", false, "User-owned tokens (/user/tokens) instead of the account's")
	}
	tokensPermGroupsCmd.Flags().String("filter", "", "Only groups whose name or scope contains this")
	tokensCreateCmd.Flags().String("name", "", "Token name")
	tokensCreateCmd.Flags().String("policies", "", "Policies JSON array (or @file, or -)")
	tokensCreateCmd.Flags().String("value-out", "", "File to write the new token value to (mode 0600)")
	tokensCreateCmd.Flags().String("expires", "", "Expiry time (RFC 3339)")
	tokensDeleteCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
}
