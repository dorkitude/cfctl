package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// D1 migrations, compatible with wrangler: numbered .sql files in a
// migrations directory (default ./migrations), applied in order and recorded
// in a d1_migrations table (id, name, applied_at).

var d1MigrationsCmd = &cobra.Command{
	Use:   "migrations",
	Short: "Create, list, and apply SQL migrations (wrangler-compatible)",
	Long: `Manage D1 migrations the way wrangler does: numbered files such as
migrations/0001_create_users.sql, applied in order and recorded in the
d1_migrations table of the database. Databases migrated by wrangler and by
cfctl are interchangeable.

Before applying, cfctl prints the database's current Time Travel bookmark so
a bad migration can be rolled back with 'cfctl d1 time-travel restore'.

Examples:
  cfctl d1 migrations create my-db create_users
  cfctl d1 migrations list my-db
  cfctl d1 migrations apply my-db
  cfctl d1 migrations apply my-db --migrations-dir db/migrations --yes`,
}

var d1MigrationName = regexp.MustCompile(`^(\d+)_?.*\.sql$`)

// d1MigrationFiles returns the .sql files in dir, sorted by number then name.
func d1MigrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	num := func(n string) int {
		if m := d1MigrationName.FindStringSubmatch(n); m != nil {
			v, _ := strconv.Atoi(m[1])
			return v
		}
		return 1 << 30
	}
	sort.Slice(names, func(i, j int) bool {
		if ni, nj := num(names[i]), num(names[j]); ni != nj {
			return ni < nj
		}
		return names[i] < names[j]
	})
	return names, nil
}

func d1MigrationsFlags(cmd *cobra.Command) (dir, table string) {
	dir, _ = cmd.Flags().GetString("migrations-dir")
	table, _ = cmd.Flags().GetString("table")
	return dir, table
}

var d1TableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// d1Applied ensures the migrations table exists and returns applied names.
func d1Applied(c *stClient, id, table string) (map[string]bool, error) {
	if !d1TableName.MatchString(table) {
		return nil, fmt.Errorf("invalid --table %q", table)
	}
	create := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s(
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT UNIQUE,
    applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
);`, table)
	if _, err := c.result(stReq{Method: "POST", Path: c.p("d1/database", id, "query"), Body: map[string]any{"sql": create}}); err != nil {
		return nil, fmt.Errorf("creating %s: %w", table, err)
	}
	raw, err := c.result(stReq{Method: "POST", Path: c.p("d1/database", id, "query"), Body: map[string]any{"sql": "SELECT name FROM " + table + " ORDER BY id"}})
	if err != nil {
		return nil, err
	}
	var res []struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("reading %s: %w", table, err)
	}
	applied := map[string]bool{}
	for _, r := range res {
		for _, row := range r.Results {
			applied[row.Name] = true
		}
	}
	return applied, nil
}

// d1Pending returns migration files not yet applied.
func d1Pending(c *stClient, id, dir, table string) ([]string, error) {
	files, err := d1MigrationFiles(dir)
	if err != nil {
		return nil, err
	}
	applied, err := d1Applied(c, id, table)
	if err != nil {
		return nil, err
	}
	var pending []string
	for _, f := range files {
		if !applied[f] {
			pending = append(pending, f)
		}
	}
	return pending, nil
}

var d1MigrationsCreateCmd = &cobra.Command{
	Use:   "create <database> <message>",
	Short: "Create the next numbered migration file",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := d1MigrationsFlags(cmd)
		files, err := d1MigrationFiles(dir)
		if err != nil {
			return err
		}
		next := 1
		for _, f := range files {
			if m := d1MigrationName.FindStringSubmatch(f); m != nil {
				if n, _ := strconv.Atoi(m[1]); n >= next {
					next = n + 1
				}
			}
		}
		slug := strings.Trim(regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(args[1], "_"), "_")
		if slug == "" {
			return fmt.Errorf("the migration message must contain letters or digits")
		}
		name := fmt.Sprintf("%04d_%s.sql", next, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(dir, name)
		content := fmt.Sprintf("-- Migration number: %04d \t %s\n", next, time.Now().UTC().Format(time.RFC3339Nano))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		return stEmitValue(map[string]any{"database": args[0], "file": path, "number": next}, func() error {
			fmt.Println(ui.Success("Created " + path))
			return nil
		})
	},
}

var d1MigrationsListCmd = &cobra.Command{
	Use:   "list <database>",
	Short: "List migrations not yet applied",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, table := d1MigrationsFlags(cmd)
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		pending, err := d1Pending(c, id, dir, table)
		if err != nil {
			return err
		}
		if pending == nil {
			pending = []string{}
		}
		return stEmitValue(pending, func() error {
			if len(pending) == 0 {
				fmt.Println(ui.Success("No migrations to apply"))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("📜 %d migrations to apply to %s", len(pending), args[0])))
			for _, p := range pending {
				fmt.Println("  " + p)
			}
			return nil
		})
	},
}

var d1MigrationsApplyCmd = &cobra.Command{
	Use:   "apply <database>",
	Short: "Apply pending migrations in order",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, table := d1MigrationsFlags(cmd)
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		id, err := d1ID(c, args[0])
		if err != nil {
			return err
		}
		pending, err := d1Pending(c, id, dir, table)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return stEmitValue(map[string]any{"applied": []string{}}, func() error {
				fmt.Println(ui.Success("No migrations to apply"))
				return nil
			})
		}
		if !jsonOutput {
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("📜 %d migrations to apply to %s", len(pending), args[0])))
			for _, p := range pending {
				fmt.Println("  " + p)
			}
		}
		if err := confirm(cmd, fmt.Sprintf("apply %d migrations to %s", len(pending), args[0])); err != nil {
			return err
		}
		var bookmark string
		if raw, err := c.get(c.p("d1/database", id, "time_travel", "bookmark"), nil); err == nil {
			var b struct {
				Bookmark string `json:"bookmark"`
			}
			_ = json.Unmarshal(raw, &b)
			bookmark = b.Bookmark
			if bookmark != "" && !jsonOutput {
				fmt.Println(ui.Info("current bookmark " + bookmark + " (roll back with: cfctl d1 time-travel restore " + args[0] + " --bookmark " + bookmark + ")"))
			}
		}
		var applied []string
		for _, name := range pending {
			sqlBytes, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return err
			}
			sql := strings.TrimRight(string(sqlBytes), " \t\r\n")
			if !d1Terminated(sql) {
				sql += "\n;"
			}
			sql += fmt.Sprintf("\nINSERT INTO %s (name) VALUES ('%s');", table, strings.ReplaceAll(name, "'", "''"))
			if _, err := c.result(stReq{Method: "POST", Path: c.p("d1/database", id, "query"), Body: map[string]any{"sql": sql}}); err != nil {
				if !jsonOutput {
					fmt.Println(ui.Err(name + ": " + err.Error()))
				}
				return fmt.Errorf("migration %s failed (%d of %d applied before it): %w", name, len(applied), len(pending), err)
			}
			applied = append(applied, name)
			if !jsonOutput {
				fmt.Println(ui.Success(name))
			}
		}
		return stEmitValue(map[string]any{"applied": applied, "bookmark_before": bookmark}, func() error {
			fmt.Println(ui.Success(fmt.Sprintf("Applied %d migrations to %s", len(applied), args[0])))
			return nil
		})
	},
}

// d1Terminated reports whether the last line of code (ignoring blank lines
// and -- comments) ends with a semicolon, or there is no code at all.
func d1Terminated(sql string) bool {
	lines := strings.Split(sql, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.HasPrefix(l, "--") {
			continue
		}
		if j := strings.Index(l, " --"); j >= 0 {
			l = strings.TrimSpace(l[:j])
		}
		return strings.HasSuffix(l, ";")
	}
	return true
}

func init() {
	for _, c := range []*cobra.Command{d1MigrationsCreateCmd, d1MigrationsListCmd, d1MigrationsApplyCmd} {
		c.Flags().String("migrations-dir", "migrations", "Directory of migration .sql files")
		c.Flags().String("table", "d1_migrations", "Table that records applied migrations")
	}
	stYes(d1MigrationsApplyCmd)
	d1MigrationsCmd.AddCommand(d1MigrationsCreateCmd, d1MigrationsListCmd, d1MigrationsApplyCmd)
	d1Cmd.AddCommand(d1MigrationsCmd)
}
