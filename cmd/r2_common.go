package cmd

// Shared helpers for the storage-and-data command groups (kv, r2, d1, queues,
// hyperdrive, vectorize, secrets-store, k2, basin, artifacts, agent-memory).
// Identifiers are prefixed "st" to keep them apart from other groups.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/dorkitude/cfctl/internal/version"
	"github.com/spf13/cobra"
)

// stClient is a raw API session with the account resolved.
type stClient struct {
	s    *apiSession
	c    *api.Client
	ctx  context.Context
	acct string
}

// newST builds a raw API client and resolves the account ID.
func newST(cmd *cobra.Command) (*stClient, error) {
	s, err := newAPISession(cmd)
	if err != nil {
		return nil, err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	acct, err := s.account(ctx)
	if err != nil {
		return nil, err
	}
	return &stClient{s: s, c: s.c, ctx: ctx, acct: acct}, nil
}

// p builds "/accounts/{acct}/<base>/<seg>/<seg>..." with each seg escaped.
// base is used as-is ("storage/kv/namespaces").
func (c *stClient) p(base string, segs ...string) string {
	out := "/accounts/" + url.PathEscape(c.acct) + "/" + strings.Trim(base, "/")
	for _, s := range segs {
		out += "/" + url.PathEscape(s)
	}
	return out
}

// stReq is one request built by a storage command.
type stReq struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	// Body is JSON-encoded unless it is []byte (sent with ContentType) or nil.
	Body        any
	ContentType string
}

// do sends a request and returns the response (envelope decoded).
func (c *stClient) do(r stReq) (*api.Response, error) {
	req := api.Request{Method: r.Method, Path: r.Path, Query: r.Query, Header: r.Header, ContentType: r.ContentType}
	switch b := r.Body.(type) {
	case nil:
	case []byte:
		req.Body = b
		if req.ContentType == "" {
			req.ContentType = "application/octet-stream"
		}
	case json.RawMessage:
		req.Body = b
		if req.ContentType == "" {
			req.ContentType = "application/json"
		}
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		req.Body = data
		if req.ContentType == "" {
			req.ContentType = "application/json"
		}
	}
	return c.c.Do(c.ctx, req)
}

// result sends a request and returns the envelope's result (or the raw body
// when the response isn't an envelope).
func (c *stClient) result(r stReq) (json.RawMessage, error) {
	resp, err := c.do(r)
	if err != nil {
		return nil, err
	}
	stInfoMessages(resp)
	if resp.Envelope == nil {
		return json.RawMessage(resp.Body), nil
	}
	return resp.Envelope.Result, nil
}

// get is result for a GET.
func (c *stClient) get(path string, q url.Values) (json.RawMessage, error) {
	return c.result(stReq{Method: "GET", Path: path, Query: q})
}

// all fetches every page of a list endpoint (page or cursor pagination).
func (c *stClient) all(path string, q url.Values) (json.RawMessage, error) {
	res, err := c.c.All(c.ctx, api.Request{Method: "GET", Path: path, Query: q}, 0)
	if err != nil {
		return nil, err
	}
	if res.Truncated {
		fmt.Fprintln(os.Stderr, ui.Warn(fmt.Sprintf("stopped after %d pages; results are incomplete", res.Pages)))
	}
	if len(bytes.TrimSpace(res.Result)) == 0 || string(bytes.TrimSpace(res.Result)) == "null" {
		return json.RawMessage("[]"), nil
	}
	return res.Result, nil
}

// graphql runs a GraphQL query and decodes data into out; GraphQL errors are
// returned as a Go error.
func (c *stClient) graphql(query string, vars map[string]any, out any) error {
	body, err := c.c.GraphQL(c.ctx, query, vars)
	if err != nil && body == nil {
		return fmt.Errorf("GraphQL request failed: %w", err)
	}
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if jerr := json.Unmarshal(body, &resp); jerr != nil {
		return fmt.Errorf("decoding GraphQL response: %w", jerr)
	}
	if len(resp.Errors) > 0 {
		var msgs []string
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("GraphQL errors: %s", strings.Join(msgs, "; "))
	}
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(resp.Data, out)
	}
	return nil
}

// stInfoMessages prints an envelope's informational messages to stderr.
func stInfoMessages(resp *api.Response) {
	if resp == nil || resp.Envelope == nil {
		return
	}
	for _, m := range resp.Envelope.Messages {
		if m.Message != "" {
			fmt.Fprintln(os.Stderr, ui.Info(m.Message))
		}
	}
}

// stream sends a request whose body (request or response) may be large,
// without buffering it. The caller must close the response body. Errors
// (non-2xx) are decoded like api.Client.Do's.
func (c *stClient) stream(method, path string, q url.Values, hdr http.Header, body io.Reader, size int64, reopen func() (io.ReadCloser, error)) (*http.Response, error) {
	u, err := c.c.URL(path, q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(c.ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
		if reopen != nil {
			req.GetBody = reopen
		}
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	token, err := config.LoadToken()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "cfctl/"+version.Version)
	resp, err := c.c.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL can carry query values; don't echo it
		}
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		e := &api.Error{Status: resp.StatusCode, Method: method, Path: req.URL.Path}
		var env api.Envelope
		if json.Unmarshal(data, &env) == nil && len(env.Errors) > 0 {
			e.Errors = env.Errors
		} else if s := strings.TrimSpace(string(data)); s != "" && len(s) < 300 && !strings.HasPrefix(s, "<") {
			e.Errors = []api.Message{{Message: s}}
		}
		return nil, e
	}
	return resp, nil
}

// --- output -----------------------------------------------------------------

// stEmit prints raw as JSON with --json, otherwise calls render.
func stEmit(raw json.RawMessage, render func() error) error {
	if jsonOutput {
		return printBody(stNonNull(raw), nil)
	}
	return render()
}

// stEmitValue prints v as JSON with --json, otherwise calls render.
func stEmitValue(v any, render func() error) error {
	if jsonOutput {
		return printJSONValue(v)
	}
	return render()
}

func stNonNull(raw json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return json.RawMessage("null")
	}
	return t
}

// stCol is a table column: a header and a dot path into each item
// ("name", "settings.delivery_paused", "consumers.#" for an array length).
type stCol struct {
	Header string
	Path   string
	// Fmt formats the value (nil → stString).
	Fmt func(v any) string
}

// stItems decodes a list result into maps. A result that is an object with
// one array field (R2's {"buckets":[...]}) yields that array's items.
func stItems(raw json.RawMessage, field ...string) []map[string]any {
	var items []map[string]any
	if json.Unmarshal(raw, &items) == nil {
		return items
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	if len(field) > 0 {
		_ = json.Unmarshal(obj[field[0]], &items)
		return items
	}
	for _, v := range obj {
		if json.Unmarshal(v, &items) == nil && items != nil {
			return items
		}
	}
	return nil
}

// stGet reads a dot path from a decoded JSON value.
func stGet(v any, path string) any {
	if path == "" {
		return v
	}
	for _, part := range strings.Split(path, ".") {
		if part == "#" {
			if a, ok := v.([]any); ok {
				return len(a)
			}
			return nil
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
	}
	return v
}

// stString renders a JSON value for a table cell.
func stString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return stShortTime(x)
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case []any:
		var parts []string
		for _, e := range x {
			if _, isObj := e.(map[string]any); isObj {
				b, _ := json.Marshal(e)
				parts = append(parts, string(b))
				continue
			}
			parts = append(parts, stString(e))
		}
		return strings.Join(parts, ", ")
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

var stISOTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}`)

// stShortTime shortens RFC 3339 timestamps to "2006-01-02 15:04" (UTC).
func stShortTime(s string) string {
	if !stISOTime.MatchString(s) {
		return s
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// stTable prints rows under plain headers, aligned.
func stTable(headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  "+strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, "  "+strings.Join(r, "\t"))
	}
	tw.Flush()
}

// stList renders list items as a titled table (or a warning when empty).
func stList(title, empty string, items []map[string]any, cols []stCol) {
	if len(items) == 0 {
		fmt.Println(ui.Warn(empty))
		return
	}
	fmt.Println(ui.TitleStyle.Render(fmt.Sprintf(title, len(items))))
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.Header
	}
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		row := make([]string, len(cols))
		for i, c := range cols {
			f := c.Fmt
			if f == nil {
				f = stString
			}
			row[i] = f(stGet(it, c.Path))
		}
		rows = append(rows, row)
	}
	stTable(headers, rows)
}

// stDetail prints an object's fields as "label: value" lines, in the API's
// key order. Nested objects are printed indented.
func stDetail(title string, raw json.RawMessage) error {
	fmt.Println(ui.TitleStyle.Render(title))
	return stPrintFields(raw, "  ")
}

func stPrintFields(raw json.RawMessage, indent string) error {
	keys, vals, err := stOrderedObject(raw)
	if err != nil {
		// Not an object: print as JSON.
		return printBody(raw, nil)
	}
	width := 0
	for _, k := range keys {
		width = max(width, len(k)+1)
	}
	width = min(width, 28)
	for i, k := range keys {
		v := bytes.TrimSpace(vals[i])
		if len(v) > 0 && v[0] == '{' && len(v) > 2 {
			fmt.Printf("%s%s\n", indent, ui.SubtleStyle.Render(k+":"))
			if err := stPrintFields(v, indent+"  "); err != nil {
				return err
			}
			continue
		}
		var dv any
		_ = json.Unmarshal(v, &dv)
		s := stString(dv)
		if s == "" && string(v) != `""` && string(v) != "null" {
			s = string(v)
		}
		fmt.Printf("%s%-*s %s\n", indent, width, k+":", s)
	}
	return nil
}

// stOrderedObject splits a JSON object into keys and raw values, in order.
func stOrderedObject(raw json.RawMessage) ([]string, []json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, fmt.Errorf("not an object")
	}
	var keys []string
	var vals []json.RawMessage
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		keys = append(keys, kt.(string))
		vals = append(vals, v)
	}
	return keys, vals, nil
}

// stOK prints a success line (suppressed with --json, where the API result
// is printed instead).
func stOK(raw json.RawMessage, msg string) error {
	if jsonOutput {
		if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
			return printJSONValue(map[string]any{"success": true})
		}
		return printBody(raw, nil)
	}
	fmt.Println(ui.Success(msg))
	return nil
}

// --- flags and input --------------------------------------------------------

// stYes adds --yes/-y to a destructive command.
func stYes(cmd *cobra.Command) {
	cmd.Flags().BoolP("yes", "y", false, "Don't ask for confirmation")
}

// stDataFlag adds --data (a JSON body or @file or -) to a create/update
// command; flags set on top of it win.
func stDataFlag(cmd *cobra.Command) {
	cmd.Flags().String("data", "", "Request body as JSON (or @file, or - for stdin); other flags are merged on top")
}

// stBody returns the --data object (or an empty one).
func stBody(cmd *cobra.Command) (map[string]any, error) {
	body := map[string]any{}
	if cmd.Flags().Lookup("data") == nil || !cmd.Flags().Changed("data") {
		return body, nil
	}
	spec, _ := cmd.Flags().GetString("data")
	data, err := api.ReadData(spec, os.Stdin)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("--data must be a JSON object: %w", err)
	}
	return body, nil
}

// stSet sets a dot path in a nested map, creating objects as needed.
func stSet(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}

// stFlagStr / stFlagInt / stFlagBool copy a flag into body[path] if the
// user set it.
func stFlagStr(cmd *cobra.Command, body map[string]any, flag, path string) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetString(flag)
		stSet(body, path, v)
	}
}

func stFlagInt(cmd *cobra.Command, body map[string]any, flag, path string) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetInt(flag)
		stSet(body, path, v)
	}
}

func stFlagBool(cmd *cobra.Command, body map[string]any, flag, path string) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetBool(flag)
		stSet(body, path, v)
	}
}

func stFlagList(cmd *cobra.Command, body map[string]any, flag, path string) {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetStringSlice(flag)
		stSet(body, path, v)
	}
}

// stReadInput reads a value given literally, as @file, or - for stdin.
func stReadInput(spec string) ([]byte, error) {
	return api.ReadData(spec, os.Stdin)
}

// --- name → ID resolution ---------------------------------------------------

var stUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)

// stLooksLikeID reports whether s is a 32-hex or UUID identifier.
func stLooksLikeID(s string) bool {
	return hexID.MatchString(strings.ToLower(s)) || stUUID.MatchString(s)
}

// resolve turns a name or ID into an ID by listing listPath and matching
// nameField (exactly) or idField. kind is used in errors ("KV namespace").
func (c *stClient) resolve(kind, listPath string, q url.Values, arg, idField, nameField string) (string, error) {
	if arg == "" {
		return "", fmt.Errorf("missing %s name or ID", kind)
	}
	raw, err := c.all(listPath, q)
	if err != nil {
		if stLooksLikeID(arg) {
			return arg, nil
		}
		return "", fmt.Errorf("looking up %s %q: %w", kind, arg, err)
	}
	var byName []string
	for _, it := range stItems(raw) {
		id := stString(stGet(it, idField))
		if id == arg {
			return id, nil
		}
		if stString(stGet(it, nameField)) == arg {
			byName = append(byName, id)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		if stLooksLikeID(arg) {
			return arg, nil
		}
		return "", fmt.Errorf("%s %q not found", kind, arg)
	}
	return "", fmt.Errorf("%d %ss are named %q; use the ID (%s)", len(byName), kind, arg, strings.Join(byName, ", "))
}

// stTransferTimeout raises the per-request timeout for commands that move
// object data, unless the user passed --timeout.
func stTransferTimeout(cmd *cobra.Command, d time.Duration) {
	if f := cmd.Flags().Lookup("timeout"); f != nil && f.Changed {
		return
	}
	if f := cmd.InheritedFlags().Lookup("timeout"); f != nil && f.Changed {
		return
	}
	if api.Timeout < d {
		api.Timeout = d
	}
}

// stSortedKeys returns map keys sorted.
func stSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stParseTime accepts RFC 3339, "2006-01-02", or a Unix timestamp.
func stParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q: use RFC 3339 (2026-10-03T12:00:00Z), YYYY-MM-DD, or a Unix timestamp", s)
}

// stUnix formats Unix seconds as RFC 3339 (UTC).
func stUnix(n int64) string {
	return time.Unix(n, 0).UTC().Format(time.RFC3339)
}
