package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// DefaultMaxPages bounds every pagination loop.
const DefaultMaxPages = 1000

func errorsAs(err error, target any) bool { return errors.As(err, target) }

// PageResult is the outcome of All.
type PageResult struct {
	// Result is the concatenation of every page's result (see Merge).
	Result json.RawMessage
	// Pages is the number of requests made.
	Pages int
	// Truncated is true when maxPages stopped the loop early.
	Truncated bool
	// Last is the last page's response.
	Last *Response
	// Style is the pagination style that was followed: "cursor", "page",
	// "offset", "before", or "" for a single page.
	Style string
}

// Pagination names the query parameters an endpoint pages with. Empty
// fields mean the endpoint doesn't take that parameter. A nil
// Request.Paging means "look the endpoint up in the API spec" (see
// SpecPagination), falling back to response-driven detection.
type Pagination struct {
	// PageParam is the page-number parameter: "page" or "page_no".
	PageParam string
	// SizeParam is the page-size parameter: per_page, page_size, pageSize,
	// perPage, or limit.
	SizeParam string
	// CursorParam is the opaque-token parameter: cursor, page_token,
	// continuation_token, continuationToken, next_cursor, or scan_cursor.
	CursorParam string
	// OffsetParam / LimitParam are offset/limit pagination ("offset", "limit").
	OffsetParam, LimitParam string
	// BeforeParam is a time-window parameter ("before") that pages backwards
	// through newest-first lists by the last item's creation time (Stream).
	BeforeParam string
}

// IsZero reports whether p names no parameters.
func (p *Pagination) IsZero() bool { return p == nil || *p == Pagination{} }

// cursorParams / pageParams / sizeParams are the request parameter names
// seen in Cloudflare's spec, most common first.
var (
	cursorParams = []string{"cursor", "page_token", "continuation_token", "continuationToken", "next_cursor", "scan_cursor"}
	pageParams   = []string{"page", "page_no"}
	sizeParams   = []string{"per_page", "page_size", "pageSize", "perPage"}
)

// PaginationFromParams derives Pagination from an operation's query
// parameter names.
func PaginationFromParams(names []string) *Pagination {
	has := map[string]bool{}
	for _, n := range names {
		has[n] = true
	}
	first := func(cands []string) string {
		for _, c := range cands {
			if has[c] {
				return c
			}
		}
		return ""
	}
	p := &Pagination{
		PageParam:   first(pageParams),
		SizeParam:   first(sizeParams),
		CursorParam: first(cursorParams),
	}
	if has["limit"] {
		p.LimitParam = "limit"
		if p.SizeParam == "" {
			p.SizeParam = "limit"
		}
	}
	if has["offset"] {
		p.OffsetParam = "offset"
	}
	// before/after are plain filters on most endpoints (audit logs, alert
	// history); they only page when nothing else does (Stream videos).
	if has["before"] && p.PageParam == "" && p.CursorParam == "" && p.OffsetParam == "" {
		p.BeforeParam = "before"
	}
	return p
}

// SpecPagination, when set, returns the pagination parameters of the
// operation matching method and a concrete path (nil if unknown). cmd wires
// it to the embedded OpenAPI spec so that both `api request` and generated
// commands page correctly without per-command code.
var SpecPagination func(method, path string) *Pagination

// tokenFields maps response fields that carry the next page's token to the
// request parameter that takes it (when the endpoint isn't known).
var tokenFields = []struct{ field, param string }{
	{"cursor", "cursor"},
	{"next_cursor", "cursor"},
	{"nextCursor", "cursor"},
	{"next_page_token", "page_token"},
	{"nextPageToken", "page_token"},
	{"nextContinuationToken", "continuationToken"},
	{"continuation_token", "continuation_token"},
	{"continuationToken", "continuationToken"},
}

// pageMeta collects the pagination hints of one response: result_info, a
// top-level paging/pagination object, other top-level scalars, and scalar
// fields of an object result. Earlier sources win.
type pageMeta struct {
	srcs []map[string]json.RawMessage
}

func newPageMeta(body []byte, result json.RawMessage) *pageMeta {
	m := &pageMeta{}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return m
	}
	add := func(raw json.RawMessage) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil && obj != nil {
			m.srcs = append(m.srcs, obj)
		}
	}
	add(top["result_info"])
	add(top["paging"])
	add(top["pagination"])
	m.srcs = append(m.srcs, top)
	add(result)
	return m
}

func (m *pageMeta) raw(key string) (json.RawMessage, bool) {
	for _, s := range m.srcs {
		if v, ok := s[key]; ok && len(v) > 0 && string(v) != "null" {
			return v, true
		}
	}
	return nil, false
}

func (m *pageMeta) str(key string) string {
	v, ok := m.raw(key)
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

func (m *pageMeta) num(keys ...string) (float64, bool) {
	for _, k := range keys {
		v, ok := m.raw(k)
		if !ok {
			continue
		}
		var f float64
		if json.Unmarshal(v, &f) == nil {
			return f, true
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func (m *pageMeta) boolean(keys ...string) (val, ok bool) {
	for _, k := range keys {
		v, found := m.raw(k)
		if !found {
			continue
		}
		var b bool
		if json.Unmarshal(v, &b) == nil {
			return b, true
		}
	}
	return false, false
}

// nextToken returns the next-page token and the request parameter it
// belongs to by default.
func (m *pageMeta) nextToken() (token, param string) {
	if v, ok := m.raw("cursors"); ok {
		var c struct {
			After string `json:"after"`
		}
		if json.Unmarshal(v, &c) == nil && c.After != "" {
			return c.After, "cursor"
		}
	}
	for _, tf := range tokenFields {
		if s := m.str(tf.field); s != "" {
			return s, tf.param
		}
	}
	return "", ""
}

// moreFlag reports an explicit "is there more" signal, if the response has one.
func (m *pageMeta) moreFlag() (more, ok bool) {
	return m.boolean("has_more", "hasMore", "is_truncated", "isTruncated", "truncated", "more")
}

// All fetches every page of a list endpoint and concatenates the results.
//
// Which query parameters page the endpoint comes from r.Paging, else
// SpecPagination (the OpenAPI spec), else the response itself. Each
// response is then checked in this order:
//
//   - cursor: a next token in result_info, the result, or the body
//     (cursor, cursors.after, next_cursor, nextCursor, next_page_token,
//     continuation_token, nextContinuationToken, ...) → repeat with the
//     endpoint's token parameter (cursor, page_token, continuation_token,
//     continuationToken, next_cursor, scan_cursor). An explicit
//     has_more/is_truncated=false stops.
//   - page: page or page_no → next page while total_pages, total_count,
//     has_more, or a full page (count ≥ per_page/page_size/pageSize/perPage)
//     says there is more.
//   - offset: offset/limit → offset += items while total or a full page
//     says there is more.
//   - before: newest-first time windows (Stream) → before = the last item's
//     created time while the page is full.
//   - anything else → the single response is returned.
//
// The loop stops after maxPages requests (≤0 means DefaultMaxPages), on an
// empty page, a repeated cursor, or a page identical to the previous one
// (an endpoint ignoring the page parameter), so it always terminates.
func (c *Client) All(ctx context.Context, r Request, maxPages int) (*PageResult, error) {
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}
	q := url.Values{}
	for k, v := range r.Query {
		q[k] = append([]string(nil), v...)
	}
	r.Query = q

	hints := r.Paging
	known := !hints.IsZero()
	if !known && SpecPagination != nil {
		if p := SpecPagination(r.Method, r.Path); !p.IsZero() {
			hints, known = p, true
		}
	}
	if hints == nil {
		hints = &Pagination{}
	}

	var results []json.RawMessage
	out := &PageResult{}
	seenCursors := map[string]bool{}
	var prev []byte
	page := 1
	if hints.PageParam != "" {
		if p, err := strconv.Atoi(q.Get(hints.PageParam)); err == nil && p > 0 {
			page = p
		}
	} else if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		page = p
	}
	offset := 0
	if hints.OffsetParam != "" {
		offset, _ = strconv.Atoi(q.Get(hints.OffsetParam))
	}
	lastBefore := ""

	for {
		resp, err := c.Do(ctx, r)
		out.Pages++
		if err != nil {
			return nil, err
		}
		out.Last = resp
		var result json.RawMessage
		switch {
		case resp.Envelope != nil && len(resp.Envelope.Result) > 0 && string(resp.Envelope.Result) != "null":
			result = resp.Envelope.Result
		case isObject(resp.Body) && hasTopLevelArrays(resp.Body):
			// Bodies without a result ({"data": [...], "paging": {...}}):
			// page over the body's own array fields.
			result = stripEnvelopeFields(resp.Body)
		case resp.Envelope != nil:
			result = resp.Envelope.Result
		case out.Pages == 1:
			out.Result = resp.Body
			return out, nil
		default:
			return nil, fmt.Errorf("page %d was not a Cloudflare JSON envelope", out.Pages)
		}
		n := resultLen(result)
		if out.Pages > 1 && bytes.Equal(result, prev) {
			break // the endpoint ignored the page parameter
		}
		prev = result
		results = append(results, result)
		if n == 0 {
			break
		}
		meta := newPageMeta(resp.Body, result)
		more, hasMoreFlag := meta.moreFlag()
		if hasMoreFlag && !more {
			break
		}

		next := false
		tok, defParam := meta.nextToken()
		switch {
		case tok != "" && (hints.CursorParam != "" || !known):
			if seenCursors[tok] {
				break
			}
			seenCursors[tok] = true
			param := hints.CursorParam
			if param == "" {
				param = defParam
			}
			r.Query.Set(param, tok)
			out.Style, next = "cursor", true

		case hints.PageParam != "" || (!known && hasPageInfo(meta)):
			param := hints.PageParam
			if param == "" {
				param = "page"
			}
			if morePages(meta, hints, q, page, n, hasMoreFlag && more, known) {
				page++
				r.Query.Set(param, strconv.Itoa(page))
				out.Style, next = "page", true
			}

		case hints.OffsetParam != "":
			limit := querySize(q, hints.LimitParam)
			if limit == 0 {
				if f, ok := meta.num("limit"); ok {
					limit = int(f)
				}
			}
			offset += n
			total, hasTotal := meta.num("total", "total_count", "totalCount")
			switch {
			case hasMoreFlag && more:
				next = true
			case hasTotal:
				next = float64(offset) < total
			case limit > 0:
				next = n >= limit
			default:
				next = true // bounded by the empty-page / repeat checks
			}
			if next {
				r.Query.Set(hints.OffsetParam, strconv.Itoa(offset))
				out.Style = "offset"
			}

		case hints.BeforeParam != "":
			limit := querySize(q, hints.LimitParam)
			created := lastItemTime(result)
			if created != "" && created != lastBefore && (limit == 0 || n >= limit) {
				lastBefore = created
				r.Query.Set(hints.BeforeParam, created)
				out.Style, next = "before", true
			}
		}
		if !next {
			break
		}
		if out.Pages >= maxPages {
			out.Truncated = true
			break
		}
	}
	merged, err := Merge(results)
	if err != nil {
		return nil, err
	}
	out.Result = merged
	return out, nil
}

// hasPageInfo reports whether an unknown endpoint's response looks
// page-numbered (total_pages, or page plus total_count).
func hasPageInfo(m *pageMeta) bool {
	if _, ok := m.num("total_pages", "totalPages"); ok {
		return true
	}
	_, p := m.num("page")
	_, t := m.num("total_count", "totalCount")
	return p && t
}

// morePages decides whether a page-numbered list has another page.
func morePages(m *pageMeta, h *Pagination, q url.Values, page, n int, hasMore, known bool) bool {
	if tp, ok := m.num("total_pages", "totalPages"); ok && tp > 0 {
		return float64(page) < tp
	}
	if hasMore {
		return true
	}
	size := querySize(q, h.SizeParam)
	if size == 0 {
		if f, ok := m.num("per_page", "page_size", "pageSize", "perPage", "limit"); ok {
			size = int(f)
		}
	}
	if tc, ok := m.num("total_count", "totalCount", "total"); ok && tc > 0 && size > 0 {
		return float64(page*size) < tc
	}
	// No totals: keep going while pages come back full (only for endpoints
	// the spec says are page-numbered).
	return known && size > 0 && n >= size
}

func querySize(q url.Values, param string) int {
	if param == "" {
		return 0
	}
	n, _ := strconv.Atoi(q.Get(param))
	return n
}

func isObject(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && t[0] == '{'
}

func hasTopLevelArrays(b []byte) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil {
		return false
	}
	for k, v := range obj {
		if k == "errors" || k == "messages" {
			continue
		}
		if t := bytes.TrimSpace(v); len(t) > 0 && t[0] == '[' {
			return true
		}
	}
	return false
}

// stripEnvelopeFields drops success/errors/messages/result_info from a body
// that carries its list at the top level.
func stripEnvelopeFields(b []byte) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil {
		return b
	}
	for _, k := range []string{"success", "errors", "messages", "result_info", "result"} {
		delete(obj, k)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return b
	}
	return out
}

// lastItemTime returns the creation time of the last item of an array
// result (created, created_at, createdAt, or timestamp).
func lastItemTime(result json.RawMessage) string {
	var arr []map[string]json.RawMessage
	if json.Unmarshal(result, &arr) != nil || len(arr) == 0 {
		return ""
	}
	last := arr[len(arr)-1]
	for _, k := range []string{"created", "created_at", "createdAt", "timestamp"} {
		var s string
		if json.Unmarshal(last[k], &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

// resultLen returns the number of items in a result: array length, or the
// total length of array-valued fields of an object (R2 wraps lists as
// {"buckets": [...]}). Scalars and null count as 0; objects with no array
// fields count as 1.
func resultLen(raw json.RawMessage) int {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		return len(arr)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil && obj != nil {
		n, arrays := 0, 0
		for _, v := range obj {
			var a []json.RawMessage
			if json.Unmarshal(v, &a) == nil && a != nil {
				arrays++
				n += len(a)
			}
		}
		if arrays == 0 {
			return 1
		}
		return n
	}
	return 0
}

// Merge concatenates page results. Arrays are appended; objects have their
// array-valued fields appended (other fields come from the first page).
// A single page is returned unchanged.
func Merge(pages []json.RawMessage) (json.RawMessage, error) {
	if len(pages) == 0 {
		return json.RawMessage("[]"), nil
	}
	if len(pages) == 1 {
		return pages[0], nil
	}
	var first []json.RawMessage
	if json.Unmarshal(pages[0], &first) == nil {
		all := first
		for _, p := range pages[1:] {
			var a []json.RawMessage
			if err := json.Unmarshal(p, &a); err != nil {
				continue // null or mismatched page: skip
			}
			all = append(all, a...)
		}
		if all == nil {
			all = []json.RawMessage{}
		}
		return json.Marshal(all)
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(pages[0], &base); err != nil || base == nil {
		return pages[0], nil
	}
	lists := map[string][]json.RawMessage{}
	for k, v := range base {
		var a []json.RawMessage
		if json.Unmarshal(v, &a) == nil && a != nil {
			lists[k] = a
		}
	}
	for _, p := range pages[1:] {
		var obj map[string]json.RawMessage
		if json.Unmarshal(p, &obj) != nil {
			continue
		}
		for k := range lists {
			var a []json.RawMessage
			if json.Unmarshal(obj[k], &a) == nil {
				lists[k] = append(lists[k], a...)
			}
		}
	}
	for k, a := range lists {
		b, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		base[k] = b
	}
	return json.Marshal(base)
}

// specPathMatch reports whether a concrete path matches a templated one
// ("/zones/{zone_id}/x"), and how many literal segments matched.
func specPathMatch(tmpl, path string) (bool, int) {
	ts := strings.Split(strings.Trim(tmpl, "/"), "/")
	ps := strings.Split(strings.Trim(path, "/"), "/")
	if len(ts) != len(ps) {
		return false, 0
	}
	lit := 0
	for i := range ts {
		if strings.HasPrefix(ts[i], "{") && strings.HasSuffix(ts[i], "}") {
			if ps[i] == "" {
				return false, 0
			}
			continue
		}
		if ts[i] != ps[i] {
			return false, 0
		}
		lit++
	}
	return true, lit
}

// MatchTemplate returns the index of the template best matching path (most
// literal segments), or -1. Exported for the spec wiring in cmd.
func MatchTemplate(templates []string, path string) int {
	path = strings.TrimPrefix(path, "/client/v4")
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if u, err := url.Parse(path); err == nil && u.Host != "" {
		path = strings.TrimPrefix(u.Path, "/client/v4")
	}
	best, bestLit := -1, -1
	for i, t := range templates {
		if ok, lit := specPathMatch(t, path); ok && lit > bestLit {
			best, bestLit = i, lit
		}
	}
	return best
}
