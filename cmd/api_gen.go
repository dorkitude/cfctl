package cmd

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/apispec"
	"github.com/spf13/cobra"
)

// Annotation keys on generated commands.
const (
	annOperationID = "cfctl/operation-id"
)

// reservedFlags can't be used for query parameters: they're cfctl's own
// flags on generated commands or inherited from parents. A query parameter
// with one of these names becomes --param-<name>.
var reservedFlags = map[string]bool{
	"data": true, "form": true, "query": true, "header": true, "all": true, "max-pages": true,
	"raw": true, "yes": true, "content-type": true, "zone": true, "json": true, "account": true,
	"no-color": true, "debug": true, "read-only": true, "timeout": true, "help": true, "version": true,
}

var (
	flagNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	flagCharRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// queryFlag maps a CLI flag to the API query parameter it sets.
type queryFlag struct {
	flag  string
	param apispec.Param
}

// queryFlags assigns a flag name to each query parameter: per_page →
// --per-page; names clashing with cfctl flags → --param-<name>. Parameters
// whose names can't be flags (e.g. "meta.<field>[<operator>]") get none and
// are set with --query.
func queryFlags(o *apispec.Op) (flags []queryFlag, unmapped []apispec.Param) {
	used := map[string]bool{}
	for _, p := range o.QueryParams() {
		// "subject~neq" → --subject-neq; templates like "meta.<field>[<op>]"
		// can't be a fixed flag.
		name := strings.Trim(flagCharRE.ReplaceAllString(strings.ReplaceAll(p.Name, "_", "-"), "-"), "-")
		if strings.Contains(p.Name, "<") || !flagNameRE.MatchString(name) {
			unmapped = append(unmapped, p)
			continue
		}
		if reservedFlags[name] || used[name] {
			name = "param-" + name
		}
		if used[name] {
			name = "param-" + p.Name
		}
		if used[name] {
			unmapped = append(unmapped, p)
			continue
		}
		used[name] = true
		flags = append(flags, queryFlag{flag: name, param: p})
	}
	return flags, unmapped
}

// positionalParams are the path params given as arguments (all but
// {account_id} and {zone_id}, which come from config and --zone).
func positionalParams(o *apispec.Op) []apispec.Param {
	var out []apispec.Param
	for _, p := range o.PathParams() {
		if !isAccountParam(p.Name) && !isZoneParam(p.Name) {
			out = append(out, p)
		}
	}
	return out
}

func usesZone(o *apispec.Op) bool {
	for _, p := range o.PathParams() {
		if isZoneParam(p.Name) {
			return true
		}
	}
	return false
}

// Generated tag commands are created at startup; their operation commands
// are created on demand by prepareCommands, because building all ~3,600 of
// them (with flags and help) costs ~60 ms and ~70 MB of allocations.
var (
	tagCommands = map[string]*cobra.Command{}
	tagOps      = map[string][]*apispec.Op{}
	tagBuilt    = map[string]bool{}
)

// addGeneratedCommands adds one command group per OpenAPI tag under parent
// (cfctl api). Operation commands are added by populateTag.
func addGeneratedCommands(parent *cobra.Command) {
	all := apispec.Ops()
	for i := range all {
		o := &all[i]
		if _, ok := tagCommands[o.TagSlug]; !ok {
			slug := o.TagSlug
			tc := &cobra.Command{
				Use:     slug,
				Short:   o.Tag,
				GroupID: groupAPIOps,
				Args:    unknownSubcommand,
				// A mistyped operation with flags (`api <tag> typo --raw`)
				// should say "unknown command", not "unknown flag: --raw".
				FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
				Long: fmt.Sprintf("Generated commands for the Cloudflare API tag %q.\n\n"+
					"Each command maps to one API operation; see 'cfctl api describe %s <command>'.", o.Tag, slug),
				RunE: func(cmd *cobra.Command, args []string) error {
					populateTag(slug)
					return cmd.Help()
				},
			}
			tagCommands[slug] = tc
			parent.AddCommand(tc)
		}
		tagOps[o.TagSlug] = append(tagOps[o.TagSlug], o)
	}
	for slug, tc := range tagCommands {
		tc.Short = fmt.Sprintf("%s (%d)", tc.Short, len(tagOps[slug]))
	}
}

// populateTag creates the operation commands of one tag (idempotent).
func populateTag(slug string) {
	tc, ok := tagCommands[slug]
	if !ok || tagBuilt[slug] {
		return
	}
	tagBuilt[slug] = true
	for _, o := range tagOps[slug] {
		tc.AddCommand(newOpCommand(o))
	}
}

// populateAllTags creates every operation command (tests, docs).
func populateAllTags() {
	for slug := range tagCommands {
		populateTag(slug)
	}
}

// prepareCommands creates the operation commands an invocation can reach:
// those of any tag named after an "api" argument.
func prepareCommands(args []string) {
	for i, a := range args {
		if a != "api" {
			continue
		}
		for _, b := range args[i+1:] {
			if _, ok := tagCommands[b]; ok {
				populateTag(b)
			}
		}
		return
	}
}

func newOpCommand(o *apispec.Op) *cobra.Command {
	pos := positionalParams(o)
	qflags, unmapped := queryFlags(o)

	use := o.Slug
	for _, p := range pos {
		use += " <" + p.Name + ">"
	}
	short := o.Summary
	if o.Deprecated {
		short = "[deprecated] " + short
	}

	var long strings.Builder
	fmt.Fprintf(&long, "%s\n\n  %s %s\n  operationId: %s\n", o.Summary, o.Method, o.Path, o.ID)
	if o.Deprecated {
		long.WriteString("  Deprecated.\n")
	}
	if o.Desc != "" && o.Desc != o.Summary {
		fmt.Fprintf(&long, "\n%s\n", o.Desc)
	}
	if len(o.PathParams()) > 0 {
		long.WriteString("\nPath parameters:\n")
		for _, p := range o.PathParams() {
			switch {
			case isAccountParam(p.Name):
				fmt.Fprintf(&long, "  %-22s from your config or --account\n", p.Name)
			case isZoneParam(p.Name):
				fmt.Fprintf(&long, "  %-22s from --zone <name-or-id>\n", p.Name)
			default:
				fmt.Fprintf(&long, "  %-22s argument, %s%s\n", "<"+p.Name+">", p.Type, dashDesc(p.Desc))
			}
		}
	}
	if hs := o.HeaderParams(); len(hs) > 0 {
		long.WriteString("\nHeader parameters (pass with --header 'Name: value'):\n")
		for _, p := range hs {
			req := ""
			if p.Required {
				req = " (required)"
			}
			fmt.Fprintf(&long, "  %-22s %s%s%s\n", p.Name, p.Type, req, dashDesc(p.Desc))
		}
	}
	if len(unmapped) > 0 {
		long.WriteString("\nOther query parameters (pass with --query key=value):\n")
		for _, p := range unmapped {
			fmt.Fprintf(&long, "  %-22s %s%s\n", p.Name, p.Type, dashDesc(p.Desc))
		}
	}
	if o.HasBody() {
		req := "optional"
		if o.BodyRequired {
			req = "required"
		}
		fmt.Fprintf(&long, "\nRequest body (%s): %s\n", req, strings.Join(o.BodyTypes, ", "))
		if contains(o.BodyTypes, "multipart/form-data") {
			long.WriteString("  Pass --form name=value / name=@file[;type=mime], or --data.\n")
		} else {
			long.WriteString("  Pass --data '<json>', --data @file, or --data - (stdin).\n")
		}
		fmt.Fprintf(&long, "  Schema and example: %s api describe %s %s\n", BinName(), o.TagSlug, o.Slug)
	}
	if o.Method == "DELETE" || o.Confirm {
		long.WriteString("\nAsks for confirmation; pass --yes to skip.\n")
	}

	c := &cobra.Command{
		Use:         use,
		Short:       short,
		Long:        long.String(),
		Args:        exactArgsNamed(pos),
		Annotations: map[string]string{annOperationID: o.ID},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOp(cmd, o, args, qflags)
		},
	}
	for _, qf := range qflags {
		p := qf.param
		// pflag treats the first `quoted` word as the value name; descriptions
		// use backticks for code, so swap them for quotes.
		usage := strings.ReplaceAll(p.Desc, "`", "'")
		if p.Required {
			usage = "(required) " + usage
		}
		if t := strings.TrimPrefix(strings.TrimSuffix(p.Type, ">"), "array<"); t != "string" {
			usage += " (" + t + ")"
		}
		if len(p.Enum) > 0 {
			usage += " [" + strings.Join(p.Enum, "|") + "]"
		}
		if p.Default != "" {
			usage += " (API default " + p.Default + ")"
		}
		if qf.flag != p.Name {
			usage += " {query: " + p.Name + "}"
		}
		if p.IsArray() {
			c.Flags().StringArray(qf.flag, nil, usage+" (repeatable)")
		} else {
			c.Flags().String(qf.flag, "", usage)
			if p.Type == "boolean" {
				c.Flags().Lookup(qf.flag).NoOptDefVal = "true"
			}
		}
	}
	addRequestFlags(c, o.HasBody())
	if o.HasBody() {
		c.Flags().String("content-type", "", "Request body content type (default "+o.BodyTypes[0]+")")
	}
	if o.Method == "DELETE" || o.Confirm {
		c.Flags().BoolP("yes", "y", false, "Don't ask for confirmation")
	}
	if o.Method != "GET" {
		_ = c.Flags().MarkHidden("all")
		_ = c.Flags().MarkHidden("max-pages")
	}
	return c
}

func dashDesc(d string) string {
	if d == "" {
		return ""
	}
	return " — " + d
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func exactArgsNamed(pos []apispec.Param) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == len(pos) {
			return nil
		}
		var names []string
		for _, p := range pos {
			names = append(names, "<"+p.Name+">")
		}
		if len(pos) == 0 {
			return fmt.Errorf("%s takes no arguments, got %d", cmd.CommandPath(), len(args))
		}
		return fmt.Errorf("%s needs %d argument(s): %s (got %d)", cmd.CommandPath(), len(pos), strings.Join(names, " "), len(args))
	}
}

func runOp(cmd *cobra.Command, o *apispec.Op, args []string, qflags []queryFlag) error {
	ctx := context.Background()
	opts := readRequestOptions(cmd)
	if opts.all && o.Method != "GET" {
		return fmt.Errorf("--all only works with GET operations")
	}

	vals := map[string]string{}
	for i, p := range positionalParams(o) {
		if strings.TrimSpace(args[i]) == "" {
			return fmt.Errorf("<%s> must not be empty", p.Name)
		}
		vals[p.Name] = args[i]
	}

	q := url.Values{}
	extra, _ := cmd.Flags().GetStringArray("query")
	if err := parseKV(extra, q); err != nil {
		return err
	}
	for _, qf := range qflags {
		fl := cmd.Flags().Lookup(qf.flag)
		p := qf.param
		if !fl.Changed {
			if p.Required && !q.Has(p.Name) {
				return fmt.Errorf("missing required flag --%s (query parameter %s)", qf.flag, p.Name)
			}
			continue
		}
		var values []string
		if p.IsArray() {
			values, _ = cmd.Flags().GetStringArray(qf.flag)
		} else {
			v, _ := cmd.Flags().GetString(qf.flag)
			values = []string{v}
		}
		for _, v := range values {
			if len(p.Enum) > 0 && !contains(p.Enum, v) {
				return fmt.Errorf("invalid --%s %q: must be one of %s (use --query %s=... to bypass)", qf.flag, v, strings.Join(p.Enum, ", "), p.Name)
			}
			q.Add(p.Name, v)
		}
	}

	hs, _ := cmd.Flags().GetStringArray("header")
	headers, err := parseHeaders(hs)
	if err != nil {
		return err
	}
	for _, p := range o.HeaderParams() {
		if p.Required && headers.Get(p.Name) == "" {
			return fmt.Errorf("missing required header: --header '%s: <value>'", p.Name)
		}
	}

	var body []byte
	var ct string
	if o.HasBody() {
		defCT := headers.Get("Content-Type")
		if v, _ := cmd.Flags().GetString("content-type"); v != "" {
			defCT = v
		}
		if defCT == "" {
			defCT = o.BodyTypes[0]
		}
		body, ct, err = readBody(cmd, defCT)
		if err != nil {
			return err
		}
		if body == nil && o.BodyRequired {
			return fmt.Errorf("this operation needs a request body: pass --data (see '%s api describe %s %s')", BinName(), o.TagSlug, o.Slug)
		}
	}

	if o.Method == "DELETE" || o.Confirm {
		if err := confirm(cmd, o.Method+" "+o.Path+" ("+o.Summary+")"); err != nil {
			return err
		}
	}

	s, err := newAPISession(cmd)
	if err != nil {
		return err
	}
	if usesZone(o) && s.zoneArg == "" {
		return fmt.Errorf("this operation needs a zone: pass --zone <name-or-id>")
	}
	path, err := s.fillPath(ctx, o.Path, vals)
	if err != nil {
		return err
	}
	req := api.Request{Method: o.Method, Path: path, Query: q, Header: headers, Body: body, ContentType: ct}
	return execRequest(ctx, s, req, opts)
}
