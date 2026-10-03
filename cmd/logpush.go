package cmd

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var logpushCols = []col{{"ID", "id"}, {"Name", "name"}, {"Dataset", "dataset"}, {"On", "enabled"}, {"Destination", "destination_conf"}, {"Last complete", "last_complete"}, {"Last error", "last_error"}}

var logpushCmd = group("logpush", "Logpush jobs (zone with --zone, else account)", `Logpush jobs push logs (http_requests, firewall_events, workers_trace_events,
audit_logs, ...) to R2, S3, HTTP endpoints, and other destinations.

Examples:
  cfctl logpush jobs list --zone example.com
  cfctl logpush jobs list
  cfctl logpush jobs get <job-id> --zone example.com
  cfctl logpush jobs create --zone example.com --name http-to-r2 --dataset http_requests \
      --destination 'r2://logs/http/{DATE}?account-id=...&access-key-id=...&secret-access-key=...' \
      --fields ClientIP,ClientRequestHost,EdgeResponseStatus
  cfctl logpush jobs delete <job-id> --zone example.com
  cfctl logpush fields http_requests --zone example.com`, nil,
	group("jobs", "Logpush jobs", "", []string{"job"},
		readSpec{Use: "list", Short: "List Logpush jobs", Scope: scopeEither, Path: "/zones/{zone_id}/logpush/jobs", AcctPath: "/accounts/{account_id}/logpush/jobs", Title: "Logpush jobs", Cols: logpushCols, Feature: "Logpush", Transform: redactLogpush}.build(),
		readSpec{Use: "get <job-id>", Short: "Show a Logpush job", Scope: scopeEither, Path: "/zones/{zone_id}/logpush/jobs/{job_id}", AcctPath: "/accounts/{account_id}/logpush/jobs/{job_id}", Args: []string{"job_id"}, Title: "Logpush job", Feature: "Logpush", Transform: redactLogpush,
			Fields: []col{{"ID", "id"}, {"Name", "name"}, {"Dataset", "dataset"}, {"Enabled", "enabled"}, {"Frequency", "frequency"}, {"Kind", "kind"}, {"Fields", "output_options.field_names"}, {"Filter", "filter"}, {"Last complete", "last_complete"}, {"Last error", "last_error"}, {"Error message", "error_message"}}}.build(),
		writeSpec{
			Use: "create", Short: "Create a Logpush job", Method: "POST", Scope: scopeEither, Path: "/zones/{zone_id}/logpush/jobs", AcctPath: "/accounts/{account_id}/logpush/jobs",
			Body: func(c *cobra.Command, args []string) (any, error) {
				name, _ := c.Flags().GetString("name")
				ds, _ := c.Flags().GetString("dataset")
				dest, _ := c.Flags().GetString("destination")
				if !c.Flags().Changed("data") && (ds == "" || dest == "") {
					return nil, fmt.Errorf("pass --dataset and --destination (or --data)")
				}
				b := map[string]any{}
				setMap(b, "name", name)
				setMap(b, "dataset", ds)
				setMap(b, "destination_conf", dest)
				if f, _ := c.Flags().GetStringArray("fields"); len(f) > 0 {
					b["output_options"] = map[string]any{"field_names": splitList(f)}
				}
				if f, _ := c.Flags().GetString("filter"); f != "" {
					b["filter"] = f
				}
				if tok, _ := c.Flags().GetString("ownership-challenge"); tok != "" {
					b["ownership_challenge"] = tok
				}
				en, _ := c.Flags().GetBool("enabled")
				b["enabled"] = en
				return b, nil
			},
			DataFlag: true,
			Flags: func(c *cobra.Command) {
				c.Flags().String("name", "", "Job name")
				c.Flags().String("dataset", "", "Dataset: http_requests, firewall_events, dns_logs, workers_trace_events, audit_logs, ...")
				c.Flags().String("destination", "", "destination_conf (r2://, s3://, https://, ...)")
				c.Flags().StringArray("fields", nil, "Fields to include (repeatable or comma-separated)")
				c.Flags().String("filter", "", "Filter JSON string")
				c.Flags().String("ownership-challenge", "", "Ownership challenge token (needed by some destinations)")
				c.Flags().Bool("enabled", true, "Enable the job")
			},
			Done: "Logpush job created", Feature: "Logpush",
		}.build(),
		writeSpec{Use: "update <job-id>", Short: "Update a Logpush job (--data JSON)", Method: "PUT", Scope: scopeEither, Path: "/zones/{zone_id}/logpush/jobs/{job_id}", AcctPath: "/accounts/{account_id}/logpush/jobs/{job_id}", Args: []string{"job_id"}, DataFlag: true, Done: "Logpush job %s updated", Feature: "Logpush"}.build(),
		writeSpec{Use: "delete <job-id>", Short: "Delete a Logpush job", Method: "DELETE", Scope: scopeEither, Path: "/zones/{zone_id}/logpush/jobs/{job_id}", AcctPath: "/accounts/{account_id}/logpush/jobs/{job_id}", Args: []string{"job_id"}, Confirm: "delete Logpush job %s", Done: "Logpush job %s deleted", Feature: "Logpush"}.build(),
	),
	readSpec{Use: "fields <dataset>", Short: "List the fields available in a dataset", Scope: scopeEither, Path: "/zones/{zone_id}/logpush/datasets/{dataset_id}/fields", AcctPath: "/accounts/{account_id}/logpush/datasets/{dataset_id}/fields", Args: []string{"dataset_id"}, Title: "Fields", Feature: "Logpush"}.build(),
)

// redactLogpush hides credentials embedded in destination_conf query
// strings (secret-access-key, SAS tokens, Authorization headers, ...).
func redactLogpush(v any) any {
	redact := func(it any) {
		m, ok := it.(map[string]any)
		if !ok {
			return
		}
		if d, ok := m["destination_conf"].(string); ok {
			m["destination_conf"] = redactDestination(d)
		}
	}
	if a, ok := v.([]any); ok {
		for _, it := range a {
			redact(it)
		}
	} else {
		redact(v)
	}
	return v
}

var secretParamRE = regexp.MustCompile(`(?i)(secret|key|token|sig|auth|password|credential|header_)`)

func redactDestination(d string) string {
	base, query, ok := strings.Cut(d, "?")
	if !ok {
		if u, err := url.Parse(d); err == nil && u.User != nil {
			u.User = url.User("REDACTED")
			return u.String()
		}
		return d
	}
	var parts []string
	for _, kv := range strings.Split(query, "&") {
		k, _, _ := strings.Cut(kv, "=")
		if secretParamRE.MatchString(k) {
			kv = k + "=REDACTED"
		}
		parts = append(parts, kv)
	}
	return base + "?" + strings.Join(parts, "&")
}

func setMap(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func init() { rootCmd.AddCommand(logpushCmd) }
