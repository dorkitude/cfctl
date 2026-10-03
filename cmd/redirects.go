package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const redirectPhase = "http_request_dynamic_redirect"

var redirectCols = []col{{"ID", "id"}, {"On", "enabled"}, {"Status", "action_parameters.from_value.status_code"}, {"Description", "description"}, {"Expression", "expression"}, {"Target", "action_parameters.from_value.target_url.value"}, {"Target expr", "action_parameters.from_value.target_url.expression"}}

var redirectsCmd = &cobra.Command{
	Use:     "redirects",
	Aliases: []string{"redirect"},
	Short:   "Single redirect rules (per zone) and bulk redirect lists (per account)",
	Long: `Manage redirect rules.

Single redirects are rules in a zone's http_request_dynamic_redirect phase.
Bulk redirects are account-level lists of source → target URLs (lists of kind
"redirect") enabled by rules in the account's http_request_redirect phase.

Examples:
  cfctl redirects list example.com
  cfctl redirects add example.com --from https://example.com/old --to https://example.com/new
  cfctl redirects add example.com --from 'https://www.example.com/*' --to 'https://example.com/${1}' --status 301
  cfctl redirects add example.com --expression '(http.host eq "old.example.com")' --to https://example.com --preserve-query
  cfctl redirects delete example.com <rule-id>
  cfctl redirects bulk lists
  cfctl redirects bulk items <list-id>
  cfctl redirects bulk add <list-id> --from example.com/a --to https://example.com/b
  cfctl redirects bulk rules`,
}

var redirectsListCmd = phaseListCmd("list <zone>", "List single redirect rules", "Redirect rules", []string{redirectPhase}, redirectCols, "redirect rules")

// redirectRule builds a single-redirect rule from flags.
func redirectRule(c *cobra.Command) (map[string]any, error) {
	from, _ := c.Flags().GetString("from")
	to, _ := c.Flags().GetString("to")
	expr, _ := c.Flags().GetString("expression")
	status, _ := c.Flags().GetInt("status")
	pq, _ := c.Flags().GetBool("preserve-query")
	desc, _ := c.Flags().GetString("description")
	if to == "" {
		return nil, fmt.Errorf("pass --to <target URL>")
	}
	if (from == "") == (expr == "") {
		return nil, fmt.Errorf("pass either --from <URL pattern> or --expression <filter>")
	}
	switch status {
	case 301, 302, 303, 307, 308:
	default:
		return nil, fmt.Errorf("--status must be 301, 302, 303, 307, or 308")
	}
	target := map[string]any{}
	if from != "" {
		if strings.Contains(from, "*") {
			expr = fmt.Sprintf("(http.request.full_uri wildcard %s)", strconv.Quote(from))
			if strings.Contains(to, "${") {
				target["expression"] = fmt.Sprintf("wildcard_replace(http.request.full_uri, %s, %s)", strconv.Quote(from), strconv.Quote(to))
			}
		} else {
			expr = fmt.Sprintf("(http.request.full_uri eq %s)", strconv.Quote(from))
		}
	}
	if target["expression"] == nil {
		target["value"] = to
	}
	if desc == "" {
		desc = "Redirect to " + to
		if from != "" {
			desc = "Redirect " + from + " to " + to
		}
	}
	return map[string]any{
		"expression":  expr,
		"action":      "redirect",
		"description": desc,
		"enabled":     true,
		"action_parameters": map[string]any{"from_value": map[string]any{
			"status_code":           status,
			"target_url":            target,
			"preserve_query_string": pq,
		}},
	}, nil
}

var redirectsAddCmd = &cobra.Command{
	Use:   "add <zone>",
	Short: "Add a single redirect rule",
	Long: `Add a single redirect rule.

--from takes a URL; with '*' wildcards it matches with 'wildcard' and --to may
use ${1}, ${2}, ... for the captured parts. --expression takes any rule filter
instead.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		rule, err := redirectRule(cmd)
		if err != nil {
			return err
		}
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		raw, err := addPhaseRule(ctx, s, true, redirectPhase, rule, "redirect rules")
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			rules, _ := jget(v, "rules").([]any)
			id := ""
			if len(rules) > 0 {
				id = jstr(rules[len(rules)-1], "id")
			}
			fmt.Println(ui.Success("Redirect rule " + id + " added: " + jstr(rule, "description")))
		})
	},
}

var redirectsDeleteCmd = phaseDeleteCmd("delete <zone> <rule-id>", "Delete a single redirect rule", []string{redirectPhase}, "redirect rules")

var bulkItemCols = []col{{"ID", "id"}, {"Source", "redirect.source_url"}, {"Target", "redirect.target_url"}, {"Status", "redirect.status_code"}, {"Comment", "comment"}}

var redirectsBulkCmd = group("bulk", "Bulk redirect lists (account level)", "", nil,
	&cobra.Command{
		Use:   "lists",
		Short: "List bulk redirect lists",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := adminSession(cmd, "")
			if err != nil {
				return err
			}
			raw, err := fetch(context.Background(), s, "/accounts/{account_id}/rules/lists", nil, nil, true, "lists")
			if err != nil {
				return err
			}
			var keep []any
			for _, it := range asList(raw, "") {
				if jstr(it, "kind") == "redirect" {
					keep = append(keep, it)
				}
			}
			if keep == nil {
				keep = []any{}
			}
			b, _ := json.Marshal(keep)
			return emit(b, func(any) { printTable("Bulk redirect lists", keep, listCols) })
		},
	},
	readSpec{
		Use: "items <list-id>", Short: "List the redirects in a bulk redirect list", Scope: scopeAccount,
		Path: "/accounts/{account_id}/rules/lists/{list_id}/items", Args: []string{"list_id"},
		Title: "Redirects", Cols: bulkItemCols, Feature: "lists", Paginate: false,
		Query: func(c *cobra.Command, q url.Values) error { return nil },
	}.allPages(),
	writeSpec{
		Use: "add <list-id>", Short: "Add a redirect to a bulk redirect list", Method: "POST", Scope: scopeAccount,
		Path: "/accounts/{account_id}/rules/lists/{list_id}/items", Args: []string{"list_id"},
		Body: func(c *cobra.Command, args []string) (any, error) {
			from, _ := c.Flags().GetString("from")
			to, _ := c.Flags().GetString("to")
			if from == "" || to == "" {
				return nil, fmt.Errorf("pass --from and --to")
			}
			status, _ := c.Flags().GetInt("status")
			pq, _ := c.Flags().GetBool("preserve-query")
			sub, _ := c.Flags().GetBool("subpath-matching")
			comment, _ := c.Flags().GetString("comment")
			item := map[string]any{"redirect": map[string]any{"source_url": from, "target_url": to, "status_code": status, "preserve_query_string": pq, "subpath_matching": sub}}
			if comment != "" {
				item["comment"] = comment
			}
			return []any{item}, nil
		},
		Flags: func(c *cobra.Command) {
			c.Flags().String("from", "", "Source URL without scheme (example.com/path)")
			c.Flags().String("to", "", "Target URL")
			c.Flags().Int("status", 301, "Status code: 301, 302, 307, or 308")
			c.Flags().Bool("preserve-query", false, "Keep the query string")
			c.Flags().Bool("subpath-matching", false, "Also match subpaths of the source")
			c.Flags().String("comment", "", "Item comment")
		},
		Feature: "lists",
		Human: func(v any) {
			fmt.Println(ui.Success("Redirect queued (bulk operation " + jstr(v, "operation_id") + "); check it with 'cfctl lists operation " + jstr(v, "operation_id") + "'"))
		},
	}.build(),
	&cobra.Command{
		Use:   "rules",
		Short: "Show the account's bulk redirect rules (which lists are enabled)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			s, err := adminSession(cmd, "")
			if err != nil {
				return err
			}
			raw, found, err := getEntrypoint(ctx, s, false, "http_request_redirect", "bulk redirects")
			if err != nil {
				return err
			}
			rules := []any{}
			if found {
				if r, ok := jget(decodeAny(raw), "rules").([]any); ok {
					rules = r
				}
			}
			b, _ := json.Marshal(rules)
			return emit(b, func(any) {
				printTable("Bulk redirect rules", rules, []col{{"ID", "id"}, {"On", "enabled"}, {"List", "action_parameters.from_list.name"}, {"Description", "description"}, {"Expression", "expression"}})
			})
		},
	},
)

// allPages builds a read command that always follows pagination (for
// cursor-paged endpoints that don't take --page).
func (r readSpec) allPages() *cobra.Command {
	c := r.build()
	c.RunE = func(cmd *cobra.Command, args []string) error {
		s, path, vals, err := resolveScope(cmd, r.Scope, r.Path, r.AcctPath, r.Args, args)
		if err != nil {
			return err
		}
		q := url.Values{}
		if r.Query != nil {
			if err := r.Query(cmd, q); err != nil {
				return err
			}
		}
		raw, err := fetch(context.Background(), s, path, vals, q, true, r.Feature)
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			b, _ := json.Marshal(v)
			printTable(r.Title, asList(b, r.ListKey), r.Cols)
		})
	}
	return c
}

func init() {
	rootCmd.AddCommand(redirectsCmd)
	redirectsCmd.AddCommand(redirectsListCmd, redirectsAddCmd, redirectsDeleteCmd, redirectsBulkCmd)
	redirectsAddCmd.Flags().String("from", "", "Source URL (wildcards * allowed)")
	redirectsAddCmd.Flags().String("to", "", "Target URL (may use ${1}.. with a wildcard --from)")
	redirectsAddCmd.Flags().String("expression", "", "Match with this rule expression instead of --from")
	redirectsAddCmd.Flags().Int("status", 301, "Status code: 301, 302, 303, 307, or 308")
	redirectsAddCmd.Flags().Bool("preserve-query", false, "Keep the query string")
	redirectsAddCmd.Flags().String("description", "", "Rule description")
}
