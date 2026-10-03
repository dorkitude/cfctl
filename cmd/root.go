package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/dorkitude/cfctl/internal/client"
	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var (
	jsonOutput  bool
	accountFlag string
	noColorFlag bool
	debugFlag   bool
)

// BinName returns the name this binary was invoked as.
func BinName() string {
	return filepath.Base(os.Args[0])
}

var rootCmd = &cobra.Command{
	Use:   "cfctl",
	Short: "A CLI for Cloudflare domains, zones, and DNS",
	Long: ui.TitleStyle.Render("☁️  cfctl") + `
A CLI for Cloudflare Registrar domains, zones, and DNS records.

` + ui.SubtleStyle.Render("Commands:") + `
  auth        Authenticate with Cloudflare
  whoami      Show current identity
  domains     Manage Registrar domains
  zones       Manage zones
  records     Manage DNS records
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if noColorFlag || os.Getenv("NO_COLOR") != "" {
			ui.DisableColor()
		}
		client.Debug = debugFlag || (os.Getenv("CFCTL_DEBUG") != "" && os.Getenv("CFCTL_DEBUG") != "0")
		return config.Init(cmd.Flags().Lookup("account"))
	},
}

// SetVersion sets the version shown by --version. Release builds pass it in
// via -ldflags; `go install pkg@vX.Y.Z` builds fall back to the module version.
func SetVersion(v string) {
	if v == "" || v == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		} else {
			v = "dev"
		}
	}
	rootCmd.Version = v
}

// Execute runs the root command.
func Execute() {
	// Adapt the root command name to whatever binary name was used
	rootCmd.Use = BinName()

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
	rootCmd.CompletionOptions.DisableDefaultCmd = true
}
