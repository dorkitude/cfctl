package cmd

// platform_kit.go: shared plumbing for the hand-written "platform & apps"
// groups (pages, ai, ai-search, workflows, containers, browser, flagship,
// email, turnstile, tunnel, vpc, cert, mtls-certificate). Everything is
// prefixed "plat" so it can't collide with other command groups.
//
// A platSpec describes one REST-backed command declaratively: its path
// template, which placeholders become positional arguments, how to build
// the query/body from flags, and how to render the result. platCommand
// turns it into a Cobra command that goes through the shared raw client
// (and therefore the read-only guard).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// platCol is one output column (table) or field (detail view).
type platCol struct {
	// H is the header / label.
	H string
	// Path is a dotted JSON path ("deployment_trigger.metadata.branch");
	// "a|b" tries a, then b.
	Path string
	// W truncates the value to W runes (0 = default 60 for tables).
	W int
}

// platSpec declares one command.
type platSpec struct {
	Use, Short, Long string
	Aliases          []string
	Example          string
	// Method defaults to GET.
	Method string
	// Path is a template under /client/v4. {account_id} is filled from
	// config; every other placeholder (including {zone_id}, which accepts a
	// zone name) is a positional argument, in path order.
	Path string
	// PathFlags fills placeholders from flags instead of positional args
	// (placeholder → flag name); the flags must be defined by Flags.
	PathFlags map[string]string
	// ExtraArgs is the number of positional arguments after the
	// placeholders (consumed by Body/Query). OptionalArgs are allowed beyond that.
	ExtraArgs, OptionalArgs int
	// List follows pagination (GET only).
	List bool
	// ItemsKey picks an array field out of an object result for tables.
	ItemsKey string
	Cols     []platCol
	// Fields is the detail view (nil = every top-level field).
	Fields []platCol
	// Title for tables ("%d Pages projects") or details ("Pages project %s", arg 0).
	Title string
	// Product names the product in friendly errors ("Pages").
	Product string
	// Flags adds command-specific flags.
	Flags func(c *cobra.Command)
	// Query builds query parameters from flags/args.
	Query func(c *cobra.Command, args []string, q url.Values) error
	// Body builds a JSON body from flags/args. With Data, --data JSON is
	// also accepted (and merged over the built body when both are given).
	Body func(c *cobra.Command, args []string) (any, error)
	Data bool
	// Confirm is the confirmation prompt ("delete Pages project %s"); %s
	// gets the positional args joined by " ". Adds --yes.
	Confirm string
	// Done is the success line for writes ("Deleted Pages project %s").
	Done string
	// Secrets are dotted paths redacted unless --reveal is passed.
	Secrets []string
	// Raw: the endpoint doesn't use the Cloudflare envelope.
	Raw bool
	// Print overrides output (after --json handling unless JSONToo).
	Print func(c *cobra.Command, args []string, raw json.RawMessage) error
	// ArgsHook rewrites positional args before the request (e.g. resolve
	// "latest" to an ID).
	ArgsHook func(ctx context.Context, s *apiSession, c *cobra.Command, args []string) ([]string, error)
	// Run replaces the whole request flow (custom commands that still want
	// the spec's flags/usage).
	Run func(c *cobra.Command, args []string) error
}

// platPlaceholders lists the placeholders in path order, minus {account_id}.
func platPlaceholders(path string) []string {
	var out []string
	for _, m := range placeholderRE.FindAllStringSubmatch(path, -1) {
		if !isAccountParam(m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// platCommand builds a Cobra command from a spec.
func platCommand(spec platSpec) *cobra.Command {
	n := len(platPlaceholders(platStripFlags(spec.Path, spec.PathFlags))) + spec.ExtraArgs
	c := &cobra.Command{
		Use:     spec.Use,
		Short:   spec.Short,
		Long:    spec.Long,
		Aliases: spec.Aliases,
		Example: spec.Example,
		Args:    cobra.RangeArgs(n, n+spec.OptionalArgs),
	}
	if c.Long == "" {
		c.Long = spec.Short + "."
	}
	if spec.Run != nil {
		c.RunE = spec.Run
	} else {
		c.RunE = func(cmd *cobra.Command, args []string) error { return platRun(cmd, spec, args) }
	}
	if spec.Flags != nil {
		spec.Flags(c)
	}
	if spec.Data {
		c.Flags().String("data", "", "Request body: JSON string, @file, or - for stdin (merged over flags)")
	}
	if spec.Confirm != "" {
		c.Flags().BoolP("yes", "y", false, "Don't ask for confirmation")
	}
	if len(spec.Secrets) > 0 {
		c.Flags().Bool("reveal", false, "Print secret values instead of redacting them")
	}
	return c
}

// platStripFlags removes flag-filled placeholders (for counting args).
func platStripFlags(path string, flags map[string]string) string {
	for ph := range flags {
		path = strings.ReplaceAll(path, "{"+ph+"}", "x")
	}
	return path
}

// platPathFromFlags substitutes flag-filled placeholders.
func platPathFromFlags(cmd *cobra.Command, path string, flags map[string]string) string {
	for ph, fl := range flags {
		v, _ := cmd.Flags().GetString(fl)
		path = strings.ReplaceAll(path, "{"+ph+"}", url.PathEscape(v))
	}
	return path
}

// platFill resolves a path template: positional args fill placeholders in
// order; {zone_id} accepts a zone name.
func platFill(ctx context.Context, s *apiSession, path string, args []string) (string, []string, error) {
	vals := map[string]string{}
	ph := platPlaceholders(path)
	if len(args) < len(ph) {
		return "", nil, fmt.Errorf("need %d arguments (%s)", len(ph), strings.Join(ph, ", "))
	}
	for i, name := range ph {
		if isZoneParam(name) {
			s.zoneArg, s.zoneID = args[i], ""
			id, err := s.zone(ctx)
			if err != nil {
				return "", nil, err
			}
			vals[name] = id
			continue
		}
		vals[name] = args[i]
	}
	p, err := s.fillPath(ctx, path, vals)
	return p, args[len(ph):], err
}

// platRun is the default request flow for a spec.
func platRun(cmd *cobra.Command, spec platSpec, args []string) error {
	ctx := context.Background()
	s, err := newAPISession(cmd)
	if err != nil {
		return err
	}
	if spec.ArgsHook != nil {
		if args, err = spec.ArgsHook(ctx, s, cmd, args); err != nil {
			return err
		}
	}
	method := spec.Method
	if method == "" {
		method = "GET"
	}
	path, _, err := platFill(ctx, s, platPathFromFlags(cmd, spec.Path, spec.PathFlags), args)
	if err != nil {
		return err
	}
	q := url.Values{}
	if spec.Query != nil {
		if err := spec.Query(cmd, args, q); err != nil {
			return err
		}
	}
	req := api.Request{Method: method, Path: path, Query: q}
	if spec.Body != nil || spec.Data {
		body, err := platBuildBody(cmd, spec, args)
		if err != nil {
			return err
		}
		if body != nil {
			req.Body, req.ContentType = body, "application/json"
		}
	}
	if spec.Confirm != "" {
		if err := confirm(cmd, platFormat(spec.Confirm, args)); err != nil {
			return err
		}
	}
	raw, err := platSend(ctx, s, req, spec.List, spec.Raw)
	if err != nil {
		return platErr(spec.Product, err)
	}
	return platOutput(cmd, spec, args, raw)
}

// platSend performs a request (following pagination when list) and returns
// the result (or the whole body for non-envelope endpoints).
func platSend(ctx context.Context, s *apiSession, req api.Request, list, raw bool) (json.RawMessage, error) {
	if list && req.Method == "GET" && !raw {
		res, err := s.c.All(ctx, req, api.DefaultMaxPages)
		if err != nil {
			return nil, err
		}
		if res.Truncated {
			fmt.Fprintln(os.Stderr, ui.Warn(fmt.Sprintf("stopped after %d pages; results are incomplete", res.Pages)))
		}
		return res.Result, nil
	}
	resp, err := s.c.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if raw || resp.Envelope == nil {
		return json.RawMessage(resp.Body), nil
	}
	return resp.Envelope.Result, nil
}

// platBuildBody merges the spec's Body with --data.
func platBuildBody(cmd *cobra.Command, spec platSpec, args []string) ([]byte, error) {
	var body any
	if spec.Body != nil {
		b, err := spec.Body(cmd, args)
		if err != nil {
			return nil, err
		}
		body = b
	}
	if spec.Data && cmd.Flags().Changed("data") {
		d, _ := cmd.Flags().GetString("data")
		data, err := api.ReadData(d, os.Stdin)
		if err != nil {
			return nil, err
		}
		var extra any
		if err := json.Unmarshal(data, &extra); err != nil {
			return nil, fmt.Errorf("--data is not valid JSON: %w", err)
		}
		if bm, ok := body.(map[string]any); ok {
			if em, ok := extra.(map[string]any); ok {
				for k, v := range em {
					bm[k] = v
				}
				extra = bm
			}
		}
		body = extra
	}
	if body == nil {
		return nil, nil
	}
	return json.Marshal(body)
}

func platFormat(f string, args []string) string {
	if strings.Contains(f, "%s") {
		return fmt.Sprintf(f, strings.Join(args, " "))
	}
	return f
}

// platOutput renders a result per the spec.
func platOutput(cmd *cobra.Command, spec platSpec, args []string, raw json.RawMessage) error {
	if len(spec.Secrets) > 0 {
		if reveal, _ := cmd.Flags().GetBool("reveal"); !reveal {
			raw = platRedact(raw, spec.Secrets)
		}
	}
	if jsonOutput {
		return platPrintJSON(raw)
	}
	if spec.Print != nil {
		return spec.Print(cmd, args, raw)
	}
	if spec.Done != "" {
		fmt.Println(ui.Success(platFormat(spec.Done, args)))
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil || v == nil {
		if len(strings.TrimSpace(string(raw))) > 0 && err != nil {
			_, _ = os.Stdout.Write(raw)
			return nil
		}
		fmt.Println(ui.Success("Done"))
		return nil
	}
	if spec.ItemsKey != "" {
		if m, ok := v.(map[string]any); ok {
			v = m[spec.ItemsKey]
		}
	}
	title := platFormat(spec.Title, args)
	switch t := v.(type) {
	case []any:
		platTable(title, t, spec.Cols)
	case map[string]any:
		platDetail(title, t, spec.Fields)
	default:
		fmt.Println(platStr(t))
	}
	return nil
}

func platPrintJSON(raw json.RawMessage) error {
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return printJSONValue(map[string]any{"success": true})
	}
	return printBody(raw, nil)
}

// platErr turns API errors into friendlier ones: product not enabled,
// missing token permission, not found. Messages that are themselves JSON
// ({"error":"..."}) are unwrapped.
func platErr(product string, err error) error {
	var ae *api.Error
	if !errors.As(err, &ae) {
		return err
	}
	if product == "" {
		product = "this product"
	}
	cp := *ae
	cp.Errors = nil
	for _, m := range ae.Errors {
		var inner struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if strings.HasPrefix(strings.TrimSpace(m.Message), "{") && json.Unmarshal([]byte(m.Message), &inner) == nil {
			if inner.Error != "" {
				m.Message = inner.Error
			} else if inner.Message != "" {
				m.Message = inner.Message
			}
		}
		cp.Errors = append(cp.Errors, m)
	}
	err = &cp
	low := strings.ToLower(cp.Error())
	switch ae.Status {
	case 401:
		if strings.Contains(low, "invalid api token") || strings.Contains(low, "authentication error") {
			return fmt.Errorf("%w; the API token was rejected (run 'cfctl auth status --verify')", err)
		}
		return fmt.Errorf("%w; %s isn't available to this account or token (it may need a plan upgrade or a token permission)", err, product)
	case 403:
		return fmt.Errorf("%w; %s may not be enabled on this account, or your API token lacks permission for it (edit the token under My Profile > API Tokens)", err, product)
	case 404:
		return fmt.Errorf("%w; check the name/ID (or %s isn't set up on this account)", err, product)
	}
	return err
}

// platRedact replaces secret fields (dotted paths; arrays are traversed)
// with a placeholder.
func platRedact(raw json.RawMessage, paths []string) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	for _, p := range paths {
		v = platRedactPath(v, strings.Split(p, "."))
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

const platRedacted = "[redacted: pass --reveal]"

func platRedactPath(v any, parts []string) any {
	switch t := v.(type) {
	case []any:
		for i := range t {
			t[i] = platRedactPath(t[i], parts)
		}
		return t
	case map[string]any:
		if len(parts) == 1 {
			if x, ok := t[parts[0]]; ok && x != nil && x != "" {
				t[parts[0]] = platRedacted
			}
			return t
		}
		if x, ok := t[parts[0]]; ok {
			t[parts[0]] = platRedactPath(x, parts[1:])
		}
		return t
	case string:
		// A bare string result is itself the secret (e.g. tunnel token).
		if len(parts) == 1 && parts[0] == "" {
			return platRedacted
		}
	}
	return v
}

// platGet reads a dotted path ("a.b|c") from a decoded JSON value.
func platGet(v any, path string) any {
	for _, alt := range strings.Split(path, "|") {
		cur := v
		for _, p := range strings.Split(alt, ".") {
			if a, ok := cur.([]any); ok {
				i, err := strconv.Atoi(p)
				if err != nil || i < 0 || i >= len(a) {
					cur = nil
					break
				}
				cur = a[i]
				continue
			}
			m, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = m[p]
		}
		if cur != nil && cur != "" {
			return cur
		}
	}
	return nil
}

// platStr renders a JSON value for a table cell.
func platStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		if len(t) >= 19 {
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999"} {
				if ts, err := time.Parse(layout, t); err == nil {
					return ts.UTC().Format("2006-01-02 15:04")
				}
			}
		}
		return t
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			switch x.(type) {
			case map[string]any, []any:
				b, _ := json.Marshal(x)
				parts = append(parts, string(b))
			default:
				parts = append(parts, platStr(x))
			}
		}
		return strings.Join(parts, ", ")
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func platTrunc(s string, w int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if w > 3 && len(r) > w {
		return string(r[:w-1]) + "…"
	}
	return s
}

// platTable prints items as an aligned table.
func platTable(title string, items []any, cols []platCol) {
	if len(items) == 0 {
		fmt.Println(ui.Warn("Nothing found"))
		return
	}
	if title != "" {
		if strings.Contains(title, "%d") {
			title = fmt.Sprintf(title, len(items))
		}
		fmt.Println(ui.TitleStyle.Render(title))
	}
	if len(cols) == 0 {
		cols = platGuessCols(items)
	}
	rows := make([][]string, len(items))
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = lipgloss.Width(c.H)
	}
	for r, it := range items {
		row := make([]string, len(cols))
		for i, c := range cols {
			w := c.W
			if w == 0 {
				w = 60
			}
			if m, ok := it.(map[string]any); ok {
				row[i] = platTrunc(platStr(platGet(m, c.Path)), w)
			} else if i == 0 {
				row[i] = platTrunc(platStr(it), w)
			}
			if lw := lipgloss.Width(row[i]); lw > widths[i] {
				widths[i] = lw
			}
		}
		rows[r] = row
	}
	var hdr []string
	for i, c := range cols {
		hdr = append(hdr, platPad(strings.ToUpper(c.H), widths[i]))
	}
	fmt.Println("  " + ui.SubtleStyle.Render(strings.TrimRight(strings.Join(hdr, "  "), " ")))
	for _, row := range rows {
		var cells []string
		for i, cell := range row {
			if i == 0 {
				cells = append(cells, ui.AccentStyle.Render(platPad(cell, widths[i])))
				continue
			}
			cells = append(cells, platPad(cell, widths[i]))
		}
		fmt.Println("  " + strings.TrimRight(strings.Join(cells, "  "), " "))
	}
}

func platPad(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// platGuessCols picks columns from the first item's scalar fields.
func platGuessCols(items []any) []platCol {
	m, ok := items[0].(map[string]any)
	if !ok {
		return []platCol{{H: "value", Path: ""}}
	}
	pref := []string{"id", "name", "key", "status", "created_on", "created_at", "modified_on"}
	var cols []platCol
	seen := map[string]bool{}
	for _, k := range pref {
		if _, ok := m[k]; ok {
			cols = append(cols, platCol{H: k, Path: k})
			seen[k] = true
		}
	}
	var rest []string
	for k, v := range m {
		switch v.(type) {
		case map[string]any, []any:
			continue
		}
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		if len(cols) >= 6 {
			break
		}
		cols = append(cols, platCol{H: k, Path: k, W: 40})
	}
	return cols
}

// platDetail prints one object as label/value lines.
func platDetail(title string, m map[string]any, fields []platCol) {
	if title != "" {
		fmt.Println(ui.TitleStyle.Render(title))
	}
	if fields == nil {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fields = append(fields, platCol{H: k, Path: k, W: 100})
		}
	}
	lw := 0
	for _, f := range fields {
		if l := lipgloss.Width(f.H); l > lw {
			lw = l
		}
	}
	for _, f := range fields {
		val := platGet(m, f.Path)
		if val == nil {
			continue
		}
		w := f.W
		if w == 0 {
			w = 100
		}
		fmt.Printf("  %s  %s\n", ui.SubtleStyle.Render(platPad(f.H+":", lw+1)), platTrunc(platStr(val), w))
	}
}

// platDecode unmarshals a result into v.
func platDecode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("unexpected API response: %w", err)
	}
	return nil
}

// Flag helpers -------------------------------------------------------------

// platSetIf sets m[key] from a string flag when it was given.
func platSetStr(c *cobra.Command, m map[string]any, flag, key string) {
	if c.Flags().Changed(flag) {
		v, _ := c.Flags().GetString(flag)
		m[key] = v
	}
}

func platSetBool(c *cobra.Command, m map[string]any, flag, key string) {
	if c.Flags().Changed(flag) {
		v, _ := c.Flags().GetBool(flag)
		m[key] = v
	}
}

func platSetInt(c *cobra.Command, m map[string]any, flag, key string) {
	if c.Flags().Changed(flag) {
		v, _ := c.Flags().GetInt(flag)
		m[key] = v
	}
}

func platSetStrs(c *cobra.Command, m map[string]any, flag, key string) {
	if c.Flags().Changed(flag) {
		v, _ := c.Flags().GetStringSlice(flag)
		m[key] = v
	}
}

// platQueryStr copies string flags to query params when set.
func platQueryStr(c *cobra.Command, q url.Values, pairs ...string) {
	for i := 0; i+1 < len(pairs); i += 2 {
		if c.Flags().Changed(pairs[i]) {
			v, _ := c.Flags().GetString(pairs[i])
			q.Set(pairs[i+1], v)
		}
	}
}

// platQueryInt copies int flags to query params when set.
func platQueryInt(c *cobra.Command, q url.Values, pairs ...string) {
	for i := 0; i+1 < len(pairs); i += 2 {
		if c.Flags().Changed(pairs[i]) {
			v, _ := c.Flags().GetInt(pairs[i])
			q.Set(pairs[i+1], strconv.Itoa(v))
		}
	}
}

// platGroup builds a parent command and adds children.
func platGroup(use, short, long string, aliases []string, children ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, Long: long, Aliases: aliases}
	if c.Long == "" {
		c.Long = short + "."
	}
	c.AddCommand(children...)
	return c
}

// platSpecs builds several commands.
func platSpecs(specs ...platSpec) []*cobra.Command {
	out := make([]*cobra.Command, 0, len(specs))
	for _, s := range specs {
		out = append(out, platCommand(s))
	}
	return out
}

// platDo is a one-off request helper for custom commands.
func platDo(ctx context.Context, s *apiSession, method, path string, q url.Values, body any) (json.RawMessage, error) {
	req := api.Request{Method: method, Path: path, Query: q}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req.Body, req.ContentType = b, "application/json"
	}
	return platSend(ctx, s, req, false, false)
}

// platAll fetches every page of a list.
func platAll(ctx context.Context, s *apiSession, path string, q url.Values) (json.RawMessage, error) {
	return platSend(ctx, s, api.Request{Method: "GET", Path: path, Query: q}, true, false)
}
