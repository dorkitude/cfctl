package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// transformPhases maps --type to the ruleset phase.
var transformPhases = map[string]string{
	"url-rewrite":      "http_request_transform",
	"request-headers":  "http_request_late_transform",
	"response-headers": "http_response_headers_transform",
}

var allTransformPhases = []string{"http_request_transform", "http_request_late_transform", "http_response_headers_transform"}

var transformCols = []col{{"ID", "id"}, {"Phase", "phase"}, {"On", "enabled"}, {"Description", "description"}, {"Expression", "expression"}, {"Params", "action_parameters"}}

var transformCmd = &cobra.Command{
	Use:   "transform",
	Short: "Transform rules: URL rewrites and request/response header changes",
	Long: `Transform rules rewrite URLs and modify request or response headers.

Types: url-rewrite (http_request_transform), request-headers
(http_request_late_transform), response-headers (http_response_headers_transform).

Examples:
  cfctl transform list example.com
  cfctl transform list example.com --type response-headers
  cfctl transform add example.com --type response-headers --expression 'true' --set-header 'X-Frame-Options: DENY'
  cfctl transform add example.com --type request-headers --expression '(http.host eq "api.example.com")' --remove-header X-Debug
  cfctl transform add example.com --type url-rewrite --expression '(http.request.uri.path eq "/old")' --path /new
  cfctl transform delete example.com <rule-id>`,
}

var transformListCmd = &cobra.Command{
	Use:   "list <zone>",
	Short: "List transform rules (all types, or --type)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		phases := allTransformPhases
		if t, _ := cmd.Flags().GetString("type"); t != "" {
			ph, ok := transformPhases[t]
			if !ok {
				return fmt.Errorf("--type must be url-rewrite, request-headers, or response-headers")
			}
			phases = []string{ph}
		}
		return phaseListCmd("list", "", "Transform rules", phases, transformCols, "transform rules").RunE(cmd, args)
	},
}

var transformAddCmd = &cobra.Command{
	Use:   "add <zone>",
	Short: "Add a transform rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, _ := cmd.Flags().GetString("type")
		phase, ok := transformPhases[t]
		if !ok {
			return fmt.Errorf("--type must be url-rewrite, request-headers, or response-headers")
		}
		expr, _ := cmd.Flags().GetString("expression")
		if expr == "" {
			return fmt.Errorf("pass --expression (use 'true' to match everything)")
		}
		desc, _ := cmd.Flags().GetString("description")
		ap := map[string]any{}
		if t == "url-rewrite" {
			uri := map[string]any{}
			if p, _ := cmd.Flags().GetString("path"); p != "" {
				uri["path"] = map[string]any{"value": p}
			}
			if pe, _ := cmd.Flags().GetString("path-expression"); pe != "" {
				uri["path"] = map[string]any{"expression": pe}
			}
			if q, _ := cmd.Flags().GetString("query"); q != "" {
				uri["query"] = map[string]any{"value": q}
			}
			if len(uri) == 0 {
				return fmt.Errorf("url-rewrite needs --path, --path-expression, or --query")
			}
			ap["uri"] = uri
		} else {
			headers := map[string]any{}
			sets, _ := cmd.Flags().GetStringArray("set-header")
			for _, h := range sets {
				k, v, ok := strings.Cut(h, ":")
				if !ok {
					return fmt.Errorf("invalid --set-header %q: want 'Name: value'", h)
				}
				headers[strings.TrimSpace(k)] = map[string]any{"operation": "set", "value": strings.TrimSpace(v)}
			}
			adds, _ := cmd.Flags().GetStringArray("add-header")
			for _, h := range adds {
				k, v, ok := strings.Cut(h, ":")
				if !ok {
					return fmt.Errorf("invalid --add-header %q: want 'Name: value'", h)
				}
				headers[strings.TrimSpace(k)] = map[string]any{"operation": "add", "value": strings.TrimSpace(v)}
			}
			rms, _ := cmd.Flags().GetStringArray("remove-header")
			for _, h := range splitList(rms) {
				headers[h] = map[string]any{"operation": "remove"}
			}
			if len(headers) == 0 {
				return fmt.Errorf("header rules need --set-header, --add-header, or --remove-header")
			}
			ap["headers"] = headers
		}
		rule := map[string]any{"expression": expr, "action": "rewrite", "action_parameters": ap, "enabled": true}
		if desc != "" {
			rule["description"] = desc
		}
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		raw, err := addPhaseRule(ctx, s, true, phase, rule, "transform rules")
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			rules, _ := jget(v, "rules").([]any)
			id := ""
			if len(rules) > 0 {
				id = jstr(rules[len(rules)-1], "id")
			}
			fmt.Println(ui.Success("Transform rule " + id + " added (" + t + ")"))
		})
	},
}

var transformDeleteCmd = phaseDeleteCmd("delete <zone> <rule-id>", "Delete a transform rule", allTransformPhases, "transform rules")

func init() {
	rootCmd.AddCommand(transformCmd)
	transformCmd.AddCommand(transformListCmd, transformAddCmd, transformDeleteCmd)
	transformListCmd.Flags().String("type", "", "url-rewrite, request-headers, or response-headers")
	f := transformAddCmd.Flags()
	f.String("type", "", "url-rewrite, request-headers, or response-headers (required)")
	f.String("expression", "", "When the rule applies ('true' for every request)")
	f.String("description", "", "Rule description")
	f.String("path", "", "url-rewrite: new static path")
	f.String("path-expression", "", "url-rewrite: dynamic path expression")
	f.String("query", "", "url-rewrite: new static query string")
	f.StringArray("set-header", nil, "Set a header 'Name: value' (repeatable)")
	f.StringArray("add-header", nil, "Add a header 'Name: value' (repeatable)")
	f.StringArray("remove-header", nil, "Remove a header (repeatable)")
}
