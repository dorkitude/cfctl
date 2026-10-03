package cmd

var pageRuleCols = []col{{"ID", "id"}, {"Status", "status"}, {"Priority", "priority"}, {"Target", "targets.0.constraint.value"}, {"Actions", "actions"}}

var pageRulesCmd = group("page-rules", "Legacy Page Rules (list, get, delete)", `Legacy Page Rules. New configurations should use rules (cfctl redirects,
cfctl transform, cfctl rulesets). Page Rules endpoints only accept
user-owned API tokens.

Examples:
  cfctl page-rules list example.com
  cfctl page-rules get example.com <rule-id>
  cfctl page-rules delete example.com <rule-id>`, []string{"pagerules"},
	readSpec{
		Use: "list <zone>", Short: "List page rules", Scope: scopeZone, Path: "/zones/{zone_id}/pagerules",
		Title: "Page rules", Cols: pageRuleCols, Feature: "Page Rules",
	}.build(),
	readSpec{
		Use: "get <zone> <rule-id>", Short: "Show a page rule", Scope: scopeZone, Path: "/zones/{zone_id}/pagerules/{pagerule_id}",
		Args: []string{"pagerule_id"}, Title: "Page rule", Feature: "Page Rules",
	}.build(),
	writeSpec{
		Use: "delete <zone> <rule-id>", Short: "Delete a page rule", Method: "DELETE", Scope: scopeZone,
		Path: "/zones/{zone_id}/pagerules/{pagerule_id}", Args: []string{"pagerule_id"},
		Confirm: "delete page rule %s", Done: "Page rule %s deleted", Feature: "Page Rules",
	}.build(),
)

func init() {
	rootCmd.AddCommand(pageRulesCmd)
}
