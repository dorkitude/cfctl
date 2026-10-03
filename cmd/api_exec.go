package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/output"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// apiSession is an authenticated raw client plus lazily resolved IDs.
type apiSession struct {
	c         *api.Client
	accountID string
	zoneArg   string
	zoneID    string
}

// newAPISession builds a raw client from stored credentials. The account and
// zone are resolved only if a request needs them.
func newAPISession(cmd *cobra.Command) (*apiSession, error) {
	token, err := config.LoadToken()
	if err != nil {
		return nil, err
	}
	zone, _ := cmd.Flags().GetString("zone")
	if zone == "" {
		zone = strings.TrimSpace(os.Getenv("CFCTL_ZONE"))
	}
	return &apiSession{c: api.New(token, config.APIBaseURL()), zoneArg: zone}, nil
}

// account returns the account ID: --account / CFCTL_ACCOUNT_ID / config, or
// the only account the token can see.
func (s *apiSession) account(ctx context.Context) (string, error) {
	if s.accountID != "" {
		return s.accountID, nil
	}
	if id := config.AccountID(); id != "" {
		s.accountID = id
		return id, nil
	}
	res, err := s.c.All(ctx, api.Request{Method: "GET", Path: "/accounts", Query: url.Values{"per_page": {"50"}}}, 20)
	if err != nil {
		return "", fmt.Errorf("no account ID configured and listing accounts failed: %w", err)
	}
	var accts []struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(res.Result, &accts)
	if len(accts) != 1 {
		return "", fmt.Errorf("no account ID configured and the token sees %d accounts; pass --account <id> or run 'cfctl auth login'", len(accts))
	}
	s.accountID = accts[0].ID
	return s.accountID, nil
}

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// zone resolves --zone (a zone name or 32-char ID) to a zone ID.
func (s *apiSession) zone(ctx context.Context) (string, error) {
	if s.zoneID != "" {
		return s.zoneID, nil
	}
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.zoneArg)), ".")
	if name == "" {
		return "", fmt.Errorf("this request needs a zone: pass --zone <name-or-id> (or set CFCTL_ZONE)")
	}
	if hexID.MatchString(name) {
		s.zoneID = name
		return name, nil
	}
	q := url.Values{"name": {name}}
	if id := config.AccountID(); id != "" {
		q.Set("account.id", id)
	}
	resp, err := s.c.Do(ctx, api.Request{Method: "GET", Path: "/zones", Query: q})
	if err != nil {
		return "", fmt.Errorf("failed to look up zone %q: %w", name, err)
	}
	var zs []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if resp.Envelope != nil {
		_ = json.Unmarshal(resp.Envelope.Result, &zs)
	}
	for _, z := range zs {
		if strings.EqualFold(z.Name, name) {
			s.zoneID = z.ID
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("zone %q not found in this Cloudflare account", name)
}

var placeholderRE = regexp.MustCompile(`\{([^{}/]+)\}`)

// isAccountParam / isZoneParam name the path params filled from config.
func isAccountParam(name string) bool { return name == "account_id" }
func isZoneParam(name string) bool    { return name == "zone_id" || name == "zone_identifier" }

// fillPath substitutes {placeholders}: values from vals first, then
// {account_id} from the account and {zone_id}/{zone_identifier} from --zone.
// Values are path-escaped. Unknown placeholders are an error.
func (s *apiSession) fillPath(ctx context.Context, path string, vals map[string]string) (string, error) {
	var firstErr error
	out := placeholderRE.ReplaceAllStringFunc(path, func(m string) string {
		name := m[1 : len(m)-1]
		if v, ok := vals[name]; ok {
			return url.PathEscape(v)
		}
		var v string
		var err error
		switch {
		case isAccountParam(name):
			v, err = s.account(ctx)
		case isZoneParam(name):
			v, err = s.zone(ctx)
		default:
			err = fmt.Errorf("no value for path placeholder {%s}", name)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return url.PathEscape(v)
	})
	return out, firstErr
}

// requestOptions are the output/pagination flags shared by `api request`
// and generated commands.
type requestOptions struct {
	all      bool
	raw      bool
	maxPages int
}

func addRequestFlags(cmd *cobra.Command, withBody bool) {
	if withBody {
		cmd.Flags().String("data", "", "Request body: JSON string, @file, or - for stdin")
		cmd.Flags().StringArray("form", nil, "Multipart form field: name=value or name=@file[;type=mime][;filename=x] (repeatable)")
	}
	cmd.Flags().StringArray("query", nil, "Extra query parameter key=value (repeatable)")
	cmd.Flags().StringArray("header", nil, "Extra request header 'Name: value' (repeatable)")
	cmd.Flags().Bool("all", false, "Follow pagination (page, cursor, offset, or before-time) and concatenate results")
	cmd.Flags().Int("max-pages", api.DefaultMaxPages, "Stop --all after this many requests")
	cmd.Flags().Bool("raw", false, "Print the whole response envelope instead of just result")
}

func readRequestOptions(cmd *cobra.Command) requestOptions {
	var o requestOptions
	o.all, _ = cmd.Flags().GetBool("all")
	o.raw, _ = cmd.Flags().GetBool("raw")
	o.maxPages, _ = cmd.Flags().GetInt("max-pages")
	return o
}

// parseKV parses repeated key=value flags into url.Values.
func parseKV(pairs []string, into url.Values) error {
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("invalid --query %q: want key=value", kv)
		}
		into.Add(k, v)
	}
	return nil
}

// parseHeaders parses repeated "Name: value" (or Name=value) flags.
func parseHeaders(hs []string) (http.Header, error) {
	h := http.Header{}
	for _, s := range hs {
		k, v, ok := strings.Cut(s, ":")
		if !ok {
			k, v, ok = strings.Cut(s, "=")
		}
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --header %q: want 'Name: value'", s)
		}
		if strings.EqualFold(k, "Authorization") {
			return nil, fmt.Errorf("--header Authorization is not allowed; cfctl sends your stored token")
		}
		h.Add(k, strings.TrimSpace(v))
	}
	return h, nil
}

// readBody resolves --data / --form into a body and content type.
// defaultCT is used for --data when no Content-Type header is given.
func readBody(cmd *cobra.Command, defaultCT string) ([]byte, string, error) {
	if cmd.Flags().Lookup("data") == nil {
		return nil, "", nil
	}
	data, _ := cmd.Flags().GetString("data")
	forms, _ := cmd.Flags().GetStringArray("form")
	dataSet := cmd.Flags().Changed("data")
	if dataSet && len(forms) > 0 {
		return nil, "", fmt.Errorf("use either --data or --form, not both")
	}
	if len(forms) > 0 {
		var fields []api.FormField
		for _, f := range forms {
			ff, err := api.ParseFormField(f)
			if err != nil {
				return nil, "", err
			}
			fields = append(fields, ff)
		}
		return api.BuildMultipart(fields)
	}
	if !dataSet {
		return nil, "", nil
	}
	body, err := api.ReadData(data, os.Stdin)
	if err != nil {
		return nil, "", err
	}
	ct := defaultCT
	trimmed := bytes.TrimSpace(body)
	isJSON := json.Valid(trimmed)
	if ct == "" {
		ct = "application/octet-stream"
		if isJSON {
			ct = "application/json"
		}
	}
	if strings.Contains(ct, "json") && !strings.Contains(ct, "ndjson") && !strings.Contains(ct, "jsonl") && !isJSON {
		return nil, "", fmt.Errorf("--data is not valid JSON (content type %s)", ct)
	}
	return body, ct, nil
}

// execRequest sends req and prints the result. Shared by `api request` and
// every generated command.
func execRequest(ctx context.Context, s *apiSession, req api.Request, o requestOptions) error {
	if o.all {
		if req.Method != "GET" {
			return fmt.Errorf("--all only works with GET requests")
		}
		res, err := s.c.All(ctx, req, o.maxPages)
		if err != nil {
			return printFailure(err, o)
		}
		if res.Truncated {
			fmt.Fprintln(os.Stderr, ui.Warn(fmt.Sprintf("stopped after %d pages (--max-pages); results are incomplete", res.Pages)))
		}
		if o.raw && res.Last != nil && res.Last.Envelope != nil {
			env := map[string]any{
				"success":     true,
				"errors":      []any{},
				"messages":    []any{},
				"result":      res.Result,
				"result_info": map[string]any{"pages_fetched": res.Pages, "count": countItems(res.Result)},
			}
			return printJSONValue(env)
		}
		return printBody(res.Result, res.Last)
	}

	resp, err := s.c.Do(ctx, req)
	if err != nil {
		return printFailure(err, o)
	}
	if resp.Envelope != nil {
		for _, m := range resp.Envelope.Messages {
			if m.Message != "" {
				fmt.Fprintln(os.Stderr, ui.Info(m.Message))
			}
		}
	}
	if o.raw || resp.Envelope == nil {
		return printBody(resp.Body, resp)
	}
	return printBody(resp.Envelope.Result, resp)
}

func countItems(raw json.RawMessage) int {
	var a []json.RawMessage
	if json.Unmarshal(raw, &a) == nil {
		return len(a)
	}
	return 0
}

// printFailure prints the error envelope for --raw, then returns err.
func printFailure(err error, o requestOptions) error {
	var apiErr *api.Error
	if o.raw && errors.As(err, &apiErr) && apiErr.Response != nil && len(apiErr.Response.Body) > 0 {
		_ = printBody(apiErr.Response.Body, apiErr.Response)
	}
	return err
}

// printBody pretty-prints JSON (preserving key order) or writes other
// content as-is.
func printBody(body []byte, resp *api.Response) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	if json.Valid(trimmed) {
		var buf bytes.Buffer
		if err := json.Indent(&buf, trimmed, "", "  "); err == nil {
			buf.WriteByte('\n')
			_, err := os.Stdout.Write(buf.Bytes())
			return err
		}
	}
	_, err := os.Stdout.Write(body)
	if err == nil && !bytes.HasSuffix(body, []byte("\n")) && term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println()
	}
	return err
}

func printJSONValue(v any) error {
	v = output.EmptyList(v)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// confirm asks before a destructive request unless --yes was passed. In
// read-only mode it doesn't ask: the guard will refuse the request anyway.
func confirm(cmd *cobra.Command, what string) error {
	if yes, _ := cmd.Flags().GetBool("yes"); yes || api.ReadOnly() {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("refusing to %s without confirmation; pass --yes", what)
	}
	fmt.Fprintf(os.Stderr, "%s %s? [y/N] ", ui.Warn("About to"), what)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return fmt.Errorf("aborted")
}
