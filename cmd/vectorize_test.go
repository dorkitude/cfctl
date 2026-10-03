package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vectorizeFake(t *testing.T) *fakeCF {
	idx := map[string]any{"name": "docs", "config": map[string]any{"dimensions": 3, "metric": "cosine"}, "created_on": "2026-10-01T00:00:00Z"}
	p := "/vectorize/v2/indexes/docs"
	return stFake(t, map[string]stHandler{
		"GET /vectorize/v2/indexes":    stJSON([]any{idx}),
		"POST /vectorize/v2/indexes":   stJSON(idx),
		"GET " + p:                     stJSON(idx),
		"DELETE " + p:                  stJSON(nil),
		"GET " + p + "/info":           stJSON(map[string]any{"dimensions": 3, "vectorCount": 42}),
		"POST " + p + "/insert":        stJSON(map[string]any{"mutationId": "mut-1"}),
		"POST " + p + "/upsert":        stJSON(map[string]any{"mutationId": "mut-2"}),
		"POST " + p + "/query":         stJSON(map[string]any{"count": 1, "matches": []any{map[string]any{"id": "v1", "score": 0.98, "metadata": map[string]any{"g": "jazz"}}}}),
		"POST " + p + "/get_by_ids":    stJSON([]any{map[string]any{"id": "v1", "values": []any{0.1, 0.2, 0.3}}}),
		"POST " + p + "/delete_by_ids": stJSON(map[string]any{"mutationId": "mut-3"}),
		"GET " + p + "/list": func(w http.ResponseWriter, r *http.Request, req fakeRequest) {
			if r.URL.Query().Get("cursor") == "" {
				writeJSON(w, 200, ok(map[string]any{"vectors": []any{map[string]any{"id": "v1"}}, "isTruncated": true, "nextCursor": "c2", "totalCount": 2}))
				return
			}
			writeJSON(w, 200, ok(map[string]any{"vectors": []any{map[string]any{"id": "v2"}}, "isTruncated": false, "totalCount": 2}))
		},
		"POST " + p + "/metadata_index/create": stJSON(map[string]any{"mutationId": "m"}),
		"POST " + p + "/metadata_index/delete": stJSON(map[string]any{"mutationId": "m"}),
		"GET " + p + "/metadata_index/list":    stJSON(map[string]any{"metadataIndexes": []any{map[string]any{"propertyName": "g", "indexType": "string"}}}),
	})
}

func TestVectorizeIndexes(t *testing.T) {
	f := vectorizeFake(t)
	out, _ := stRun(t, "", "vectorize", "list")
	stMust(t, out, "docs", "cosine", "3")
	out, _ = stRun(t, "", "vectorize", "info", "docs")
	stMust(t, out, "vectorCount:", "42")
	stRun(t, "", "vectorize", "get", "docs", "--json")
	stRun(t, "", "vectorize", "create", "docs", "--dimensions", "3", "--metric", "cosine", "--description", "d")
	r := stFind(t, f, "POST", "/vectorize/v2/indexes")
	if stGet(r.Body, "config.dimensions") != float64(3) || stGet(r.Body, "config.metric") != "cosine" || r.Body["description"] != "d" {
		t.Fatalf("create body: %v", r.Body)
	}
	stRunErr(t, "", "vectorize", "create", "x", "--dimensions", "3")
	stRunErr(t, "", "vectorize", "create", "x", "--preset", "p", "--metric", "cosine")
	stRunErr(t, "", "vectorize", "delete", "docs")
	stRun(t, "", "vectorize", "delete", "docs", "-y")
	stFind(t, f, "DELETE", "/vectorize/v2/indexes/docs")
}

func TestVectorizeVectors(t *testing.T) {
	f := vectorizeFake(t)
	old := vecBatchLines
	vecBatchLines = 2
	t.Cleanup(func() { vecBatchLines = old })
	file := filepath.Join(t.TempDir(), "v.ndjson")
	_ = os.WriteFile(file, []byte(`{"id":"1","values":[1,2,3]}
{"id":"2","values":[1,2,3]}

{"id":"3","values":[1,2,3]}
`), 0o600)
	out, _ := stRun(t, "", "vectorize", "insert", "docs", file, "--unparsable-behavior", "discard")
	stMust(t, out, "Inserted 3 vectors", "2 batches")
	n := 0
	for _, r := range f.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/insert") {
			n++
			if r.ContentType != "application/x-ndjson" || !strings.Contains(r.Query, "unparsable-behavior=discard") {
				t.Fatalf("bad insert request: %+v", r)
			}
		}
	}
	if n != 2 {
		t.Fatalf("want 2 insert batches, got %d", n)
	}
	stRun(t, `{"id":"9","values":[1,2,3]}`, "vectorize", "upsert", "docs", "-")

	out, _ = stRun(t, "", "vectorize", "query", "docs", "--vector", "[0.1,0.2,0.3]", "--top-k", "2", "--return-metadata", "all", "--filter", `{"g":"jazz"}`)
	stMust(t, out, "v1", "0.98")
	r := stFind(t, f, "POST", "/vectorize/v2/indexes/docs/query")
	if r.Body["topK"] != float64(2) || r.Body["returnMetadata"] != "all" || stGet(r.Body, "filter.g") != "jazz" || len(r.Body["vector"].([]any)) != 3 {
		t.Fatalf("query body: %v", r.Body)
	}
	stRunErr(t, "", "vectorize", "query", "docs")

	out, _ = stRun(t, "", "vectorize", "get-vectors", "docs", "v1")
	stMust(t, out, `"v1"`)
	stRun(t, "", "vectorize", "delete-vectors", "docs", "v1", "v2", "--yes")
	r = stFind(t, f, "POST", "/vectorize/v2/indexes/docs/delete_by_ids")
	if len(r.Body["ids"].([]any)) != 2 {
		t.Fatalf("delete body: %v", r.Body)
	}
	out, _ = stRun(t, "", "vectorize", "list-vectors", "docs")
	stMust(t, out, "v1", "--cursor c2")
	out, _ = stRun(t, "", "vectorize", "list-vectors", "docs", "--all")
	stMust(t, out, "2 vectors", "v2")
}

func TestVectorizeMetadataIndexes(t *testing.T) {
	f := vectorizeFake(t)
	stRun(t, "", "vectorize", "create-metadata-index", "docs", "--property-name", "g", "--type", "string")
	r := stFind(t, f, "POST", "/vectorize/v2/indexes/docs/metadata_index/create")
	if r.Body["propertyName"] != "g" || r.Body["indexType"] != "string" {
		t.Fatalf("body: %v", r.Body)
	}
	out, _ := stRun(t, "", "vectorize", "metadata-index", "list", "docs")
	stMust(t, out, "g", "string")
	stRun(t, "", "vectorize", "delete-metadata-index", "docs", "--property-name", "g", "-y")
	stFind(t, f, "POST", "/vectorize/v2/indexes/docs/metadata_index/delete")
}

func TestVectorizeReadOnly(t *testing.T) {
	f := vectorizeFake(t)
	stRunErr(t, "", "--read-only", "vectorize", "create", "x", "--preset", "p")
	stRunErr(t, "", "--read-only", "vectorize", "query", "docs", "--vector", "[1]")
	stNoMutation(t, f)
}
