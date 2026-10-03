package cmd

import (
	"context"
	"net/url"

	"github.com/spf13/cobra"
)

var accountFields = []col{{"ID", "id"}, {"Name", "name"}, {"Type", "type"}, {"Created", "created_on"}, {"Enforce 2FA", "settings.enforce_twofactor"}, {"Abuse email", "settings.abuse_contact_email"}, {"Managed by", "managed_by.parent_org_name"}}

var accountCols = []col{{"ID", "id"}, {"Name", "name"}, {"Type", "type"}, {"Created", "created_on"}}

var accountsCmd = group("accounts", "Accounts the token can see", `List and inspect Cloudflare accounts.

Examples:
  cfctl accounts list
  cfctl accounts get                 (the configured account)
  cfctl accounts get <account-id>`, []string{"account"},
	readSpec{Use: "list", Short: "List accounts", Path: "/accounts", Title: "Accounts", Cols: accountCols, Paginate: true, Feature: "accounts"}.build(),
	accountsGet(),
)

func accountsGet() *cobra.Command {
	c := readSpec{Use: "get [account-id]", Short: "Show an account (default: the configured one)", Path: "/accounts/{account_id}", Title: "Account", Feature: "accounts",
		Fields: accountFields,
	}.build()
	c.Args = cobra.MaximumNArgs(1)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		vals := map[string]string{}
		if len(args) == 1 {
			vals["account_id"] = args[0]
		}
		raw, err := fetch(context.Background(), s, "/accounts/{account_id}", vals, nil, false, "accounts")
		if err != nil {
			return err
		}
		return emit(raw, func(v any) { printDetail("Account "+jstr(v, "name"), v, accountFields) })
	}
	return c
}

var memberCols = []col{{"ID", "id"}, {"Email", "user.email"}, {"Name", "user.first_name"}, {"Status", "status"}, {"Roles", "#names:roles"}, {"2FA", "user.two_factor_authentication_enabled"}}

var membersCmd = group("members", "Account members", `List and inspect account members.

Examples:
  cfctl members list
  cfctl members list --status pending
  cfctl members get <member-id>
  cfctl members remove <member-id>`, []string{"member"},
	readSpec{Use: "list", Short: "List members", Scope: scopeAccount, Path: "/accounts/{account_id}/members", Title: "Members", Cols: memberCols, Paginate: true, Feature: "account members",
		Flags: func(c *cobra.Command) { c.Flags().String("status", "", "accepted, pending, or rejected") },
		Query: func(c *cobra.Command, q url.Values) error {
			s, _ := c.Flags().GetString("status")
			setIf(q, "status", s)
			return nil
		}}.build(),
	readSpec{Use: "get <member-id>", Short: "Show a member and their roles", Scope: scopeAccount, Path: "/accounts/{account_id}/members/{member_id}", Args: []string{"member_id"}, Title: "Member", Feature: "account members",
		Fields: []col{{"ID", "id"}, {"Email", "user.email"}, {"First name", "user.first_name"}, {"Last name", "user.last_name"}, {"Status", "status"}, {"Roles", "#names:roles"}, {"2FA", "user.two_factor_authentication_enabled"}, {"Policies", "#len:policies"}}}.build(),
	writeSpec{Use: "remove <member-id>", Short: "Remove a member from the account", Method: "DELETE", Scope: scopeAccount, Path: "/accounts/{account_id}/members/{member_id}", Args: []string{"member_id"},
		Confirm: "remove member %s from the account", Done: "Member %s removed", Feature: "account members"}.build(),
)

var rolesCmd = group("roles", "Account roles", `List account roles (for member invitations).

Examples:
  cfctl roles list
  cfctl roles get <role-id>`, []string{"role"},
	readSpec{Use: "list", Short: "List roles", Scope: scopeAccount, Path: "/accounts/{account_id}/roles", Title: "Roles", Cols: []col{{"ID", "id"}, {"Name", "name"}, {"Description", "description"}}, Paginate: true, Feature: "account roles"}.build(),
	readSpec{Use: "get <role-id>", Short: "Show a role and its permissions", Scope: scopeAccount, Path: "/accounts/{account_id}/roles/{role_id}", Args: []string{"role_id"}, Title: "Role", Feature: "account roles"}.build(),
)

func init() {
	rootCmd.AddCommand(accountsCmd, membersCmd, rolesCmd)
}
