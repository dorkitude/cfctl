package cmd

import (
	"context"
	"fmt"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var listCols = []col{{"ID", "id"}, {"Name", "name"}, {"Kind", "kind"}, {"Items", "num_items"}, {"Refs", "num_referencing_filters"}, {"Description", "description"}, {"Modified", "modified_on"}}
var listItemCols = []col{{"ID", "id"}, {"IP", "ip"}, {"Hostname", "hostname.url_hostname"}, {"ASN", "asn"}, {"Redirect", "redirect.source_url"}, {"Comment", "comment"}, {"Created", "created_on"}}

func queuedOp(v any) {
	op := jstr(v, "operation_id")
	fmt.Println(ui.Success("Queued as bulk operation " + op + "; check it with 'cfctl lists operation " + op + "'"))
}

var listsCmd = group("lists", "Account lists (IP, hostname, ASN, redirect) and their items", `Account-level lists used in rule expressions ($list_name) and bulk redirects.

Examples:
  cfctl lists list
  cfctl lists get <list-id>
  cfctl lists create blocked_ips --kind ip --description "Bad actors"
  cfctl lists items <list-id>
  cfctl lists items-add <list-id> 192.0.2.1 198.51.100.0/24 --comment "abuse"
  cfctl lists items-remove <list-id> <item-id> [<item-id>...]
  cfctl lists operation <operation-id>
  cfctl lists delete <list-id>`, nil,
	readSpec{Use: "list", Short: "List account lists", Scope: scopeAccount, Path: "/accounts/{account_id}/rules/lists", Title: "Lists", Cols: listCols, Feature: "lists"}.build(),
	readSpec{Use: "get <list-id>", Short: "Show a list", Scope: scopeAccount, Path: "/accounts/{account_id}/rules/lists/{list_id}", Args: []string{"list_id"}, Title: "List", Feature: "lists"}.build(),
	writeSpec{
		Use: "create <name>", Short: "Create a list", Method: "POST", Scope: scopeAccount, Path: "/accounts/{account_id}/rules/lists", Args: []string{"name"},
		Body: func(c *cobra.Command, args []string) (any, error) {
			kind, _ := c.Flags().GetString("kind")
			desc, _ := c.Flags().GetString("description")
			b := map[string]any{"name": args[0], "kind": kind}
			if desc != "" {
				b["description"] = desc
			}
			return b, nil
		},
		Flags: func(c *cobra.Command) {
			c.Flags().String("kind", "ip", "List kind: ip, hostname, asn, or redirect")
			c.Flags().String("description", "", "Description")
		},
		Done: "List %s created", Feature: "lists",
	}.build(),
	writeSpec{
		Use: "delete <list-id>", Short: "Delete a list", Method: "DELETE", Scope: scopeAccount, Path: "/accounts/{account_id}/rules/lists/{list_id}", Args: []string{"list_id"},
		Confirm: "delete list %s and all its items", Done: "List %s deleted", Feature: "lists",
	}.build(),
	readSpec{
		Use: "items <list-id>", Short: "List the items in a list", Scope: scopeAccount, Path: "/accounts/{account_id}/rules/lists/{list_id}/items",
		Args: []string{"list_id"}, Title: "Items", Cols: listItemCols, Feature: "lists",
	}.allPages(),
	&cobra.Command{
		Use:   "items-add <list-id> <value>...",
		Short: "Add IPs/CIDRs, hostnames, or ASNs to a list (by the list's kind)",
		Args:  cobra.MinimumNArgs(2),
		RunE:  listsItemsAdd,
	},
	writeSpec{
		Use: "items-remove <list-id> <item-id>", Short: "Remove items from a list by item ID", Method: "DELETE", Scope: scopeAccount,
		Path: "/accounts/{account_id}/rules/lists/{list_id}/items", Args: []string{"list_id", "item_id"},
		Body: func(c *cobra.Command, args []string) (any, error) {
			more, _ := c.Flags().GetStringArray("item")
			ids := append([]string{args[1]}, splitList(more)...)
			var items []any
			for _, id := range ids {
				items = append(items, map[string]any{"id": id})
			}
			return map[string]any{"items": items}, nil
		},
		Flags:   func(c *cobra.Command) { c.Flags().StringArray("item", nil, "More item IDs to remove (repeatable)") },
		Confirm: "remove item %s (and any --item) from the list", Feature: "lists", Human: queuedOp,
	}.build(),
	readSpec{
		Use: "operation <operation-id>", Short: "Show the status of a bulk list operation", Scope: scopeAccount,
		Path: "/accounts/{account_id}/rules/lists/bulk_operations/{operation_id}", Args: []string{"operation_id"}, Title: "Operation", Feature: "lists",
	}.build(),
)

func listsItemsAdd(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	s, err := adminSession(cmd, "")
	if err != nil {
		return err
	}
	raw, err := fetch(ctx, s, "/accounts/{account_id}/rules/lists/{list_id}", map[string]string{"list_id": args[0]}, nil, false, "lists")
	if err != nil {
		return err
	}
	kind := jstr(decodeAny(raw), "kind")
	comment, _ := cmd.Flags().GetString("comment")
	var items []any
	for _, v := range args[1:] {
		it := map[string]any{}
		switch kind {
		case "ip":
			it["ip"] = v
		case "hostname":
			it["hostname"] = map[string]any{"url_hostname": v}
		case "asn":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
				return fmt.Errorf("invalid ASN %q", v)
			}
			it["asn"] = n
		default:
			return fmt.Errorf("list kind %q: use 'cfctl redirects bulk add' for redirect lists, or the generated 'cfctl api lists ...'", kind)
		}
		if comment != "" {
			it["comment"] = comment
		}
		items = append(items, it)
	}
	res, err := send(ctx, s, "POST", "/accounts/{account_id}/rules/lists/{list_id}/items", map[string]string{"list_id": args[0]}, nil, items, "lists")
	if err != nil {
		return err
	}
	return emit(res, queuedOp)
}

func init() {
	rootCmd.AddCommand(listsCmd)
	for _, c := range listsCmd.Commands() {
		if c.Name() == "items-add" {
			c.Flags().String("comment", "", "Comment for the new items")
		}
	}
}
