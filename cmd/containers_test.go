package cmd

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const ctAppID = "0a1b2c3d-0000-4000-8000-000000000001"

func TestContainersCommands(t *testing.T) {
	p := platFake(t)
	ct := platA + "/containers"
	p.routes["GET "+ct+"/applications"] = []any{map[string]any{"id": ctAppID, "name": "web", "instances": 2, "configuration": map[string]any{"image": "registry.cloudflare.com/x/web:v1", "instance_type": "basic"}, "created_at": "2026-01-01T00:00:00Z"}}
	p.routes["GET "+ct+"/registries"] = []any{map[string]any{"domain": "123.dkr.ecr.us-east-1.amazonaws.com", "is_public": false}}
	p.routes["POST "+ct+"/registries/docker.io/credentials"] = map[string]any{"username": "v1", "password": "REGISTRY-SECRET-PW"}
	platCheck(t, p, []platCase{
		{args: []string{"containers", "list"}, method: "GET", path: ct + "/applications", want: "registry.cloudflare.com/x/web:v1"},
		{args: []string{"containers", "info", "web"}, method: "GET", path: ct + "/applications/" + ctAppID},
		{args: []string{"containers", "info", ctAppID}, method: "GET", path: ct + "/applications/" + ctAppID},
		{args: []string{"containers", "instances", "web"}, method: "GET", path: ct + "/applications/" + ctAppID + "/instances-v2"},
		{args: []string{"containers", "versions", "web"}, method: "GET", path: ct + "/applications/" + ctAppID + "/versions"},
		{args: []string{"containers", "delete", "web", "--yes"}, method: "DELETE", path: ct + "/applications/" + ctAppID},
		{args: []string{"containers", "registries", "list"}, method: "GET", path: ct + "/registries", want: "amazonaws.com"},
		{args: []string{"containers", "registries", "configure", "docker.io", "--public"}, method: "POST", path: ct + "/registries", body: map[string]any{"domain": "docker.io", "is_public": true}},
		{args: []string{"containers", "registries", "delete", "docker.io", "--yes"}, method: "DELETE", path: ct + "/registries/docker.io"},
		{args: []string{"containers", "registries", "credentials", "docker.io", "--permissions", "pull,push"}, method: "POST", path: ct + "/registries/docker.io/credentials",
			body: map[string]any{"permissions": []any{"pull", "push"}, "expiration_minutes": 15}, want: "redacted"},
	})
	if msg := platRunErr(t, "", "containers", "info", "nope"); !strings.Contains(msg, "no container application named") {
		t.Fatal(msg)
	}
	// Registry passwords are secrets.
	out := platRunOK(t, "", "containers", "registries", "credentials", "docker.io", "--json")
	if strings.Contains(out, "REGISTRY-SECRET-PW") {
		t.Fatal("password printed without --reveal")
	}
	if out := platRunOK(t, "", "containers", "registries", "credentials", "docker.io", "--json", "--reveal"); !strings.Contains(out, "REGISTRY-SECRET-PW") {
		t.Fatal("--reveal didn't reveal")
	}
	if msg := platRunErr(t, "", "containers", "ssh", "x"); !strings.Contains(msg, "not applicable") {
		t.Fatal(msg)
	}
}

// fakeRegistry is a Docker v2 registry speaking just enough for images list/delete.
func fakeRegistry(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var log []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		log = append(log, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("v1:REGPW")) {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/v2/_catalog" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/_catalog?tags=true&last=x>; rel="next"`)
			_, _ = w.Write([]byte(`{"repositories":{"` + acctID + `/web":["v1","v2"]}}`))
		case r.URL.Path == "/v2/_catalog":
			_, _ = w.Write([]byte(`{"repositories":{"` + acctID + `/api":["latest"]}}`))
		case r.Method == "HEAD" && strings.HasSuffix(r.URL.Path, "/manifests/v1"):
			w.Header().Set("Docker-Content-Digest", "sha256:abc")
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/manifests/sha256:abc"):
			w.WriteHeader(202)
		case r.Method == "PUT" && r.URL.Path == "/v2/gc/layers":
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &log
}

func TestContainersImagesAndDocker(t *testing.T) {
	p := platFake(t)
	reg, log := fakeRegistry(t)
	host := strings.TrimPrefix(reg.URL, "http://")
	t.Setenv("CLOUDFLARE_CONTAINER_REGISTRY", host)
	old := ctRegistryScheme
	ctRegistryScheme = "http"
	t.Cleanup(func() { ctRegistryScheme = old })
	p.routes["POST "+platA+"/containers/registries/"+host+"/credentials"] = map[string]any{"username": "v1", "password": "REGPW", "registry_host": host}

	out := platRunOK(t, "", "containers", "images", "list")
	if !strings.Contains(out, "web") || !strings.Contains(out, "v1, v2") || !strings.Contains(out, "api") || strings.Contains(out, "REGPW") {
		t.Fatalf("images list: %s", out)
	}
	if r := p.last(t, "POST", platA+"/containers/registries/"+host+"/credentials"); !strings.Contains(string(r.RawBody), `"pull"`) {
		t.Fatalf("creds body: %s", r.RawBody)
	}
	out = platRunOK(t, "", "containers", "images", "delete", "web:v1", "--yes")
	if !strings.Contains(out, "sha256:abc") {
		t.Fatal(out)
	}
	joined := strings.Join(*log, "\n")
	for _, want := range []string{"HEAD /v2/" + acctID + "/web/manifests/v1", "DELETE /v2/" + acctID + "/web/manifests/sha256:abc", "PUT /v2/gc/layers"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("registry log lacks %s:\n%s", want, joined)
		}
	}

	// docker build/push via a fake docker that records argv and stdin.
	dir := t.TempDir()
	rec := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "docker")
	_ = os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" >> "+rec+"\nif [ \"$1\" = login ]; then cat >> "+rec+".stdin; fi\n"), 0o755)
	t.Setenv("CFCTL_DOCKER", script)
	platRunOK(t, "", "containers", "build", ".", "-t", "web:v3", "--push")
	calls, _ := os.ReadFile(rec)
	stdin, _ := os.ReadFile(rec + ".stdin")
	cs := string(calls)
	for _, want := range []string{"build --platform linux/amd64 -t web:v3 .", "login --password-stdin --username v1 " + host, "tag web:v3 " + host + "/" + acctID + "/web:v3", "push " + host + "/" + acctID + "/web:v3"} {
		if !strings.Contains(cs, want) {
			t.Fatalf("docker calls lack %q:\n%s", want, cs)
		}
	}
	if strings.Contains(cs, "REGPW") || string(stdin) != "REGPW" {
		t.Fatalf("password must go via stdin only: argv=%q stdin=%q", cs, stdin)
	}
	t.Setenv("CFCTL_DOCKER", filepath.Join(dir, "missing-docker"))
	if msg := platRunErr(t, "", "containers", "push", "web:v3"); !strings.Contains(msg, "docker") {
		t.Fatal(msg)
	}
}

func TestBrowserCommands(t *testing.T) {
	p := platFake(t)
	br := platA + "/browser-rendering"
	p.routes["GET "+br+"/devtools/session"] = func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		writeJSON(w, 200, []any{map[string]any{"sessionId": "s1", "startTime": 1700000000000}})
	}
	targets := []any{
		map[string]any{"id": "t0", "type": "service_worker", "devtoolsFrontendUrl": "https://devtools/sw"},
		map[string]any{"id": "t1", "type": "page", "url": "https://example.com", "title": "Example", "devtoolsFrontendUrl": "https://devtools/page"},
	}
	p.routes["POST "+br+"/devtools/browser"] = func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		writeJSON(w, 200, map[string]any{"sessionId": "s2", "targets": targets})
	}
	p.routes["GET "+br+"/devtools/browser/s1/json"] = func(w http.ResponseWriter, r *http.Request, req fakeRequest) { writeJSON(w, 200, targets) }
	p.routes["POST "+br+"/screenshot"] = func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNG"))
	}
	p.routes["POST "+br+"/markdown"] = "# Example"
	p.routes["POST "+br+"/links"] = []any{"https://a", "https://b"}
	platCheck(t, p, []platCase{
		{args: []string{"browser", "list"}, method: "GET", path: br + "/devtools/session", want: "s1"},
		{args: []string{"browser", "get", "s1"}, method: "GET", path: br + "/devtools/session/s1"},
		{args: []string{"browser", "create", "--keep-alive", "60", "--lab"}, method: "POST", path: br + "/devtools/browser", query: "keep_alive=60000", want: "https://devtools/page"},
		{args: []string{"browser", "view", "s1"}, method: "GET", path: br + "/devtools/browser/s1/json", want: "https://devtools/page"},
		{args: []string{"browser", "view", "s1", "--target", "t0"}, method: "GET", path: br + "/devtools/browser/s1/json", want: "https://devtools/sw"},
		{args: []string{"browser", "close", "s1"}, method: "DELETE", path: br + "/devtools/browser/s1"},
		{args: []string{"browser", "markdown", "https://example.com"}, method: "POST", path: br + "/markdown", body: map[string]any{"url": "https://example.com"}, want: "# Example"},
		{args: []string{"browser", "links", "https://example.com"}, method: "POST", path: br + "/links", want: "https://b"},
		{args: []string{"browser", "content", "https://example.com", "--data", `{"gotoOptions":{"waitUntil":"networkidle0"}}`}, method: "POST", path: br + "/content", body: map[string]any{"gotoOptions": map[string]any{"waitUntil": "networkidle0"}}},
		{args: []string{"browser", "pdf", "https://example.com", "-o", filepath.Join(t.TempDir(), "x.pdf")}, method: "POST", path: br + "/pdf"},
	})
	dest := filepath.Join(t.TempDir(), "s.png")
	platRunOK(t, "", "browser", "screenshot", "https://example.com", "-o", dest)
	if b, _ := os.ReadFile(dest); string(b) != "PNG" {
		t.Fatalf("screenshot: %q", b)
	}
	if msg := platRunErr(t, "", "browser", "screenshot", "https://example.com"); !strings.Contains(msg, "--output") {
		t.Fatal(msg)
	}
}
