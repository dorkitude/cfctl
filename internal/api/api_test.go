package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/r2"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

const testToken = "cfctl-api-test-SECRET-token"

// server records every request it sees.
type server struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []string
}

func newServer(t *testing.T, h http.HandlerFunc) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.reqs) }

func (s *server) base() string { return s.URL + "/client/v4" }

func envelope(result any, info any) []byte {
	m := map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": result}
	if info != nil {
		m["result_info"] = info
	}
	b, _ := json.Marshal(m)
	return b
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(envelope(map[string]any{"ok": true}, nil))
}

// guardOn turns the guard on for one test.
func guardOn(t *testing.T) {
	t.Helper()
	SetReadOnly(true)
	t.Cleanup(func() { SetReadOnly(false) })
}

func captureDebug(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := DebugOut
	DebugOut = &buf
	SetDebug(true)
	t.Cleanup(func() { DebugOut = old; SetDebug(false) })
	return &buf
}

func TestAllowedReadOnly(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/client/v4/zones", true},
		{"get", "/client/v4/zones", true},
		{"HEAD", "/client/v4/zones", true},
		{"OPTIONS", "/client/v4/zones", true},
		{"POST", "/client/v4/graphql", true},
		{"POST", "/client/v4/graphql/", true},
		{"POST", "/client/v4/zones", false},
		{"POST", "/client/v4/graphql/x", false},
		{"POST", "/client/v4/accounts/x/graphql", false},
		{"PUT", "/client/v4/graphql", false},
		{"PUT", "/client/v4/zones/x", false},
		{"PATCH", "/client/v4/zones/x", false},
		{"DELETE", "/client/v4/zones/x", false},
		{"TRACE", "/client/v4/zones", false},
		{"CONNECT", "/client/v4/zones", false},
	}
	for _, c := range cases {
		if got := AllowedReadOnly(c.method, c.path); got != c.want {
			t.Errorf("AllowedReadOnly(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestReadOnlyEnv(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "false": false, "off": false, "no": false, "1": true, "true": true, "yes": true} {
		t.Setenv("CFCTL_READONLY", v)
		if got := ReadOnly(); got != want {
			t.Errorf("CFCTL_READONLY=%q: ReadOnly() = %v, want %v", v, got, want)
		}
	}
}

func TestGuardRawClient(t *testing.T) {
	s := newServer(t, okHandler)
	c := New(testToken, s.base())
	ctx := context.Background()
	guardOn(t)
	dbg := captureDebug(t)

	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		_, err := c.Do(ctx, Request{Method: m, Path: "/accounts/abc/r2/buckets", Body: []byte(`{"name":"x"}`)})
		want := fmt.Sprintf("refusing %s /client/v4/accounts/abc/r2/buckets: read-only mode", m)
		if err == nil || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", m, err, want)
		}
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s: error should wrap ErrReadOnly", m)
		}
	}
	if n := s.count(); n != 0 {
		t.Fatalf("server saw %d requests; guard must refuse before sending", n)
	}
	if !strings.Contains(dbg.String(), "refused, not sent") || strings.Contains(dbg.String(), "->  2") {
		t.Errorf("debug output: %q", dbg.String())
	}

	// Allowed: GET, HEAD, OPTIONS, GraphQL POST.
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		if _, err := c.Do(ctx, Request{Method: m, Path: "/zones"}); err != nil {
			t.Errorf("%s refused: %v", m, err)
		}
	}
	if _, err := c.GraphQL(ctx, "{ viewer { __typename } }", nil); err != nil {
		t.Errorf("graphql refused: %v", err)
	}
	if n := s.count(); n != 4 {
		t.Errorf("server saw %d requests, want 4", n)
	}
}

func TestGuardEnvVarAlone(t *testing.T) {
	s := newServer(t, okHandler)
	t.Setenv("CFCTL_READONLY", "1")
	_, err := New(testToken, s.base()).Do(context.Background(), Request{Method: "DELETE", Path: "/zones/x"})
	if !errors.Is(err, ErrReadOnly) || s.count() != 0 {
		t.Fatalf("env guard: err=%v requests=%d", err, s.count())
	}
}

func TestGuardOffSends(t *testing.T) {
	s := newServer(t, okHandler)
	if _, err := New(testToken, s.base()).Do(context.Background(), Request{Method: "POST", Path: "/zones", Body: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	if s.count() != 1 {
		t.Fatal("POST should be sent when the guard is off")
	}
}

// sdkClient builds a cloudflare-go client the way internal/client does.
func sdkClient(base string) *cloudflare.Client {
	return cloudflare.NewClient(
		option.WithAPIToken(testToken),
		option.WithBaseURL(base),
		option.WithHTTPClient(NewHTTPClient()),
		option.WithMaxRetries(0),
	)
}

func TestGuardSDK(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(envelope([]any{}, map[string]any{"page": 1, "per_page": 20, "total_pages": 1, "count": 0}))
	})
	c := sdkClient(s.base() + "/")
	ctx := context.Background()
	guardOn(t)

	// Mutations via the SDK are refused without a request.
	_, err := c.R2.Buckets.New(ctx, r2.BucketNewParams{AccountID: cloudflare.F("abc"), Name: cloudflare.F("x")})
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("SDK bucket create: err = %v, want read-only refusal", err)
	}
	_, err = c.Zones.Delete(ctx, zones.ZoneDeleteParams{ZoneID: cloudflare.F("z")})
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("SDK zone delete: err = %v", err)
	}
	if s.count() != 0 {
		t.Fatalf("server saw %d requests from refused SDK calls", s.count())
	}
	// Reads via the SDK go through.
	if _, err := c.Zones.List(ctx, zones.ZoneListParams{}); err != nil {
		t.Fatalf("SDK zone list: %v", err)
	}
	if s.count() != 1 {
		t.Fatalf("server saw %d requests, want 1", s.count())
	}
}

func TestDebugNeverLogsSecrets(t *testing.T) {
	s := newServer(t, okHandler)
	dbg := captureDebug(t)
	q := map[string][]string{"secret_query": {"QUERYVALUE"}}
	if _, err := New(testToken, s.base()).Do(context.Background(), Request{Method: "GET", Path: "/zones", Query: q, Header: http.Header{"X-Custom": {"HEADERVALUE"}}}); err != nil {
		t.Fatal(err)
	}
	out := dbg.String()
	if !strings.Contains(out, "debug: GET /client/v4/zones -> 200") {
		t.Errorf("debug line missing: %q", out)
	}
	for _, bad := range []string{testToken, "QUERYVALUE", "HEADERVALUE", "Bearer", "secret_query"} {
		if strings.Contains(out, bad) {
			t.Errorf("debug output leaked %q: %q", bad, out)
		}
	}
}

func fastRetries(t *testing.T) {
	t.Helper()
	ob, om := RetryBaseDelay, RetryMaxDelay
	RetryBaseDelay, RetryMaxDelay = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { RetryBaseDelay, RetryMaxDelay = ob, om })
}

func TestRetries(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Method == "PUT" && string(body) != `{"a":1}` {
			t.Errorf("retried PUT lost its body: %q", body)
		}
		if n <= 2 {
			code := 503
			if r.URL.Path == "/client/v4/rate" {
				code = 429
				w.Header().Set("Retry-After", "0")
			}
			w.WriteHeader(code)
			return
		}
		okHandler(w, r)
	})
	c := New(testToken, s.base())
	ctx := context.Background()

	if _, err := c.Do(ctx, Request{Method: "GET", Path: "/x"}); err != nil || calls.Load() != 3 {
		t.Fatalf("GET 503 retry: err=%v calls=%d", err, calls.Load())
	}
	calls.Store(0)
	if _, err := c.Do(ctx, Request{Method: "PUT", Path: "/x", Body: []byte(`{"a":1}`)}); err != nil || calls.Load() != 3 {
		t.Fatalf("PUT 503 retry: err=%v calls=%d", err, calls.Load())
	}
	calls.Store(0)
	if _, err := c.Do(ctx, Request{Method: "POST", Path: "/rate", Body: []byte(`{}`)}); err != nil || calls.Load() != 3 {
		t.Fatalf("POST 429 retry: err=%v calls=%d", err, calls.Load())
	}
	// POST + 5xx is not retried (not idempotent).
	calls.Store(0)
	_, err := c.Do(ctx, Request{Method: "POST", Path: "/x", Body: []byte(`{}`)})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 503 || calls.Load() != 1 {
		t.Fatalf("POST 503: err=%v calls=%d", err, calls.Load())
	}
	// 4xx (other than 429) is never retried.
	var n4 atomic.Int32
	s4 := newServer(t, func(w http.ResponseWriter, r *http.Request) { n4.Add(1); w.WriteHeader(404) })
	if _, err := New(testToken, s4.base()).Do(ctx, Request{Method: "GET", Path: "/x"}); err == nil || n4.Load() != 1 {
		t.Fatalf("404: calls=%d err=%v", n4.Load(), err)
	}
}

func TestRetriesBounded(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
	_, err := New(testToken, s.base()).Do(context.Background(), Request{Method: "GET", Path: "/x"})
	if err == nil || int(calls.Load()) != MaxRetries+1 {
		t.Fatalf("err=%v calls=%d, want %d", err, calls.Load(), MaxRetries+1)
	}
}

func TestTimeoutPerAttempt(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	old := Timeout
	Timeout = 100 * time.Millisecond
	t.Cleanup(func() { Timeout = old })
	start := time.Now()
	_, err := New(testToken, s.base()).Do(context.Background(), Request{Method: "GET", Path: "/x"})
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %s", err, time.Since(start))
	}
}

func TestEnvelopeErrors(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/v4/denied":
			w.WriteHeader(403)
			w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Unauthorized"}],"messages":[],"result":null}`))
		case "/client/v4/soft":
			w.Write([]byte(`{"success":false,"errors":[{"code":1,"message":"nope"}],"result":null}`))
		case "/client/v4/text":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("hello value"))
		}
	})
	c := New(testToken, s.base())
	ctx := context.Background()
	_, err := c.Do(ctx, Request{Path: "/denied"})
	if err == nil || err.Error() != "HTTP 403: Unauthorized (code 9109)" {
		t.Errorf("403: %v", err)
	}
	_, err = c.Do(ctx, Request{Path: "denied"}) // relative path without slash
	if err == nil {
		t.Error("expected error")
	}
	_, err = c.Do(ctx, Request{Path: "/soft"})
	if err == nil || !strings.Contains(err.Error(), "nope (code 1)") {
		t.Errorf("success=false with 200: %v", err)
	}
	resp, err := c.Do(ctx, Request{Path: "/client/v4/text"})
	if err != nil || resp.Envelope != nil || string(resp.Body) != "hello value" {
		t.Errorf("text: %v %+v", err, resp)
	}
	// Absolute URLs to another host are refused (the token would leak).
	if _, err := c.Do(ctx, Request{Path: "https://evil.example.com/x"}); err == nil || !strings.Contains(err.Error(), "refusing to send credentials") {
		t.Errorf("foreign host: %v", err)
	}
}

func TestAllPagePagination(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if p == 0 {
			p = 1
		}
		if r.URL.Query().Get("per_page") != "2" {
			t.Errorf("per_page not preserved: %q", r.URL.RawQuery)
		}
		w.Write(envelope([]any{fmt.Sprintf("p%d-a", p), fmt.Sprintf("p%d-b", p)}, map[string]any{"page": p, "per_page": 2, "total_pages": 3, "count": 2, "total_count": 6}))
	})
	res, err := New(testToken, s.base()).All(context.Background(), Request{Path: "/zones", Query: map[string][]string{"per_page": {"2"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Result) != `["p1-a","p1-b","p2-a","p2-b","p3-a","p3-b"]` || res.Pages != 3 {
		t.Fatalf("got %s in %d pages", res.Result, res.Pages)
	}
}

func TestAllCursorPagination(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("cursor") {
		case "":
			w.Write(envelope(map[string]any{"buckets": []any{"a", "b"}}, map[string]any{"cursor": "c2", "per_page": 2}))
		case "c2":
			w.Write(envelope(map[string]any{"buckets": []any{"c"}}, map[string]any{"cursors": map[string]any{"after": "c3"}}))
		case "c3":
			w.Write(envelope(map[string]any{"buckets": []any{"d"}}, map[string]any{"cursor": ""}))
		}
	})
	res, err := New(testToken, s.base()).All(context.Background(), Request{Path: "/accounts/x/r2/buckets"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Result) != `{"buckets":["a","b","c","d"]}` || res.Pages != 3 {
		t.Fatalf("got %s in %d pages", res.Result, res.Pages)
	}
}

func TestAllBounded(t *testing.T) {
	var calls atomic.Int32
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		// A broken API: a new cursor forever.
		w.Write(envelope([]any{n}, map[string]any{"cursor": fmt.Sprintf("c%d", n)}))
	})
	res, err := New(testToken, s.base()).All(context.Background(), Request{Path: "/x"}, 7)
	if err != nil || res.Pages != 7 || !res.Truncated || calls.Load() != 7 {
		t.Fatalf("err=%v pages=%d truncated=%v calls=%d", err, res.Pages, res.Truncated, calls.Load())
	}
	// Repeating cursor stops immediately.
	calls.Store(0)
	s2 := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write(envelope([]any{1}, map[string]any{"cursor": "same"}))
	})
	res, err = New(testToken, s2.base()).All(context.Background(), Request{Path: "/x"}, 0)
	if err != nil || calls.Load() != 2 {
		t.Fatalf("repeating cursor: err=%v calls=%d", err, calls.Load())
	}
}

func TestMultipart(t *testing.T) {
	dir := t.TempDir()
	js := filepath.Join(dir, "worker.js")
	os.WriteFile(js, []byte("export default {}"), 0o600)
	var fields []FormField
	for _, s := range []string{`metadata={"main_module":"worker.js"};type=application/json`, "worker.js=@" + js + ";type=application/javascript+module", "plain=hello"} {
		f, err := ParseFormField(s)
		if err != nil {
			t.Fatal(err)
		}
		fields = append(fields, f)
	}
	body, ct, err := BuildMultipart(fields)
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := mime.ParseMediaType(ct)
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	got := map[string]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		got[p.FormName()] = p.FileName() + "|" + p.Header.Get("Content-Type") + "|" + string(b)
	}
	want := map[string]string{
		"metadata":  `|application/json|{"main_module":"worker.js"}`,
		"worker.js": "worker.js|application/javascript+module|export default {}",
		"plain":     "||hello",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("part %s = %q, want %q", k, got[k], v)
		}
	}
	if _, err := ParseFormField("novalue"); err == nil {
		t.Error("expected parse error")
	}
}

func TestMerge(t *testing.T) {
	got, _ := Merge([]json.RawMessage{[]byte(`[1]`), []byte(`null`), []byte(`[2,3]`)})
	if string(got) != `[1,2,3]` {
		t.Errorf("got %s", got)
	}
	got, _ = Merge([]json.RawMessage{[]byte(`{"a":[1],"k":"v"}`), []byte(`{"a":[2],"k":"w"}`)})
	if string(got) != `{"a":[1,2],"k":"v"}` {
		t.Errorf("got %s", got)
	}
}
