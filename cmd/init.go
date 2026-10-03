package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// cfctl init scaffolds a minimal Worker project (no npm install, no
// network): wrangler.jsonc, src/index.ts, package.json, tsconfig.json,
// .gitignore. dev/types/setup are wrangler-only (local runtime).

var initName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type initTemplate struct {
	desc  string
	files func(name, date string) map[string]string
}

var initTemplates = map[string]initTemplate{
	"worker": {"fetch handler returning Hello World (TypeScript)", func(name, date string) map[string]string {
		return map[string]string{
			"wrangler.jsonc": initWranglerConfig(name, date, ""),
			"src/index.ts": `export default {
	async fetch(request, env, ctx): Promise<Response> {
		const url = new URL(request.url);
		return new Response(` + "`Hello World from ${url.pathname}!`" + `);
	},
} satisfies ExportedHandler<Env>;

interface Env {}
`,
		}
	}},
	"scheduled": {"cron trigger (scheduled handler) plus a fetch handler", func(name, date string) map[string]string {
		return map[string]string{
			"wrangler.jsonc": initWranglerConfig(name, date, `,
	"triggers": { "crons": ["*/30 * * * *"] }`),
			"src/index.ts": `export default {
	async fetch(): Promise<Response> {
		return new Response("This Worker runs on a schedule; see wrangler.jsonc triggers.");
	},
	async scheduled(controller, env, ctx): Promise<void> {
		console.log("cron fired:", controller.cron, new Date(controller.scheduledTime).toISOString());
	},
} satisfies ExportedHandler<Env>;

interface Env {}
`,
		}
	}},
	"assets": {"static site from ./public served by Workers Static Assets, with an API route", func(name, date string) map[string]string {
		return map[string]string{
			"wrangler.jsonc": initWranglerConfig(name, date, `,
	"assets": { "directory": "./public", "binding": "ASSETS", "not_found_handling": "404-page", "run_worker_first": ["/api/*"] }`),
			"src/index.ts": `export default {
	async fetch(request, env): Promise<Response> {
		const url = new URL(request.url);
		if (url.pathname.startsWith("/api/")) {
			return Response.json({ hello: "world", path: url.pathname });
		}
		return env.ASSETS.fetch(request);
	},
} satisfies ExportedHandler<Env>;

interface Env {
	ASSETS: Fetcher;
}
`,
			"public/index.html": "<!doctype html>\n<html>\n<head><meta charset=\"utf-8\"><title>" + name + "</title></head>\n<body><h1>" + name + "</h1><p>Served by Workers Static Assets.</p></body>\n</html>\n",
			"public/404.html":   "<!doctype html>\n<html><body><h1>Not found</h1></body></html>\n",
		}
	}},
}

func initWranglerConfig(name, date, extra string) string {
	return `{
	"$schema": "node_modules/wrangler/config-schema.json",
	"name": "` + name + `",
	"main": "src/index.ts",
	"compatibility_date": "` + date + `",
	"compatibility_flags": ["nodejs_compat"],
	"observability": { "enabled": true }` + extra + `
}
`
}

func initCommonFiles(name string) map[string]string {
	pkg, _ := json.MarshalIndent(map[string]any{
		"name":    name,
		"version": "0.0.0",
		"private": true,
		"scripts": map[string]string{
			"dev":        "wrangler dev",
			"deploy":     "wrangler deploy",
			"cf-typegen": "wrangler types",
		},
		"devDependencies": map[string]string{
			"typescript":                "^5.5.0",
			"wrangler":                  "^4.0.0",
			"@cloudflare/workers-types": "^4.20250101.0",
		},
	}, "", "  ")
	return map[string]string{
		"package.json": string(pkg) + "\n",
		"tsconfig.json": `{
  "compilerOptions": {
    "target": "es2022",
    "lib": ["es2022"],
    "module": "es2022",
    "moduleResolution": "bundler",
    "types": ["@cloudflare/workers-types"],
    "strict": true,
    "noEmit": true,
    "skipLibCheck": true
  },
  "include": ["src/**/*.ts"]
}
`,
		".gitignore": "node_modules/\n.wrangler/\n.dev.vars*\n.env*\ndist/\n",
	}
}

var initCmd = &cobra.Command{
	Use:   "init <name>",
	Short: "Scaffold a minimal Worker project (wrangler.jsonc + src/index.ts)",
	Long: `Create a new Worker project directory with wrangler.jsonc, src/index.ts,
package.json, tsconfig.json, and .gitignore. Nothing is installed and no
network calls are made; run 'npm install' afterwards, then 'npx wrangler dev'
to develop locally and 'npx wrangler deploy' to deploy.

Templates:
` + initTemplateHelp() + `
For framework projects (React, Next.js, Astro, ...) use 'npm create cloudflare@latest'.`,
	Example: `  cfctl init my-worker
  cfctl init my-site --template assets
  cfctl init jobs --template scheduled --dir ./services/jobs`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if !initName.MatchString(name) {
			return fmt.Errorf("invalid Worker name %q: use lowercase letters, digits, and dashes (max 63)", name)
		}
		tname, _ := cmd.Flags().GetString("template")
		tpl, ok := initTemplates[tname]
		if !ok {
			return fmt.Errorf("unknown template %q; choose one of: %s", tname, strings.Join(initTemplateNames(), ", "))
		}
		dir, _ := cmd.Flags().GetString("dir")
		if dir == "" {
			dir = name
		}
		date, _ := cmd.Flags().GetString("compatibility-date")
		if date == "" {
			date = time.Now().UTC().Format("2006-01-02")
		}
		files := initCommonFiles(name)
		for k, v := range tpl.files(name, date) {
			files[k] = v
		}
		force, _ := cmd.Flags().GetBool("force")
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		if !force {
			for _, p := range paths {
				if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
					return fmt.Errorf("%s already exists; pass --force to overwrite", filepath.Join(dir, p))
				}
			}
		}
		for _, p := range paths {
			full := filepath.Join(dir, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, []byte(files[p]), 0o644); err != nil {
				return err
			}
		}
		if jsonOutput {
			return printJSONValue(map[string]any{"name": name, "dir": dir, "template": tname, "files": paths})
		}
		fmt.Println(ui.Success(fmt.Sprintf("Created %s (%s template) in %s", name, tname, dir)))
		for _, p := range paths {
			fmt.Println("  " + ui.SubtleStyle.Render(filepath.Join(dir, p)))
		}
		fmt.Printf("\nNext:\n  cd %s && npm install\n  npx wrangler dev      # local development\n  npx wrangler deploy   # deploy\n", dir)
		return nil
	},
}

func initTemplateNames() []string {
	var n []string
	for k := range initTemplates {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func initTemplateHelp() string {
	var b strings.Builder
	for _, n := range initTemplateNames() {
		fmt.Fprintf(&b, "  %-10s %s\n", n, initTemplates[n].desc)
	}
	return b.String()
}

func init() {
	initCmd.Flags().StringP("template", "t", "worker", "Template: "+strings.Join(initTemplateNames(), ", "))
	initCmd.Flags().String("dir", "", "Directory to create (default: ./<name>)")
	initCmd.Flags().String("compatibility-date", "", "Compatibility date (default: today)")
	initCmd.Flags().Bool("force", false, "Overwrite existing files")
	rootCmd.AddCommand(initCmd,
		platLocalOnly("dev", "Start a local server for developing a Worker", "wrangler dev", "it runs your Worker in the local workerd runtime (Miniflare)"),
		platLocalOnly("types", "Generate TypeScript types from your Worker configuration", "wrangler types", "it derives runtime types from the local workerd version and your wrangler config"),
		platLocalOnly("setup", "Set up a framework project to deploy on Cloudflare", "wrangler setup", "it detects and rewrites local framework build config (use 'cfctl init' for a plain Worker)"),
	)
}
