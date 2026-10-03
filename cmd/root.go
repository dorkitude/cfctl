package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/dorkitude/cfctl/internal/version"
	"github.com/spf13/cobra"
)

var (
	jsonOutput   bool
	accountFlag  string
	noColorFlag  bool
	debugFlag    bool
	readOnlyFlag bool
	timeoutFlag  time.Duration
)

// BinName returns the name this binary was invoked as.
func BinName() string {
	return filepath.Base(os.Args[0])
}

var rootCmd = &cobra.Command{
	Use:   "cfctl",
	Short: "A CLI for Cloudflare: domains, zones, DNS, and every API operation",
	Long: ui.TitleStyle.Render("☁️  cfctl") + `
A CLI for Cloudflare: Registrar domains, zones, DNS records, Workers and
storage (what wrangler does, against the API), zone and account admin, and
every Cloudflare API operation.

` + ui.SubtleStyle.Render("Areas:") + `
  Core            auth, whoami, domains, zones, records, docs, completion
  Workers         workers, deploy, versions, deployments, rollback, secret, tail, ...
  Storage         kv, r2, d1, queues, hyperdrive, vectorize, secrets-store, ...
  Platform        pages, ai, ai-search, workflows, containers, tunnel, email, ...
  Zone & account  cache, ssl, rulesets, waf, lb, analytics, tokens, audit-logs, ...
  API & raw       api (all 3,645 operations + raw requests), graphql

` + ui.SubtleStyle.Render("Docs:") + `
  cfctl docs              README (offline)    cfctl docs --list   all topics
  cfctl docs api          generated API tree, raw requests, GraphQL
  cfctl docs cfctl-vs-wrangler

` + ui.SubtleStyle.Render("Safety:") + `
  --read-only (or CFCTL_READONLY=1) refuses every request except GET/HEAD/OPTIONS
  and GraphQL queries, before anything is sent. Agents: use --json on reads.
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if noColorFlag || os.Getenv("NO_COLOR") != "" {
			ui.DisableColor()
		}
		api.SetDebug(debugFlag || (os.Getenv("CFCTL_DEBUG") != "" && os.Getenv("CFCTL_DEBUG") != "0"))
		api.SetReadOnly(readOnlyFlag)
		if timeoutFlag <= 0 {
			return fmt.Errorf("--timeout must be positive")
		}
		api.Timeout = timeoutFlag
		return config.Init(cmd.Flags().Lookup("account"))
	},
}

// SetVersion sets the version shown by --version. Release builds may pass
// one in via -ldflags; otherwise it is internal/version.Version (0.2.NNN).
func SetVersion(v string) {
	if v == "" || v == "dev" {
		v = version.Version
	}
	// Keep internal/version in sync so User-Agent headers match --version.
	version.Version = v
	rootCmd.Version = v
}

// Execute runs the root command.
func Execute() {
	// Adapt the root command name to whatever binary name was used
	rootCmd.Use = BinName()
	prepareCommands(os.Args[1:])
	groupRootCommands()

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, ui.Err(err.Error()))
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	rootCmd.PersistentFlags().StringVar(&accountFlag, "account", "", "Cloudflare account ID (overrides cached)")
	rootCmd.PersistentFlags().BoolVar(&noColorFlag, "no-color", false, "Disable colored output")
	rootCmd.PersistentFlags().BoolVar(&debugFlag, "debug", false, "Log each API request (method, path, status, duration) to stderr; also CFCTL_DEBUG=1")
	rootCmd.PersistentFlags().BoolVar(&readOnlyFlag, "read-only", false, "Refuse every API request that could change anything (only GET/HEAD/OPTIONS and GraphQL queries are sent); also CFCTL_READONLY=1")
	rootCmd.PersistentFlags().DurationVar(&timeoutFlag, "timeout", 30*time.Second, "Timeout for each API request attempt")
	rootCmd.CompletionOptions.DisableDefaultCmd = true
}

// Command groups shown in `cfctl --help`, by area. Commands not listed here
// land under "Additional Commands".
var rootGroups = []struct {
	id, title string
	cmds      []string
}{
	{"core", "Core:", []string{"auth", "whoami", "domains", "zones", "records", "docs", "completion"}},
	{"workers", "Workers:", []string{"workers", "deploy", "versions", "deployments", "rollback", "secret", "triggers", "tail", "dispatch-namespace", "preview", "init", "dev", "types", "setup"}},
	{"storage", "Storage:", []string{"kv", "r2", "d1", "queues", "hyperdrive", "vectorize", "secrets-store", "k2", "basin", "artifacts", "agent-memory"}},
	{"platform", "Platform:", []string{"pages", "ai", "ai-search", "workflows", "containers", "browser", "flagship", "email", "turnstile", "tunnel", "vpc", "cert", "mtls-certificate"}},
	{"admin", "Zone & account:", []string{"dns", "cache", "ssl", "rulesets", "redirects", "transform", "waf", "page-rules", "firewall", "lists", "lb", "analytics", "accounts", "members", "roles", "tokens", "audit-logs", "billing", "logpush", "notifications", "healthchecks", "waiting-rooms", "spectrum", "access", "user"}},
	{"raw", "API & raw:", []string{"api", "graphql"}},
}

// groupRootCommands assigns every top-level command to its area group. It
// runs after all init()s, so every command is registered.
func groupRootCommands() {
	if len(rootCmd.Groups()) > 0 {
		return
	}
	area := map[string]string{}
	for _, g := range rootGroups {
		rootCmd.AddGroup(&cobra.Group{ID: g.id, Title: g.title})
		for _, name := range g.cmds {
			area[name] = g.id
		}
	}
	for _, c := range rootCmd.Commands() {
		if id, ok := area[c.Name()]; ok && c.GroupID == "" {
			c.GroupID = id
		}
	}
	rootCmd.SetHelpCommandGroupID("core")
}
