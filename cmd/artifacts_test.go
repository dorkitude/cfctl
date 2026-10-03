package cmd

import (
	"net/http"
	"testing"
)

func artifactsFake(t *testing.T) *fakeCF {
	r := "/artifacts/namespaces/ns/repos/site"
	return stFake(t, map[string]stHandler{
		"GET /artifacts/namespaces":           stListH([]map[string]any{{"namespace": "ns", "jurisdiction": "eu"}}),
		"POST /artifacts/namespaces":          stJSON(map[string]any{"namespace": "ns"}),
		"GET /artifacts/namespaces/ns":        stJSON(map[string]any{"namespace": "ns"}),
		"DELETE /artifacts/namespaces/ns":     stJSON(nil),
		"GET /artifacts/namespaces/ns/repos":  stListH([]map[string]any{{"name": "site", "default_branch": "main"}}),
		"POST /artifacts/namespaces/ns/repos": stJSON(map[string]any{"name": "site"}),
		"GET " + r:                            stJSON(map[string]any{"name": "site"}),
		"DELETE " + r:                         stJSON(nil),
		"POST " + r + "/fork":                 stJSON(map[string]any{"name": "site2"}),
		"POST " + r + "/import":               stJSON(map[string]any{"name": "site"}),
		"GET " + r + "/log":                   stJSON(map[string]any{"commits": []any{map[string]any{"hash": "0123456789abcdef0123", "message": "init", "author": map[string]any{"name": "Kyle"}}}}),
		"GET " + r + "/raw/main/docs/README.md": func(w http.ResponseWriter, req *http.Request, fr fakeRequest) {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("# hello\n"))
		},
		"GET " + r + "/commit/abc":                  stJSON(map[string]any{"hash": "abc"}),
		"GET " + r + "/tokens":                      stListH([]map[string]any{{"id": "t1", "scope": "read", "state": "active"}}),
		"POST /artifacts/namespaces/ns/tokens":      stJSON(map[string]any{"id": "t2", "token": "repo-tok"}),
		"DELETE /artifacts/namespaces/ns/tokens/t1": stJSON(nil),
	})
}

func TestArtifacts(t *testing.T) {
	f := artifactsFake(t)
	out, _ := stRun(t, "", "artifacts", "namespaces", "list")
	stMust(t, out, "ns", "eu")
	stRun(t, "", "artifacts", "namespaces", "create", "ns", "--jurisdiction", "eu")
	if r := stFind(t, f, "POST", "/artifacts/namespaces"); r.Body["namespace"] != "ns" || r.Body["jurisdiction"] != "eu" {
		t.Fatalf("body %v", r.Body)
	}
	stRun(t, "", "artifacts", "namespaces", "get", "ns")
	stRunErr(t, "", "artifacts", "namespaces", "delete", "ns")
	stRun(t, "", "artifacts", "namespaces", "delete", "ns", "-y")

	out, _ = stRun(t, "", "artifacts", "repos", "list", "ns")
	stMust(t, out, "site", "main")
	stRun(t, "", "artifacts", "repos", "create", "ns", "site", "--description", "d", "--read-only-repo")
	if r := stFind(t, f, "POST", "/artifacts/namespaces/ns/repos"); r.Body["name"] != "site" || r.Body["read_only"] != true {
		t.Fatalf("body %v", r.Body)
	}
	stRun(t, "", "artifacts", "repos", "get", "ns", "site")
	stRun(t, "", "artifacts", "repos", "fork", "ns", "site", "site2")
	if r := stFind(t, f, "POST", "/artifacts/namespaces/ns/repos/site/fork"); r.Body["name"] != "site2" {
		t.Fatalf("fork body %v", r.Body)
	}
	stRunErr(t, "", "artifacts", "repos", "import", "ns", "site")
	stRun(t, "", "artifacts", "repos", "import", "ns", "site", "--url", "https://github.com/o/r.git", "--depth", "1")
	out, _ = stRun(t, "", "artifacts", "repos", "log", "ns", "site")
	stMust(t, out, "0123456789ab", "Kyle", "init")
	out, _ = stRun(t, "", "artifacts", "repos", "file", "ns", "site", "main", "docs/README.md")
	stMust(t, out, "# hello")
	stRun(t, "", "artifacts", "repos", "commit", "ns", "site", "abc")
	out, _ = stRun(t, "", "artifacts", "repos", "tokens", "ns", "site")
	stMust(t, out, "t1", "read")
	out, _ = stRun(t, "", "artifacts", "repos", "issue-token", "ns", "site", "--scope", "write", "--ttl", "60")
	stMust(t, out, "repo-tok")
	if r := stFind(t, f, "POST", "/artifacts/namespaces/ns/tokens"); r.Body["repo"] != "site" || r.Body["scope"] != "write" || r.Body["ttl"] != float64(60) {
		t.Fatalf("token body %v", r.Body)
	}
	stRun(t, "", "artifacts", "repos", "revoke-token", "ns", "t1", "-y")
	stRun(t, "", "artifacts", "repos", "delete", "ns", "site", "-y")
	stFind(t, f, "DELETE", "/artifacts/namespaces/ns/repos/site")
}

func TestArtifactsReadOnly(t *testing.T) {
	f := artifactsFake(t)
	stRunErr(t, "", "--read-only", "artifacts", "repos", "create", "ns", "x")
	stNoMutation(t, f)
}
