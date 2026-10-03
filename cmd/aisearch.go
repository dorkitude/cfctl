package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const (
	aisNS   = "/accounts/{account_id}/ai-search/namespaces"
	aisInst = aisNS + "/{namespace}/instances"
)

var aisNSFlag = map[string]string{"namespace": "namespace"}

func aisFlags(extra func(c *cobra.Command)) func(c *cobra.Command) {
	return func(c *cobra.Command) {
		c.Flags().StringP("namespace", "n", "default", "AI Search namespace")
		if extra != nil {
			extra(c)
		}
	}
}

func aisInstanceFlags(c *cobra.Command) {
	c.Flags().String("type", "", "Source type: r2, web-crawler, or builtin (items uploaded via the API; the default)")
	c.Flags().String("source", "", "R2 bucket name or website URL")
	c.Flags().String("embedding-model", "", "Embedding model")
	c.Flags().String("generation-model", "", "LLM used for chat completions")
	c.Flags().Int("chunk-size", 0, "Chunk size for document splitting (min 64)")
	c.Flags().Int("chunk-overlap", 0, "Overlap between chunks")
	c.Flags().Int("max-num-results", 0, "Maximum search results per query")
	c.Flags().Bool("reranking", false, "Enable reranking")
	c.Flags().String("reranking-model", "", "Reranking model")
	c.Flags().Bool("hybrid-search", false, "Enable hybrid (keyword + vector) search")
	c.Flags().Bool("cache", false, "Enable response caching")
	c.Flags().Float64("score-threshold", 0, "Minimum relevance score (0-1)")
}

func aisInstanceBody(c *cobra.Command, args []string) (any, error) {
	b := map[string]any{}
	if c.Name() == "create" {
		b["id"] = args[0]
	}
	// "builtin" (items uploaded via the API) is expressed by omitting type;
	// the API only accepts r2 and web-crawler.
	if t, _ := c.Flags().GetString("type"); c.Flags().Changed("type") && t != "builtin" {
		b["type"] = t
	}
	platSetStr(c, b, "source", "source")
	platSetStr(c, b, "embedding-model", "embedding_model")
	platSetStr(c, b, "generation-model", "ai_search_model")
	platSetInt(c, b, "chunk-size", "chunk_size")
	platSetInt(c, b, "chunk-overlap", "chunk_overlap")
	platSetInt(c, b, "max-num-results", "max_num_results")
	platSetBool(c, b, "reranking", "reranking")
	platSetStr(c, b, "reranking-model", "reranking_model")
	platSetBool(c, b, "hybrid-search", "hybrid_search_enabled")
	platSetBool(c, b, "cache", "cache")
	if c.Flags().Changed("score-threshold") {
		v, _ := c.Flags().GetFloat64("score-threshold")
		b["score_threshold"] = v
	}
	return b, nil
}

var aisInstanceCols = []platCol{
	{H: "name", Path: "id"}, {H: "type", Path: "type"}, {H: "source", Path: "source"},
	{H: "status", Path: "status"}, {H: "embedding", Path: "embedding_model", W: 40}, {H: "created", Path: "created_at"},
}

var aisJobCols = []platCol{
	{H: "id", Path: "id"}, {H: "source", Path: "source"}, {H: "started", Path: "started_at"},
	{H: "ended", Path: "ended_at"}, {H: "end reason", Path: "end_reason", W: 40},
}

func init() {
	namespace := platGroup("namespace", "Manage AI Search namespaces", "", []string{"namespaces", "ns"},
		platSpecs(
			platSpec{Use: "list", Short: "List namespaces", Aliases: []string{"ls"}, Path: aisNS, List: true,
				Cols: []platCol{{H: "name", Path: "name"}, {H: "description", Path: "description", W: 50}, {H: "created", Path: "created_at"}}, Title: "🔍 %d namespaces", Product: "AI Search"},
			platSpec{Use: "get <name>", Short: "Show a namespace", Path: aisNS + "/{name}", Title: "🔍 Namespace %s", Product: "AI Search"},
			platSpec{Use: "create <name>", Short: "Create a namespace", Method: "POST", Path: aisNS, ExtraArgs: 1,
				Flags: func(c *cobra.Command) { c.Flags().String("description", "", "Description") },
				Body: func(c *cobra.Command, args []string) (any, error) {
					b := map[string]any{"name": args[0]}
					platSetStr(c, b, "description", "description")
					return b, nil
				}, Data: true, Done: "Created namespace %s", Product: "AI Search"},
			platSpec{Use: "update <name>", Short: "Update a namespace", Method: "PUT", Path: aisNS + "/{name}",
				Flags: func(c *cobra.Command) { c.Flags().String("description", "", "Description") },
				Body: func(c *cobra.Command, args []string) (any, error) {
					b := map[string]any{}
					platSetStr(c, b, "description", "description")
					return b, nil
				}, Data: true, Done: "Updated namespace %s", Product: "AI Search"},
			platSpec{Use: "delete <name>", Short: "Delete a namespace", Aliases: []string{"rm"}, Method: "DELETE", Path: aisNS + "/{name}",
				Confirm: "delete AI Search namespace %s", Done: "Deleted namespace %s", Product: "AI Search"},
		)...)

	jobs := platGroup("jobs", "AI Search indexing jobs", "", []string{"job"},
		platSpecs(
			platSpec{Use: "list <instance>", Short: "List indexing jobs", Aliases: []string{"ls"}, Path: aisInst + "/{id}/jobs", PathFlags: aisNSFlag, Flags: aisFlags(nil), List: true,
				Cols: aisJobCols, Title: "🔍 %d jobs", Product: "AI Search"},
			platSpec{Use: "get <instance> <job-id>", Short: "Show an indexing job", Path: aisInst + "/{id}/jobs/{job_id}", PathFlags: aisNSFlag, Flags: aisFlags(nil), Title: "🔍 Job", Product: "AI Search"},
			platSpec{Use: "create <instance>", Short: "Trigger a new indexing job (sync)", Aliases: []string{"sync"}, Method: "POST", Path: aisInst + "/{id}/jobs", PathFlags: aisNSFlag,
				Flags: aisFlags(func(c *cobra.Command) { c.Flags().String("description", "", "Description for the job") }),
				// Always a JSON object body (like wrangler), even when empty.
				Body: func(c *cobra.Command, _ []string) (any, error) {
					b := map[string]any{}
					platSetStr(c, b, "description", "description")
					return b, nil
				},
				Title: "🔍 Indexing job started", Product: "AI Search"},
			platSpec{Use: "cancel <instance> <job-id>", Short: "Cancel an in-progress job", Method: "PATCH", Path: aisInst + "/{id}/jobs/{job_id}", PathFlags: aisNSFlag, Flags: aisFlags(nil),
				Body:    func(*cobra.Command, []string) (any, error) { return map[string]any{"action": "cancel"}, nil },
				Confirm: "cancel indexing job %s", Done: "Canceled job", Product: "AI Search"},
			platSpec{Use: "logs <instance> <job-id>", Short: "Show a job's log entries", Path: aisInst + "/{id}/jobs/{job_id}/logs", PathFlags: aisNSFlag, Flags: aisFlags(nil), List: true,
				Cols: []platCol{{H: "time", Path: "created_at"}, {H: "type", Path: "message_type"}, {H: "message", Path: "message", W: 100}}, Product: "AI Search"},
		)...)

	items := platGroup("items", "Items (documents) indexed by an instance", "", []string{"item"},
		platSpecs(
			platSpec{Use: "list <instance>", Short: "List items", Aliases: []string{"ls"}, Path: aisInst + "/{id}/items", PathFlags: aisNSFlag, Flags: aisFlags(nil), List: true,
				Cols: []platCol{{H: "id", Path: "id"}, {H: "key", Path: "key|name", W: 60}, {H: "status", Path: "status"}, {H: "updated", Path: "updated_at|last_seen_at"}}, Title: "🔍 %d items", Product: "AI Search"},
			platSpec{Use: "get <instance> <item-id>", Short: "Show an item", Path: aisInst + "/{id}/items/{item_id}", PathFlags: aisNSFlag, Flags: aisFlags(nil), Product: "AI Search"},
			platSpec{Use: "chunks <instance> <item-id>", Short: "List an item's chunks", Path: aisInst + "/{id}/items/{item_id}/chunks", PathFlags: aisNSFlag, Flags: aisFlags(nil), List: true, Product: "AI Search"},
			platSpec{Use: "logs <instance> <item-id>", Short: "Show an item's processing logs", Path: aisInst + "/{id}/items/{item_id}/logs", PathFlags: aisNSFlag, Flags: aisFlags(nil), List: true, Product: "AI Search"},
			platSpec{Use: "delete <instance> <item-id>", Short: "Delete an item", Aliases: []string{"rm"}, Method: "DELETE", Path: aisInst + "/{id}/items/{item_id}", PathFlags: aisNSFlag, Flags: aisFlags(nil),
				Confirm: "delete item %s", Done: "Deleted item", Product: "AI Search"},
		)...)

	tokens := platGroup("tokens", "AI Search service tokens", "", []string{"token"},
		platSpecs(
			platSpec{Use: "list", Short: "List AI Search tokens", Aliases: []string{"ls"}, Path: "/accounts/{account_id}/ai-search/tokens", List: true,
				Cols: []platCol{{H: "id", Path: "id"}, {H: "name", Path: "name"}, {H: "created", Path: "created_at"}}, Product: "AI Search"},
		)...)

	aisCmd := platGroup("ai-search", "AI Search (AutoRAG) instances, search, jobs, items", `Manage AI Search instances (formerly AutoRAG).

Instances live in a namespace ("default" unless --namespace is given).

  cfctl ai-search list|get|create|update|delete|stats
  cfctl ai-search search <instance> "query"
  cfctl ai-search jobs list|get|create|cancel|logs <instance>
  cfctl ai-search items list|get|chunks|logs|delete <instance>
  cfctl ai-search namespace list|get|create|update|delete

Generated equivalents: 'cfctl api ai-search-instances ...' and friends.`, []string{"aisearch", "autorag"},
		append(platSpecs(
			platSpec{Use: "list", Short: "List AI Search instances", Aliases: []string{"ls"}, Path: aisInst, PathFlags: aisNSFlag, Flags: aisFlags(nil), List: true,
				Cols: aisInstanceCols, Title: "🔍 %d AI Search instances", Product: "AI Search"},
			platSpec{Use: "get <instance>", Short: "Show an instance", Aliases: []string{"info"}, Path: aisInst + "/{id}", PathFlags: aisNSFlag, Flags: aisFlags(nil), Title: "🔍 %s", Product: "AI Search"},
			platSpec{Use: "create <instance>", Short: "Create an instance", Method: "POST", Path: aisInst, PathFlags: aisNSFlag, ExtraArgs: 1,
				Flags: aisFlags(aisInstanceFlags), Body: aisInstanceBody, Data: true, Done: "Created AI Search instance %s", Product: "AI Search"},
			platSpec{Use: "update <instance>", Short: "Update an instance's configuration", Method: "PUT", Path: aisInst + "/{id}", PathFlags: aisNSFlag,
				Flags: aisFlags(aisInstanceFlags), Body: aisInstanceBody, Data: true, Done: "Updated AI Search instance %s", Product: "AI Search"},
			platSpec{Use: "delete <instance>", Short: "Delete an instance", Aliases: []string{"rm"}, Method: "DELETE", Path: aisInst + "/{id}", PathFlags: aisNSFlag, Flags: aisFlags(nil),
				Confirm: "delete AI Search instance %s", Done: "Deleted AI Search instance %s", Product: "AI Search"},
			platSpec{Use: "stats <instance>", Short: "Show usage statistics", Path: aisInst + "/{id}/stats", PathFlags: aisNSFlag, Flags: aisFlags(nil), Title: "🔍 Stats for %s", Product: "AI Search"},
			platSpec{Use: "search <instance> <query>", Short: "Run a semantic search (retrieval only, no generation)", Method: "POST", Path: aisInst + "/{id}/search", PathFlags: aisNSFlag, ExtraArgs: 1,
				Flags: aisFlags(func(c *cobra.Command) {
					c.Flags().Int("max-num-results", 0, "Override the maximum number of results")
					c.Flags().Float64("score-threshold", 0, "Override the minimum relevance score (0-1)")
					c.Flags().Bool("reranking", false, "Override reranking")
					c.Flags().StringArray("filter", nil, "Metadata filter key=value (repeatable)")
				}),
				Body: aisSearchBody, Print: aisPrintSearch, Product: "AI Search"},
		), namespace, jobs, items, tokens)...)
	rootCmd.AddCommand(aisCmd)
}

func aisSearchBody(c *cobra.Command, args []string) (any, error) {
	body := map[string]any{"messages": []map[string]string{{"role": "user", "content": args[1]}}}
	retrieval := map[string]any{}
	platSetInt(c, retrieval, "max-num-results", "max_num_results")
	if c.Flags().Changed("score-threshold") {
		v, _ := c.Flags().GetFloat64("score-threshold")
		retrieval["match_threshold"] = v
	}
	if fs, _ := c.Flags().GetStringArray("filter"); len(fs) > 0 {
		filters := map[string]any{}
		for _, f := range fs {
			k, v, ok := strings.Cut(f, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("invalid --filter %q: want key=value", f)
			}
			filters[k] = v
		}
		retrieval["filters"] = filters
	}
	opts := map[string]any{}
	if len(retrieval) > 0 {
		opts["retrieval"] = retrieval
	}
	if c.Flags().Changed("reranking") {
		v, _ := c.Flags().GetBool("reranking")
		opts["reranking"] = map[string]any{"enabled": v}
	}
	if len(opts) > 0 {
		body["ai_search_options"] = opts
	}
	return body, nil
}

func aisPrintSearch(_ *cobra.Command, _ []string, raw json.RawMessage) error {
	var r struct {
		SearchQuery string           `json:"search_query"`
		Chunks      []map[string]any `json:"chunks"`
		Data        []map[string]any `json:"data"`
	}
	if err := platDecode(raw, &r); err != nil {
		return err
	}
	chunks := r.Chunks
	if chunks == nil {
		chunks = r.Data
	}
	fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🔍 %q (%d results)", r.SearchQuery, len(chunks))))
	for i, ch := range chunks {
		src := platStr(platGet(ch, "item.key|filename|item.name"))
		fmt.Printf("%s %s %s\n", ui.AccentStyle.Render(fmt.Sprintf("%d.", i+1)), src, ui.SubtleStyle.Render("score "+platStr(platGet(ch, "score"))))
		text := platStr(platGet(ch, "text|content"))
		if text == "" {
			if cs, ok := ch["content"].([]any); ok && len(cs) > 0 {
				text = platStr(platGet(cs[0], "text"))
			}
		}
		fmt.Println("   " + platTrunc(text, 300))
	}
	return nil
}
