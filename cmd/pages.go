package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const pagesBase = "/accounts/{account_id}/pages/projects"

var pagesProjectCols = []platCol{
	{H: "name", Path: "name"},
	{H: "subdomain", Path: "subdomain"},
	{H: "domains", Path: "domains", W: 50},
	{H: "production branch", Path: "production_branch"},
	{H: "source", Path: "source.type"},
	{H: "last deploy", Path: "latest_deployment.created_on"},
}

var pagesDeploymentCols = []platCol{
	{H: "id", Path: "id"},
	{H: "env", Path: "environment"},
	{H: "branch", Path: "deployment_trigger.metadata.branch"},
	{H: "commit", Path: "deployment_trigger.metadata.commit_hash", W: 8},
	{H: "status", Path: "latest_stage.status"},
	{H: "stage", Path: "latest_stage.name"},
	{H: "url", Path: "url"},
	{H: "created", Path: "created_on"},
}

var pagesDeploymentFields = []platCol{
	{H: "ID", Path: "id"},
	{H: "Project", Path: "project_name"},
	{H: "Environment", Path: "environment"},
	{H: "URL", Path: "url"},
	{H: "Aliases", Path: "aliases"},
	{H: "Branch", Path: "deployment_trigger.metadata.branch"},
	{H: "Commit", Path: "deployment_trigger.metadata.commit_hash"},
	{H: "Message", Path: "deployment_trigger.metadata.commit_message"},
	{H: "Trigger", Path: "deployment_trigger.type"},
	{H: "Stage", Path: "latest_stage.name"},
	{H: "Status", Path: "latest_stage.status"},
	{H: "Source", Path: "source.type"},
	{H: "Created", Path: "created_on"},
	{H: "Modified", Path: "modified_on"},
}

func pagesEnvQuery(c *cobra.Command, _ []string, q url.Values) error {
	if env, _ := c.Flags().GetString("env"); env != "" {
		if env != "production" && env != "preview" {
			return fmt.Errorf("--env must be production or preview")
		}
		q.Set("env", env)
	}
	return nil
}

func pagesProjectBody(c *cobra.Command, args []string) (any, error) {
	b := map[string]any{}
	if len(args) > 0 && c.Name() == "create" {
		b["name"] = args[0]
	}
	platSetStr(c, b, "production-branch", "production_branch")
	dc := map[string]any{}
	for _, env := range []string{"production", "preview"} {
		e := map[string]any{}
		platSetStr(c, e, "compatibility-date", "compatibility_date")
		platSetStrs(c, e, "compatibility-flags", "compatibility_flags")
		if len(e) > 0 {
			dc[env] = e
		}
	}
	if len(dc) > 0 {
		b["deployment_configs"] = dc
	}
	if bc := map[string]any{}; true {
		platSetStr(c, bc, "build-command", "build_command")
		platSetStr(c, bc, "destination-dir", "destination_dir")
		platSetStr(c, bc, "root-dir", "root_dir")
		if len(bc) > 0 {
			b["build_config"] = bc
		}
	}
	return b, nil
}

func pagesProjectFlags(c *cobra.Command, defBranch string) {
	c.Flags().String("production-branch", defBranch, "Branch whose deployments go to production")
	c.Flags().String("compatibility-date", "", "Workers compatibility date for Functions (both environments)")
	c.Flags().StringSlice("compatibility-flags", nil, "Workers compatibility flags for Functions (both environments)")
	c.Flags().String("build-command", "", "Build command (Git-connected projects)")
	c.Flags().String("destination-dir", "", "Build output directory (Git-connected projects)")
	c.Flags().String("root-dir", "", "Root directory (Git-connected projects)")
}

func init() {
	project := platGroup("project", "Manage Pages projects", "", []string{"projects"},
		platSpecs(
			platSpec{Use: "list", Short: "List Pages projects", Aliases: []string{"ls"}, Path: pagesBase, List: true,
				Cols: pagesProjectCols, Title: "📄 %d Pages projects", Product: "Pages"},
			platSpec{Use: "get <project>", Short: "Show a Pages project", Aliases: []string{"info", "show"}, Path: pagesBase + "/{project_name}",
				Product: "Pages", Title: "📄 Pages project %s", Print: pagesPrintProject},
			platSpec{Use: "create <project>", Short: "Create a Direct Upload Pages project", Method: "POST", Path: pagesBase, ExtraArgs: 1,
				Long: `Create a Pages project for Direct Upload (cfctl pages deploy / wrangler pages deploy).
Git-connected projects need the dashboard (OAuth to GitHub/GitLab); pass --data with a
"source" block if you already have the installation IDs.`,
				Example: "  cfctl pages project create my-site --production-branch main",
				Flags:   func(c *cobra.Command) { pagesProjectFlags(c, "main") },
				Body: func(c *cobra.Command, args []string) (any, error) {
					b, _ := pagesProjectBody(c, args)
					m := b.(map[string]any)
					m["name"] = args[0]
					if _, ok := m["production_branch"]; !ok {
						pb, _ := c.Flags().GetString("production-branch")
						m["production_branch"] = pb
					}
					return m, nil
				},
				Data: true, Product: "Pages", Done: "Created Pages project %s"},
			platSpec{Use: "edit <project>", Short: "Update a Pages project's settings", Aliases: []string{"update"}, Method: "PATCH", Path: pagesBase + "/{project_name}",
				Flags: func(c *cobra.Command) { pagesProjectFlags(c, "") }, Body: pagesProjectBody, Data: true, Product: "Pages", Done: "Updated Pages project %s"},
			platSpec{Use: "delete <project>", Short: "Delete a Pages project", Aliases: []string{"rm"}, Method: "DELETE", Path: pagesBase + "/{project_name}",
				Confirm: "delete Pages project %s and all its deployments", Product: "Pages", Done: "Deleted Pages project %s"},
			platSpec{Use: "purge-build-cache <project>", Short: "Purge a project's build cache", Method: "POST", Path: pagesBase + "/{project_name}/purge_build_cache",
				Product: "Pages", Done: "Purged the build cache for %s"},
		)...)

	deployment := platGroup("deployment", "Manage Pages deployments", "", []string{"deployments"},
		platSpecs(
			platSpec{Use: "list <project>", Short: "List a project's deployments", Aliases: []string{"ls"}, Path: pagesBase + "/{project_name}/deployments", List: true,
				Flags: func(c *cobra.Command) { c.Flags().String("env", "", "Only production or preview deployments") },
				Query: pagesEnvQuery, Cols: pagesDeploymentCols, Title: "🚀 %d deployments", Product: "Pages"},
			platSpec{Use: "get <project> <deployment-id>", Short: "Show a deployment", Aliases: []string{"info", "show"}, Path: pagesBase + "/{project_name}/deployments/{deployment_id}",
				Fields: pagesDeploymentFields, Title: "🚀 Deployment %s", Product: "Pages"},
			platSpec{Use: "delete <project> <deployment-id>", Short: "Delete a deployment", Aliases: []string{"rm"}, Method: "DELETE", Path: pagesBase + "/{project_name}/deployments/{deployment_id}",
				Flags: func(c *cobra.Command) {
					c.Flags().Bool("force", false, "Also delete a deployment that has aliases (e.g. the latest for a branch)")
				},
				Query: func(c *cobra.Command, _ []string, q url.Values) error {
					if f, _ := c.Flags().GetBool("force"); f {
						q.Set("force", "true")
					}
					return nil
				},
				Confirm: "delete Pages deployment %s", Product: "Pages", Done: "Deleted deployment %s"},
			platSpec{Use: "retry <project> <deployment-id>", Short: "Retry a deployment (rebuild from the same source)", Method: "POST", Path: pagesBase + "/{project_name}/deployments/{deployment_id}/retry",
				Fields: pagesDeploymentFields, Title: "🚀 Retry started", Product: "Pages"},
			platSpec{Use: "rollback <project> <deployment-id>", Short: "Roll production back to a previous deployment", Method: "POST", Path: pagesBase + "/{project_name}/deployments/{deployment_id}/rollback",
				Confirm: "roll production of %s back", Fields: pagesDeploymentFields, Title: "🚀 Rolled back", Product: "Pages"},
			platSpec{Use: "logs <project> <deployment-id>", Short: "Show a deployment's build logs", Path: pagesBase + "/{project_name}/deployments/{deployment_id}/history/logs",
				Query:   func(c *cobra.Command, _ []string, q url.Values) error { q.Set("size", "10000000"); return nil },
				Product: "Pages", Print: pagesPrintLogs},
			pagesTailSpec(),
		)...)

	domains := platGroup("domains", "Manage a Pages project's custom domains", "", []string{"domain"},
		platSpecs(
			platSpec{Use: "list <project>", Short: "List custom domains", Aliases: []string{"ls"}, Path: pagesBase + "/{project_name}/domains", List: true,
				Cols:  []platCol{{H: "name", Path: "name"}, {H: "status", Path: "status"}, {H: "certificate", Path: "certificate_authority"}, {H: "validation", Path: "validation_data.status"}, {H: "verification", Path: "verification_data.status"}, {H: "created", Path: "created_on"}},
				Title: "🌐 %d custom domains", Product: "Pages"},
			platSpec{Use: "get <project> <domain>", Short: "Show a custom domain", Path: pagesBase + "/{project_name}/domains/{domain_name}", Product: "Pages", Title: "🌐 %s"},
			platSpec{Use: "add <project> <domain>", Short: "Add a custom domain", Aliases: []string{"create"}, Method: "POST", Path: pagesBase + "/{project_name}/domains", ExtraArgs: 1,
				Body:    func(c *cobra.Command, args []string) (any, error) { return map[string]any{"name": args[1]}, nil },
				Product: "Pages", Title: "🌐 Added %s"},
			platSpec{Use: "retry <project> <domain>", Short: "Retry validation of a custom domain", Method: "PATCH", Path: pagesBase + "/{project_name}/domains/{domain_name}",
				Product: "Pages", Done: "Retrying validation for %s"},
			platSpec{Use: "delete <project> <domain>", Short: "Remove a custom domain", Aliases: []string{"rm"}, Method: "DELETE", Path: pagesBase + "/{project_name}/domains/{domain_name}",
				Confirm: "remove custom domain %s", Product: "Pages", Done: "Removed %s"},
		)...)

	secret := platGroup("secret", "Manage a Pages project's secrets (environment variables of type secret_text)", "", []string{"secrets"},
		pagesSecretList(), pagesSecretPut(), pagesSecretBulk(), pagesSecretDelete())

	download := platGroup("download", "Download settings from a Pages project", "", nil, pagesDownloadConfig())

	functions := platGroup("functions", "Pages Functions helpers", "", nil, platLocalOnly("build", "Compile a folder of Pages Functions into a single Worker",
		"wrangler pages functions build", "it bundles TypeScript/JavaScript with esbuild and Pages' routing shim"))

	pagesCmd := platGroup("pages", "Manage Cloudflare Pages: projects, deployments, deploys, domains, secrets",
		`Manage Cloudflare Pages.

  cfctl pages project list|get|create|edit|delete       projects
  cfctl pages deploy <dir> --project-name <name>        Direct Upload deploy
  cfctl pages deployment list|get|logs|tail|retry|rollback|delete
  cfctl pages domains list|add|retry|delete             custom domains
  cfctl pages secret list|put|bulk|delete               secrets
  cfctl pages download config <project>                 project config as wrangler.jsonc

Generated equivalents: 'cfctl api pages-project ...', 'pages-deployment', 'pages-domains'.`, nil,
		project, deployment, domains, secret, download, functions, pagesDeployCmd(),
		platLocalOnly("dev", "Develop a Pages application locally", "wrangler pages dev", "it runs the local workerd runtime"))
	rootCmd.AddCommand(pagesCmd)
}

func pagesPrintProject(_ *cobra.Command, args []string, raw json.RawMessage) error {
	var m map[string]any
	if err := platDecode(raw, &m); err != nil {
		return err
	}
	platDetail("📄 Pages project "+args[0], m, []platCol{
		{H: "Name", Path: "name"}, {H: "ID", Path: "id"}, {H: "Subdomain", Path: "subdomain"},
		{H: "Domains", Path: "domains"}, {H: "Prod branch", Path: "production_branch"},
		{H: "Source", Path: "source.type"}, {H: "Repo", Path: "source.config.repo_name"},
		{H: "Build cmd", Path: "build_config.build_command"}, {H: "Output dir", Path: "build_config.destination_dir"},
		{H: "Created", Path: "created_on"},
		{H: "Latest", Path: "latest_deployment.url"}, {H: "Latest ID", Path: "latest_deployment.id"},
		{H: "Canonical", Path: "canonical_deployment.url"},
	})
	for _, env := range []string{"production", "preview"} {
		cfg, _ := platGet(m, "deployment_configs."+env).(map[string]any)
		if cfg == nil {
			continue
		}
		fmt.Println()
		fmt.Println("  " + ui.AccentStyle.Render(env))
		fmt.Printf("    compatibility: %s %s\n", platStr(cfg["compatibility_date"]), platStr(cfg["compatibility_flags"]))
		if vars, ok := cfg["env_vars"].(map[string]any); ok && len(vars) > 0 {
			names := make([]string, 0, len(vars))
			for k := range vars {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, k := range names {
				typ := platStr(platGet(vars[k], "type"))
				val := platStr(platGet(vars[k], "value"))
				if typ == "secret_text" {
					val = "(secret)"
				}
				fmt.Printf("    %s = %s\n", k, platTrunc(val, 60))
			}
		}
		var bindings []string
		for k, v := range cfg {
			if vm, ok := v.(map[string]any); ok && k != "env_vars" && len(vm) > 0 {
				keys := make([]string, 0, len(vm))
				for b := range vm {
					keys = append(keys, b)
				}
				sort.Strings(keys)
				bindings = append(bindings, k+": "+strings.Join(keys, ", "))
			}
		}
		sort.Strings(bindings)
		for _, b := range bindings {
			fmt.Println("    " + b)
		}
	}
	return nil
}

func pagesPrintLogs(_ *cobra.Command, _ []string, raw json.RawMessage) error {
	var l struct {
		Data []struct {
			Line string `json:"line"`
			TS   string `json:"ts"`
		} `json:"data"`
	}
	if err := platDecode(raw, &l); err != nil {
		return err
	}
	if len(l.Data) == 0 {
		fmt.Println(ui.Warn("No build logs (Direct Upload deployments have none)"))
	}
	for _, d := range l.Data {
		fmt.Printf("%s %s\n", ui.SubtleStyle.Render(platStr(d.TS)), d.Line)
	}
	return nil
}

// Secrets ---------------------------------------------------------------

func pagesSecretEnvFlag(c *cobra.Command) {
	c.Flags().String("env", "production", "Environment: production or preview")
}

func pagesSecretEnv(c *cobra.Command) (string, error) {
	env, _ := c.Flags().GetString("env")
	if env != "production" && env != "preview" {
		return "", fmt.Errorf("--env must be production or preview")
	}
	return env, nil
}

func pagesSecretList() *cobra.Command {
	return platCommand(platSpec{
		Use: "list <project>", Short: "List secret names for a Pages project", Aliases: []string{"ls"},
		Path: pagesBase + "/{project_name}", Flags: pagesSecretEnvFlag, Product: "Pages",
		Run: func(c *cobra.Command, args []string) error {
			env, err := pagesSecretEnv(c)
			if err != nil {
				return err
			}
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			path, _, err := platFill(ctx, s, pagesBase+"/{project_name}", args)
			if err != nil {
				return err
			}
			raw, err := platDo(ctx, s, "GET", path, nil, nil)
			if err != nil {
				return platErr("Pages", err)
			}
			var m map[string]any
			if err := platDecode(raw, &m); err != nil {
				return err
			}
			vars, _ := platGet(m, "deployment_configs."+env+".env_vars").(map[string]any)
			var names []string
			for k, v := range vars {
				if platStr(platGet(v, "type")) == "secret_text" {
					names = append(names, k)
				}
			}
			sort.Strings(names)
			if jsonOutput {
				out := make([]map[string]string, 0, len(names))
				for _, n := range names {
					out = append(out, map[string]string{"name": n, "type": "secret_text"})
				}
				return printJSONValue(out)
			}
			if len(names) == 0 {
				fmt.Println(ui.Warn("No secrets in " + env))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🔑 %d secrets (%s)", len(names), env)))
			for _, n := range names {
				fmt.Println("  " + n + ui.SubtleStyle.Render("  secret_text"))
			}
			return nil
		},
	})
}

// pagesPatchEnvVars PATCHes deployment_configs.<env>.env_vars.
func pagesPatchEnvVars(c *cobra.Command, project, env string, vars map[string]any) error {
	ctx := context.Background()
	s, err := newAPISession(c)
	if err != nil {
		return err
	}
	path, _, err := platFill(ctx, s, pagesBase+"/{project_name}", []string{project})
	if err != nil {
		return err
	}
	body := map[string]any{"deployment_configs": map[string]any{env: map[string]any{"env_vars": vars}}}
	if _, err := platDo(ctx, s, "PATCH", path, nil, body); err != nil {
		return platErr("Pages", err)
	}
	return nil
}

// platReadSecret reads a secret value: hidden prompt on a TTY, else stdin.
func platReadSecret(prompt string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func pagesSecretPut() *cobra.Command {
	return platCommand(platSpec{
		Use: "put <project> <NAME>", Short: "Create or update a secret (value from a hidden prompt or stdin)", Aliases: []string{"set"},
		Path: pagesBase + "/{project_name}", ExtraArgs: 1, Flags: pagesSecretEnvFlag,
		Example: "  printf %s \"$TOKEN\" | cfctl pages secret put my-site API_TOKEN",
		Run: func(c *cobra.Command, args []string) error {
			env, err := pagesSecretEnv(c)
			if err != nil {
				return err
			}
			val, err := platReadSecret("Enter a secret value: ")
			if err != nil {
				return err
			}
			if val == "" {
				return fmt.Errorf("empty secret value")
			}
			if err := pagesPatchEnvVars(c, args[0], env, map[string]any{args[1]: map[string]any{"type": "secret_text", "value": val}}); err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(map[string]any{"project": args[0], "env": env, "name": args[1], "updated": true})
			}
			fmt.Println(ui.Success(fmt.Sprintf("Set secret %s on %s (%s)", args[1], args[0], env)))
			return nil
		},
	})
}

func pagesSecretBulk() *cobra.Command {
	return platCommand(platSpec{
		Use: "bulk <project> [file.json]", Short: "Upload several secrets from a JSON object file (or stdin)",
		Path: pagesBase + "/{project_name}", OptionalArgs: 1, Flags: pagesSecretEnvFlag,
		Run: func(c *cobra.Command, args []string) error {
			env, err := pagesSecretEnv(c)
			if err != nil {
				return err
			}
			var data []byte
			if len(args) > 1 && args[1] != "-" {
				data, err = os.ReadFile(args[1])
			} else {
				data, err = io.ReadAll(os.Stdin)
			}
			if err != nil {
				return err
			}
			var in map[string]any
			if err := json.Unmarshal(data, &in); err != nil {
				return fmt.Errorf("secrets must be a JSON object of NAME: \"value\": %w", err)
			}
			vars := map[string]any{}
			for k, v := range in {
				sv, ok := v.(string)
				if !ok {
					return fmt.Errorf("secret %s: value must be a string", k)
				}
				vars[k] = map[string]any{"type": "secret_text", "value": sv}
			}
			if len(vars) == 0 {
				return fmt.Errorf("no secrets in input")
			}
			if err := pagesPatchEnvVars(c, args[0], env, vars); err != nil {
				return err
			}
			names := make([]string, 0, len(vars))
			for k := range vars {
				names = append(names, k)
			}
			sort.Strings(names)
			if jsonOutput {
				return printJSONValue(map[string]any{"project": args[0], "env": env, "names": names})
			}
			fmt.Println(ui.Success(fmt.Sprintf("Set %d secrets on %s (%s): %s", len(names), args[0], env, strings.Join(names, ", "))))
			return nil
		},
	})
}

func pagesSecretDelete() *cobra.Command {
	return platCommand(platSpec{
		Use: "delete <project> <NAME>", Short: "Delete a secret", Aliases: []string{"rm"},
		Path: pagesBase + "/{project_name}", ExtraArgs: 1, Flags: pagesSecretEnvFlag, Confirm: "delete secret %s",
		Run: func(c *cobra.Command, args []string) error {
			env, err := pagesSecretEnv(c)
			if err != nil {
				return err
			}
			if err := confirm(c, fmt.Sprintf("delete secret %s from %s (%s)", args[1], args[0], env)); err != nil {
				return err
			}
			if err := pagesPatchEnvVars(c, args[0], env, map[string]any{args[1]: nil}); err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(map[string]any{"project": args[0], "env": env, "name": args[1], "deleted": true})
			}
			fmt.Println(ui.Success(fmt.Sprintf("Deleted secret %s from %s (%s)", args[1], args[0], env)))
			return nil
		},
	})
}

// Download config --------------------------------------------------------

func pagesDownloadConfig() *cobra.Command {
	return platCommand(platSpec{
		Use: "config <project>", Short: "Write a project's settings as a wrangler.jsonc file",
		Long: `Download a Pages project's settings (compatibility date/flags, vars, bindings) as a
Wrangler configuration file. Secrets are never included (only their names, as comments).`,
		Path: pagesBase + "/{project_name}",
		Flags: func(c *cobra.Command) {
			c.Flags().StringP("output", "o", "wrangler.jsonc", "File to write (- for stdout)")
			c.Flags().Bool("force", false, "Overwrite an existing file")
		},
		Run: func(c *cobra.Command, args []string) error {
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			path, _, err := platFill(ctx, s, pagesBase+"/{project_name}", args)
			if err != nil {
				return err
			}
			raw, err := platDo(ctx, s, "GET", path, nil, nil)
			if err != nil {
				return platErr("Pages", err)
			}
			var m map[string]any
			if err := platDecode(raw, &m); err != nil {
				return err
			}
			out := pagesWranglerConfig(m)
			outPath, _ := c.Flags().GetString("output")
			if outPath == "-" {
				fmt.Print(out)
				return nil
			}
			if force, _ := c.Flags().GetBool("force"); !force {
				if _, err := os.Stat(outPath); err == nil {
					return fmt.Errorf("%s already exists; pass --force to overwrite", outPath)
				}
			}
			if err := os.WriteFile(outPath, []byte(out), 0o644); err != nil {
				return err
			}
			if jsonOutput {
				return printJSONValue(map[string]any{"project": args[0], "path": outPath})
			}
			fmt.Println(ui.Success(fmt.Sprintf("Wrote %s for Pages project %s", outPath, args[0])))
			return nil
		},
	})
}

// pagesWranglerConfig renders a Pages project as wrangler.jsonc (production
// settings at the top level, preview under env.preview when it differs).
func pagesWranglerConfig(p map[string]any) string {
	conf := func(env string) (map[string]any, []string) {
		cfg, _ := platGet(p, "deployment_configs."+env).(map[string]any)
		out := map[string]any{}
		var secrets []string
		if cfg == nil {
			return out, nil
		}
		if v := cfg["compatibility_date"]; v != nil && v != "" {
			out["compatibility_date"] = v
		}
		if v, ok := cfg["compatibility_flags"].([]any); ok && len(v) > 0 {
			out["compatibility_flags"] = v
		}
		if vars, ok := cfg["env_vars"].(map[string]any); ok {
			plain := map[string]any{}
			for k, v := range vars {
				if platStr(platGet(v, "type")) == "secret_text" {
					secrets = append(secrets, k)
					continue
				}
				plain[k] = platGet(v, "value")
			}
			if len(plain) > 0 {
				out["vars"] = plain
			}
		}
		sort.Strings(secrets)
		simple := func(src, dst, idKey, outKey string) {
			if m, ok := cfg[src].(map[string]any); ok && len(m) > 0 {
				var list []any
				names := make([]string, 0, len(m))
				for k := range m {
					names = append(names, k)
				}
				sort.Strings(names)
				for _, name := range names {
					entry := map[string]any{"binding": name}
					if idKey != "" {
						entry[outKey] = platGet(m[name], idKey)
					}
					list = append(list, entry)
				}
				out[dst] = list
			}
		}
		simple("kv_namespaces", "kv_namespaces", "namespace_id", "id")
		simple("d1_databases", "d1_databases", "id", "database_id")
		simple("r2_buckets", "r2_buckets", "name", "bucket_name")
		simple("queue_producers", "queues_producers_tmp", "name", "queue")
		if q, ok := out["queues_producers_tmp"]; ok {
			out["queues"] = map[string]any{"producers": q}
			delete(out, "queues_producers_tmp")
		}
		simple("durable_object_namespaces", "durable_objects_tmp", "class_name", "class_name")
		if d, ok := out["durable_objects_tmp"]; ok {
			for _, e := range d.([]any) {
				e.(map[string]any)["name"] = e.(map[string]any)["binding"]
				delete(e.(map[string]any), "binding")
			}
			out["durable_objects"] = map[string]any{"bindings": d}
			delete(out, "durable_objects_tmp")
		}
		simple("services", "services", "service", "service")
		simple("analytics_engine_datasets", "analytics_engine_datasets", "dataset", "dataset")
		simple("vectorize_bindings", "vectorize", "index_name", "index_name")
		simple("hyperdrive_bindings", "hyperdrive", "id", "id")
		if m, ok := cfg["ai_bindings"].(map[string]any); ok && len(m) > 0 {
			for k := range m {
				out["ai"] = map[string]any{"binding": k}
				break
			}
		}
		if pl, ok := cfg["placement"].(map[string]any); ok && len(pl) > 0 {
			out["placement"] = pl
		}
		return out, secrets
	}
	prod, prodSecrets := conf("production")
	prev, prevSecrets := conf("preview")
	top := map[string]any{"name": p["name"]}
	dir := platStr(platGet(p, "build_config.destination_dir"))
	if dir == "" {
		dir = "./dist"
	}
	top["pages_build_output_dir"] = dir
	for k, v := range prod {
		top[k] = v
	}
	pj, _ := json.Marshal(prod)
	vj, _ := json.Marshal(prev)
	if string(pj) != string(vj) && len(prev) > 0 {
		top["env"] = map[string]any{"preview": prev}
	}
	b, _ := json.MarshalIndent(top, "", "  ")
	var sb strings.Builder
	sb.WriteString("// Generated by cfctl pages download config from Pages project \"" + platStr(p["name"]) + "\".\n")
	sb.WriteString("// " + `"pages_build_output_dir" is a guess; point it at your build output.` + "\n")
	if len(prodSecrets) > 0 {
		sb.WriteString("// Production secrets (not downloaded): " + strings.Join(prodSecrets, ", ") + "\n")
	}
	if len(prevSecrets) > 0 {
		sb.WriteString("// Preview secrets (not downloaded): " + strings.Join(prevSecrets, ", ") + "\n")
	}
	sb.WriteString("{\n  \"$schema\": \"node_modules/wrangler/config-schema.json\",\n")
	sb.WriteString(strings.TrimPrefix(string(b), "{\n"))
	sb.WriteString("\n")
	return sb.String()
}

// platLocalOnly is a placeholder for wrangler commands that need the local
// Workers runtime or bundler: it explains what to run instead.
func platLocalOnly(use, short, wranglerCmd, why string) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short + " (use wrangler; not applicable to cfctl)",
		Long:               fmt.Sprintf("%s.\n\ncfctl talks to the Cloudflare API only; %s, so this stays a wrangler job:\n\n  npx %s\n", short, why, wranglerCmd),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("not applicable: %s; run 'npx %s' instead", why, wranglerCmd)
		},
	}
}
