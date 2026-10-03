package cmd

// Shared kit for the zone and account administration commands (settings,
// cache, ssl, rulesets, waf, lb, accounts, tokens, logpush, ...). These are
// thin, declarative wrappers over the raw request layer (internal/api): every
// request goes through the read-only guard, and API errors get a friendly
// hint for the common "token lacks permission" / "plan not entitled" cases.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// col is one output column (table) or field (detail view): a header and a
// dotted JSON path into the item ("settings.value", "origins.0.address").
// "#len:<path>" renders the length of an array, "#names:<path>" joins the
// "name" fields of an array of objects.
type col struct{ H, P string }

// adminSession builds a raw-client session; zone (a name or ID) may be "".
func adminSession(cmd *cobra.Command, zone string) (*apiSession, error) {
	s, err := newAPISession(cmd)
	if err != nil {
		return nil, err
	}
	if zone != "" {
		s.zoneArg = zone
	}
	return s, nil
}

// friendly adds a hint to API errors that usually mean "missing token
// permission" or "not available on this plan/account". The original message
// is kept; nothing from the request (headers, token) is included.
func friendly(feature string, err error) error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	if !errors.As(err, &ae) {
		return err
	}
	msg := strings.ToLower(ae.Error())
	hint := ""
	switch {
	case strings.Contains(msg, "not support account owned tokens"):
		hint = "this endpoint needs a user-owned API token (My Profile → API Tokens), not an account-owned one"
	case strings.Contains(msg, "valid user-level authentication not found") || strings.Contains(msg, "authentication failed (status: 400)"):
		hint = "this is a /user endpoint: it needs a user-owned API token, not an account-owned one"
	case strings.Contains(msg, "not_enabled") || strings.Contains(msg, "not enabled"):
		hint = feature + " is not enabled on this account (turn it on in the dashboard first)"
	case strings.Contains(msg, "plan") || strings.Contains(msg, "entitle") || strings.Contains(msg, "not allowed") || strings.Contains(msg, "subscription"):
		hint = feature + " is not available on this zone's or account's plan"
	case ae.Status == 403:
		hint = "the token lacks permission for " + feature + ", or the account isn't entitled to it; check the token's permission groups ('cfctl tokens permission-groups')"
	case ae.Status == 401:
		hint = "the token is invalid or expired; run 'cfctl auth status'"
	}
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w\n  hint: %s", err, hint)
}

// fetch GETs one endpoint and returns its result; with all, it follows
// pagination.
func fetch(ctx context.Context, s *apiSession, path string, vals map[string]string, q url.Values, all bool, feature string) (json.RawMessage, error) {
	p, err := s.fillPath(ctx, path, vals)
	if err != nil {
		return nil, err
	}
	req := api.Request{Method: "GET", Path: p, Query: q}
	if all {
		res, err := s.c.All(ctx, req, api.DefaultMaxPages)
		if err != nil {
			return nil, friendly(feature, err)
		}
		if res.Truncated {
			fmt.Fprintln(os.Stderr, ui.Warn("stopped after the page limit; results are incomplete"))
		}
		return res.Result, nil
	}
	resp, err := s.c.Do(ctx, req)
	if err != nil {
		return nil, friendly(feature, err)
	}
	if resp.Envelope == nil {
		return resp.Body, nil
	}
	return resp.Envelope.Result, nil
}

// send makes a write request with a JSON body (nil for none) and returns
// the result.
func send(ctx context.Context, s *apiSession, method, path string, vals map[string]string, q url.Values, body any, feature string) (json.RawMessage, error) {
	p, err := s.fillPath(ctx, path, vals)
	if err != nil {
		return nil, err
	}
	req := api.Request{Method: method, Path: p, Query: q}
	switch b := body.(type) {
	case nil:
	case []byte:
		req.Body, req.ContentType = b, "application/json"
	case json.RawMessage:
		req.Body, req.ContentType = b, "application/json"
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		req.Body, req.ContentType = data, "application/json"
	}
	resp, err := s.c.Do(ctx, req)
	if err != nil {
		return nil, friendly(feature, err)
	}
	if resp.Envelope == nil {
		return resp.Body, nil
	}
	return resp.Envelope.Result, nil
}

// decodeAny parses JSON into generic values (numbers as json.Number).
func decodeAny(raw []byte) any {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return nil
	}
	return v
}

// jget walks a dotted path ("a.b.0.c") through generic JSON values.
func jget(v any, path string) any {
	if path == "" || path == "." {
		return v
	}
	for _, part := range strings.Split(path, ".") {
		switch t := v.(type) {
		case map[string]any:
			v = t[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(t) {
				return nil
			}
			v = t[i]
		default:
			return nil
		}
	}
	return v
}

// jstr renders a value for a table cell.
func jstr(v any, path string) string {
	if n, ok := strings.CutPrefix(path, "#len:"); ok {
		if a, ok := jget(v, n).([]any); ok {
			return strconv.Itoa(len(a))
		}
		return "0"
	}
	if n, ok := strings.CutPrefix(path, "#names:"); ok {
		a, _ := jget(v, n).([]any)
		var names []string
		for _, e := range a {
			names = append(names, jstr(e, "name"))
		}
		return strings.Join(names, ", ")
	}
	// "a|b": the first of several paths that has a value (for APIs whose
	// field names changed between versions).
	if alts := strings.Split(path, "|"); len(alts) > 1 {
		for _, a := range alts {
			if s := jstr(v, a); s != "" {
				return s
			}
		}
		return ""
	}
	return fmtVal(jget(v, path))
}

func fmtVal(x any) string {
	switch t := x.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		allScalar := true
		var parts []string
		for _, e := range t {
			switch e.(type) {
			case map[string]any, []any:
				allScalar = false
			}
			parts = append(parts, fmtVal(e))
		}
		if allScalar {
			return strings.Join(parts, ", ")
		}
	}
	b, _ := json.Marshal(x)
	return string(b)
}

// asList turns a result into a list of items: an array, or the first array
// field of an object (some lists are wrapped), or a single object.
func asList(raw json.RawMessage, key string) []any {
	v := decodeAny(raw)
	if key != "" {
		v = jget(v, key)
	}
	switch t := v.(type) {
	case []any:
		return t
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if a, ok := t[k].([]any); ok {
				return a
			}
		}
		return []any{t}
	}
	return nil
}

// printTable prints items as an aligned table under a title.
func printTable(title string, items []any, cols []col) {
	if len(items) == 0 {
		fmt.Println(ui.Warn("No " + strings.ToLower(title) + " found"))
		return
	}
	fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("%s (%d)", title, len(items))))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	var hs []string
	for _, c := range cols {
		hs = append(hs, strings.ToUpper(c.H))
	}
	fmt.Fprintln(tw, "  "+strings.Join(hs, "\t"))
	for _, it := range items {
		var cells []string
		for _, c := range cols {
			cell := strings.ReplaceAll(jstr(it, c.P), "\n", " ")
			cells = append(cells, truncate(cell, 70))
		}
		fmt.Fprintln(tw, "  "+strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
}

// printDetail prints one object as label: value lines. With no fields, every
// top-level field is shown (sorted), objects as compact JSON.
func printDetail(title string, obj any, fields []col) {
	fmt.Println(ui.TitleStyle.Render(title))
	if fields == nil {
		m, ok := obj.(map[string]any)
		if !ok {
			fmt.Println("  " + fmtVal(obj))
			return
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fields = append(fields, col{k, k})
		}
	}
	w := 0
	for _, f := range fields {
		if len(f.H) > w {
			w = len(f.H)
		}
	}
	for _, f := range fields {
		v := jstr(obj, f.P)
		if v == "" {
			continue
		}
		fmt.Printf("  %-*s  %s\n", w+1, f.H+":", v)
	}
}

// emit prints raw as JSON for --json, else calls human.
func emit(raw json.RawMessage, human func(v any)) error {
	if jsonOutput {
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = json.RawMessage("null") // --json always prints a JSON value
		}
		return printBody(raw, nil)
	}
	human(decodeAny(raw))
	return nil
}

// isEmptyJSON reports whether raw is missing or JSON null.
func isEmptyJSON(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// --- declarative read/write commands --------------------------------------

// scope says where a command's resource lives.
type scope int

const (
	scopeNone    scope = iota
	scopeZone          // first positional argument is the zone (name or ID)
	scopeAccount       // {account_id} from --account / config
	scopeEither        // --zone <zone> for zone level, else account level
)

// readSpec declares a list or get command.
type readSpec struct {
	Use, Short, Long string
	Aliases          []string
	Scope            scope
	// Path is the endpoint; for scopeEither, AcctPath is the account form.
	Path, AcctPath string
	// Args are positional placeholders after the zone, e.g. {"ruleset_id"}.
	Args []string
	// Title is the heading; Cols makes a table (list), Fields a detail view.
	Title  string
	Cols   []col
	Fields []col
	// ListKey picks the array out of an object result ("" = auto).
	ListKey string
	// Paginate follows every page (and adds --page/--per-page).
	Paginate bool
	// Feature names the product for friendly errors.
	Feature string
	// Flags adds flags; Query turns them into query parameters.
	Flags func(c *cobra.Command)
	Query func(c *cobra.Command, q url.Values) error
	// Human, when set, replaces the default rendering.
	Human func(v any)
	// Transform, when set, rewrites the result before any output (e.g. to
	// redact secrets); it applies to --json too.
	Transform func(v any) any
}

func (r readSpec) build() *cobra.Command {
	nargs := len(r.Args)
	if r.Scope == scopeZone {
		nargs++
	}
	c := &cobra.Command{
		Use:     r.Use,
		Short:   r.Short,
		Long:    r.Long,
		Aliases: r.Aliases,
		Args:    cobra.ExactArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
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
			all := r.Paginate
			if r.Paginate {
				if p, _ := cmd.Flags().GetInt("page"); p > 0 {
					q.Set("page", strconv.Itoa(p))
					all = false
				}
				if pp, _ := cmd.Flags().GetInt("per-page"); pp > 0 {
					q.Set("per_page", strconv.Itoa(pp))
				}
			}
			raw, err := fetch(ctx, s, path, vals, q, all, r.Feature)
			if err != nil {
				return err
			}
			if r.Transform != nil {
				raw, _ = json.Marshal(r.Transform(decodeAny(raw)))
			}
			if r.Cols != nil && r.ListKey == "" && isEmptyJSON(raw) {
				// Some list endpoints answer result:null when there's nothing;
				// --json should still print a list.
				raw = json.RawMessage("[]")
			}
			return emit(raw, func(v any) {
				switch {
				case r.Human != nil:
					r.Human(v)
				case r.Cols != nil:
					b, _ := json.Marshal(v)
					printTable(r.Title, asList(b, r.ListKey), r.Cols)
				default:
					title := r.Title
					if len(args) > 0 {
						title += " " + args[len(args)-1]
					}
					printDetail(title, v, r.Fields)
				}
			})
		},
	}
	if r.Scope == scopeEither {
		c.Flags().String("zone", "", "Zone name or ID (default: account level)")
	}
	if r.Paginate {
		c.Flags().Int("page", 0, "Fetch only this page (default: all pages)")
		c.Flags().Int("per-page", 0, "Results per page")
	}
	if r.Flags != nil {
		r.Flags(c)
	}
	return c
}

// resolveScope builds the session and placeholder values for a command.
func resolveScope(cmd *cobra.Command, sc scope, path, acctPath string, names, args []string) (*apiSession, string, map[string]string, error) {
	zone := ""
	switch sc {
	case scopeZone:
		zone, args = args[0], args[1:]
	case scopeEither:
		zone, _ = cmd.Flags().GetString("zone")
		if zone == "" {
			zone = strings.TrimSpace(os.Getenv("CFCTL_ZONE"))
		}
		if zone == "" {
			path = acctPath
		}
	}
	s, err := adminSession(cmd, zone)
	if err != nil {
		return nil, "", nil, err
	}
	vals := map[string]string{}
	for i, n := range names {
		if i < len(args) {
			if strings.TrimSpace(args[i]) == "" {
				return nil, "", nil, fmt.Errorf("<%s> must not be empty", n)
			}
			vals[n] = args[i]
		}
	}
	return s, path, vals, nil
}

// writeSpec declares a create/update/delete command.
type writeSpec struct {
	Use, Short, Long string
	Aliases          []string
	Method           string
	Scope            scope
	Path, AcctPath   string
	Args             []string
	// Confirm, when set, asks first ("delete ruleset %s" gets the last arg).
	Confirm string
	// Body builds the JSON body from flags (nil + DataFlag → --data).
	Body func(c *cobra.Command, args []string) (any, error)
	// DataFlag adds --data JSON|@file|- (merged over Body's output).
	DataFlag bool
	// Query adds query parameters.
	Query   func(c *cobra.Command, q url.Values) error
	Flags   func(c *cobra.Command)
	Done    string // success message; %s gets the last arg
	Feature string
	// Human, when set, renders the result instead of Done.
	Human func(v any)
}

func (w writeSpec) build() *cobra.Command {
	nargs := len(w.Args)
	if w.Scope == scopeZone {
		nargs++
	}
	c := &cobra.Command{
		Use:     w.Use,
		Short:   w.Short,
		Long:    w.Long,
		Aliases: w.Aliases,
		Args:    cobra.ExactArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			var body any
			if w.Body != nil {
				b, err := w.Body(cmd, args)
				if err != nil {
					return err
				}
				body = b
			}
			if w.DataFlag && w.Body == nil && !cmd.Flags().Changed("data") {
				return fmt.Errorf("pass the request body with --data JSON (or @file, or -)")
			}
			if w.DataFlag {
				merged, err := mergeData(cmd, body)
				if err != nil {
					return err
				}
				body = merged
			}
			last := ""
			if len(args) > 0 {
				last = args[len(args)-1]
			}
			if w.Confirm != "" {
				what := w.Confirm
				if strings.Contains(what, "%s") {
					what = fmt.Sprintf(what, last)
				}
				if w.Scope == scopeZone {
					what += " (zone " + args[0] + ")"
				}
				if err := confirm(cmd, what); err != nil {
					return err
				}
			}
			s, path, vals, err := resolveScope(cmd, w.Scope, w.Path, w.AcctPath, w.Args, args)
			if err != nil {
				return err
			}
			q := url.Values{}
			if w.Query != nil {
				if err := w.Query(cmd, q); err != nil {
					return err
				}
			}
			raw, err := send(ctx, s, w.Method, path, vals, q, body, w.Feature)
			if err != nil {
				return err
			}
			return emit(raw, func(v any) {
				if w.Human != nil {
					w.Human(v)
					return
				}
				msg := w.Done
				if strings.Contains(msg, "%s") {
					msg = fmt.Sprintf(msg, last)
				}
				if id := jstr(v, "id"); id != "" && !strings.Contains(msg, id) {
					msg += " (id " + id + ")"
				}
				fmt.Println(ui.Success(msg))
			})
		},
	}
	if w.Scope == scopeEither {
		c.Flags().String("zone", "", "Zone name or ID (default: account level)")
	}
	if w.Confirm != "" {
		c.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	}
	if w.DataFlag {
		c.Flags().String("data", "", "JSON body (or @file, or - for stdin); merged over the flags")
	}
	if w.Flags != nil {
		w.Flags(c)
	}
	return c
}

// mergeData merges --data (a JSON object) over a flag-built body.
func mergeData(cmd *cobra.Command, body any) (any, error) {
	if !cmd.Flags().Changed("data") {
		return body, nil
	}
	data, _ := cmd.Flags().GetString("data")
	raw, err := api.ReadData(data, os.Stdin)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("--data is not valid JSON: %w", err)
	}
	m, isObj := v.(map[string]any)
	base, baseObj := body.(map[string]any)
	if !isObj || !baseObj {
		return v, nil
	}
	for k, x := range m {
		base[k] = x
	}
	return base, nil
}

// parseValue turns a CLI value into JSON: valid JSON (numbers, true/false,
// objects, arrays, quoted strings) is used as-is, anything else is a string.
func parseValue(s string) any {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	if v := decodeAny([]byte(t)); v != nil || t == "null" {
		if _, isNum := v.(json.Number); isNum || t[0] == '{' || t[0] == '[' || t[0] == '"' || t == "true" || t == "false" || t == "null" {
			return v
		}
	}
	return s
}

// parseSince turns "24h", "7d", "30m", "2w", or an RFC 3339 / YYYY-MM-DD
// time into a time.
func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return now.Add(-24 * time.Hour), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("invalid time %q: use 30m, 24h, 7d, 2w, YYYY-MM-DD, or RFC 3339", s)
	}
	var d time.Duration
	switch unit {
	case 'm':
		d = time.Duration(n) * time.Minute
	case 'h':
		d = time.Duration(n) * time.Hour
	case 'd':
		d = time.Duration(n) * 24 * time.Hour
	case 'w':
		d = time.Duration(n) * 7 * 24 * time.Hour
	default:
		return time.Time{}, fmt.Errorf("invalid time %q: use 30m, 24h, 7d, 2w, YYYY-MM-DD, or RFC 3339", s)
	}
	return now.Add(-d), nil
}

// group makes a parent command with subcommands.
func group(use, short, long string, aliases []string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, Long: long, Aliases: aliases}
	c.AddCommand(subs...)
	return c
}

// splitList splits comma-separated and repeated flag values.
func splitList(vals []string) []string {
	var out []string
	for _, v := range vals {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// sortRows sorts generic rows by the given fields.
func sortRows(rows []any, keys ...string) {
	sort.SliceStable(rows, func(i, j int) bool {
		for _, k := range keys {
			a, b := jstr(rows[i], k), jstr(rows[j], k)
			if a != b {
				return a < b
			}
		}
		return false
	})
}
