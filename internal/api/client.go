package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/dorkitude/cfctl/internal/version"
)

// DefaultBaseURL is the Cloudflare API v4 base.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// MaxResponseBytes caps how much of a response body is read into memory.
var MaxResponseBytes int64 = 512 << 20

// Client is a raw Cloudflare API v4 client. It knows auth, the response
// envelope, and pagination; it knows nothing about individual endpoints.
type Client struct {
	// BaseURL is the API base, e.g. https://api.cloudflare.com/client/v4.
	BaseURL string
	token   string
	HTTP    *http.Client
}

// New returns a raw client using the shared transport (read-only guard,
// retries, timeouts, debug logging). baseURL "" means DefaultBaseURL.
func New(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimSuffix(baseURL, "/"), token: token, HTTP: NewHTTPClient()}
}

// Request is one API call. Path is relative to BaseURL ("/zones") or an
// absolute URL on the same host.
type Request struct {
	Method      string
	Path        string
	Query       url.Values
	Header      http.Header
	Body        []byte
	ContentType string
	// Paging names the endpoint's pagination parameters for All; nil means
	// look them up in the API spec (SpecPagination) or detect them.
	Paging *Pagination
}

// Message is one entry in an envelope's errors or messages.
type Message struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message"`
}

// ResultInfo is the envelope's pagination block. Cloudflare uses either page
// numbers (page, per_page, total_pages) or cursors (cursor, or cursors.after).
type ResultInfo struct {
	Page       float64 `json:"page,omitempty"`
	PerPage    float64 `json:"per_page,omitempty"`
	Count      float64 `json:"count,omitempty"`
	TotalCount float64 `json:"total_count,omitempty"`
	TotalPages float64 `json:"total_pages,omitempty"`
	Cursor     string  `json:"cursor,omitempty"`
	Cursors    *struct {
		After  string `json:"after,omitempty"`
		Before string `json:"before,omitempty"`
	} `json:"cursors,omitempty"`
}

// NextCursor returns the cursor for the next page, or "".
func (ri *ResultInfo) NextCursor() string {
	if ri == nil {
		return ""
	}
	if ri.Cursor != "" {
		return ri.Cursor
	}
	if ri.Cursors != nil {
		return ri.Cursors.After
	}
	return ""
}

// Envelope is Cloudflare's standard response wrapper.
type Envelope struct {
	Success    *bool           `json:"success"`
	Errors     []Message       `json:"errors"`
	Messages   []Message       `json:"messages"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *ResultInfo     `json:"result_info,omitempty"`
}

// Response is a completed API call.
type Response struct {
	Status      int
	Header      http.Header
	Body        []byte
	ContentType string
	// Envelope is set when the body is a Cloudflare JSON envelope.
	Envelope *Envelope
}

// IsJSON reports whether the response body is JSON.
func (r *Response) IsJSON() bool {
	return strings.Contains(r.ContentType, "json") || json.Valid(bytes.TrimSpace(r.Body))
}

// Error is a non-2xx response or an envelope with success=false. It never
// includes request headers, so the token can't leak through it.
type Error struct {
	Status       int
	Method, Path string
	Errors       []Message
	// Response is the failed response (for --raw output).
	Response *Response
}

func (e *Error) Error() string {
	var msgs []string
	for _, m := range e.Errors {
		switch {
		case m.Code != 0 && m.Message != "":
			msgs = append(msgs, fmt.Sprintf("%s (code %d)", m.Message, m.Code))
		case m.Message != "":
			msgs = append(msgs, m.Message)
		}
	}
	msg := strings.Join(msgs, "; ")
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if msg == "" {
		msg = "request failed"
	}
	if e.Status < 300 {
		// A 2xx whose envelope says success:false.
		return fmt.Sprintf("API error: %s", msg)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, msg)
}

// URL builds the full URL for a request path and query.
func (c *Client) URL(path string, q url.Values) (string, error) {
	var u string
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		base, _ := url.Parse(c.BaseURL)
		target, err := url.Parse(path)
		if err != nil {
			return "", fmt.Errorf("invalid URL %q: %w", path, err)
		}
		if base == nil || target.Host != base.Host {
			return "", fmt.Errorf("refusing to send credentials to %s; only %s is allowed", target.Host, c.BaseURL)
		}
		u = path
	} else {
		path = strings.TrimPrefix(path, "/client/v4")
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		u = c.BaseURL + path
	}
	if len(q) > 0 {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + q.Encode()
	}
	return u, nil
}

// Do sends a request and decodes the envelope. Non-2xx statuses and
// success=false envelopes return *Error (with the Response attached).
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	if method == "" {
		method = http.MethodGet
	}
	u, err := c.URL(r.Path, r.Query)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if r.ContentType != "" {
		req.Header.Set("Content-Type", r.ContentType)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", "cfctl/"+version.Version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, cleanTransportError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	if err != nil {
		if IsTimeout(err) {
			return nil, fmt.Errorf("timed out reading the response from the Cloudflare API (%w)", context.DeadlineExceeded)
		}
		return nil, err
	}
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: data, ContentType: resp.Header.Get("Content-Type")}
	if len(bytes.TrimSpace(data)) > 0 && out.IsJSON() {
		var env Envelope
		if json.Unmarshal(data, &env) == nil && (env.Success != nil || env.Result != nil || env.Errors != nil) {
			out.Envelope = &env
		}
	}
	failed := resp.StatusCode >= 300 || (out.Envelope != nil && out.Envelope.Success != nil && !*out.Envelope.Success)
	if failed {
		e := &Error{Status: resp.StatusCode, Method: method, Path: req.URL.Path, Response: out}
		if out.Envelope != nil {
			e.Errors = unwrapJSONMessages(out.Envelope.Errors)
		} else if msgs := jsonErrorMessages(data); len(msgs) > 0 {
			e.Errors = msgs
		} else if s := strings.TrimSpace(string(data)); s != "" && len(s) < 300 && !strings.HasPrefix(s, "<") {
			e.Errors = []Message{{Message: s}}
		}
		return out, e
	}
	return out, nil
}

// unwrapJSONMessages replaces envelope error messages that are themselves
// JSON documents (some services wrap their own error body as the message,
// e.g. {"code":1000,"message":"{\"error\":\"Unauthorized...\"}"}) with
// the readable text inside. The envelope's code is kept.
func unwrapJSONMessages(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		t := strings.TrimSpace(m.Message)
		if strings.HasPrefix(t, "{") {
			if inner := jsonErrorMessages([]byte(t)); len(inner) > 0 {
				for i, im := range inner {
					if im.Code == 0 && i == len(inner)-1 {
						im.Code = m.Code
					}
					out = append(out, im)
				}
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// jsonErrorMessages pulls readable messages out of a JSON error body that
// isn't a Cloudflare envelope, so errors don't show raw JSON. It knows
// {"message"|"error"|"detail"|"title": "..."}, {"error": {"message": ...}},
// {"errors": ["..." | {"message": ...}]}, and validation errors shaped
// {"formErrors": [...], "fieldErrors": {"field": ["..."]}}.
func jsonErrorMessages(data []byte) []Message {
	var obj map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(data), &obj) != nil {
		return nil
	}
	str := func(raw json.RawMessage) string {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return strings.TrimSpace(s)
		}
		var m struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		}
		if json.Unmarshal(raw, &m) == nil {
			return strings.TrimSpace(m.Message)
		}
		return ""
	}
	var out []Message
	if raw, ok := obj["fieldErrors"]; ok {
		var fields map[string][]string
		_ = json.Unmarshal(raw, &fields)
		names := make([]string, 0, len(fields))
		for k := range fields {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if len(fields[k]) > 0 {
				out = append(out, Message{Message: k + ": " + strings.Join(fields[k], ", ")})
			}
		}
	}
	if raw, ok := obj["formErrors"]; ok {
		var form []string
		_ = json.Unmarshal(raw, &form)
		for _, f := range form {
			out = append(out, Message{Message: f})
		}
	}
	if raw, ok := obj["errors"]; ok && len(out) == 0 {
		var list []json.RawMessage
		_ = json.Unmarshal(raw, &list)
		for _, item := range list {
			if s := str(item); s != "" {
				out = append(out, Message{Message: s})
			}
		}
	}
	if len(out) == 0 {
		var code int
		if raw, ok := obj["code"]; ok {
			_ = json.Unmarshal(raw, &code)
		}
		for _, k := range []string{"message", "error", "detail", "title", "error_description"} {
			if raw, ok := obj[k]; ok {
				if s := str(raw); s != "" {
					out = append(out, Message{Message: s, Code: code})
					break
				}
			}
		}
	}
	return out
}

// cleanTransportError strips the URL wrapper from transport errors (the URL
// can carry query values) and maps timeouts to a friendly message.
func cleanTransportError(err error) error {
	var ue *url.Error
	if errorsAs(err, &ue) {
		err = ue.Err
	}
	if IsTimeout(err) {
		return fmt.Errorf("timed out talking to the Cloudflare API (%w); raise --timeout if this is expected", context.DeadlineExceeded)
	}
	return err
}

// GraphQL posts a query to the GraphQL Analytics API and returns the raw
// response body ({"data":...,"errors":...}). GraphQL-level errors are not
// turned into Go errors here; HTTP errors are.
func (c *Client) GraphQL(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	if variables == nil {
		variables = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(ctx, Request{Method: http.MethodPost, Path: "/graphql", Body: body, ContentType: "application/json"})
	if err != nil {
		var apiErr *Error
		// GraphQL can return 4xx/5xx with a {data,errors} body; return it.
		if errorsAs(err, &apiErr) && resp != nil && resp.Envelope == nil && json.Valid(resp.Body) {
			return resp.Body, err
		}
		return nil, err
	}
	return resp.Body, nil
}
