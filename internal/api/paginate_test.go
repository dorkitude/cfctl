package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
)

func mustAll(t *testing.T, s *server, r Request, max int) *PageResult {
	t.Helper()
	res, err := New(testToken, s.base()).All(context.Background(), r, max)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func items(n0, n int) []any {
	var out []any
	for i := n0; i < n0+n; i++ {
		out = append(out, i)
	}
	return out
}

func TestPaginationFromSpec(t *testing.T) {
	cases := []struct {
		path string
		want Pagination
	}{
		{"/zones", Pagination{PageParam: "page", SizeParam: "per_page"}},
		{"/accounts/abc/r2/buckets", Pagination{SizeParam: "per_page", CursorParam: "cursor"}},
		{"/accounts/abc/images/v2", Pagination{SizeParam: "per_page", CursorParam: "continuation_token"}},
		{"/accounts/abc/r2/buckets/b/jobs", Pagination{CursorParam: "continuationToken"}},
		{"/accounts/abc/containers/applications", Pagination{SizeParam: "per_page", CursorParam: "page_token"}},
		{"/accounts/abc/realtime/kit/apps", Pagination{PageParam: "page_no", SizeParam: "per_page"}},
		{"/accounts/abc/workers/observability/destinations", Pagination{PageParam: "page", SizeParam: "perPage"}},
		{"/accounts/abc/stream", Pagination{SizeParam: "limit", LimitParam: "limit", BeforeParam: "before"}},
		{"/accounts/abc/urlscanner/scan", Pagination{SizeParam: "limit", LimitParam: "limit", CursorParam: "next_cursor"}},
		{"/accounts/abc/audit_logs", Pagination{PageParam: "page", SizeParam: "per_page"}},
	}
	for _, c := range cases {
		got := SpecPagination("GET", c.path)
		if got == nil {
			t.Errorf("%s: no spec match", c.path)
			continue
		}
		g := *got
		g.OffsetParam = "" // checked separately below
		if g != c.want {
			t.Errorf("%s: got %+v, want %+v", c.path, g, c.want)
		}
	}
	if p := SpecPagination("GET", "/accounts/abc/ai/finetunes/public"); p == nil || p.OffsetParam != "offset" || p.LimitParam != "limit" {
		t.Errorf("offset/limit: %+v", p)
	}
	if p := SpecPagination("GET", "/no/such/endpoint/here"); p != nil {
		t.Errorf("unknown path matched: %+v", p)
	}
}

// page_no + per_page with no totals: keep going while pages are full;
// items live at the top level ({"success", "data", "paging"}).
func TestAllPageNoTopLevelData(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("page_no"))
		if p == 0 {
			p = 1
		}
		n := 2
		if p == 3 {
			n = 1
		}
		b, _ := json.Marshal(map[string]any{"success": true, "data": items(p*10, n), "paging": map[string]any{"start_offset": 0}})
		w.Write(b)
	})
	res := mustAll(t, s, Request{Path: "/accounts/abc/realtime/kit/apps", Query: map[string][]string{"per_page": {"2"}}}, 0)
	if string(res.Result) != `{"data":[10,11,20,21,30],"paging":{"start_offset":0}}` || res.Pages != 3 || res.Style != "page" {
		t.Fatalf("got %s in %d pages (%s)", res.Result, res.Pages, res.Style)
	}
}

// page + total_count, no total_pages.
func TestAllPageTotalCount(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if p == 0 {
			p = 1
		}
		w.Write(envelope(items(p*10, 2), map[string]any{"page": p, "per_page": 2, "count": 2, "total_count": 5}))
	})
	res := mustAll(t, s, Request{Path: "/accounts"}, 0)
	if res.Pages != 3 {
		t.Fatalf("got %s in %d pages", res.Result, res.Pages)
	}
}

// page_token in, next_page_token out (result_info, then the result object).
func TestAllPageToken(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page_token") {
		case "":
			w.Write(envelope([]any{"a"}, map[string]any{"next_page_token": "t2", "per_page": 1}))
		case "t2":
			w.Write(envelope([]any{"b"}, map[string]any{"next_page_token": "", "per_page": 1}))
		default:
			t.Errorf("bad token %q", r.URL.RawQuery)
		}
	})
	res := mustAll(t, s, Request{Path: "/accounts/abc/containers/applications"}, 0)
	if string(res.Result) != `["a","b"]` || res.Style != "cursor" {
		t.Fatalf("got %s (%s)", res.Result, res.Style)
	}

	s2 := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page_token") {
		case "":
			w.Write(envelope(map[string]any{"namespaces": []any{"n1"}, "next_page_token": "x"}, nil))
		case "x":
			w.Write(envelope(map[string]any{"namespaces": []any{"n2"}, "next_page_token": nil}, nil))
		}
	})
	res = mustAll(t, s2, Request{Path: "/accounts/abc/basin-catalog/bkt/namespaces"}, 0)
	if string(res.Result) != `{"namespaces":["n1","n2"],"next_page_token":"x"}` || res.Pages != 2 {
		t.Fatalf("got %s in %d pages", res.Result, res.Pages)
	}
}

// continuation_token (Images v2) and continuationToken/nextContinuationToken (R2 jobs).
func TestAllContinuationTokens(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("continuation_token") {
		case "":
			w.Write(envelope(map[string]any{"images": []any{"i1", "i2"}, "continuation_token": "ct"}, nil))
		case "ct":
			w.Write(envelope(map[string]any{"images": []any{"i3"}, "continuation_token": ""}, nil))
		}
	})
	res := mustAll(t, s, Request{Path: "/accounts/abc/images/v2"}, 0)
	if res.Pages != 2 || resultLen(res.Result) != 3 {
		t.Fatalf("images: %s in %d", res.Result, res.Pages)
	}

	s2 := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("continuationToken") {
		case "":
			w.Write(envelope(map[string]any{"jobs": []any{"j1"}, "nextContinuationToken": "n2"}, nil))
		case "n2":
			w.Write(envelope(map[string]any{"jobs": []any{"j2"}}, nil))
		}
	})
	res = mustAll(t, s2, Request{Path: "/accounts/abc/r2/buckets/b/jobs"}, 0)
	if res.Pages != 2 || resultLen(res.Result) != 2 {
		t.Fatalf("r2 jobs: %s in %d", res.Result, res.Pages)
	}
}

// offset/limit: with a total, and without (stop at a short page).
func TestAllOffsetLimit(t *testing.T) {
	var offsets []string
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		offsets = append(offsets, r.URL.Query().Get("offset"))
		n := 3
		if off+n > 7 {
			n = 7 - off
		}
		w.Write(envelope(items(off, n), map[string]any{"count": n, "limit": 3, "offset": off, "total": 7}))
	})
	res := mustAll(t, s, Request{Path: "/accounts/abc/ai/finetunes/public", Query: map[string][]string{"limit": {"3"}}}, 0)
	if string(res.Result) != `[0,1,2,3,4,5,6]` || res.Style != "offset" || fmt.Sprint(offsets) != "[ 3 6]" {
		t.Fatalf("got %s offsets %v (%s)", res.Result, offsets, res.Style)
	}

	// A body with no envelope and a top-level total ({"matches": [...], "total": N}).
	s2 := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		b, _ := json.Marshal(map[string]any{"matches": items(off, 2), "total": 4})
		w.Write(b)
	})
	res = mustAll(t, s2, Request{Path: "/x", Paging: &Pagination{OffsetParam: "offset", LimitParam: "limit"}}, 0)
	if string(res.Result) != `{"matches":[0,1,2,3],"total":4}` || res.Pages != 2 {
		t.Fatalf("top-level total: %s in %d", res.Result, res.Pages)
	}
}

// before: newest-first windows (Stream).
func TestAllBefore(t *testing.T) {
	var befores []string
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		b := r.URL.Query().Get("before")
		befores = append(befores, b)
		switch b {
		case "":
			w.Write(envelope([]any{map[string]any{"uid": "v1", "created": "2026-03"}, map[string]any{"uid": "v2", "created": "2026-02"}}, nil))
		case "2026-02":
			w.Write(envelope([]any{map[string]any{"uid": "v3", "created": "2026-01"}}, nil))
		}
	})
	res := mustAll(t, s, Request{Path: "/accounts/abc/stream", Query: map[string][]string{"limit": {"2"}}}, 0)
	if res.Pages != 2 || resultLen(res.Result) != 3 || res.Style != "before" {
		t.Fatalf("got %s in %d (%v)", res.Result, res.Pages, befores)
	}
}

// next_cursor / scan_cursor request params take result_info.cursor or next_cursor.
func TestAllNamedCursorParams(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("scan_cursor") {
		case "":
			w.Write(envelope([]any{1}, map[string]any{"cursor": "s2", "count": 1}))
		case "s2":
			w.Write(envelope([]any{2}, map[string]any{"cursor": "", "count": 1}))
		}
	})
	res := mustAll(t, s, Request{Path: "/accounts/abc/managed-defense/vulnerability-discovery/repos/r1"}, 0)
	if string(res.Result) != `[1,2]` {
		t.Fatalf("scan_cursor: %s", res.Result)
	}
	s2 := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("cursor") {
		case "":
			w.Write(envelope([]any{1}, map[string]any{"next_cursor": "z", "count": 1}))
		case "z":
			w.Write(envelope([]any{2}, map[string]any{"count": 1}))
		}
	})
	res = mustAll(t, s2, Request{Path: "/accounts/abc/email/sending/suppressions"}, 0)
	if string(res.Result) != `[1,2]` {
		t.Fatalf("next_cursor: %s", res.Result)
	}
}

// has_more=false stops even with a cursor; an endpoint that ignores the
// page parameter stops after one repeat.
func TestAllStopSignals(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(envelope(map[string]any{"items": []any{1}, "cursor": "c", "has_more": false}, nil))
	})
	if res := mustAll(t, s, Request{Path: "/x"}, 0); res.Pages != 1 {
		t.Fatalf("has_more=false: %d pages", res.Pages)
	}
	s2 := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(envelope([]any{1, 2}, map[string]any{"per_page": 2}))
	})
	res := mustAll(t, s2, Request{Path: "/x", Paging: &Pagination{PageParam: "page", SizeParam: "per_page"}}, 0)
	if res.Pages != 2 || string(res.Result) != `[1,2]` {
		t.Fatalf("ignored page param: %s in %d", res.Result, res.Pages)
	}
	// Unknown endpoint, no totals: one page.
	s3 := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(envelope([]any{1, 2}, map[string]any{"per_page": 2, "page": 1}))
	})
	if res := mustAll(t, s3, Request{Path: "/no/such/thing"}, 0); res.Pages != 1 {
		t.Fatalf("unknown endpoint: %d pages", res.Pages)
	}
}
