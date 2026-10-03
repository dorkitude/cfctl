package cmd

import "github.com/spf13/cobra"

// accessGroup builds list/get for one Access resource at account level (or
// zone level with --zone where the API has a zone form).
func accessGroup(use, short, title, acctPath, zonePath, idName string, cols []col) *cobra.Command {
	list := readSpec{Use: "list", Short: "List " + title, Scope: scopeEither, Path: zonePath, AcctPath: acctPath, Title: title, Cols: cols, Paginate: true, Feature: "Cloudflare Access"}
	get := readSpec{Use: "get <id>", Short: "Show one", Scope: scopeEither, Path: zonePath + "/{" + idName + "}", AcctPath: acctPath + "/{" + idName + "}", Args: []string{idName}, Title: title, Feature: "Cloudflare Access"}
	if zonePath == "" {
		list.Scope, list.Path = scopeAccount, acctPath
		get.Scope, get.Path = scopeAccount, get.AcctPath
	}
	return group(use, short, "", nil, list.build(), get.build())
}

var accessCmd = group("access", "Cloudflare Access / Zero Trust basics: apps, policies, groups, identity providers", `Zero Trust Access basics (read): applications, reusable policies, groups,
identity providers, and the organization. Apps, groups, and identity
providers also exist per zone (--zone).

Examples:
  cfctl access apps list
  cfctl access apps get <app-id>
  cfctl access policies list
  cfctl access groups list
  cfctl access idps list
  cfctl access organization

For changes use the generated commands: cfctl api access-applications ..., etc.`, []string{"zero-trust", "zt"},
	accessGroup("apps", "Access applications", "Access applications", "/accounts/{account_id}/access/apps", "/zones/{zone_id}/access/apps", "app_id",
		[]col{{"ID", "id"}, {"Name", "name"}, {"Type", "type"}, {"Domain", "domain"}, {"Session", "session_duration"}, {"Policies", "#len:policies"}}),
	accessGroup("policies", "Reusable Access policies", "Access policies", "/accounts/{account_id}/access/policies", "", "policy_id",
		[]col{{"ID", "id"}, {"Name", "name"}, {"Decision", "decision"}, {"Apps", "app_count"}, {"Updated", "updated_at"}}),
	accessGroup("groups", "Access groups", "Access groups", "/accounts/{account_id}/access/groups", "/zones/{zone_id}/access/groups", "group_id",
		[]col{{"ID", "id"}, {"Name", "name"}, {"Include", "#len:include"}, {"Exclude", "#len:exclude"}, {"Require", "#len:require"}, {"Updated", "updated_at"}}),
	accessGroup("idps", "Identity providers", "Identity providers", "/accounts/{account_id}/access/identity_providers", "/zones/{zone_id}/access/identity_providers", "identity_provider_id",
		[]col{{"ID", "id"}, {"Name", "name"}, {"Type", "type"}}),
	readSpec{Use: "organization", Short: "Show the Zero Trust organization", Scope: scopeAccount, Path: "/accounts/{account_id}/access/organizations", Title: "Organization", Feature: "Cloudflare Access",
		Fields: []col{{"Name", "name"}, {"Auth domain", "auth_domain"}, {"Session", "session_duration"}, {"Login page", "login_design.header_text"}, {"Created", "created_at"}}}.build(),
)

func init() { rootCmd.AddCommand(accessCmd) }
