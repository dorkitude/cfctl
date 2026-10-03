package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Retry policy. Variables so tests can shrink the delays.
var (
	// MaxRetries is the number of retries after the first attempt.
	MaxRetries = 3
	// RetryBaseDelay doubles per retry (0.5s, 1s, 2s), capped at RetryMaxDelay.
	RetryBaseDelay = 500 * time.Millisecond
	RetryMaxDelay  = 8 * time.Second
	// MaxRetryAfter caps how long a Retry-After header can make us wait.
	MaxRetryAfter = 30 * time.Second
)

// retryTransport applies a per-attempt timeout and retries 429 and 5xx
// responses with exponential backoff. 5xx is only retried for requests that
// are safe to repeat (GET, HEAD, OPTIONS, PUT, DELETE, and GraphQL queries);
// 429 means the request was not processed, so it is always retried. Network
// errors and timeouts are not retried. Requests whose body can't be replayed
// are never retried.
type retryTransport struct{ next http.RoundTripper }

func (t retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		r := req
		if attempt > 0 && req.Body != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r = req.Clone(req.Context())
			r.Body = body
		}
		resp, err := t.attempt(r)
		if err != nil {
			return nil, err
		}
		if attempt >= MaxRetries || !retryable(req, resp.StatusCode) {
			return resp, nil
		}
		delay := backoff(attempt, resp)
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(delay):
		}
	}
}

// attempt sends one request bounded by Timeout. The timeout covers reading
// the body too: the context is cancelled when the body is closed.
func (t retryTransport) attempt(req *http.Request) (*http.Response, error) {
	if Timeout <= 0 {
		return t.next.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), Timeout)
	resp, err := t.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func retryable(req *http.Request, status int) bool {
	if req.Body != nil && req.GetBody == nil {
		return false
	}
	if status == http.StatusTooManyRequests {
		return true
	}
	if status < 500 || status == http.StatusNotImplemented {
		return false
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	case http.MethodPost:
		return strings.TrimSuffix(req.URL.Path, "/") == GraphQLPath
	}
	return false
}

func backoff(attempt int, resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.ParseFloat(s, 64); err == nil && secs >= 0 {
			return min(time.Duration(secs*float64(time.Second)), MaxRetryAfter)
		}
		if when, err := http.ParseTime(s); err == nil {
			return min(max(time.Until(when), 0), MaxRetryAfter)
		}
	}
	d := RetryBaseDelay << attempt
	if d > RetryMaxDelay || d <= 0 {
		d = RetryMaxDelay
	}
	return d
}
