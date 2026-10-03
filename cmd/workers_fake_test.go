package cmd

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// wkFake is the fake Cloudflare API plus Workers routes, the assets upload
// endpoint (JWT auth), and a tail WebSocket server.
type wkFake struct {
	*fakeCF
	mu          sync.Mutex
	versions    []map[string]any // newest first
	deployments []map[string]any // newest first
	routes      []map[string]any
	domains     []map[string]any
	secrets     []map[string]any
	previews    map[string]map[string]any
	// assetUploads are requests to /workers/assets/upload (JWT-authed).
	assetUploads []fakeRequest
	// needUpload is the set of hashes the upload session asks for.
	needUpload bool
	// Tail WebSocket: what the client sent, and what to send it.
	wsHeader   http.Header
	wsFirstMsg string
	wsEvents   []string
	scripts    []map[string]any
}

const (
	wkVer1 = "11111111-1111-4111-8111-111111111111"
	wkVer2 = "22222222-2222-4222-8222-222222222222"
	wkVer3 = "33333333-3333-4333-8333-333333333333"
	wkJWT  = "session-jwt-0001"
	wkDone = "completion-jwt-0002"
)

func newWorkersFake(t *testing.T) *wkFake {
	t.Helper()
	f := &wkFake{fakeCF: apiFakeBase(t), previews: map[string]map[string]any{}, needUpload: true}
	for i, id := range []string{wkVer3, wkVer2, wkVer1} {
		f.versions = append(f.versions, map[string]any{
			"id": id, "number": 3 - i,
			"metadata":    map[string]any{"created_on": fmt.Sprintf("2026-01-0%dT00:00:00Z", 3-i), "source": "wrangler", "author_email": "dev@example.com"},
			"annotations": map[string]any{"workers/triggered_by": "upload"},
		})
	}
	f.deployments = []map[string]any{
		{"id": "dep-2", "source": "api", "strategy": "percentage", "versions": []any{map[string]any{"version_id": wkVer3, "percentage": 100}}, "created_on": "2026-01-03T00:00:00Z", "annotations": map[string]any{"workers/triggered_by": "upload"}},
		{"id": "dep-1", "source": "wrangler", "strategy": "percentage", "versions": []any{map[string]any{"version_id": wkVer2, "percentage": 100}}, "created_on": "2026-01-02T00:00:00Z"},
	}
	f.secrets = []map[string]any{{"name": "EXISTING", "type": "secret_text"}}
	f.scripts = []map[string]any{{"id": "my-worker", "modified_on": "2026-01-03T00:00:00Z", "compatibility_date": "2025-01-01", "handlers": []any{"fetch"}, "last_deployed_from": "wrangler", "migration_tag": "v1"}}
	f.wsEvents = []string{
		`{"outcome":"ok","scriptName":"my-worker","eventTimestamp":1767225600000,"event":{"request":{"method":"GET","url":"https://example.com/hi"},"response":{"status":200}},"logs":[{"message":["hello",1],"level":"log","timestamp":1767225600000}],"exceptions":[]}`,
		`{"outcome":"exception","scriptName":"my-worker","eventTimestamp":1767225601000,"event":{"cron":"*/5 * * * *","scheduledTime":1767225601000},"logs":[],"exceptions":[{"name":"Error","message":"boom","timestamp":1767225601000}]}`,
	}
	f.custom = f.serveWorkers

	// Intercept the JWT-authed assets upload and the tail socket before the
	// fake's token check.
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/workers/assets/upload"):
			f.serveAssetUpload(w, r)
		case strings.HasPrefix(r.URL.Path, "/tail-ws/"):
			f.serveTailWS(w, r)
		default:
			inner.ServeHTTP(w, r)
		}
	})
	return f
}

// apiFakeBase is newFakeCF + testEnv + login (without apiFake's catch-alls).
func apiFakeBase(t *testing.T) *fakeCF {
	f := newFakeCF(t)
	testEnv(t, f)
	login(t)
	return f
}

func (f *wkFake) serveWorkers(w http.ResponseWriter, r *http.Request, req fakeRequest) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	acct := "/accounts/" + acctID
	p := req.Path
	q := r.URL.Query()
	m := r.Method
	script := acct + "/workers/scripts/my-worker"
	switch {
	case m == "GET" && p == acct+"/workers/scripts":
		writeJSON(w, 200, ok(f.scripts))
	case m == "PUT" && p == script:
		writeJSON(w, 200, ok(map[string]any{"id": "my-worker", "etag": "abc"}))
	case m == "DELETE" && p == script:
		writeJSON(w, 200, ok(nil))
	case m == "GET" && p == acct+"/workers/workers/my-worker":
		writeJSON(w, 200, ok(map[string]any{"id": "wid", "name": "my-worker", "created_on": "2026-01-01T00:00:00Z", "updated_on": "2026-01-03T00:00:00Z",
			"subdomain":            map[string]any{"enabled": true, "url": "https://my-worker.team.workers.dev"},
			"previews_base_config": map[string]any{"env": map[string]any{"BASE_SECRET": map[string]any{"type": "secret_text"}, "PLAIN": map[string]any{"type": "plain_text", "text": "x"}}}}))
	case m == "GET" && p == acct+"/workers/workers/my-worker/versions/latest":
		writeJSON(w, 200, ok(map[string]any{"id": wkVer3, "number": 3, "bindings": []any{
			map[string]any{"name": "API", "type": "plain_text", "text": "https://api"},
			map[string]any{"name": "TOKEN", "type": "secret_text"},
		}}))
	case m == "PATCH" && p == acct+"/workers/workers/my-worker/versions/latest":
		writeJSON(w, 200, ok(map[string]any{"id": "55555555-5555-4555-8555-555555555555", "number": 5}))
	case m == "PATCH" && p == acct+"/workers/workers/my-worker":
		writeJSON(w, 200, ok(map[string]any{"id": "wid"}))
	case m == "GET" && p == script+"/settings":
		writeJSON(w, 200, ok(map[string]any{"compatibility_date": "2025-01-01", "usage_model": "standard", "bindings": []any{
			map[string]any{"name": "API", "type": "plain_text", "text": "https://api"},
			map[string]any{"name": "TOKEN", "type": "secret_text"},
		}}))
	case m == "GET" && p == script+"/schedules":
		writeJSON(w, 200, ok(map[string]any{"schedules": []any{map[string]any{"cron": "0 * * * *"}}}))
	case m == "PUT" && p == script+"/schedules":
		writeJSON(w, 200, ok(map[string]any{"schedules": []any{}}))
	case m == "GET" && p == script+"/deployments":
		writeJSON(w, 200, ok(map[string]any{"deployments": f.deployments}))
	case m == "POST" && p == script+"/deployments":
		writeJSON(w, 200, ok(map[string]any{"id": "dep-3", "versions": req.Body["versions"]}))
	case m == "GET" && p == script+"/versions":
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		per := 2 // the fake caps page size to exercise pagination
		start := (page - 1) * per
		items := []any{}
		for i := start; i < start+per && i < len(f.versions); i++ {
			items = append(items, f.versions[i])
		}
		writeJSON(w, 200, map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": map[string]any{"items": items},
			"result_info": map[string]any{"page": page, "per_page": per, "count": len(items), "total_count": len(f.versions)}})
	case m == "POST" && p == script+"/versions":
		writeJSON(w, 200, ok(map[string]any{"id": "44444444-4444-4444-8444-444444444444", "number": 4}))
	case m == "GET" && strings.HasPrefix(p, script+"/versions/"):
		id := strings.TrimPrefix(p, script+"/versions/")
		for _, v := range f.versions {
			if v["id"] == id {
				out := map[string]any{}
				for k, x := range v {
					out[k] = x
				}
				out["resources"] = map[string]any{"script_runtime": map[string]any{"compatibility_date": "2025-01-01"}, "bindings": []any{map[string]any{"name": "TOKEN", "type": "secret_text"}}}
				writeJSON(w, 200, ok(out))
				return true
			}
		}
		writeJSON(w, 404, fail(10007, "version not found"))
	case m == "GET" && p == script+"/secrets":
		writeJSON(w, 200, ok(f.secrets))
	case m == "PUT" && p == script+"/secrets":
		writeJSON(w, 200, ok(map[string]any{"name": req.Body["name"], "type": "secret_text"}))
	case m == "DELETE" && strings.HasPrefix(p, script+"/secrets/"):
		writeJSON(w, 200, ok(nil))
	case m == "PATCH" && p == script+"/secrets-bulk":
		writeJSON(w, 200, ok(map[string]any{}))
	case m == "GET" && p == script+"/subdomain":
		writeJSON(w, 200, ok(map[string]any{"enabled": false, "previews_enabled": false}))
	case m == "POST" && p == script+"/subdomain":
		writeJSON(w, 200, ok(req.Body))
	case m == "POST" && p == script+"/assets-upload-session":
		var body struct {
			Manifest map[string]struct {
				Hash string `json:"hash"`
			} `json:"manifest"`
		}
		_ = json.Unmarshal(req.RawBody, &body)
		var hashes []string
		for _, e := range body.Manifest {
			hashes = append(hashes, e.Hash)
		}
		buckets := [][]string{}
		if f.needUpload {
			buckets = append(buckets, hashes)
		}
		jwt := wkJWT
		if !f.needUpload {
			jwt = wkDone
		}
		writeJSON(w, 200, ok(map[string]any{"jwt": jwt, "buckets": buckets}))
	case m == "POST" && p == script+"/tails":
		writeJSON(w, 200, ok(map[string]any{"id": "tail-1", "url": "ws://" + r.Host + "/tail-ws/tail-1", "expires_at": "2026-01-01T00:00:00Z"}))
	case m == "DELETE" && p == script+"/tails/tail-1":
		writeJSON(w, 200, ok(nil))
	case m == "GET" && p == acct+"/workers/subdomain":
		writeJSON(w, 200, ok(map[string]any{"subdomain": "team"}))
	case m == "PUT" && p == acct+"/workers/subdomain":
		writeJSON(w, 200, ok(req.Body))
	case m == "GET" && p == acct+"/workers/domains":
		writeJSON(w, 200, ok(f.domains))
	case m == "PUT" && p == acct+"/workers/domains":
		d := map[string]any{"id": "dom-1"}
		for k, v := range req.Body {
			d[k] = v
		}
		f.domains = append(f.domains, d)
		writeJSON(w, 200, ok(d))
	case m == "DELETE" && strings.HasPrefix(p, acct+"/workers/domains/"):
		writeJSON(w, 200, ok(nil))
	case strings.HasPrefix(p, acct+"/workers/dispatch/namespaces"):
		rest := strings.TrimPrefix(p, acct+"/workers/dispatch/namespaces")
		switch {
		case m == "GET" && rest == "":
			writeJSON(w, 200, okList([]any{map[string]any{"namespace_name": "customers", "namespace_id": "ns-1", "script_count": 3}}, 1))
		case m == "POST" && rest == "":
			writeJSON(w, 200, ok(map[string]any{"namespace_name": req.Body["name"], "namespace_id": "ns-2"}))
		case m == "GET" && rest == "/customers":
			writeJSON(w, 200, ok(map[string]any{"namespace_name": "customers", "namespace_id": "ns-1", "script_count": 3}))
		case m == "PATCH" && rest == "/customers":
			writeJSON(w, 200, ok(map[string]any{"namespace_name": req.Body["name"], "namespace_id": "ns-1"}))
		case m == "DELETE" && rest == "/customers":
			writeJSON(w, 200, ok(nil))
		case m == "GET" && rest == "/customers/scripts":
			writeJSON(w, 200, okList([]any{map[string]any{"script": map[string]any{"id": "tenant-a", "modified_on": "2026-01-01T00:00:00Z"}}}, 1))
		default:
			writeJSON(w, 404, fail(7003, "no route"))
		}
	case strings.HasPrefix(p, acct+"/workers/workers/my-worker/previews"):
		rest := strings.TrimPrefix(p, acct+"/workers/workers/my-worker/previews")
		switch {
		case m == "GET" && rest == "":
			var out []any
			for _, pv := range f.previews {
				out = append(out, pv)
			}
			writeJSON(w, 200, okList(out, len(out)))
		case m == "POST" && rest == "":
			pv := map[string]any{"id": "pv-1", "name": req.Body["name"], "slug": req.Body["name"], "urls": []any{"https://feature-x-my-worker.team.workers.dev"}}
			f.previews["feature-x"] = pv
			f.previews["pv-1"] = pv
			writeJSON(w, 200, ok(pv))
		case m == "GET" && strings.Count(rest, "/") == 1:
			pv, found := f.previews[strings.TrimPrefix(rest, "/")]
			if !found {
				writeJSON(w, 404, fail(10200, "preview not found"))
				return true
			}
			writeJSON(w, 200, ok(pv))
		case m == "DELETE" && strings.Count(rest, "/") == 1:
			writeJSON(w, 200, ok(nil))
		case m == "POST" && strings.HasSuffix(rest, "/deployments"):
			writeJSON(w, 200, ok(map[string]any{"id": "pdep-1", "urls": []any{"https://abc123-my-worker.team.workers.dev"}}))
		case m == "GET" && strings.HasSuffix(rest, "/deployments/latest"):
			writeJSON(w, 200, ok(map[string]any{"id": "pdep-1", "env": map[string]any{"PV_SECRET": map[string]any{"type": "secret_text"}, "V": map[string]any{"type": "plain_text", "text": "1"}}}))
		case m == "PATCH" && strings.HasSuffix(rest, "/deployments/latest"):
			writeJSON(w, 200, ok(map[string]any{"id": "pdep-2"}))
		case m == "GET" && strings.HasSuffix(rest, "/deployments"):
			writeJSON(w, 200, okList([]any{map[string]any{"id": "pdep-1", "created_on": "2026-01-01T00:00:00Z"}}, 1))
		default:
			writeJSON(w, 404, fail(7003, "no route"))
		}
	case m == "POST" && p == acct+"/workers/observability/telemetry/query":
		writeJSON(w, 200, ok(map[string]any{"events": map[string]any{"events": []any{
			map[string]any{"timestamp": 1767225600000.0, "$metadata": map[string]any{"level": "error", "message": "it broke"}},
		}}}))
	case strings.HasPrefix(p, "/zones/"+zoneID+"/workers/routes"):
		rest := strings.TrimPrefix(p, "/zones/"+zoneID+"/workers/routes")
		switch {
		case m == "GET" && rest == "":
			writeJSON(w, 200, ok(f.routes))
		case m == "POST" && rest == "":
			rt := map[string]any{"id": fmt.Sprintf("route-%d", len(f.routes)+1), "pattern": req.Body["pattern"], "script": req.Body["script"]}
			f.routes = append(f.routes, rt)
			writeJSON(w, 200, ok(rt))
		case m == "PUT":
			writeJSON(w, 200, ok(req.Body))
		case m == "DELETE":
			writeJSON(w, 200, ok(nil))
		}
	default:
		return false
	}
	return true
}

func (f *wkFake) serveAssetUpload(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	req := fakeRequest{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/client/v4"), Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), ContentType: r.Header.Get("Content-Type"), RawBody: b, Header: r.Header.Clone()}
	f.mu.Lock()
	f.assetUploads = append(f.assetUploads, req)
	f.mu.Unlock()
	if req.Auth != "Bearer "+wkJWT {
		writeJSON(w, 401, fail(10000, "bad upload jwt"))
		return
	}
	writeJSON(w, 201, ok(map[string]any{"jwt": wkDone}))
}

// serveTailWS is a tiny WebSocket server: handshake, read the client's
// first message, send wsEvents, then close.
func (f *wkFake) serveTailWS(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.wsHeader = r.Header.Clone()
	events := append([]string(nil), f.wsEvents...)
	f.mu.Unlock()
	key := r.Header.Get("Sec-WebSocket-Key")
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	hj, _ := w.(http.Hijacker)
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]), r.Header.Get("Sec-WebSocket-Protocol"))
	rw.Flush()
	// Read one masked client frame.
	if msg, err := readClientFrame(rw.Reader); err == nil {
		f.mu.Lock()
		f.wsFirstMsg = string(msg)
		f.mu.Unlock()
	}
	for _, e := range events {
		writeServerFrame(rw.Writer, 0x1, []byte(e))
	}
	writeServerFrame(rw.Writer, 0x8, []byte{0x03, 0xe8})
	rw.Flush()
	// Wait for the client's close reply (or EOF).
	_, _ = readClientFrame(rw.Reader)
}

func readClientFrame(br *bufio.Reader) ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return nil, err
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		io.ReadFull(br, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		io.ReadFull(br, b[:])
		n = int(binary.BigEndian.Uint64(b[:]))
	}
	var mask [4]byte
	if h[1]&0x80 != 0 {
		io.ReadFull(br, mask[:])
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(br, p); err != nil {
		return nil, err
	}
	for i := range p {
		p[i] ^= mask[i%4]
	}
	return p, nil
}

func writeServerFrame(w *bufio.Writer, op byte, p []byte) {
	w.WriteByte(0x80 | op)
	switch {
	case len(p) < 126:
		w.WriteByte(byte(len(p)))
	case len(p) < 1<<16:
		w.WriteByte(126)
		w.WriteByte(byte(len(p) >> 8))
		w.WriteByte(byte(len(p)))
	default:
		w.WriteByte(127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(len(p)))
		w.Write(b[:])
	}
	w.Write(p)
	w.Flush()
}

// multipartParts parses a multipart request body into name → (content type, data).
func wkParts(t *testing.T, r fakeRequest) map[string][2]string {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.ContentType)
	if err != nil {
		t.Fatalf("bad content type %q: %v", r.ContentType, err)
	}
	mr := multipart.NewReader(strings.NewReader(string(r.RawBody)), params["boundary"])
	out := map[string][2]string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("multipart: %v", err)
		}
		b, _ := io.ReadAll(part)
		out[part.FormName()] = [2]string{part.Header.Get("Content-Type"), string(b)}
	}
	return out
}
