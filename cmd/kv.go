package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var kvCmd = &cobra.Command{
	Use:   "kv",
	Short: "Manage Workers KV namespaces, keys, and values",
	Long: `Manage Workers KV: namespaces, single keys, and bulk operations.

A namespace can be given by ID or by title everywhere.

Examples:
  cfctl kv namespace list
  cfctl kv namespace create my-cache
  cfctl kv key list my-cache --prefix user:
  cfctl kv key put my-cache greeting "hello" --ttl 3600 --metadata '{"v":1}'
  cfctl kv key get my-cache greeting
  cfctl kv bulk put my-cache data.json

Generated equivalents: cfctl api workers-kv-namespace <op>.`,
}

var kvNamespaceCmd = &cobra.Command{
	Use:     "namespace",
	Aliases: []string{"namespaces", "ns"},
	Short:   "List, create, rename, and delete KV namespaces",
}

var kvNamespaceCols = []stCol{
	{Header: "TITLE", Path: "title"},
	{Header: "ID", Path: "id"},
	{Header: "URL ENCODING", Path: "supports_url_encoding"},
}

var kvNamespaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List KV namespaces",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := c.all(c.p("storage/kv/namespaces"), url.Values{"per_page": {"100"}})
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("🗂️  %d KV namespaces", "No KV namespaces found", stItems(raw), kvNamespaceCols)
			return nil
		})
	},
}

// kvNamespaceID resolves a namespace title or ID.
func kvNamespaceID(c *stClient, arg string) (string, error) {
	return c.resolve("KV namespace", c.p("storage/kv/namespaces"), url.Values{"per_page": {"100"}}, arg, "id", "title")
}

var kvNamespaceGetCmd = &cobra.Command{
	Use:   "get <namespace>",
	Short: "Show a KV namespace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		raw, err := c.get(c.p("storage/kv/namespaces", id), nil)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error { return stDetail("🗂️  KV namespace", raw) })
	},
}

var kvNamespaceCreateCmd = &cobra.Command{
	Use:   "create <title>",
	Short: "Create a KV namespace",
	Long: `Create a KV namespace.

Examples:
  cfctl kv namespace create my-cache
  cfctl kv namespace create eu-cache --jurisdiction eu`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		body := map[string]any{"title": args[0]}
		stFlagStr(cmd, body, "jurisdiction", "jurisdiction")
		raw, err := c.result(stReq{Method: "POST", Path: c.p("storage/kv/namespaces"), Body: body})
		if err != nil {
			return err
		}
		var ns struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal(raw, &ns)
		if err := stOK(raw, fmt.Sprintf("Created KV namespace %s (%s)", ns.Title, ns.ID)); err != nil || jsonOutput {
			return err
		}
		fmt.Println(ui.SubtleStyle.Render("  Binding config (wrangler.jsonc):"))
		fmt.Printf("    \"kv_namespaces\": [{ \"binding\": \"%s\", \"id\": \"%s\" }]\n", kvBindingName(ns.Title), ns.ID)
		return nil
	},
}

// kvBindingName suggests a binding name from a title (MY_CACHE).
func kvBindingName(title string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(title) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" {
		return "KV"
	}
	return s
}

var kvNamespaceRenameCmd = &cobra.Command{
	Use:   "rename <namespace> <new-title>",
	Short: "Rename a KV namespace",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "PUT", Path: c.p("storage/kv/namespaces", id), Body: map[string]any{"title": args[1]}})
		if err != nil {
			return err
		}
		return stOK(raw, fmt.Sprintf("Renamed KV namespace %s to %s", id, args[1]))
	},
}

var kvNamespaceDeleteCmd = &cobra.Command{
	Use:   "delete <namespace>",
	Short: "Delete a KV namespace and all its keys",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		if err := confirm(cmd, fmt.Sprintf("delete KV namespace %s (%s) and every key in it", args[0], id)); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "DELETE", Path: c.p("storage/kv/namespaces", id)})
		if err != nil {
			return err
		}
		return stOK(raw, "Deleted KV namespace "+args[0])
	},
}

// --- keys ---------------------------------------------------------------------

var kvKeyCmd = &cobra.Command{
	Use:     "key",
	Aliases: []string{"keys"},
	Short:   "List, read, write, and delete single KV keys",
}

var kvKeyListCmd = &cobra.Command{
	Use:   "list <namespace>",
	Short: "List keys in a namespace (names, expirations, metadata)",
	Long: `List keys in a namespace. All keys are listed by default (following the
cursor); use --limit to stop early.

Examples:
  cfctl kv key list my-cache
  cfctl kv key list my-cache --prefix user: --limit 50
  cfctl kv key list my-cache --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		prefix, _ := cmd.Flags().GetString("prefix")
		limit, _ := cmd.Flags().GetInt("limit")
		keys, err := kvListKeys(c, id, prefix, limit)
		if err != nil {
			return err
		}
		return stEmitValue(keys, func() error {
			stList("🔑 %d keys", "No keys found", keys, []stCol{
				{Header: "KEY", Path: "name"},
				{Header: "EXPIRES", Path: "expiration", Fmt: kvExpiry},
				{Header: "METADATA", Path: "metadata"},
			})
			return nil
		})
	},
}

func kvExpiry(v any) string {
	f, ok := v.(float64)
	if !ok || f == 0 {
		return ""
	}
	return stShortTime(stUnix(int64(f)))
}

// kvListKeys pages through /keys with the cursor.
func kvListKeys(c *stClient, nsID, prefix string, limit int) ([]map[string]any, error) {
	q := url.Values{"limit": {"1000"}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	for page := 0; page < api.DefaultMaxPages; page++ {
		if limit > 0 && limit-len(out) < 1000 {
			q.Set("limit", strconv.Itoa(max(limit-len(out), 10)))
		}
		resp, err := c.do(stReq{Method: "GET", Path: c.p("storage/kv/namespaces", nsID, "keys"), Query: q})
		if err != nil {
			return nil, err
		}
		if resp.Envelope == nil {
			return nil, fmt.Errorf("unexpected response listing keys")
		}
		out = append(out, stItems(resp.Envelope.Result)...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
		cur := resp.Envelope.ResultInfo.NextCursor()
		if cur == "" || seen[cur] {
			return out, nil
		}
		seen[cur] = true
		q.Set("cursor", cur)
	}
	fmt.Fprintln(os.Stderr, ui.Warn("stopped after too many pages; results are incomplete"))
	return out, nil
}

var kvKeyGetCmd = &cobra.Command{
	Use:   "get <namespace> <key>",
	Short: "Read a value (to stdout, or --output file)",
	Long: `Read the value stored under a key. The value is written as-is to stdout
(or to --output). Use --metadata to print the key's metadata instead.

Examples:
  cfctl kv key get my-cache greeting
  cfctl kv key get my-cache image.png --output image.png
  cfctl kv key get my-cache greeting --metadata`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		if meta, _ := cmd.Flags().GetBool("metadata"); meta {
			raw, err := c.get(c.p("storage/kv/namespaces", id, "metadata", args[1]), nil)
			if err != nil {
				return err
			}
			return printBody(stNonNull(raw), nil)
		}
		resp, err := c.do(stReq{Method: "GET", Path: c.p("storage/kv/namespaces", id, "values", args[1])})
		if err != nil {
			return err
		}
		if outPath, _ := cmd.Flags().GetString("output"); outPath != "" && outPath != "-" {
			if err := os.WriteFile(outPath, resp.Body, 0o644); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, ui.Success(fmt.Sprintf("Wrote %d bytes to %s", len(resp.Body), outPath)))
			return nil
		}
		_, err = os.Stdout.Write(resp.Body)
		return err
	},
}

var kvKeyPutCmd = &cobra.Command{
	Use:   "put <namespace> <key> [value]",
	Short: "Write a value (from an argument, --path file, or stdin)",
	Long: `Write a single key. The value comes from the argument, --path <file>, or
stdin when the value is "-".

Examples:
  cfctl kv key put my-cache greeting hello
  cfctl kv key put my-cache logo.png --path ./logo.png
  echo '{"a":1}' | cfctl kv key put my-cache config -
  cfctl kv key put my-cache session abc --ttl 3600
  cfctl kv key put my-cache session abc --expiration 2026-12-31T00:00:00Z
  cfctl kv key put my-cache user:1 '{"name":"Kyle"}' --metadata '{"plan":"pro"}'`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		pathFlag, _ := cmd.Flags().GetString("path")
		var value []byte
		switch {
		case pathFlag != "" && len(args) == 3:
			return fmt.Errorf("give the value as an argument or --path, not both")
		case pathFlag != "":
			b, err := os.ReadFile(pathFlag)
			if err != nil {
				return err
			}
			value = b
		case len(args) == 3 && args[2] == "-":
			b, err := stReadInput("-")
			if err != nil {
				return err
			}
			value = b
		case len(args) == 3:
			value = []byte(args[2])
		default:
			return fmt.Errorf("missing value: pass it as an argument, - for stdin, or --path <file>")
		}
		q := url.Values{}
		if err := kvExpirationQuery(cmd, q); err != nil {
			return err
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		req := stReq{Method: "PUT", Path: c.p("storage/kv/namespaces", id, "values", args[1]), Query: q, Body: value}
		if meta, _ := cmd.Flags().GetString("metadata"); meta != "" {
			if !json.Valid([]byte(meta)) {
				return fmt.Errorf("--metadata must be JSON")
			}
			tmp, err := os.CreateTemp("", "cfctl-kv-*")
			if err != nil {
				return err
			}
			defer os.Remove(tmp.Name())
			if _, err := tmp.Write(value); err != nil {
				return err
			}
			tmp.Close()
			body, ct, err := api.BuildMultipart([]api.FormField{
				{Name: "value", File: tmp.Name(), FileName: "value", ContentType: "application/octet-stream"},
				{Name: "metadata", Value: meta},
			})
			if err != nil {
				return err
			}
			req.Body, req.ContentType = body, ct
		}
		raw, err := c.result(req)
		if err != nil {
			return err
		}
		return stOK(raw, fmt.Sprintf("Wrote %s (%d bytes)", args[1], len(value)))
	},
}

// kvExpirationQuery maps --ttl / --expiration to query params.
func kvExpirationQuery(cmd *cobra.Command, q url.Values) error {
	if ttl, _ := cmd.Flags().GetInt("ttl"); ttl > 0 {
		if ttl < 60 {
			return fmt.Errorf("--ttl must be at least 60 seconds")
		}
		q.Set("expiration_ttl", strconv.Itoa(ttl))
	}
	if exp, _ := cmd.Flags().GetString("expiration"); exp != "" {
		t, err := stParseTime(exp)
		if err != nil {
			return err
		}
		q.Set("expiration", strconv.FormatInt(t.Unix(), 10))
	}
	return nil
}

var kvKeyDeleteCmd = &cobra.Command{
	Use:   "delete <namespace> <key>",
	Short: "Delete a key",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		if err := confirm(cmd, fmt.Sprintf("delete key %q from %s", args[1], args[0])); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "DELETE", Path: c.p("storage/kv/namespaces", id, "values", args[1])})
		if err != nil {
			return err
		}
		return stOK(raw, "Deleted "+args[1])
	},
}

// --- bulk -------------------------------------------------------------------

var kvBulkCmd = &cobra.Command{
	Use:   "bulk",
	Short: "Write, read, or delete many keys at once from a JSON file",
}

// kvBulkPutLimit / kvBulkDeleteLimit / kvBulkGetLimit are the API's per-request maximums.
var (
	kvBulkPutLimit    = 10000
	kvBulkDeleteLimit = 10000
	kvBulkGetLimit    = 100
)

var kvBulkPutCmd = &cobra.Command{
	Use:   "put <namespace> <file.json>",
	Short: "Write key-value pairs from a JSON array",
	Long: `Write key-value pairs from a JSON file (or - for stdin): an array of
objects like

  [{"key": "a", "value": "1"},
   {"key": "b", "value": "aGk=", "base64": true, "expiration_ttl": 3600, "metadata": {"x": 1}}]

Large files are sent in batches of 10,000 (the API maximum).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		items, err := kvReadArray(args[1])
		if err != nil {
			return err
		}
		for i, it := range items {
			m, ok := it.(map[string]any)
			if !ok || m["key"] == nil || m["value"] == nil {
				return fmt.Errorf("item %d: want an object with \"key\" and \"value\"", i)
			}
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		var results []json.RawMessage
		for start := 0; start < len(items); start += kvBulkPutLimit {
			end := min(start+kvBulkPutLimit, len(items))
			raw, err := c.result(stReq{Method: "PUT", Path: c.p("storage/kv/namespaces", id, "bulk"), Body: items[start:end]})
			if err != nil {
				return fmt.Errorf("batch %d-%d: %w", start, end-1, err)
			}
			results = append(results, raw)
		}
		return stEmitValue(map[string]any{"written": len(items), "batches": results}, func() error {
			fmt.Println(ui.Success(fmt.Sprintf("Wrote %d keys", len(items))))
			return nil
		})
	},
}

var kvBulkDeleteCmd = &cobra.Command{
	Use:   "delete <namespace> <file.json>",
	Short: "Delete keys listed in a JSON array",
	Long: `Delete the keys in a JSON file (or - for stdin): an array of key names, or
of objects with a "key" field (so a bulk put file works too).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		keys, err := kvReadKeys(args[1])
		if err != nil {
			return err
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		if err := confirm(cmd, fmt.Sprintf("delete %d keys from %s", len(keys), args[0])); err != nil {
			return err
		}
		deleted := 0
		var unsuccessful []string
		for start := 0; start < len(keys); start += kvBulkDeleteLimit {
			end := min(start+kvBulkDeleteLimit, len(keys))
			raw, err := c.result(stReq{Method: "POST", Path: c.p("storage/kv/namespaces", id, "bulk", "delete"), Body: keys[start:end]})
			if err != nil {
				return fmt.Errorf("batch %d-%d: %w", start, end-1, err)
			}
			var r struct {
				SuccessfulKeyCount *int     `json:"successful_key_count"`
				UnsuccessfulKeys   []string `json:"unsuccessful_keys"`
			}
			_ = json.Unmarshal(raw, &r)
			if r.SuccessfulKeyCount != nil {
				deleted += *r.SuccessfulKeyCount
			} else {
				deleted += end - start
			}
			unsuccessful = append(unsuccessful, r.UnsuccessfulKeys...)
		}
		return stEmitValue(map[string]any{"deleted": deleted, "unsuccessful_keys": unsuccessful}, func() error {
			fmt.Println(ui.Success(fmt.Sprintf("Deleted %d keys", deleted)))
			if len(unsuccessful) > 0 {
				fmt.Println(ui.Warn(fmt.Sprintf("%d keys failed (retry them): %s", len(unsuccessful), strings.Join(unsuccessful, ", "))))
			}
			return nil
		})
	},
}

var kvBulkGetCmd = &cobra.Command{
	Use:   "get <namespace> <file.json | key...>",
	Short: "Read many keys (JSON array file, or keys as arguments)",
	Long: `Read many keys at once. Give a JSON file (array of key names or of
objects with "key"), or the keys as arguments. Prints a JSON object mapping
each key to its value (batches of 100, the API maximum).

Examples:
  cfctl kv bulk get my-cache keys.json
  cfctl kv bulk get my-cache a b c --metadata
  cfctl kv bulk get my-cache keys.json --type json`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		var keys []string
		if len(args) == 2 && (strings.HasSuffix(args[1], ".json") || args[1] == "-") {
			k, err := kvReadKeys(args[1])
			if err != nil {
				return err
			}
			keys = k
		} else {
			keys = args[1:]
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := kvNamespaceID(c, args[0])
		if err != nil {
			return err
		}
		meta, _ := cmd.Flags().GetBool("metadata")
		typ, _ := cmd.Flags().GetString("type")
		values := map[string]json.RawMessage{}
		for start := 0; start < len(keys); start += kvBulkGetLimit {
			end := min(start+kvBulkGetLimit, len(keys))
			body := map[string]any{"keys": keys[start:end], "withMetadata": meta}
			if typ != "" {
				body["type"] = typ
			}
			raw, err := c.result(stReq{Method: "POST", Path: c.p("storage/kv/namespaces", id, "bulk", "get"), Body: body})
			if err != nil {
				return err
			}
			var r struct {
				Values map[string]json.RawMessage `json:"values"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return fmt.Errorf("decoding bulk get response: %w", err)
			}
			for k, v := range r.Values {
				values[k] = v
			}
		}
		return printJSONValue(values)
	},
}

// kvReadArray reads a JSON array from a file or stdin ("-").
func kvReadArray(spec string) ([]any, error) {
	if spec != "-" {
		spec = "@" + strings.TrimPrefix(spec, "@")
	}
	data, err := stReadInput(spec)
	if err != nil {
		return nil, err
	}
	var items []any
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("want a JSON array: %w", err)
	}
	return items, nil
}

// kvReadKeys reads key names: an array of strings or of {"key": ...}.
func kvReadKeys(spec string) ([]string, error) {
	items, err := kvReadArray(spec)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(items))
	for i, it := range items {
		switch v := it.(type) {
		case string:
			keys = append(keys, v)
		case map[string]any:
			k, ok := v["key"].(string)
			if !ok {
				return nil, fmt.Errorf("item %d has no \"key\"", i)
			}
			keys = append(keys, k)
		default:
			return nil, fmt.Errorf("item %d: want a key name or an object with \"key\"", i)
		}
	}
	return keys, nil
}

func init() {
	kvNamespaceCreateCmd.Flags().String("jurisdiction", "", "Restrict data to a jurisdiction: eu, fedramp, or us")
	stYes(kvNamespaceDeleteCmd)
	kvNamespaceCmd.AddCommand(kvNamespaceListCmd, kvNamespaceGetCmd, kvNamespaceCreateCmd, kvNamespaceRenameCmd, kvNamespaceDeleteCmd)

	kvKeyListCmd.Flags().String("prefix", "", "Only keys starting with this prefix")
	kvKeyListCmd.Flags().Int("limit", 0, "Stop after this many keys (0 = all)")
	kvKeyGetCmd.Flags().StringP("output", "o", "", "Write the value to this file instead of stdout")
	kvKeyGetCmd.Flags().Bool("metadata", false, "Print the key's metadata (JSON) instead of its value")
	kvKeyPutCmd.Flags().String("path", "", "Read the value from this file")
	kvKeyPutCmd.Flags().String("metadata", "", "JSON metadata to store with the key")
	kvKeyPutCmd.Flags().Int("ttl", 0, "Expire the key after this many seconds (at least 60)")
	kvKeyPutCmd.Flags().String("expiration", "", "Expire the key at this time (RFC 3339, YYYY-MM-DD, or Unix seconds)")
	stYes(kvKeyDeleteCmd)
	kvKeyCmd.AddCommand(kvKeyListCmd, kvKeyGetCmd, kvKeyPutCmd, kvKeyDeleteCmd)

	stYes(kvBulkDeleteCmd)
	kvBulkGetCmd.Flags().Bool("metadata", false, "Include each key's metadata")
	kvBulkGetCmd.Flags().String("type", "", "Value type: text (default) or json (parse stored JSON)")
	kvBulkCmd.AddCommand(kvBulkPutCmd, kvBulkDeleteCmd, kvBulkGetCmd)

	kvCmd.AddCommand(kvNamespaceCmd, kvKeyCmd, kvBulkCmd)
	rootCmd.AddCommand(kvCmd)
}
