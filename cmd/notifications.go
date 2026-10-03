package cmd

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/spf13/cobra"
)

var policyCols = []col{{"ID", "id"}, {"Name", "name"}, {"On", "enabled"}, {"Alert type", "alert_type"}, {"Email", "mechanisms.email"}, {"Webhooks", "#len:mechanisms.webhooks"}, {"Modified", "modified"}}

var notificationsCmd = group("notifications", "Notifications: alert policies, destinations, available alerts, history", `Notifications (alerting): policies, destinations (webhooks, PagerDuty), the
alert types you can subscribe to, and what fired recently.

Examples:
  cfctl notifications policies list
  cfctl notifications policies get <policy-id>
  cfctl notifications policies delete <policy-id>
  cfctl notifications destinations
  cfctl notifications available
  cfctl notifications history --since 7d`, []string{"alerts", "alerting"},
	group("policies", "Notification policies", "", []string{"policy"},
		readSpec{Use: "list", Short: "List notification policies", Scope: scopeAccount, Path: "/accounts/{account_id}/alerting/v3/policies", Title: "Policies", Cols: policyCols, Feature: "Notifications"}.build(),
		readSpec{Use: "get <policy-id>", Short: "Show a notification policy", Scope: scopeAccount, Path: "/accounts/{account_id}/alerting/v3/policies/{policy_id}", Args: []string{"policy_id"}, Title: "Policy", Feature: "Notifications"}.build(),
		writeSpec{Use: "create", Short: "Create a policy (--data JSON)", Method: "POST", Scope: scopeAccount, Path: "/accounts/{account_id}/alerting/v3/policies", DataFlag: true, Done: "Policy created", Feature: "Notifications"}.build(),
		writeSpec{Use: "delete <policy-id>", Short: "Delete a notification policy", Method: "DELETE", Scope: scopeAccount, Path: "/accounts/{account_id}/alerting/v3/policies/{policy_id}", Args: []string{"policy_id"}, Confirm: "delete notification policy %s", Done: "Policy %s deleted", Feature: "Notifications"}.build(),
	),
	&cobra.Command{
		Use:   "destinations",
		Short: "List notification destinations (webhooks, PagerDuty)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			s, err := adminSession(cmd, "")
			if err != nil {
				return err
			}
			var all []any
			for _, d := range []struct{ kind, path string }{{"webhook", "/accounts/{account_id}/alerting/v3/destinations/webhooks"}, {"pagerduty", "/accounts/{account_id}/alerting/v3/destinations/pagerduty"}} {
				raw, err := fetch(ctx, s, d.path, nil, nil, false, "Notifications")
				if err != nil {
					return err
				}
				for _, it := range asList(raw, "") {
					if m, ok := it.(map[string]any); ok {
						m["kind"] = d.kind
						delete(m, "secret") // webhook signing secrets stay hidden
					}
					all = append(all, it)
				}
			}
			if all == nil {
				all = []any{}
			}
			b, _ := json.Marshal(all)
			return emit(b, func(any) {
				printTable("Destinations", all, []col{{"ID", "id"}, {"Kind", "kind"}, {"Name", "name"}, {"Type", "type"}, {"URL", "url"}, {"Last success", "last_success"}, {"Last failure", "last_failure"}})
			})
		},
	},
	&cobra.Command{
		Use:   "available",
		Short: "List the alert types you can create policies for",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := adminSession(cmd, "")
			if err != nil {
				return err
			}
			raw, err := fetch(context.Background(), s, "/accounts/{account_id}/alerting/v3/available_alerts", nil, nil, false, "Notifications")
			if err != nil {
				return err
			}
			return emit(raw, func(v any) {
				var rows []any
				if m, ok := v.(map[string]any); ok {
					for group, list := range m {
						if a, ok := list.([]any); ok {
							for _, it := range a {
								if im, ok := it.(map[string]any); ok {
									im["group"] = group
								}
								rows = append(rows, it)
							}
						}
					}
				}
				sortRows(rows, "group", "type")
				printTable("Available alerts", rows, []col{{"Group", "group"}, {"Type", "type"}, {"Name", "display_name"}})
			})
		},
	},
	readSpec{Use: "history", Short: "Show alerts that fired recently", Scope: scopeAccount, Path: "/accounts/{account_id}/alerting/v3/history", Title: "Alert history", Feature: "Notifications", Paginate: true,
		Cols: []col{{"Sent", "sent"}, {"Type", "alert_type"}, {"Name", "name"}, {"Mechanism", "mechanism"}, {"Body", "alert_body"}},
		Flags: func(c *cobra.Command) {
			c.Flags().String("since", "7d", "Start: 24h, 7d, 30d, YYYY-MM-DD, or RFC 3339")
		},
		Query: func(c *cobra.Command, q url.Values) error {
			s, _ := c.Flags().GetString("since")
			t, err := parseSince(s, time.Now().UTC())
			if err != nil {
				return err
			}
			q.Set("since", t.Format(time.RFC3339))
			return nil
		}}.build(),
)

func init() { rootCmd.AddCommand(notificationsCmd) }
