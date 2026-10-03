// Package api is cfctl's shared HTTP layer for the Cloudflare API.
//
// Every HTTP request cfctl makes — through the cloudflare-go SDK or the raw
// client in this package — goes through the http.Client built by
// NewHTTPClient. Its transport chain is:
//
//	readOnlyTransport → retryTransport → debugTransport → http.DefaultTransport
//
// so the read-only guard is enforced in exactly one place, before anything is
// logged, retried, or sent. Retries (429/5xx) and per-attempt timeouts live
// in retryTransport, also shared by every client.
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Process-wide settings, set once from flags in the root command.
var (
	mu sync.RWMutex
	// readOnly is the --read-only flag. CFCTL_READONLY is also checked on
	// every request, so the env var can never be bypassed.
	readOnly bool
	debug    bool
	// DebugOut is where --debug lines go (stderr; swapped in tests).
	DebugOut io.Writer = os.Stderr
	// Timeout bounds each HTTP request attempt (--timeout).
	Timeout = 30 * time.Second
)

// SetReadOnly turns the read-only guard on or off (the --read-only flag).
func SetReadOnly(on bool) { mu.Lock(); readOnly = on; mu.Unlock() }

// SetDebug turns request logging on or off (the --debug flag).
func SetDebug(on bool) { mu.Lock(); debug = on; mu.Unlock() }

// Debug reports whether request logging is on.
func Debug() bool { mu.RLock(); defer mu.RUnlock(); return debug }

// ReadOnly reports whether the read-only guard is on, via --read-only or
// CFCTL_READONLY (any value except "", "0", "false", "no", "off").
func ReadOnly() bool {
	mu.RLock()
	on := readOnly
	mu.RUnlock()
	return on || envTruthy(os.Getenv("CFCTL_READONLY"))
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// ErrReadOnly is wrapped by every error the read-only guard returns.
var ErrReadOnly = errors.New("read-only mode")

// ReadOnlyError is returned for a request refused by the read-only guard.
type ReadOnlyError struct {
	Method, Path string
}

func (e *ReadOnlyError) Error() string {
	return fmt.Sprintf("refusing %s %s: read-only mode", e.Method, e.Path)
}

// Unwrap lets errors.Is(err, ErrReadOnly) match.
func (e *ReadOnlyError) Unwrap() error { return ErrReadOnly }

// GraphQLPath is the GraphQL Analytics endpoint path under the API base.
const GraphQLPath = "/client/v4/graphql"

// AllowedReadOnly reports whether a request may be sent in read-only mode:
// GET, HEAD, OPTIONS, and POST to /client/v4/graphql (queries only by
// convention; Cloudflare's GraphQL API is analytics, read-only).
func AllowedReadOnly(method, path string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	case http.MethodPost:
		return strings.TrimSuffix(path, "/") == GraphQLPath
	}
	return false
}

// readOnlyTransport refuses mutating requests when the guard is on.
type readOnlyTransport struct{ next http.RoundTripper }

func (t readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if ReadOnly() && !AllowedReadOnly(req.Method, req.URL.Path) {
		if req.Body != nil {
			req.Body.Close()
		}
		if Debug() {
			fmt.Fprintf(DebugOut, "debug: %s %s -> refused, not sent (read-only mode)\n", req.Method, req.URL.Path)
		}
		return nil, &ReadOnlyError{Method: req.Method, Path: req.URL.Path}
	}
	return t.next.RoundTrip(req)
}

// debugTransport logs METHOD path status duration. Never headers, query
// strings, or bodies, so the token cannot leak.
type debugTransport struct{ next http.RoundTripper }

func (t debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !Debug() {
		return t.next.RoundTrip(req)
	}
	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	var status string
	switch {
	case err != nil && IsTimeout(err):
		status = "timeout"
	case err != nil:
		status = "error"
	default:
		status = strconv.Itoa(resp.StatusCode)
	}
	fmt.Fprintf(DebugOut, "debug: %s %s -> %s (%s)\n", req.Method, req.URL.Path, status, time.Since(start).Round(time.Millisecond))
	return resp, err
}

// baseTransport is the innermost transport (swappable in tests).
var baseTransport http.RoundTripper = http.DefaultTransport

// NewTransport wraps next (nil = http.DefaultTransport) in cfctl's
// read-only guard, retries, per-attempt timeout, and debug logging.
func NewTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = baseTransport
	}
	return readOnlyTransport{next: retryTransport{next: debugTransport{next: next}}}
}

// NewHTTPClient returns the http.Client every cfctl API client must use.
// Each attempt is bounded by Timeout; whole commands (e.g. --all pagination)
// are not, so long runs aren't cut off.
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: NewTransport(nil)}
}

// IsTimeout reports whether err is a context deadline or network timeout.
func IsTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
