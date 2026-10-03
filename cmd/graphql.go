package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var graphqlCmd = &cobra.Command{
	Use:   "graphql [query]",
	Short: "Run a raw query against Cloudflare's GraphQL Analytics API",
	Long: `Run a raw GraphQL query against Cloudflare's GraphQL Analytics API
(https://api.cloudflare.com/client/v4/graphql) using your stored credentials.

Use this for analytics the REST API doesn't expose: R2 operations and
storage, Workers invocations, HTTP requests, firewall events, and more.

The placeholders {account_id} and {zone_id} are replaced in the query and
in string values of the variables, from your config (or --account) and
--zone <name-or-id>.

Examples:
  cfctl graphql 'query { viewer { accounts(filter:{accountTag:"{account_id}"}) { r2StorageAdaptiveGroups(limit:10, filter:{date_geq:"2026-10-01"}) { max { payloadSize objectCount } dimensions { bucketName } } } } }'
  cfctl graphql --file query.graphql
  cat query.graphql | cfctl graphql --variables '{"acct":"{account_id}","since":"2026-10-01"}'
  cfctl graphql --query 'query($z:String!){ viewer { zones(filter:{zoneTag:$z}) { httpRequests1dGroups(limit:7, filter:{date_gt:"2026-09-25"}) { sum { requests } dimensions { date } } } } }' --variables '{"z":"{zone_id}"}' --zone example.com
  cfctl graphql --file query.graphql --variables-file vars.json

The response ({"data":..., "errors":...}) is printed as JSON; the command
exits non-zero if it contains errors.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		query, err := resolveGraphQLQueryInput(cmd, args)
		if err != nil {
			return err
		}
		variables, err := resolveGraphQLVariablesInput(cmd)
		if err != nil {
			return err
		}
		s, err := newAPISession(cmd)
		if err != nil {
			return err
		}
		if query, err = substituteIDs(ctx, s, query); err != nil {
			return err
		}
		for k, v := range variables {
			if str, ok := v.(string); ok {
				if variables[k], err = substituteIDs(ctx, s, str); err != nil {
					return err
				}
			}
		}

		body, err := s.c.GraphQL(ctx, query, variables)
		if body != nil {
			_ = printBody(body, nil)
			var resp struct {
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			}
			if json.Unmarshal(body, &resp) == nil && len(resp.Errors) > 0 {
				var msgs []string
				for _, e := range resp.Errors {
					msgs = append(msgs, e.Message)
				}
				return fmt.Errorf("GraphQL errors: %s", strings.Join(msgs, "; "))
			}
		}
		if err != nil {
			return fmt.Errorf("GraphQL request failed: %w", err)
		}
		return nil
	},
}

// substituteIDs replaces {account_id} and {zone_id} in s, resolving each
// only if it appears.
func substituteIDs(ctx context.Context, sess *apiSession, s string) (string, error) {
	if strings.Contains(s, "{account_id}") {
		id, err := sess.account(ctx)
		if err != nil {
			return "", err
		}
		s = strings.ReplaceAll(s, "{account_id}", id)
	}
	if strings.Contains(s, "{zone_id}") {
		id, err := sess.zone(ctx)
		if err != nil {
			return "", err
		}
		s = strings.ReplaceAll(s, "{zone_id}", id)
	}
	return s, nil
}

// graphqlInputStdin is the stdin used for piped queries (swapped in tests).
var graphqlInputStdin = func() *os.File { return os.Stdin }

func resolveGraphQLQueryInput(cmd *cobra.Command, args []string) (string, error) {
	queryFlag, _ := cmd.Flags().GetString("query")
	fileFlag, _ := cmd.Flags().GetString("file")

	stdinPiped, err := isGraphQLStdinPiped()
	if err != nil {
		return "", fmt.Errorf("failed to inspect stdin: %w", err)
	}

	sources := 0
	if strings.TrimSpace(queryFlag) != "" {
		sources++
	}
	if strings.TrimSpace(fileFlag) != "" {
		sources++
	}
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		sources++
	}
	// A piped stdin only counts when nothing else was given, so scripts
	// that happen to run with a non-tty stdin still work with --query.
	if stdinPiped && sources == 0 {
		content, err := io.ReadAll(graphqlInputStdin())
		if err != nil {
			return "", fmt.Errorf("failed to read stdin query: %w", err)
		}
		query := strings.TrimSpace(string(content))
		if query == "" {
			return "", fmt.Errorf("no query provided; use a positional query, --query, --file, or pipe via stdin")
		}
		return query, nil
	}

	if sources == 0 {
		return "", fmt.Errorf("no query provided; use a positional query, --query, --file, or pipe via stdin")
	}
	if sources > 1 {
		return "", fmt.Errorf("provide only one query source: positional query, --query, --file, or stdin")
	}

	if strings.TrimSpace(queryFlag) != "" {
		return strings.TrimSpace(queryFlag), nil
	}
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return strings.TrimSpace(args[0]), nil
	}

	content, err := os.ReadFile(fileFlag)
	if err != nil {
		return "", fmt.Errorf("failed to read query file %q: %w", fileFlag, err)
	}
	query := strings.TrimSpace(string(content))
	if query == "" {
		return "", fmt.Errorf("query file %q is empty", fileFlag)
	}
	return query, nil
}

func isGraphQLStdinPiped() (bool, error) {
	info, err := graphqlInputStdin().Stat()
	if err != nil {
		return false, err
	}
	return (info.Mode() & os.ModeCharDevice) == 0, nil
}

func resolveGraphQLVariablesInput(cmd *cobra.Command) (map[string]any, error) {
	varsFlag, _ := cmd.Flags().GetString("variables")
	varsFileFlag, _ := cmd.Flags().GetString("variables-file")

	if strings.TrimSpace(varsFlag) != "" && strings.TrimSpace(varsFileFlag) != "" {
		return nil, fmt.Errorf("provide either --variables or --variables-file, not both")
	}

	raw := "{}"
	if strings.TrimSpace(varsFlag) != "" {
		raw = varsFlag
	} else if strings.TrimSpace(varsFileFlag) != "" {
		content, err := os.ReadFile(varsFileFlag)
		if err != nil {
			return nil, fmt.Errorf("failed to read variables file %q: %w", varsFileFlag, err)
		}
		raw = string(content)
	}

	variables := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &variables); err != nil {
		return nil, fmt.Errorf("variables must be a JSON object: %w", err)
	}
	return variables, nil
}

func init() {
	rootCmd.AddCommand(graphqlCmd)
	graphqlCmd.Flags().StringP("query", "q", "", "GraphQL query string")
	graphqlCmd.Flags().StringP("file", "f", "", "Path to a .graphql query file")
	graphqlCmd.Flags().String("variables", "", "Variables as a JSON object string")
	graphqlCmd.Flags().String("variables-file", "", "Path to a JSON object file of variables")
	graphqlCmd.Flags().String("zone", "", "Zone name or ID for {zone_id} (also CFCTL_ZONE)")
}
