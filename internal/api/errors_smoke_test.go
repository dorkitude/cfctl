package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Found by the live smoke sweep: AI Gateway answers 400 with a validation
// body that isn't a Cloudflare envelope, and the error showed raw JSON.
func TestNonEnvelopeJSONErrors(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"formErrors":[],"fieldErrors":{"start_time":["Invalid input: expected number, received NaN"],"end_time":["Required"]}}`,
			"HTTP 400: end_time: Required; start_time: Invalid input: expected number, received NaN"},
		{`{"message":"bad things"}`, "HTTP 400: bad things"},
		{`{"error":"nope"}`, "HTTP 400: nope"},
		{`{"error":{"message":"nested","code":7}}`, "HTTP 400: nested"},
		{`{"errors":["one",{"message":"two"}]}`, "HTTP 400: one; two"},
		{`plain text failure`, "HTTP 400: plain text failure"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = w.Write([]byte(c.body))
		}))
		_, err := New("tok", srv.URL).Do(context.Background(), Request{Method: "GET", Path: "/x"})
		srv.Close()
		if err == nil || err.Error() != c.want {
			t.Errorf("body %s: err = %v, want %q", c.body, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "{") {
			t.Errorf("error leaks JSON: %v", err)
		}
	}
}

func TestNonEnvelopeErrorCodeAndSuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/ns" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte("{\n  \"code\": 1000,\n  \"error\": \"not_found\"\n}"))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10003,"message":"could not find entrypoint ruleset"}],"result":null}`))
	}))
	defer srv.Close()
	c := New("tok", srv.URL)
	_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/ns"})
	if err == nil || err.Error() != "HTTP 404: not_found (code 1000)" {
		t.Errorf("pretty-printed JSON error: %v", err)
	}
	_, err = c.Do(context.Background(), Request{Method: "GET", Path: "/ok-but-not"})
	if err == nil || err.Error() != "API error: could not find entrypoint ruleset (code 10003)" {
		t.Errorf("200 with success:false: %v", err)
	}
}

// Containers and AI Gateway put their own JSON error body in the envelope's
// error message.
func TestEnvelopeMessageWrappingJSON(t *testing.T) {
	body := `{"success":false,"result":null,"messages":[],"errors":[{"code":1000,"message":"{\"error\":\"Unauthorized: You do not have access to Cloudflare Containers.\"}"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	_, err := New("tok", srv.URL).Do(context.Background(), Request{Method: "GET", Path: "/x"})
	want := "HTTP 401: Unauthorized: You do not have access to Cloudflare Containers. (code 1000)"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
	var ae *Error
	if errors.As(err, &ae) && ae.Response == nil {
		t.Error("response must stay attached for --raw")
	}
}
