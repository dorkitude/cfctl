package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var vectorizeCmd = &cobra.Command{
	Use:   "vectorize",
	Short: "Manage Vectorize indexes, vectors, and metadata indexes",
	Long: `Manage Vectorize (v2): indexes, inserting/upserting/querying vectors, and
metadata indexes for filtering. Indexes are addressed by name.

Examples:
  cfctl vectorize list
  cfctl vectorize create docs --dimensions 768 --metric cosine
  cfctl vectorize create docs --preset @cf/baai/bge-base-en-v1.5
  cfctl vectorize insert docs vectors.ndjson
  cfctl vectorize query docs --vector '[0.1, 0.2, ...]' --top-k 5 --return-metadata all
  cfctl vectorize create-metadata-index docs --property-name genre --type string

Generated equivalents: cfctl api vectorize <op>.`,
}

func vecPath(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("vectorize/v2/indexes", append([]string{args[0]}, segs...)...), nil, nil
	}
}

var vectorizeListCmd = sbListCmd("list", "List Vectorize indexes", "", cobra.NoArgs, sbFixed("vectorize/v2/indexes"),
	"🧮 %d Vectorize indexes", "No Vectorize indexes found", []stCol{
		{Header: "NAME", Path: "name"},
		{Header: "DIMENSIONS", Path: "config.dimensions"},
		{Header: "METRIC", Path: "config.metric"},
		{Header: "DESCRIPTION", Path: "description", Fmt: sbTrunc(40)},
		{Header: "CREATED", Path: "created_on"},
	})

var vectorizeGetCmd = sbGetCmd("get <index>", "Show a Vectorize index", "", "🧮 Vectorize index", cobra.ExactArgs(1), vecPath())

var vectorizeInfoCmd = sbGetCmd("info <index>", "Show an index's vector count and processing state", "", "🧮 Index info", cobra.ExactArgs(1), vecPath("info"))

var vectorizeDeleteCmd = sbDeleteCmd("delete <index>", "Delete a Vectorize index and all its vectors", "", cobra.ExactArgs(1),
	func(a []string) string { return "Vectorize index " + a[0] }, vecPath())

var vectorizeCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a Vectorize index",
	Long: `Create an index with --dimensions and --metric, or from an embedding model
--preset.

Examples:
  cfctl vectorize create docs --dimensions 768 --metric cosine --description "doc chunks"
  cfctl vectorize create docs --preset @cf/baai/bge-small-en-v1.5`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		body["name"] = args[0]
		stFlagStr(cmd, body, "description", "description")
		stFlagInt(cmd, body, "dimensions", "config.dimensions")
		stFlagStr(cmd, body, "metric", "config.metric")
		stFlagStr(cmd, body, "preset", "config.preset")
		cfg, _ := body["config"].(map[string]any)
		switch {
		case cfg == nil:
			return fmt.Errorf("pass --dimensions and --metric, or --preset")
		case cfg["preset"] != nil && (cfg["dimensions"] != nil || cfg["metric"] != nil):
			return fmt.Errorf("use --preset or --dimensions/--metric, not both")
		case cfg["preset"] == nil && (cfg["dimensions"] == nil || cfg["metric"] == nil):
			return fmt.Errorf("--dimensions and --metric are both required (or use --preset)")
		}
		return sbSendShow(c, "POST", c.p("vectorize/v2/indexes"), nil, body, "Created Vectorize index "+args[0])
	}),
}

// vecBatchLines is how many NDJSON lines go in one insert/upsert request.
var vecBatchLines = 5000

func newVecWriteCmd(op string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   op + " <index> <file.ndjson>",
		Short: strings.ToUpper(op[:1]) + op[1:] + " vectors from an NDJSON file",
		Long: fmt.Sprintf(`%s vectors from an NDJSON file (or - for stdin), one vector per line:

  {"id": "1", "values": [0.1, 0.2, 0.3], "metadata": {"genre": "jazz"}, "namespace": "a"}

Large files are sent in batches of %d lines. Writes are asynchronous; each
batch returns a mutation ID, and 'cfctl vectorize info <index>' shows when they
are processed.

Examples:
  cfctl vectorize %s docs vectors.ndjson
  cfctl vectorize %s docs vectors.ndjson --unparsable-behavior discard`, strings.ToUpper(op[:1])+op[1:], vecBatchLines, op, op),
		Args: cobra.ExactArgs(2),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			spec := args[1]
			if spec != "-" {
				spec = "@" + strings.TrimPrefix(spec, "@")
			}
			data, err := stReadInput(spec)
			if err != nil {
				return err
			}
			var lines [][]byte
			sc := bufio.NewScanner(bytes.NewReader(data))
			sc.Buffer(make([]byte, 1<<20), 64<<20)
			for sc.Scan() {
				if l := bytes.TrimSpace(sc.Bytes()); len(l) > 0 {
					lines = append(lines, append([]byte(nil), l...))
				}
			}
			if err := sc.Err(); err != nil {
				return err
			}
			if len(lines) == 0 {
				return fmt.Errorf("%s has no vectors", args[1])
			}
			q := url.Values{}
			if ub, _ := cmd.Flags().GetString("unparsable-behavior"); ub != "" {
				q.Set("unparsable-behavior", ub)
			}
			var mutations []string
			for start := 0; start < len(lines); start += vecBatchLines {
				end := min(start+vecBatchLines, len(lines))
				body := append(bytes.Join(lines[start:end], []byte("\n")), '\n')
				raw, err := c.result(stReq{Method: "POST", Path: c.p("vectorize/v2/indexes", args[0], op), Query: q, Body: body, ContentType: "application/x-ndjson"})
				if err != nil {
					return fmt.Errorf("batch of lines %d-%d: %w", start+1, end, err)
				}
				var r struct {
					MutationID string `json:"mutationId"`
				}
				_ = json.Unmarshal(raw, &r)
				mutations = append(mutations, r.MutationID)
			}
			verb := "Inserted"
			if op == "upsert" {
				verb = "Upserted"
			}
			return stEmitValue(map[string]any{"count": len(lines), "mutationIds": mutations}, func() error {
				fmt.Println(ui.Success(fmt.Sprintf("%s %d vectors into %s (%d batches; mutation %s)", verb, len(lines), args[0], len(mutations), mutations[len(mutations)-1])))
				return nil
			})
		}),
	}
	cmd.Flags().String("unparsable-behavior", "", "What to do with lines that don't parse: error or discard")
	return cmd
}

var vectorizeQueryCmd = &cobra.Command{
	Use:   "query <index>",
	Short: "Find the nearest vectors to a query vector",
	Long: `Query an index with a vector given as a JSON array (--vector) or a file
containing one (--vector-file).

Examples:
  cfctl vectorize query docs --vector '[0.12, 0.45, 0.67]' --top-k 3
  cfctl vectorize query docs --vector-file q.json --return-metadata all --filter '{"genre":"jazz"}'`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		vec, _ := cmd.Flags().GetString("vector")
		vf, _ := cmd.Flags().GetString("vector-file")
		switch {
		case vec != "" && vf != "":
			return fmt.Errorf("use --vector or --vector-file, not both")
		case vf != "":
			vec = "@" + vf
		}
		if vec != "" {
			data, err := stReadInput(vec)
			if err != nil {
				return err
			}
			var v []float64
			if err := json.Unmarshal(data, &v); err != nil {
				return fmt.Errorf("the vector must be a JSON array of numbers: %w", err)
			}
			body["vector"] = v
		}
		if body["vector"] == nil {
			return fmt.Errorf("pass --vector or --vector-file")
		}
		stFlagInt(cmd, body, "top-k", "topK")
		stFlagBool(cmd, body, "return-values", "returnValues")
		stFlagStr(cmd, body, "return-metadata", "returnMetadata")
		if f, ok, err := sbJSONFlag(cmd, "filter"); err != nil {
			return err
		} else if ok {
			body["filter"] = f
		}
		raw, err := c.result(stReq{Method: "POST", Path: c.p("vectorize/v2/indexes", args[0], "query"), Body: body})
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("🎯 %d matches", "No matches", stItems(raw, "matches"), []stCol{
				{Header: "ID", Path: "id"},
				{Header: "SCORE", Path: "score"},
				{Header: "NAMESPACE", Path: "namespace"},
				{Header: "METADATA", Path: "metadata", Fmt: sbTrunc(60)},
			})
			return nil
		})
	}),
}

var vectorizeGetVectorsCmd = &cobra.Command{
	Use:   "get-vectors <index> <id>...",
	Short: "Get vectors by ID (prints JSON)",
	Args:  cobra.MinimumNArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		raw, err := c.result(stReq{Method: "POST", Path: c.p("vectorize/v2/indexes", args[0], "get_by_ids"), Body: map[string]any{"ids": args[1:]}})
		if err != nil {
			return err
		}
		return printBody(stNonNull(raw), nil)
	}),
}

var vectorizeDeleteVectorsCmd = &cobra.Command{
	Use:   "delete-vectors <index> <id>...",
	Short: "Delete vectors by ID",
	Args:  cobra.MinimumNArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		if err := confirm(cmd, fmt.Sprintf("delete %d vectors from %s", len(args)-1, args[0])); err != nil {
			return err
		}
		return sbSend(c, "POST", c.p("vectorize/v2/indexes", args[0], "delete_by_ids"), nil, map[string]any{"ids": args[1:]},
			fmt.Sprintf("Deleting %d vectors from %s (asynchronous)", len(args)-1, args[0]))
	}),
}

var vectorizeListVectorsCmd = &cobra.Command{
	Use:   "list-vectors <index>",
	Short: "List vector IDs in an index",
	Long: `List vector IDs, one page at a time (--count, --cursor), or all with --all.

Examples:
  cfctl vectorize list-vectors docs
  cfctl vectorize list-vectors docs --count 1000 --cursor abc
  cfctl vectorize list-vectors docs --all --json`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		count, _ := cmd.Flags().GetInt("count")
		cursor, _ := cmd.Flags().GetString("cursor")
		all, _ := cmd.Flags().GetBool("all")
		var ids []map[string]any
		var next string
		var total any
		seen := map[string]bool{}
		for page := 0; page < 10000; page++ {
			q := url.Values{}
			if count > 0 {
				q.Set("count", strconv.Itoa(count))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			raw, err := c.get(c.p("vectorize/v2/indexes", args[0], "list"), q)
			if err != nil {
				return err
			}
			var r struct {
				Vectors     []map[string]any `json:"vectors"`
				IsTruncated bool             `json:"isTruncated"`
				NextCursor  string           `json:"nextCursor"`
				TotalCount  any              `json:"totalCount"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return fmt.Errorf("decoding vector list: %w", err)
			}
			ids = append(ids, r.Vectors...)
			total = r.TotalCount
			next = ""
			if r.IsTruncated {
				next = r.NextCursor
			}
			if !all || next == "" || seen[next] {
				break
			}
			seen[next] = true
			cursor = next
		}
		if ids == nil {
			ids = []map[string]any{}
		}
		return stEmitValue(map[string]any{"vectors": ids, "nextCursor": next, "totalCount": total}, func() error {
			stList("🧮 %d vectors", "No vectors", ids, []stCol{{Header: "ID", Path: "id"}})
			if next != "" {
				fmt.Println(ui.SubtleStyle.Render("  more: --cursor " + next))
			}
			return nil
		})
	}),
}

func newVecMetaCreateCmd(use string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use + " <index>",
		Short: "Enable metadata filtering on a property",
		Long: `Create a metadata index so queries can filter on a property. Vectors
inserted before the index exists aren't indexed.

Examples:
  cfctl vectorize create-metadata-index docs --property-name genre --type string`,
		Args: cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			prop, _ := cmd.Flags().GetString("property-name")
			typ, _ := cmd.Flags().GetString("type")
			if prop == "" || typ == "" {
				return fmt.Errorf("--property-name and --type are required")
			}
			return sbSend(c, "POST", c.p("vectorize/v2/indexes", args[0], "metadata_index", "create"), nil,
				map[string]any{"propertyName": prop, "indexType": typ}, fmt.Sprintf("Creating metadata index %s (%s) on %s", prop, typ, args[0]))
		}),
	}
	cmd.Flags().String("property-name", "", "Metadata property to index")
	cmd.Flags().String("type", "", "Property type: string, number, or boolean")
	return cmd
}

func newVecMetaListCmd(use string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <index>",
		Short: "List metadata indexes",
		Args:  cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			raw, err := c.get(c.p("vectorize/v2/indexes", args[0], "metadata_index", "list"), nil)
			if err != nil {
				return err
			}
			return stEmit(raw, func() error {
				stList("🏷️  %d metadata indexes", "No metadata indexes", stItems(raw, "metadataIndexes"), []stCol{
					{Header: "PROPERTY", Path: "propertyName"},
					{Header: "TYPE", Path: "indexType"},
				})
				return nil
			})
		}),
	}
}

func newVecMetaDeleteCmd(use string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use + " <index>",
		Short: "Delete a metadata index",
		Args:  cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			prop, _ := cmd.Flags().GetString("property-name")
			if prop == "" {
				return fmt.Errorf("--property-name is required")
			}
			if err := confirm(cmd, fmt.Sprintf("delete metadata index %s on %s", prop, args[0])); err != nil {
				return err
			}
			return sbSend(c, "POST", c.p("vectorize/v2/indexes", args[0], "metadata_index", "delete"), nil,
				map[string]any{"propertyName": prop}, "Deleting metadata index "+prop)
		}),
	}
	cmd.Flags().String("property-name", "", "Metadata property whose index to delete")
	stYes(cmd)
	return cmd
}

var vectorizeMetadataIndexCmd = &cobra.Command{
	Use:   "metadata-index",
	Short: "Manage metadata indexes (same as create-/list-/delete-metadata-index)",
}

func init() {
	stDataFlag(vectorizeCreateCmd)
	vectorizeCreateCmd.Flags().Int("dimensions", 0, "Vector dimensions")
	vectorizeCreateCmd.Flags().String("metric", "", "Distance metric: cosine, euclidean, or dot-product")
	vectorizeCreateCmd.Flags().String("preset", "", "Embedding model preset (sets dimensions and metric), e.g. @cf/baai/bge-base-en-v1.5")
	vectorizeCreateCmd.Flags().String("description", "", "Index description")

	stDataFlag(vectorizeQueryCmd)
	vectorizeQueryCmd.Flags().String("vector", "", "Query vector as a JSON array")
	vectorizeQueryCmd.Flags().String("vector-file", "", "File containing the query vector (JSON array)")
	vectorizeQueryCmd.Flags().Int("top-k", 0, "Number of matches (default 5)")
	vectorizeQueryCmd.Flags().Bool("return-values", false, "Include vector values")
	vectorizeQueryCmd.Flags().String("return-metadata", "", "Metadata to return: none, indexed, or all")
	vectorizeQueryCmd.Flags().String("filter", "", "Metadata filter as JSON (or @file)")

	stYes(vectorizeDeleteVectorsCmd)
	vectorizeListVectorsCmd.Flags().Int("count", 0, "Vectors per page (default 100, max 1000)")
	vectorizeListVectorsCmd.Flags().String("cursor", "", "Continue from this cursor")
	vectorizeListVectorsCmd.Flags().Bool("all", false, "Follow the cursor and list every vector ID")

	vectorizeMetadataIndexCmd.AddCommand(newVecMetaCreateCmd("create"), newVecMetaListCmd("list"), newVecMetaDeleteCmd("delete"))
	vectorizeCmd.AddCommand(vectorizeListCmd, vectorizeGetCmd, vectorizeCreateCmd, vectorizeDeleteCmd, vectorizeInfoCmd,
		newVecWriteCmd("insert"), newVecWriteCmd("upsert"), vectorizeQueryCmd, vectorizeGetVectorsCmd, vectorizeDeleteVectorsCmd,
		vectorizeListVectorsCmd, newVecMetaCreateCmd("create-metadata-index"), newVecMetaListCmd("list-metadata-index"),
		newVecMetaDeleteCmd("delete-metadata-index"), vectorizeMetadataIndexCmd)
	rootCmd.AddCommand(vectorizeCmd)
}
