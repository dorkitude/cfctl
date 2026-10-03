package cmd

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/apispec"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const (
	groupAPITools = "tools"
	groupAPIOps   = "ops"
)

var apiCmd = &cobra.Command{
	Use:   "api",
	Short: "Call any Cloudflare API operation (raw or generated from the OpenAPI spec)",
	Long: `Call any Cloudflare API v4 operation.

Two ways in:

  Raw:        cfctl api request <METHOD> <PATH> [--data ...] [--query k=v] [--all]
  Generated:  cfctl api <tag> <operation> [path params...] [--<query-param> ...]

The generated commands cover every operation in Cloudflare's OpenAPI spec
(` + fmt.Sprint(len(apispec.Ops())) + ` operations, one command group per OpenAPI tag). Find them with:

  cfctl api tags                     # every group
  cfctl api search dns records       # search operations
  cfctl api list --tag dns-records-for-a-zone
  cfctl api describe <tag> <operation>

{account_id} comes from your config (or --account); {zone_id} from --zone
(a zone name or ID; also CFCTL_ZONE). Output is the decoded "result" as
JSON; --raw prints the whole response envelope.

Use --read-only (or CFCTL_READONLY=1) to make cfctl refuse anything but
GET/HEAD/OPTIONS and GraphQL queries.`,
	Args: unknownSubcommand,
	// A mistyped tag with flags (`api zonez list --per-page 2`) should say
	// "unknown command" with suggestions, not "unknown flag".
	FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	RunE:               func(cmd *cobra.Command, args []string) error { return cmd.Help() },
}

var apiRequestCmd = &cobra.Command{
	Use:   "request <METHOD> <PATH>",
	Short: "Send a raw request to any Cloudflare API v4 path",
	Long: `Send a raw request to the Cloudflare API v4 with your stored credentials.

PATH is relative to https://api.cloudflare.com/client/v4 and may contain
{account_id} and {zone_id} placeholders, filled from your config (or
--account) and --zone <name-or-id>.

Output is the decoded "result" as pretty JSON. Use --raw for the whole
envelope (success, errors, messages, result, result_info), and --all to
follow pagination (page/per_page or cursor) and concatenate results.
Non-JSON responses (KV values, exports) are written as-is.

Examples:
  cfctl api request GET /zones --all
  cfctl api request GET /accounts/{account_id}/r2/buckets
  cfctl api request GET /zones/{zone_id}/dns_records --zone example.com --query type=A
  cfctl api request POST /zones/{zone_id}/dns_records --zone example.com \
      --data '{"type":"A","name":"www","content":"192.0.2.1","ttl":1}'
  cfctl api request PUT /accounts/{account_id}/workers/scripts/hello \
      --form 'metadata={"main_module":"index.js"};type=application/json' \
      --form 'index.js=@index.js;type=application/javascript+module'
  echo '{"name":"x"}' | cfctl api request POST /accounts/{account_id}/r2/buckets --data -`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		method := strings.ToUpper(args[0])
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			return fmt.Errorf("unsupported method %q", args[0])
		}
		s, err := newAPISession(cmd)
		if err != nil {
			return err
		}
		rawPath, rawQuery, _ := strings.Cut(args[1], "?")
		path, err := s.fillPath(ctx, rawPath, nil)
		if err != nil {
			return err
		}
		q, err := url.ParseQuery(rawQuery)
		if err != nil {
			return fmt.Errorf("invalid query string in path: %w", err)
		}
		pairs, _ := cmd.Flags().GetStringArray("query")
		if err := parseKV(pairs, q); err != nil {
			return err
		}
		hs, _ := cmd.Flags().GetStringArray("header")
		headers, err := parseHeaders(hs)
		if err != nil {
			return err
		}
		body, ct, err := readBody(cmd, headers.Get("Content-Type"))
		if err != nil {
			return err
		}
		req := api.Request{Method: method, Path: path, Query: q, Header: headers, Body: body, ContentType: ct}
		return execRequest(ctx, s, req, readRequestOptions(cmd))
	},
}

var apiListCmd = &cobra.Command{
	Use:   "list",
	Short: "List generated API operations",
	Long: `List every generated API operation, optionally filtered.

Examples:
  cfctl api list --tag r2-bucket
  cfctl api list --method delete
  cfctl api list --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tag, _ := cmd.Flags().GetString("tag")
		method, _ := cmd.Flags().GetString("method")
		var out []apispec.Op
		for _, o := range apispec.Ops() {
			if tag != "" && !strings.EqualFold(o.TagSlug, tag) && !strings.EqualFold(o.Tag, tag) {
				continue
			}
			if method != "" && !strings.EqualFold(o.Method, method) {
				continue
			}
			out = append(out, o)
		}
		if tag != "" && len(out) == 0 && method == "" {
			return fmt.Errorf("no tag %q; see 'cfctl api tags'", tag)
		}
		return printOps(out)
	},
}

var apiSearchCmd = &cobra.Command{
	Use:   "search <words...>",
	Short: "Search API operations by operationId, summary, path, or tag",
	Long: `Search API operations. Every word must match (case-insensitive) the
operationId, summary, path, tag, or command name.

Examples:
  cfctl api search r2 bucket
  cfctl api search dns_records
  cfctl api search workers script delete`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return printOps(apispec.Search(args))
	},
}

func printOps(ops []apispec.Op) error {
	type row struct {
		Command     string `json:"command"`
		Method      string `json:"method"`
		Path        string `json:"path"`
		Summary     string `json:"summary"`
		OperationID string `json:"operation_id"`
		Tag         string `json:"tag"`
		Deprecated  bool   `json:"deprecated,omitempty"`
	}
	rows := make([]row, 0, len(ops))
	for _, o := range ops {
		rows = append(rows, row{Command: o.TagSlug + " " + o.Slug, Method: o.Method, Path: o.Path, Summary: o.Summary, OperationID: o.ID, Tag: o.Tag, Deprecated: o.Deprecated})
	}
	if printJSON(rows) {
		return nil
	}
	if len(rows) == 0 {
		fmt.Println(ui.Warn("No matching operations"))
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		dep := ""
		if r.Deprecated {
			dep = " (deprecated)"
		}
		fmt.Fprintf(tw, "%s\t%s %s\t%s%s\n", r.Command, r.Method, r.Path, r.Summary, dep)
	}
	tw.Flush()
	fmt.Fprintln(os.Stderr, ui.SubtleStyle.Render(fmt.Sprintf("%d operations; run as: cfctl api <command> ...", len(rows))))
	return nil
}

var apiTagsCmd = &cobra.Command{
	Use:   "tags",
	Short: "List API command groups (OpenAPI tags)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		tags := apispec.Tags()
		if printJSON(tags) {
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, t := range tags {
			fmt.Fprintf(tw, "%s\t%d\t%s\n", t.Slug, t.Ops, t.Name)
		}
		tw.Flush()
		fmt.Fprintln(os.Stderr, ui.SubtleStyle.Render(fmt.Sprintf("%d groups, %d operations", len(tags), len(apispec.Ops()))))
		return nil
	},
}

var apiDescribeCmd = &cobra.Command{
	Use:   "describe <tag> <operation> | <operationId>",
	Short: "Show an operation's parameters and request body schema",
	Long: `Show an operation's method, path, parameters, and request body schema
(with $refs resolved and an example skeleton for --data).

Examples:
  cfctl api describe dns-records-for-a-zone create-dns-record
  cfctl api describe dns-records-for-a-zone-create-dns-record`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		o, err := findOp(args)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printJSONValue(o)
		}
		out, err := apispec.Describe(o, commandFor(o))
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	},
}

// unknownSubcommand rejects arguments to a command group with suggestions.
func unknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	cmd.SuggestionsMinimumDistance = 3
	msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
	if sugg := cmd.SuggestionsFor(args[0]); len(sugg) > 0 {
		if len(sugg) > 5 {
			sugg = sugg[:5]
		}
		msg += "; did you mean " + strings.Join(sugg, ", ") + "?"
	}
	return fmt.Errorf("%s (see 'cfctl api tags' or 'cfctl api search <words>')", msg)
}

func commandFor(o *apispec.Op) string {
	parts := []string{BinName(), "api", o.TagSlug, o.Slug}
	for _, p := range o.PathParams() {
		if isAccountParam(p.Name) || isZoneParam(p.Name) {
			continue
		}
		parts = append(parts, "<"+p.Name+">")
	}
	for _, p := range o.PathParams() {
		if isZoneParam(p.Name) {
			parts = append(parts, "--zone <zone>")
			break
		}
	}
	return strings.Join(parts, " ")
}

func findOp(args []string) (*apispec.Op, error) {
	if len(args) == 2 {
		if o, ok := apispec.Find(args[0], args[1]); ok {
			return o, nil
		}
		return nil, fmt.Errorf("no operation %q %q; try 'cfctl api search'", args[0], args[1])
	}
	if o, ok := apispec.ByID(args[0]); ok {
		return o, nil
	}
	// Accept "tag/op" or "tag op" in one arg.
	if t, op, ok := strings.Cut(strings.ReplaceAll(args[0], "/", " "), " "); ok {
		if o, ok := apispec.Find(t, op); ok {
			return o, nil
		}
	}
	return nil, fmt.Errorf("no operation with operationId %q; try 'cfctl api search'", args[0])
}

var apiSpecInfoCmd = &cobra.Command{
	Use:   "spec-info",
	Short: "Show which Cloudflare OpenAPI spec cfctl embeds",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		methods := map[string]int{}
		for _, o := range apispec.Ops() {
			methods[o.Method]++
		}
		info := map[string]any{
			"source":          apispec.SpecRepo,
			"commit":          apispec.SpecCommit,
			"date":            apispec.SpecDate,
			"openapi":         apispec.SpecOpenAPI,
			"api_version":     apispec.SpecVersion,
			"operations":      len(apispec.Ops()),
			"tags":            len(apispec.Tags()),
			"methods":         methods,
			"embedded_gzip_b": apispec.SpecSize(),
		}
		if printJSON(info) {
			return nil
		}
		fmt.Println(ui.TitleStyle.Render("📜 Cloudflare OpenAPI spec"))
		fmt.Printf("  %-14s %s\n", "Source:", apispec.SpecRepo)
		fmt.Printf("  %-14s %s\n", "Commit:", apispec.SpecCommit)
		fmt.Printf("  %-14s %s\n", "Date:", apispec.SpecDate)
		fmt.Printf("  %-14s %s (API %s)\n", "OpenAPI:", apispec.SpecOpenAPI, apispec.SpecVersion)
		fmt.Printf("  %-14s %d in %d groups\n", "Operations:", len(apispec.Ops()), len(apispec.Tags()))
		var ms []string
		for m, n := range methods {
			ms = append(ms, fmt.Sprintf("%s %d", m, n))
		}
		sort.Strings(ms)
		fmt.Printf("  %-14s %s\n", "Methods:", strings.Join(ms, ", "))
		return nil
	},
}

var apiSpecCmd = &cobra.Command{
	Use:   "spec",
	Short: "Print the embedded OpenAPI spec (JSON)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return apispec.SpecJSON(os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(apiCmd)
	apiCmd.PersistentFlags().String("zone", "", "Zone name or ID for {zone_id} (also CFCTL_ZONE)")
	apiCmd.AddGroup(
		&cobra.Group{ID: groupAPITools, Title: "Raw requests and discovery:"},
		&cobra.Group{ID: groupAPIOps, Title: "Generated operation groups (one per OpenAPI tag; see 'cfctl api tags'):"},
	)
	for _, c := range []*cobra.Command{apiRequestCmd, apiListCmd, apiSearchCmd, apiTagsCmd, apiDescribeCmd, apiSpecInfoCmd, apiSpecCmd} {
		c.GroupID = groupAPITools
		apiCmd.AddCommand(c)
	}
	addRequestFlags(apiRequestCmd, true)
	apiListCmd.Flags().String("tag", "", "Only operations in this group (slug or tag name)")
	apiListCmd.Flags().String("method", "", "Only operations with this HTTP method")

	addGeneratedCommands(apiCmd)
}
