package cmd

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/r2"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var d1Cmd = &cobra.Command{
	Use:   "d1",
	Short: "Manage D1 databases: create, query, export/import, time travel, migrations",
	Long: `Manage D1 (serverless SQLite) databases.

A database can be given by name or UUID everywhere. Everything runs against
the remote database through the API (there is no local mode; use wrangler's
--local for Miniflare).

Note: every D1 query is an API POST, so read-only mode (--read-only /
CFCTL_READONLY) blocks execute, export, and migrations, even for SELECTs.

Examples:
  cfctl d1 list
  cfctl d1 create my-db --location weur
  cfctl d1 execute my-db --command "SELECT name FROM sqlite_master WHERE type='table'"
  cfctl d1 execute my-db --file schema.sql
  cfctl d1 export my-db --output backup.sql
  cfctl d1 migrations create my-db add_users
  cfctl d1 migrations apply my-db
  cfctl d1 time-travel info my-db

Generated equivalents: cfctl api d1 <op>.`,
}

// d1PollInterval is how often export/import status is polled.
var d1PollInterval = time.Second

// d1ID resolves a database name or UUID.
func d1ID(c *stClient, arg string) (string, error) {
	q := url.Values{"per_page": {"1000"}}
	if !stLooksLikeID(arg) {
		q.Set("name", arg)
	}
	return c.resolve("D1 database", c.p("d1/database"), q, arg, "uuid", "name")
}

var d1ListCmd = &cobra.Command{
	Use:   "list",
	Short: "List D1 databases",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		q := url.Values{"per_page": {"1000"}}
		if v, _ := cmd.Flags().GetString("name"); v != "" {
			q.Set("name", v)
		}
		raw, err := c.all(c.p("d1/database"), q)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("🗄️  %d D1 databases", "No D1 databases found", stItems(raw), []stCol{
				{Header: "NAME", Path: "name"},
				{Header: "UUID", Path: "uuid"},
				{Header: "CREATED", Path: "created_at"},
				{Header: "TABLES", Path: "num_tables"},
				{Header: "SIZE", Path: "file_size", Fmt: d1Size},
				{Header: "VERSION", Path: "version"},
			})
			return nil
		})
	},
}

func d1Size(v any) string {
	f, ok := v.(float64)
	if !ok {
		return ""
	}
	return r2.HumanBytes(f)
}

var d1InfoCmd = &cobra.Command{
	Use:     "info <database>",
	Aliases: []string{"get"},
	Short:   "Show a database's size, tables, region, replication, and 24h query stats",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		raw, err := c.get(c.p("d1/database", id), nil)
		if err != nil {
			return err
		}
		stats, serr := d1Stats(c, id, 24*time.Hour)
		if jsonOutput {
			var obj map[string]any
			_ = json.Unmarshal(raw, &obj)
			if obj == nil {
				obj = map[string]any{}
			}
			if serr == nil {
				obj["last_24h"] = stats
			}
			return printJSONValue(obj)
		}
		if err := stDetail("🗄️  "+args[0], raw); err != nil {
			return err
		}
		if serr == nil {
			fmt.Printf("  %-22s %s\n", "read queries (24h):", r2.HumanCount(stats["readQueries"]))
			fmt.Printf("  %-22s %s\n", "write queries (24h):", r2.HumanCount(stats["writeQueries"]))
			fmt.Printf("  %-22s %s\n", "rows read (24h):", r2.HumanCount(stats["rowsRead"]))
			fmt.Printf("  %-22s %s\n", "rows written (24h):", r2.HumanCount(stats["rowsWritten"]))
		}
		return nil
	},
}

// d1Stats sums d1AnalyticsAdaptiveGroups for a database over the last window.
func d1Stats(c *stClient, id string, window time.Duration) (map[string]float64, error) {
	q := `query($acct: String!, $db: String!, $start: Date!, $end: Date!) {
  viewer { accounts(filter: {accountTag: $acct}) {
    d1AnalyticsAdaptiveGroups(limit: 10000, filter: {databaseId: $db, date_geq: $start, date_leq: $end}) {
      sum { readQueries writeQueries rowsRead rowsWritten }
    } } } }`
	now := time.Now().UTC()
	var data struct {
		Viewer struct {
			Accounts []struct {
				Groups []struct {
					Sum map[string]float64 `json:"sum"`
				} `json:"d1AnalyticsAdaptiveGroups"`
			} `json:"accounts"`
		} `json:"viewer"`
	}
	err := c.graphql(q, map[string]any{"acct": c.acct, "db": id, "start": now.Add(-window).Format("2006-01-02"), "end": now.Format("2006-01-02")}, &data)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{"readQueries": 0, "writeQueries": 0, "rowsRead": 0, "rowsWritten": 0}
	for _, a := range data.Viewer.Accounts {
		for _, g := range a.Groups {
			for k, v := range g.Sum {
				out[k] += v
			}
		}
	}
	return out, nil
}

var d1CreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a D1 database",
	Long: `Create a D1 database.

Examples:
  cfctl d1 create my-db
  cfctl d1 create my-db --location weur --read-replication auto
  cfctl d1 create eu-db --jurisdiction eu`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		body["name"] = args[0]
		stFlagStr(cmd, body, "location", "primary_location_hint")
		stFlagStr(cmd, body, "jurisdiction", "jurisdiction")
		stFlagStr(cmd, body, "read-replication", "read_replication.mode")
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "POST", Path: c.p("d1/database"), Body: body})
		if err != nil {
			return err
		}
		var db struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &db)
		if err := stOK(raw, fmt.Sprintf("Created D1 database %s (%s)", db.Name, db.UUID)); err != nil || jsonOutput {
			return err
		}
		fmt.Println(ui.SubtleStyle.Render("  Binding config (wrangler.jsonc):"))
		fmt.Printf("    \"d1_databases\": [{ \"binding\": \"%s\", \"database_name\": \"%s\", \"database_id\": \"%s\" }]\n", kvBindingName(db.Name), db.Name, db.UUID)
		return nil
	},
}

var d1UpdateCmd = &cobra.Command{
	Use:   "update <database> --read-replication auto|disabled",
	Short: "Change a database's read replication mode",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		stFlagStr(cmd, body, "read-replication", "read_replication.mode")
		if len(body) == 0 {
			return fmt.Errorf("nothing to change: pass --read-replication or --data")
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "PATCH", Path: c.p("d1/database", id), Body: body})
		if err != nil {
			return err
		}
		return stOK(raw, "Updated "+args[0])
	},
}

var d1DeleteCmd = &cobra.Command{
	Use:   "delete <database>",
	Short: "Delete a D1 database",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		if err := confirm(cmd, fmt.Sprintf("delete D1 database %s (%s) and all its data", args[0], id)); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "DELETE", Path: c.p("d1/database", id)})
		if err != nil {
			return err
		}
		return stOK(raw, "Deleted D1 database "+args[0])
	},
}

// --- execute ----------------------------------------------------------------

// d1RawResult is one statement's result from /raw.
type d1RawResult struct {
	Results struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	} `json:"results"`
	Success bool           `json:"success"`
	Meta    map[string]any `json:"meta"`
}

// d1ImportThreshold: --file inputs larger than this go through the import API.
var d1ImportThreshold int64 = 5 << 20

var d1ExecuteCmd = &cobra.Command{
	Use:   "execute <database> (--command SQL | --file path)",
	Short: "Run SQL against a database (tables; --json for rows as objects)",
	Long: `Run SQL against a remote D1 database. Multiple statements separated by
semicolons run as a batch; each statement's result is printed as a table.
--json prints the API's result array (rows as objects, plus meta).

Files over 5 MB are loaded with the import API instead (no rows returned),
like wrangler does for large files.

Examples:
  cfctl d1 execute my-db --command "SELECT * FROM users LIMIT 10"
  cfctl d1 execute my-db --command "SELECT * FROM users WHERE id = ?" --param 42
  cfctl d1 execute my-db --file schema.sql --yes
  cfctl d1 execute my-db --command "SELECT count(*) AS n FROM users" --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		command, _ := cmd.Flags().GetString("command")
		file, _ := cmd.Flags().GetString("file")
		params, _ := cmd.Flags().GetStringArray("param")
		if (command == "") == (file == "") {
			return fmt.Errorf("give exactly one of --command or --file")
		}
		sql := command
		if file != "" {
			st, err := os.Stat(file)
			if err != nil {
				return err
			}
			if st.Size() > d1ImportThreshold {
				c, err := newST(cmd)
				if err != nil {
					return err
				}
				id, err := d1ID(c, args[0])
				if err != nil {
					return err
				}
				if err := confirm(cmd, fmt.Sprintf("import %s (%s) into %s", file, r2.HumanBytes(float64(st.Size())), args[0])); err != nil {
					return err
				}
				return d1Import(c, id, file)
			}
			b, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			sql = string(b)
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		if file != "" && d1IsWrite(sql) {
			if err := confirm(cmd, fmt.Sprintf("run %s against %s", file, args[0])); err != nil {
				return err
			}
		}
		body := map[string]any{"sql": sql}
		if len(params) > 0 {
			body["params"] = params
		}
		if jsonOutput {
			raw, err := c.result(stReq{Method: "POST", Path: c.p("d1/database", id, "query"), Body: body})
			if err != nil {
				return err
			}
			return printBody(raw, nil)
		}
		raw, err := c.result(stReq{Method: "POST", Path: c.p("d1/database", id, "raw"), Body: body})
		if err != nil {
			return err
		}
		var results []d1RawResult
		if err := json.Unmarshal(raw, &results); err != nil {
			return printBody(raw, nil)
		}
		for i, r := range results {
			if len(results) > 1 {
				fmt.Println(ui.SubtleStyle.Render(fmt.Sprintf("-- statement %d", i+1)))
			}
			d1PrintRows(r)
		}
		return nil
	},
}

// d1IsWrite guesses whether SQL modifies anything (for file confirmation).
func d1IsWrite(sql string) bool {
	s := strings.ToUpper(sql)
	for _, kw := range []string{"INSERT", "UPDATE", "DELETE", "DROP", "CREATE", "ALTER", "REPLACE", "TRUNCATE"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func d1PrintRows(r d1RawResult) {
	if len(r.Results.Columns) > 0 {
		rows := make([][]string, 0, len(r.Results.Rows))
		for _, row := range r.Results.Rows {
			cells := make([]string, len(row))
			for i, v := range row {
				if v == nil {
					cells[i] = "NULL"
				} else {
					cells[i] = truncate(strings.ReplaceAll(stString(v), "\n", " "), 80)
				}
			}
			rows = append(rows, cells)
		}
		stTable(r.Results.Columns, rows)
	}
	var parts []string
	for _, k := range []string{"rows_read", "rows_written", "changes", "duration", "last_row_id", "served_by_region"} {
		if v, ok := r.Meta[k]; ok && v != nil {
			s := stString(v)
			if k == "duration" {
				s += "ms"
			}
			parts = append(parts, strings.ReplaceAll(k, "_", " ")+": "+s)
		}
	}
	fmt.Println(ui.SubtleStyle.Render(fmt.Sprintf("  %d rows · %s", len(r.Results.Rows), strings.Join(parts, " · "))))
}

// --- export / import ----------------------------------------------------------

// d1Polling is the export/import polling response.
type d1Polling struct {
	Success    bool            `json:"success"`
	Type       string          `json:"type"`
	AtBookmark string          `json:"at_bookmark"`
	Status     string          `json:"status"`
	Messages   []string        `json:"messages"`
	Errors     []string        `json:"errors"`
	Error      string          `json:"error"`
	Result     json.RawMessage `json:"result"`
	UploadURL  string          `json:"upload_url"`
	Filename   string          `json:"filename"`
}

func d1Poll(c *stClient, id, kind string, body map[string]any) (*d1Polling, error) {
	for i := 0; ; i++ {
		raw, err := c.result(stReq{Method: "POST", Path: c.p("d1/database", id, kind), Body: body})
		if err != nil {
			return nil, err
		}
		var p d1Polling
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("unexpected %s response: %w", kind, err)
		}
		switch {
		case p.Status == "error" || p.Error != "" || (len(p.Errors) > 0 && p.Status != "active"):
			msg := p.Error
			if msg == "" {
				msg = strings.Join(p.Errors, "; ")
			}
			return nil, fmt.Errorf("%s failed: %s", kind, msg)
		case p.Status == "complete" || (kind == "import" && body["action"] == "init" && p.UploadURL != ""):
			// An init answer with upload_url means "upload first" whatever
			// its status says (wrangler checks only for upload_url).
			return &p, nil
		}
		if i > 3600 {
			return nil, fmt.Errorf("%s still running after an hour (bookmark %s)", kind, p.AtBookmark)
		}
		if p.AtBookmark != "" {
			body["current_bookmark"] = p.AtBookmark
			if kind == "import" {
				body = map[string]any{"action": "poll", "current_bookmark": p.AtBookmark}
			}
		}
		if len(p.Messages) > 0 && !jsonOutput {
			fmt.Fprintln(os.Stderr, ui.Info(p.Messages[len(p.Messages)-1]))
		}
		time.Sleep(d1PollInterval)
	}
}

// d1Fetch GETs a pre-signed URL (no Cloudflare credentials are sent).
func d1Fetch(method, rawURL string, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	resp, err := api.NewHTTPClient().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // don't echo the signed URL
		}
		return nil, err
	}
	if resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s pre-signed URL: HTTP %d", method, resp.StatusCode)
	}
	return resp, nil
}

var d1ExportCmd = &cobra.Command{
	Use:   "export <database> --output file.sql",
	Short: "Export a database (schema and/or data) as SQL",
	Long: `Export a database as a .sql file. The export runs on Cloudflare, then the
file is downloaded from a pre-signed URL.

Examples:
  cfctl d1 export my-db --output backup.sql
  cfctl d1 export my-db --output schema.sql --no-data
  cfctl d1 export my-db --output users.sql --table users --table sessions`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		output, _ := cmd.Flags().GetString("output")
		if output == "" {
			return fmt.Errorf("--output is required (- for stdout)")
		}
		noData, _ := cmd.Flags().GetBool("no-data")
		noSchema, _ := cmd.Flags().GetBool("no-schema")
		tables, _ := cmd.Flags().GetStringArray("table")
		if noData && noSchema {
			return fmt.Errorf("--no-data and --no-schema together export nothing")
		}
		stTransferTimeout(cmd, 10*time.Minute)
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		dump := map[string]any{"no_data": noData, "no_schema": noSchema}
		if len(tables) > 0 {
			dump["tables"] = tables
		}
		p, err := d1Poll(c, id, "export", map[string]any{"output_format": "polling", "dump_options": dump})
		if err != nil {
			return err
		}
		var res struct {
			Filename  string `json:"filename"`
			SignedURL string `json:"signed_url"`
		}
		_ = json.Unmarshal(p.Result, &res)
		if res.SignedURL == "" {
			return fmt.Errorf("export finished without a download URL")
		}
		resp, err := d1Fetch("GET", res.SignedURL, nil, 0)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if output == "-" {
			_, err = io.Copy(os.Stdout, resp.Body)
			return err
		}
		n, err := r2.WriteFileAtomic(output, resp.Body)
		if err != nil {
			return err
		}
		return stEmitValue(map[string]any{"database": args[0], "output": output, "bytes": n, "bookmark": p.AtBookmark}, func() error {
			fmt.Println(ui.Success(fmt.Sprintf("Exported %s to %s (%s, bookmark %s)", args[0], output, r2.HumanBytes(float64(n)), p.AtBookmark)))
			return nil
		})
	},
}

// d1Import uploads a SQL file with the import API and waits for it.
func d1Import(c *stClient, id, file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sum := md5.Sum(data)
	etag := hex.EncodeToString(sum[:])
	init, err := d1Poll(c, id, "import", map[string]any{"action": "init", "etag": etag})
	if err != nil {
		return err
	}
	final := init
	if init.UploadURL != "" {
		resp, err := d1Fetch("PUT", init.UploadURL, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return fmt.Errorf("uploading %s: %w", file, err)
		}
		got := strings.Trim(resp.Header.Get("ETag"), `"`)
		resp.Body.Close()
		if got != "" && got != etag {
			return fmt.Errorf("upload checksum mismatch (got %s, want %s)", got, etag)
		}
		final, err = d1Poll(c, id, "import", map[string]any{"action": "ingest", "etag": etag, "filename": init.Filename})
		if err != nil {
			return err
		}
	}
	var res struct {
		NumQueries    float64        `json:"num_queries"`
		FinalBookmark string         `json:"final_bookmark"`
		Meta          map[string]any `json:"meta"`
	}
	_ = json.Unmarshal(final.Result, &res)
	return stEmitValue(map[string]any{"file": file, "num_queries": res.NumQueries, "final_bookmark": res.FinalBookmark, "meta": res.Meta}, func() error {
		fmt.Println(ui.Success(fmt.Sprintf("Imported %s: %s queries (bookmark %s)", file, r2.HumanCount(res.NumQueries), res.FinalBookmark)))
		return nil
	})
}

var d1ImportCmd = &cobra.Command{
	Use:   "import <database> <file.sql>",
	Short: "Import a SQL file (any size) with the import API",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := os.Stat(args[1]); err != nil {
			return err
		}
		stTransferTimeout(cmd, 10*time.Minute)
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		if err := confirm(cmd, fmt.Sprintf("import %s into %s", args[1], args[0])); err != nil {
			return err
		}
		return d1Import(c, id, args[1])
	},
}

// --- time travel --------------------------------------------------------------

var d1TimeTravelCmd = &cobra.Command{
	Use:   "time-travel",
	Short: "Look up bookmarks and restore a database to a point in time",
}

var d1TTInfoCmd = &cobra.Command{
	Use:   "info <database> [--timestamp T]",
	Short: "Show the current bookmark, or the one at a timestamp",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		q := url.Values{}
		if ts, _ := cmd.Flags().GetString("timestamp"); ts != "" {
			t, err := stParseTime(ts)
			if err != nil {
				return err
			}
			q.Set("timestamp", t.Format(time.RFC3339))
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		raw, err := c.get(c.p("d1/database", id, "time_travel", "bookmark"), q)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			if err := stDetail("⏱️  "+args[0], raw); err != nil {
				return err
			}
			var b struct {
				Bookmark string `json:"bookmark"`
			}
			_ = json.Unmarshal(raw, &b)
			fmt.Println(ui.SubtleStyle.Render("  restore with: cfctl d1 time-travel restore " + args[0] + " --bookmark " + b.Bookmark))
			return nil
		})
	},
}

var d1TTRestoreCmd = &cobra.Command{
	Use:   "restore <database> (--bookmark B | --timestamp T)",
	Short: "Restore a database to a bookmark or point in time",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		q := url.Values{}
		bm, _ := cmd.Flags().GetString("bookmark")
		ts, _ := cmd.Flags().GetString("timestamp")
		switch {
		case bm != "" && ts != "":
			return fmt.Errorf("give --bookmark or --timestamp, not both")
		case bm != "":
			q.Set("bookmark", bm)
		case ts != "":
			t, err := stParseTime(ts)
			if err != nil {
				return err
			}
			q.Set("timestamp", t.Format(time.RFC3339))
		default:
			return fmt.Errorf("give --bookmark or --timestamp")
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		target := bm
		if target == "" {
			target = q.Get("timestamp")
		}
		if err := confirm(cmd, fmt.Sprintf("restore %s to %s (later changes are undone; the restore itself can be undone with the previous bookmark)", args[0], target)); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "POST", Path: c.p("d1/database", id, "time_travel", "restore"), Query: q})
		if err != nil {
			return err
		}
		var r struct {
			Bookmark         string `json:"bookmark"`
			PreviousBookmark string `json:"previous_bookmark"`
			Message          string `json:"message"`
		}
		_ = json.Unmarshal(raw, &r)
		if err := stOK(raw, fmt.Sprintf("Restored %s to bookmark %s", args[0], r.Bookmark)); err != nil || jsonOutput {
			return err
		}
		if r.PreviousBookmark != "" {
			fmt.Println(ui.SubtleStyle.Render("  undo with: cfctl d1 time-travel restore " + args[0] + " --bookmark " + r.PreviousBookmark))
		}
		return nil
	},
}

// --- insights ---------------------------------------------------------------

var d1InsightsCmd = &cobra.Command{
	Use:   "insights <database>",
	Short: "Show the most expensive queries (GraphQL d1QueriesAdaptiveGroups)",
	Long: `Show query statistics for a database from the GraphQL Analytics API:
total and average duration, rows read/written/returned, and run count per
query shape.

Examples:
  cfctl d1 insights my-db
  cfctl d1 insights my-db --since 7d --sort-by reads --limit 10`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		since, _ := cmd.Flags().GetString("since")
		sortBy, _ := cmd.Flags().GetString("sort-by")
		dir, _ := cmd.Flags().GetString("sort-direction")
		limit, _ := cmd.Flags().GetInt("limit")
		window, err := d1ParseWindow(since)
		if err != nil {
			return err
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		q := `query($acct: String!, $filter: AccountD1QueriesAdaptiveGroupsFilter_InputObject) {
  viewer { accounts(filter: {accountTag: $acct}) {
    d1QueriesAdaptiveGroups(limit: 10000, filter: $filter) {
      sum { queryDurationMs rowsRead rowsWritten rowsReturned }
      avg { queryDurationMs rowsRead rowsWritten rowsReturned }
      count
      dimensions { query }
    } } } }`
		now := time.Now().UTC()
		filter := map[string]any{"databaseId": id, "datetime_geq": now.Add(-window).Format(time.RFC3339), "datetime_leq": now.Format(time.RFC3339)}
		var data struct {
			Viewer struct {
				Accounts []struct {
					Groups []struct {
						Sum   map[string]float64 `json:"sum"`
						Avg   map[string]float64 `json:"avg"`
						Count float64            `json:"count"`
						Dim   struct {
							Query string `json:"query"`
						} `json:"dimensions"`
					} `json:"d1QueriesAdaptiveGroups"`
				} `json:"accounts"`
			} `json:"viewer"`
		}
		if err := c.graphql(q, map[string]any{"acct": c.acct, "filter": filter}, &data); err != nil {
			return err
		}
		type row struct {
			Query       string  `json:"query"`
			Count       float64 `json:"count"`
			TotalMs     float64 `json:"total_duration_ms"`
			AvgMs       float64 `json:"avg_duration_ms"`
			RowsRead    float64 `json:"rows_read"`
			RowsWritten float64 `json:"rows_written"`
			RowsRet     float64 `json:"rows_returned"`
		}
		var rows []row
		for _, a := range data.Viewer.Accounts {
			for _, g := range a.Groups {
				rows = append(rows, row{Query: g.Dim.Query, Count: g.Count, TotalMs: g.Sum["queryDurationMs"], AvgMs: g.Avg["queryDurationMs"],
					RowsRead: g.Sum["rowsRead"], RowsWritten: g.Sum["rowsWritten"], RowsRet: g.Sum["rowsReturned"]})
			}
		}
		key := map[string]func(r row) float64{
			"time": func(r row) float64 { return r.TotalMs }, "reads": func(r row) float64 { return r.RowsRead },
			"writes": func(r row) float64 { return r.RowsWritten }, "count": func(r row) float64 { return r.Count },
		}[sortBy]
		if key == nil {
			return fmt.Errorf("--sort-by must be time, reads, writes, or count")
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if dir == "asc" {
				return key(rows[i]) < key(rows[j])
			}
			return key(rows[i]) > key(rows[j])
		})
		if limit > 0 && len(rows) > limit {
			rows = rows[:limit]
		}
		if rows == nil {
			rows = []row{}
		}
		return stEmitValue(rows, func() error {
			if len(rows) == 0 {
				fmt.Println(ui.Warn("No queries in the last " + since))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🔎 Top %d queries on %s (last %s, by %s)", len(rows), args[0], since, sortBy)))
			trows := [][]string{}
			for _, r := range rows {
				trows = append(trows, []string{r2.HumanCount(r.Count), fmt.Sprintf("%.1f", r.TotalMs), fmt.Sprintf("%.2f", r.AvgMs),
					r2.HumanCount(r.RowsRead), r2.HumanCount(r.RowsWritten), truncate(strings.Join(strings.Fields(r.Query), " "), 70)})
			}
			stTable([]string{"RUNS", "TOTAL MS", "AVG MS", "ROWS READ", "ROWS WRITTEN", "QUERY"}, trows)
			return nil
		})
	},
}

// d1ParseWindow parses "1d", "7d", "24h", "30m".
func d1ParseWindow(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &n); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --since %q: use e.g. 1d, 7d, 24h", s)
	}
	return d, nil
}

func init() {
	d1ListCmd.Flags().String("name", "", "Only databases whose name matches")
	d1CreateCmd.Flags().String("location", "", "Primary location hint: wnam, enam, weur, eeur, apac, oc")
	d1CreateCmd.Flags().String("jurisdiction", "", "Restrict to a jurisdiction: eu, fedramp, or us")
	d1CreateCmd.Flags().String("read-replication", "", "Read replication: auto or disabled")
	stDataFlag(d1CreateCmd)
	d1UpdateCmd.Flags().String("read-replication", "", "Read replication: auto or disabled")
	stDataFlag(d1UpdateCmd)
	stYes(d1DeleteCmd)

	d1ExecuteCmd.Flags().String("command", "", "SQL to run")
	d1ExecuteCmd.Flags().String("file", "", "File of SQL to run")
	d1ExecuteCmd.Flags().StringArray("param", nil, "Positional parameter for ? placeholders (repeatable)")
	d1ExecuteCmd.Flags().Bool("remote", true, "Accepted for wrangler compatibility (always remote)")
	_ = d1ExecuteCmd.Flags().MarkHidden("remote")
	stYes(d1ExecuteCmd)

	d1ExportCmd.Flags().StringP("output", "o", "", "Write the SQL here (- for stdout)")
	d1ExportCmd.Flags().Bool("no-data", false, "Export the schema only")
	d1ExportCmd.Flags().Bool("no-schema", false, "Export the data only")
	d1ExportCmd.Flags().StringArray("table", nil, "Export only this table (repeatable)")
	stYes(d1ImportCmd)

	d1TTInfoCmd.Flags().String("timestamp", "", "Find the bookmark at or before this time (RFC 3339 or Unix seconds)")
	d1TTRestoreCmd.Flags().String("bookmark", "", "Bookmark to restore to")
	d1TTRestoreCmd.Flags().String("timestamp", "", "Time to restore to (RFC 3339 or Unix seconds)")
	stYes(d1TTRestoreCmd)
	d1TimeTravelCmd.AddCommand(d1TTInfoCmd, d1TTRestoreCmd)

	d1InsightsCmd.Flags().String("since", "1d", "Time window: e.g. 1d, 7d, 24h")
	d1InsightsCmd.Flags().String("sort-by", "time", "Sort by: time, reads, writes, or count")
	d1InsightsCmd.Flags().String("sort-direction", "desc", "asc or desc")
	d1InsightsCmd.Flags().Int("limit", 20, "Show at most this many queries")

	d1Cmd.AddCommand(d1ListCmd, d1InfoCmd, d1CreateCmd, d1UpdateCmd, d1DeleteCmd, d1ExecuteCmd, d1ExportCmd, d1ImportCmd, d1TimeTravelCmd, d1InsightsCmd)
	rootCmd.AddCommand(d1Cmd)
}
