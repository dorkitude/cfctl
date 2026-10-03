package cmd

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var auditCols = []col{{"Time", "action.time"}, {"Actor", "actor.email"}, {"Via", "actor.context"}, {"Action", "action.type"}, {"Result", "action.result"}, {"Product", "resource.product"}, {"Description", "action.description"}, {"Zone", "zone.name"}}
var auditV1Cols = []col{{"Time", "when"}, {"Actor", "actor.email"}, {"Action", "action.type"}, {"OK", "action.result"}, {"Resource", "resource.type"}, {"Zone", "owner.name"}, {"IP", "actor.ip"}}

// secretKeys are field names whose string values are redacted from audit
// log output (request/response snapshots can carry them).
var secretKeys = map[string]bool{"private_key": true, "secret": true, "client_secret": true, "password": true, "api_key": true, "token_value": true, "key": true}

// redactSecrets replaces secret-looking string fields in generic JSON.
func redactSecrets(v any, parent string) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if _, isStr := x.(string); isStr && (secretKeys[strings.ToLower(k)] || (k == "value" && parent == "response")) {
				t[k] = "<redacted>"
				continue
			}
			t[k] = redactSecrets(x, k)
		}
	case []any:
		for i := range t {
			t[i] = redactSecrets(t[i], parent)
		}
	}
	return v
}

var auditLogsCmd = &cobra.Command{
	Use:     "audit-logs",
	Aliases: []string{"audit", "auditlogs"},
	Short:   "Account audit logs (who changed what, when)",
	Long: `Account audit logs.

Examples:
  cfctl audit-logs list
  cfctl audit-logs list --since 7d --actor kyle@example.com
  cfctl audit-logs list --since 30d --action delete --product dns_records
  cfctl audit-logs list --since 2026-09-01 --before 2026-09-15 --all --json
  cfctl audit-logs list --v1 --since 7d --action token_create`,
}

var auditLogsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List audit log entries (newest first)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		now := time.Now().UTC()
		sinceS, _ := cmd.Flags().GetString("since")
		since, err := parseSince(sinceS, now)
		if err != nil {
			return err
		}
		before := now
		if b, _ := cmd.Flags().GetString("before"); b != "" {
			if before, err = parseSince(b, now); err != nil {
				return err
			}
		}
		actor, _ := cmd.Flags().GetString("actor")
		action, _ := cmd.Flags().GetString("action")
		product, _ := cmd.Flags().GetString("product")
		zone, _ := cmd.Flags().GetString("zone")
		limit, _ := cmd.Flags().GetInt("limit")
		all, _ := cmd.Flags().GetBool("all")
		v1, _ := cmd.Flags().GetBool("v1")
		if limit <= 0 || limit > 1000 {
			limit = 1000
		}
		q := url.Values{}
		path := "/accounts/{account_id}/logs/audit"
		cols := auditCols
		if v1 {
			path, cols = "/accounts/{account_id}/audit_logs", auditV1Cols
			q.Set("since", since.Format(time.RFC3339))
			q.Set("before", before.Format(time.RFC3339))
			q.Set("per_page", strconv.Itoa(limit))
			q.Set("direction", "desc")
			setIf(q, "actor.email", actor)
			setIf(q, "action.type", action)
			setIf(q, "zone.name", zone)
		} else {
			q.Set("since", since.Format(time.RFC3339))
			q.Set("before", before.Format(time.RFC3339))
			q.Set("limit", strconv.Itoa(limit))
			q.Set("direction", "desc")
			setIf(q, "actor_email", actor)
			setIf(q, "action_type", action)
			setIf(q, "resource_product", product)
			setIf(q, "zone_name", zone)
		}
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		raw, err := fetch(context.Background(), s, path, nil, q, all, "audit logs")
		if err != nil {
			return err
		}
		v := redactSecrets(decodeAny(raw), "")
		b, _ := json.Marshal(v)
		return emit(b, func(v any) {
			b, _ := json.Marshal(v)
			printTable("Audit log entries since "+since.Format("2006-01-02 15:04"), asList(b, ""), cols)
		})
	},
}

func init() {
	rootCmd.AddCommand(auditLogsCmd)
	auditLogsCmd.AddCommand(auditLogsListCmd)
	f := auditLogsListCmd.Flags()
	f.String("since", "24h", "Start: 30m, 24h, 7d, 2w, YYYY-MM-DD, or RFC 3339")
	f.String("before", "", "End (default: now), same formats as --since")
	f.String("actor", "", "Only entries by this actor email")
	f.String("action", "", "Only this action type (v2: create, update, delete, view, ...; v1: e.g. token_create)")
	f.String("product", "", "Only this resource product (v2), e.g. dns_records, tokens, zones")
	f.String("zone", "", "Only entries for this zone name")
	f.Int("limit", 100, "Entries per page (max 1000)")
	f.Bool("all", false, "Follow pagination to the end of the time range")
	f.Bool("v1", false, "Use the older v1 audit log API")
}
