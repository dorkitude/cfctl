package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var basinCmd = &cobra.Command{
	Use:   "basin",
	Short: "Manage Basin: Pipelines (streams, sinks, pipelines) and the Basin Iceberg catalog",
	Long: `Manage Basin products: Cloudflare Pipelines (streams → SQL pipelines →
sinks) and the Basin Catalog (Apache Iceberg REST catalog on an R2 bucket).

Examples:
  cfctl basin pipelines list
  cfctl basin pipelines streams create events --http --format json
  cfctl basin pipelines sinks create events-r2 --type r2 --bucket logs --access-key-id KEY --secret-access-key SECRET
  cfctl basin pipelines create events-pipe --sql "INSERT INTO events_r2 SELECT * FROM events"
  cfctl basin catalog enable my-bucket

'basin sql query' (R2 SQL) is not part of the REST API; use wrangler for it.

Generated equivalents: cfctl api workers-pipelines-other <op>, cfctl api basin-catalog-management <op>.`,
}

var basinPipelinesCmd = &cobra.Command{
	Use:     "pipelines",
	Aliases: []string{"pipeline"},
	Short:   "Manage Cloudflare Pipelines, streams, and sinks",
}

// basinResolve resolves a v1 pipelines resource (pipelines, streams, sinks)
// by ID or name.
func basinResolve(kind, coll string) func(c *stClient, arg string) (string, error) {
	return func(c *stClient, arg string) (string, error) {
		return c.resolve(kind, c.p("pipelines/v1/"+coll), url.Values{"name": {arg}}, arg, "id", "name")
	}
}

func basinPath(kind, coll string, segs ...string) sbPathFn {
	res := basinResolve(kind, coll)
	return func(c *stClient, args []string) (string, url.Values, error) {
		id, err := res(c, args[0])
		if err != nil {
			return "", nil, err
		}
		return c.p("pipelines/v1/"+coll, append([]string{id}, segs...)...), nil, nil
	}
}

func basinListPath(coll string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("pipelines/v1/" + coll), nil, nil
	}
}

var basinCreated = stCol{Header: "CREATED", Path: "", Fmt: sbFirst("created_at", "created_on")}

var basinPipelinesListCmd = sbListCmd("list", "List pipelines", "", cobra.NoArgs, basinListPath("pipelines"),
	"🚰 %d pipelines", "No pipelines found", []stCol{
		{Header: "NAME", Path: "name"},
		{Header: "ID", Path: "id"},
		{Header: "STATUS", Path: "status"},
		{Header: "SQL", Path: "sql", Fmt: sbTrunc(50)},
		basinCreated,
	})

var basinPipelinesGetCmd = sbGetCmd("get <pipeline>", "Show a pipeline", "", "🚰 Pipeline", cobra.ExactArgs(1), basinPath("pipeline", "pipelines"))

var basinPipelinesDeleteCmd = sbDeleteCmd("delete <pipeline>", "Delete a pipeline", "", cobra.ExactArgs(1),
	func(a []string) string { return "pipeline " + a[0] }, basinPath("pipeline", "pipelines"))

// basinSQL reads --sql or --sql-file.
func basinSQL(cmd *cobra.Command, args []string) (string, error) {
	sql, _ := cmd.Flags().GetString("sql")
	file, _ := cmd.Flags().GetString("sql-file")
	if len(args) > 0 && sql == "" {
		sql = args[0]
	}
	switch {
	case sql != "" && file != "":
		return "", fmt.Errorf("use --sql or --sql-file, not both")
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		sql = string(b)
	}
	if strings.TrimSpace(sql) == "" {
		return "", fmt.Errorf("pass the SQL with --sql or --sql-file")
	}
	return sql, nil
}

func basinSQLFlags(cmd *cobra.Command) {
	cmd.Flags().String("sql", "", "Pipeline SQL (INSERT INTO <sink> SELECT ... FROM <stream>)")
	cmd.Flags().String("sql-file", "", "Read the pipeline SQL from this file")
}

var basinPipelinesCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a pipeline from SQL",
	Long: `Create a pipeline that reads from a stream and writes to a sink.

Examples:
  cfctl basin pipelines create clicks --sql "INSERT INTO clicks_sink SELECT * FROM clicks_stream"
  cfctl basin pipelines create clicks --sql-file pipeline.sql`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		sql, err := basinSQL(cmd, nil)
		if err != nil {
			return err
		}
		return sbSendShow(c, "POST", c.p("pipelines/v1/pipelines"), nil, map[string]any{"name": args[0], "sql": sql}, "Created pipeline "+args[0])
	}),
}

var basinPipelinesValidateCmd = &cobra.Command{
	Use:   "validate-sql [sql]",
	Short: "Validate pipeline SQL without creating anything",
	Args:  cobra.MaximumNArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		sql, err := basinSQL(cmd, args)
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "POST", Path: c.p("pipelines/v1/validate_sql"), Body: map[string]any{"sql": sql}})
		if err != nil {
			return err
		}
		return stEmit(raw, func() error { return stDetail("✓ SQL is valid", raw) })
	}),
}

var basinPipelinesUpdateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Replace a legacy pipeline's configuration (--legacy --data)",
	Long: `Update a legacy (pre-v1) pipeline by name, replacing its whole
configuration with --data. Pipelines created with the v1 API can't be updated:
delete and recreate them.

Examples:
  cfctl basin pipelines update old-pipe --legacy --data @pipeline.json`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		if legacy, _ := cmd.Flags().GetBool("legacy"); !legacy {
			return fmt.Errorf("only legacy pipelines can be updated (pass --legacy); v1 pipelines must be deleted and recreated")
		}
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return fmt.Errorf("--data is required (the full legacy pipeline configuration)")
		}
		if body["name"] == nil {
			body["name"] = args[0]
		}
		return sbSendShow(c, "PUT", c.p("pipelines", args[0]), nil, body, "Updated legacy pipeline "+args[0])
	}),
}

// --- streams ------------------------------------------------------------------

var basinStreamsCmd = &cobra.Command{
	Use:     "streams",
	Aliases: []string{"stream"},
	Short:   "Manage pipeline streams (ingest endpoints)",
}

var basinStreamsListCmd = sbListCmd("list", "List streams", "", cobra.NoArgs, basinListPath("streams"),
	"🌊 %d streams", "No streams found", []stCol{
		{Header: "NAME", Path: "name"},
		{Header: "ID", Path: "id"},
		{Header: "HTTP", Path: "http.enabled"},
		{Header: "WORKER BINDING", Path: "worker_binding.enabled"},
		{Header: "ENDPOINT", Path: "endpoint"},
		basinCreated,
	})

var basinStreamsGetCmd = sbGetCmd("get <stream>", "Show a stream", "", "🌊 Stream", cobra.ExactArgs(1), basinPath("stream", "streams"))

var basinStreamsDeleteCmd = sbDeleteCmd("delete <stream>", "Delete a stream", "", cobra.ExactArgs(1),
	func(a []string) string { return "stream " + a[0] }, basinPath("stream", "streams"))

func basinStreamFlags(cmd *cobra.Command, create bool) {
	cmd.Flags().Bool("http", false, "Enable the HTTP ingest endpoint (--http=false to disable)")
	cmd.Flags().Bool("http-auth", false, "Require authentication on the HTTP endpoint")
	cmd.Flags().StringSlice("cors-origins", nil, "Allowed CORS origins for the HTTP endpoint")
	cmd.Flags().Bool("worker-binding", false, "Enable the Worker binding (--worker-binding=false to disable)")
	if create {
		cmd.Flags().String("format", "", "Event format: json or parquet")
		cmd.Flags().String("schema-file", "", "JSON file with the stream schema ({\"fields\": [...]})")
	}
	stDataFlag(cmd)
}

func basinStreamBody(cmd *cobra.Command) (map[string]any, error) {
	body, err := stBody(cmd)
	if err != nil {
		return nil, err
	}
	stFlagBool(cmd, body, "http", "http.enabled")
	if cmd.Flags().Changed("http-auth") || cmd.Flags().Changed("cors-origins") {
		if stGet(body, "http.enabled") == nil {
			stSet(body, "http.enabled", true)
		}
		if stGet(body, "http.authentication") == nil {
			stSet(body, "http.authentication", false)
		}
	}
	stFlagBool(cmd, body, "http-auth", "http.authentication")
	stFlagList(cmd, body, "cors-origins", "http.cors.origins")
	if m, ok := body["http"].(map[string]any); ok && m["authentication"] == nil {
		m["authentication"] = false
	}
	stFlagBool(cmd, body, "worker-binding", "worker_binding.enabled")
	if cmd.Flags().Lookup("format") != nil {
		stFlagStr(cmd, body, "format", "format.type")
		if f, _ := cmd.Flags().GetString("schema-file"); f != "" {
			v, err := stReadInput("@" + f)
			if err != nil {
				return nil, err
			}
			var schema any
			if err := json.Unmarshal(v, &schema); err != nil {
				return nil, fmt.Errorf("--schema-file: %w", err)
			}
			body["schema"] = schema
		}
	}
	return body, nil
}

var basinStreamsCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a stream",
	Long: `Create a stream. Without a schema, events are unstructured JSON.

Examples:
  cfctl basin pipelines streams create clicks --http --worker-binding
  cfctl basin pipelines streams create clicks --http --http-auth --cors-origins https://example.com
  cfctl basin pipelines streams create clicks --schema-file schema.json`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := basinStreamBody(cmd)
		if err != nil {
			return err
		}
		body["name"] = args[0]
		return sbSendShow(c, "POST", c.p("pipelines/v1/streams"), nil, body, "Created stream "+args[0])
	}),
}

var basinStreamsUpdateCmd = &cobra.Command{
	Use:   "update <stream>",
	Short: "Update a stream's HTTP endpoint or Worker binding",
	Args:  cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := basinStreamBody(cmd)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to update: pass flags or --data")
		}
		p, _, err := basinPath("stream", "streams")(c, args)
		if err != nil {
			return err
		}
		return sbSendShow(c, "PATCH", p, nil, body, "Updated stream "+args[0])
	}),
}

// --- sinks --------------------------------------------------------------------

var basinSinksCmd = &cobra.Command{
	Use:     "sinks",
	Aliases: []string{"sink"},
	Short:   "Manage pipeline sinks (R2 or R2 Data Catalog destinations)",
}

var basinSinksListCmd = sbListCmd("list", "List sinks", "", cobra.NoArgs, basinListPath("sinks"),
	"🪣 %d sinks", "No sinks found", []stCol{
		{Header: "NAME", Path: "name"},
		{Header: "ID", Path: "id"},
		{Header: "TYPE", Path: "type"},
		{Header: "BUCKET", Path: "config.bucket"},
		{Header: "FORMAT", Path: "format.type"},
		basinCreated,
	})

var basinSinksGetCmd = sbGetCmd("get <sink>", "Show a sink", "", "🪣 Sink", cobra.ExactArgs(1), basinPath("sink", "sinks"))

var basinSinksDeleteCmd = sbDeleteCmd("delete <sink>", "Delete a sink", "", cobra.ExactArgs(1),
	func(a []string) string { return "sink " + a[0] }, basinPath("sink", "sinks"))

var basinSinksCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a sink",
	Long: `Create a sink that writes pipeline output to R2 (files) or to an R2 Data
Catalog (Iceberg) table.

Examples:
  cfctl basin pipelines sinks create clicks-r2 --type r2 --bucket logs --path clicks \
      --access-key-id "$R2_KEY" --secret-access-key "$R2_SECRET" --format parquet --compression zstd
  cfctl basin pipelines sinks create clicks-ice --type r2_data_catalog --bucket lake \
      --namespace default --table clicks --catalog-token "$TOKEN"`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		body["name"] = args[0]
		stFlagStr(cmd, body, "type", "type")
		if body["type"] == nil {
			body["type"] = "r2"
		}
		stFlagStr(cmd, body, "bucket", "config.bucket")
		stFlagStr(cmd, body, "path", "config.path")
		stFlagStr(cmd, body, "jurisdiction", "config.jurisdiction")
		stFlagStr(cmd, body, "access-key-id", "config.credentials.access_key_id")
		stFlagStr(cmd, body, "secret-access-key", "config.credentials.secret_access_key")
		stFlagStr(cmd, body, "namespace", "config.namespace")
		stFlagStr(cmd, body, "table", "config.table_name")
		stFlagStr(cmd, body, "catalog-token", "config.token")
		stFlagStr(cmd, body, "file-prefix", "config.file_naming.prefix")
		stFlagStr(cmd, body, "partitioning", "config.partitioning.time_pattern")
		stFlagInt(cmd, body, "roll-size", "config.rolling_policy.file_size_bytes")
		stFlagInt(cmd, body, "roll-interval", "config.rolling_policy.interval_seconds")
		stFlagStr(cmd, body, "format", "format.type")
		stFlagStr(cmd, body, "compression", "format.compression")
		if cfg, ok := body["config"].(map[string]any); ok && cfg["account_id"] == nil {
			cfg["account_id"] = c.acct
		}
		if stGet(body, "config.bucket") == nil {
			return fmt.Errorf("--bucket is required")
		}
		return sbSendShow(c, "POST", c.p("pipelines/v1/sinks"), nil, body, "Created sink "+args[0])
	}),
}

// --- catalog ------------------------------------------------------------------

// newCatalogCmd builds the Iceberg catalog command tree for an API base
// ("basin-catalog" or "r2-catalog").
func newCatalogCmd(use, apiBase, short string) *cobra.Command {
	prefix := "cfctl basin " + use
	if apiBase == "r2-catalog" {
		prefix = "cfctl r2 bucket " + use
	}
	root := &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + `

Examples:
  ` + prefix + ` list
  ` + prefix + ` enable my-bucket
  ` + prefix + ` credential set my-bucket --token "$R2_TOKEN"
  ` + prefix + ` compaction enable my-bucket --target-size 256
  ` + prefix + ` snapshot-expiration enable my-bucket --max-age 14d --min-snapshots 5
  ` + prefix + ` namespaces my-bucket
  ` + prefix + ` tables my-bucket default`,
	}
	bucketPath := func(segs ...string) sbPathFn {
		return func(c *stClient, args []string) (string, url.Values, error) {
			return c.p(apiBase, append([]string{args[0]}, segs...)...), nil, nil
		}
	}
	list := sbListCmd("list", "List buckets enabled as catalogs", "", cobra.NoArgs, sbFixed(apiBase),
		"🧊 %d catalogs", "No catalogs found", []stCol{
			{Header: "BUCKET", Path: "", Fmt: sbFirst("bucket", "name")},
			{Header: "ID", Path: "id"},
			{Header: "STATUS", Path: "status"},
		})
	get := sbGetCmd("get <bucket>", "Show a catalog's status, maintenance, and credential status", "", "🧊 Catalog", cobra.ExactArgs(1), bucketPath())
	post := func(use, short, seg, msg string, destructive bool) *cobra.Command {
		cmd := &cobra.Command{
			Use:   use + " <bucket>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
				var q url.Values
				if f := cmd.Flags().Lookup("force"); f != nil && f.Changed {
					q = url.Values{"force": {"true"}}
				}
				if destructive {
					if err := confirm(cmd, fmt.Sprintf("%s the catalog on %s", use, args[0])); err != nil {
						return err
					}
				}
				return sbSend(c, "POST", c.p(apiBase, args[0], seg), q, nil, fmt.Sprintf(msg, args[0]))
			}),
		}
		if destructive {
			stYes(cmd)
		}
		return cmd
	}
	enable := post("enable", "Enable a bucket as an Iceberg catalog", "enable", "Enabled the catalog on %s", false)
	disable := post("disable", "Disable the catalog on a bucket (data is kept)", "disable", "Disabled the catalog on %s", true)
	del := post("delete", "Remove the catalog's metadata (bucket objects are kept)", "delete", "Deleted the catalog metadata of %s", true)
	del.Flags().Bool("force", false, "Also remove namespaces, tables, views, and maintenance configs")

	cred := &cobra.Command{Use: "credential", Short: "Manage the API token the catalog uses for maintenance"}
	credSet := &cobra.Command{
		Use:   "set <bucket>",
		Short: "Store the R2 API token the catalog uses (from --token or stdin; never printed)",
		Args:  cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			tok, _ := cmd.Flags().GetString("token")
			if tok == "" {
				b, err := stReadInput("-")
				if err != nil {
					return err
				}
				tok = strings.TrimSpace(string(b))
			}
			if tok == "" {
				return fmt.Errorf("pass --token or pipe the token on stdin")
			}
			return sbSend(c, "POST", c.p(apiBase, args[0], "credential"), nil, map[string]any{"token": tok}, "Stored the catalog credential for "+args[0])
		}),
	}
	credSet.Flags().String("token", "", "Cloudflare API token with R2 read/write access")
	credStatus := sbGetCmd("status <bucket>", "Show whether a credential is stored", "", "🔑 Catalog credential", cobra.ExactArgs(1), bucketPath("credential", "status"))
	cred.AddCommand(credSet, credStatus)

	maint := &cobra.Command{Use: "maintenance", Short: "Show or change catalog-wide maintenance (compaction, snapshot expiration)"}
	maintGet := sbGetCmd("get <bucket>", "Show the maintenance configuration", "", "🛠  Maintenance", cobra.ExactArgs(1), bucketPath("maintenance-configs"))
	maintSet := &cobra.Command{
		Use:   "set <bucket>",
		Short: "Change the maintenance configuration",
		Args:  cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			body, err := catalogMaintBody(cmd)
			if err != nil {
				return err
			}
			return sbSendShow(c, "POST", c.p(apiBase, args[0], "maintenance-configs"), nil, body, "Updated maintenance for "+args[0])
		}),
	}
	catalogMaintFlags(maintSet)
	maint.AddCommand(maintGet, maintSet)

	// wrangler-style shortcuts: compaction enable|disable, snapshot-expiration enable|disable.
	toggle := func(group, key string) *cobra.Command {
		g := &cobra.Command{Use: group, Short: "Enable or disable automatic " + strings.ReplaceAll(group, "-", " ")}
		for _, state := range []string{"enable", "disable"} {
			state := state
			cmd := &cobra.Command{
				Use:   state + " <bucket>",
				Short: strings.ToUpper(state[:1]) + state[1:] + " " + strings.ReplaceAll(group, "-", " "),
				Args:  cobra.ExactArgs(1),
				RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
					cfg := map[string]any{"state": state + "d"}
					if key == "compaction" {
						if v, _ := cmd.Flags().GetInt("target-size"); v > 0 {
							cfg["target_size_mb"] = strconv.Itoa(v)
						}
					} else {
						if v, _ := cmd.Flags().GetString("max-age"); v != "" {
							cfg["max_snapshot_age"] = v
						}
						if cmd.Flags().Changed("min-snapshots") {
							v, _ := cmd.Flags().GetInt("min-snapshots")
							cfg["min_snapshots_to_keep"] = v
						}
					}
					return sbSend(c, "POST", c.p(apiBase, args[0], "maintenance-configs"), nil, map[string]any{key: cfg},
						fmt.Sprintf("%sd %s on %s", strings.ToUpper(state[:1])+state[1:], strings.ReplaceAll(group, "-", " "), args[0]))
				}),
			}
			if state == "enable" {
				if key == "compaction" {
					cmd.Flags().Int("target-size", 0, "Target file size in MB: 64, 128, 256, or 512")
				} else {
					cmd.Flags().String("max-age", "", "Maximum snapshot age (e.g. 7d, 24h)")
					cmd.Flags().Int("min-snapshots", 0, "Minimum snapshots to keep")
				}
			}
			g.AddCommand(cmd)
		}
		return g
	}

	namespaces := &cobra.Command{
		Use:   "namespaces <bucket>",
		Short: "List catalog namespaces",
		Args:  cobra.ExactArgs(1),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			q := catalogPageQuery(cmd)
			if p, _ := cmd.Flags().GetString("parent"); p != "" {
				q.Set("parent", p)
			}
			raw, err := c.get(c.p(apiBase, args[0], "namespaces"), q)
			if err != nil {
				return err
			}
			return stEmit(raw, func() error { return stDetail("🧊 Namespaces in "+args[0], raw) })
		}),
	}
	namespaces.Flags().String("parent", "", "Only direct children of this namespace")
	catalogPageFlags(namespaces)
	tables := &cobra.Command{
		Use:   "tables <bucket> <namespace>",
		Short: "List tables in a namespace",
		Args:  cobra.ExactArgs(2),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			raw, err := c.get(c.p(apiBase, args[0], "namespaces", args[1], "tables"), catalogPageQuery(cmd))
			if err != nil {
				return err
			}
			return stEmit(raw, func() error { return stDetail("🧊 Tables in "+args[1], raw) })
		}),
	}
	catalogPageFlags(tables)
	tablePath := func(segs ...string) sbPathFn {
		return func(c *stClient, args []string) (string, url.Values, error) {
			return c.p(apiBase, append([]string{args[0], "namespaces", args[1], "tables", args[2]}, segs...)...), nil, nil
		}
	}
	table := &cobra.Command{Use: "table", Short: "Show a table and manage its maintenance"}
	tableGet := sbGetCmd("get <bucket> <namespace> <table>", "Show a table", "", "🧊 Table", cobra.ExactArgs(3), tablePath())
	tableMaintGet := sbGetCmd("maintenance <bucket> <namespace> <table>", "Show a table's maintenance configuration", "", "🛠  Table maintenance", cobra.ExactArgs(3), tablePath("maintenance-configs"))
	tableMaintSet := &cobra.Command{
		Use:   "set-maintenance <bucket> <namespace> <table>",
		Short: "Change a table's maintenance configuration",
		Args:  cobra.ExactArgs(3),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			body, err := catalogMaintBody(cmd)
			if err != nil {
				return err
			}
			p, _, _ := tablePath("maintenance-configs")(c, args)
			return sbSendShow(c, "POST", p, nil, body, "Updated maintenance for "+args[2])
		}),
	}
	catalogMaintFlags(tableMaintSet)
	tableRun := &cobra.Command{
		Use:   "run-maintenance <bucket> <namespace> <table> <compaction|snapshot_expiration>",
		Short: "Queue a maintenance job for a table now",
		Args:  cobra.ExactArgs(4),
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			p, _, _ := tablePath("maintenance-configs", args[3], "queue")(c, args)
			return sbSend(c, "POST", p, nil, map[string]any{}, fmt.Sprintf("Queued %s for %s", args[3], args[2]))
		}),
	}
	tableRuns := sbGetCmd("maintenance-runs <bucket> <namespace> <table>", "List a table's maintenance runs", "", "🛠  Maintenance runs", cobra.ExactArgs(3), tablePath("maintenance-runs"))
	table.AddCommand(tableGet, tableMaintGet, tableMaintSet, tableRun, tableRuns)

	root.AddCommand(list, get, enable, disable, del, cred, maint, toggle("compaction", "compaction"),
		toggle("snapshot-expiration", "snapshot_expiration"), namespaces, tables, table)
	return root
}

func catalogPageFlags(cmd *cobra.Command) {
	cmd.Flags().Int("page-size", 0, "Results per page (default 100)")
	cmd.Flags().String("page-token", "", "Continue from this page token")
}

func catalogPageQuery(cmd *cobra.Command) url.Values {
	q := url.Values{}
	if n, _ := cmd.Flags().GetInt("page-size"); n > 0 {
		q.Set("page_size", strconv.Itoa(n))
	}
	if t, _ := cmd.Flags().GetString("page-token"); t != "" {
		q.Set("page_token", t)
	}
	return q
}

func catalogMaintFlags(cmd *cobra.Command) {
	cmd.Flags().String("compaction", "", "Compaction state: enabled or disabled")
	cmd.Flags().Int("target-size", 0, "Compaction target file size in MB: 64, 128, 256, or 512")
	cmd.Flags().String("snapshot-expiration", "", "Snapshot expiration state: enabled or disabled")
	cmd.Flags().String("max-snapshot-age", "", "Maximum snapshot age (e.g. 14d)")
	cmd.Flags().Int("min-snapshots", 0, "Minimum snapshots to keep")
	stDataFlag(cmd)
}

func catalogMaintBody(cmd *cobra.Command) (map[string]any, error) {
	body, err := stBody(cmd)
	if err != nil {
		return nil, err
	}
	stFlagStr(cmd, body, "compaction", "compaction.state")
	if v, _ := cmd.Flags().GetInt("target-size"); v > 0 {
		stSet(body, "compaction.target_size_mb", strconv.Itoa(v))
	}
	stFlagStr(cmd, body, "snapshot-expiration", "snapshot_expiration.state")
	stFlagStr(cmd, body, "max-snapshot-age", "snapshot_expiration.max_snapshot_age")
	stFlagInt(cmd, body, "min-snapshots", "snapshot_expiration.min_snapshots_to_keep")
	if len(body) == 0 {
		return nil, fmt.Errorf("nothing to change: pass --compaction, --snapshot-expiration, related flags, or --data")
	}
	return body, nil
}

func init() {
	basinSQLFlags(basinPipelinesCreateCmd)
	basinSQLFlags(basinPipelinesValidateCmd)
	stDataFlag(basinPipelinesUpdateCmd)
	basinPipelinesUpdateCmd.Flags().Bool("legacy", false, "Update a legacy (pre-v1) pipeline by name")

	basinStreamFlags(basinStreamsCreateCmd, true)
	basinStreamFlags(basinStreamsUpdateCmd, false)
	basinStreamsCmd.AddCommand(basinStreamsListCmd, basinStreamsGetCmd, basinStreamsCreateCmd, basinStreamsUpdateCmd, basinStreamsDeleteCmd)

	f := basinSinksCreateCmd.Flags()
	f.String("type", "r2", "Sink type: r2, r2_data_catalog, or basin_catalog")
	f.String("bucket", "", "R2 bucket")
	f.String("path", "", "Subpath in the bucket (r2)")
	f.String("jurisdiction", "", "Bucket jurisdiction (r2)")
	f.String("access-key-id", "", "R2 access key ID (r2)")
	f.String("secret-access-key", "", "R2 secret access key (r2; write-only)")
	f.String("namespace", "", "Catalog namespace (r2_data_catalog)")
	f.String("table", "", "Catalog table name (r2_data_catalog)")
	f.String("catalog-token", "", "API token for the catalog (r2_data_catalog; write-only)")
	f.String("file-prefix", "", "File name prefix (r2)")
	f.String("partitioning", "", "Time partition pattern, e.g. year=%Y/month=%m/day=%d (r2)")
	f.Int("roll-size", 0, "Roll files after this many bytes")
	f.Int("roll-interval", 0, "Roll files after this many seconds")
	f.String("format", "", "Output format: json or parquet")
	f.String("compression", "", "Compression (json: uncompressed, gzip; parquet: uncompressed, snappy, gzip, zstd, lz4)")
	stDataFlag(basinSinksCreateCmd)
	basinSinksCmd.AddCommand(basinSinksListCmd, basinSinksGetCmd, basinSinksCreateCmd, basinSinksDeleteCmd)

	basinPipelinesCmd.AddCommand(basinPipelinesListCmd, basinPipelinesGetCmd, basinPipelinesCreateCmd, basinPipelinesUpdateCmd,
		basinPipelinesDeleteCmd, basinPipelinesValidateCmd, basinStreamsCmd, basinSinksCmd)
	basinCmd.AddCommand(basinPipelinesCmd, newCatalogCmd("catalog", "basin-catalog", "Manage the Basin Catalog (Apache Iceberg REST catalog on an R2 bucket)"))
	rootCmd.AddCommand(basinCmd)
}
