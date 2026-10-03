package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Workers Logs (observability): query stored logs, as opposed to `tail`.

func newWorkersLogsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "logs [name]",
		Short: "Query a Worker's stored logs (Workers Logs / observability)",
		Long: `Query Workers Logs for a Worker (observability must be enabled on it).
Shows the newest events first. The query API is a POST, so it is refused in
read-only mode; use 'tail' for live logs.

Examples:
  cfctl workers logs my-worker
  cfctl workers logs my-worker --since 24h --search "timeout" --limit 200
  cfctl workers logs my-worker --level error --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, args)
			if err != nil {
				return err
			}
			since, _ := cmd.Flags().GetDuration("since")
			limit, _ := cmd.Flags().GetInt("limit")
			search, _ := cmd.Flags().GetString("search")
			level, _ := cmd.Flags().GetString("level")
			if since <= 0 {
				return fmt.Errorf("--since must be positive")
			}
			now := time.Now()
			filters := []map[string]any{{"key": "$metadata.service", "operation": "eq", "type": "string", "value": name}}
			if level != "" {
				filters = append(filters, map[string]any{"key": "$metadata.level", "operation": "eq", "type": "string", "value": level})
			}
			params := map[string]any{"datasets": []string{}, "filters": filters, "filterCombination": "and"}
			if search != "" {
				params["needle"] = map[string]any{"value": search}
			}
			body := map[string]any{
				"queryId":    "cfctl-workers-logs",
				"view":       "events",
				"dry":        true,
				"limit":      limit,
				"timeframe":  map[string]int64{"from": now.Add(-since).UnixMilli(), "to": now.UnixMilli()},
				"parameters": params,
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var res map[string]any
			if _, err := wkCall(cmd.Context(), s, "POST", "/accounts/{account_id}/workers/observability/telemetry/query", nil, body, &res); err != nil {
				if errors.Is(err, api.ErrReadOnly) {
					return fmt.Errorf("%w (the Workers Logs query API is a POST; use 'cfctl tail' or drop --read-only)", err)
				}
				return fmt.Errorf("failed to query logs of %s: %w", name, err)
			}
			if jsonOutput {
				return printJSONValue(res)
			}
			var events []any
			if ev, ok := res["events"].(map[string]any); ok {
				events, _ = ev["events"].([]any)
			}
			if len(events) == 0 {
				fmt.Println(ui.Warn(fmt.Sprintf("No log events for %s in the last %s", name, since)))
				return nil
			}
			for _, e := range events {
				m, _ := e.(map[string]any)
				ts := ""
				if t, ok := m["timestamp"].(float64); ok {
					ts = time.UnixMilli(int64(t)).Local().Format("2006-01-02 15:04:05")
				}
				lvl := wkStr(m, "$metadata", "level")
				msg := wkStr(m, "$metadata", "message")
				if msg == "" {
					if src, ok := m["source"]; ok {
						b, _ := json.Marshal(src)
						msg = string(b)
					}
				}
				fmt.Printf("%s %-5s %s\n", ui.SubtleStyle.Render(ts), strings.ToUpper(lvl), msg)
			}
			return nil
		},
	}
	wkAddScriptFlags(c)
	c.Flags().Duration("since", time.Hour, "How far back to look")
	c.Flags().Int("limit", 100, "Maximum number of events")
	c.Flags().String("search", "", "Full-text search across event fields")
	c.Flags().String("level", "", "Only this log level (log, info, warn, error, debug)")
	return c
}

func init() {
	workersCmd.AddCommand(newWorkersLogsCmd())
}
