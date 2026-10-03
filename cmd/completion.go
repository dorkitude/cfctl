package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion <bash|zsh|fish|powershell>",
	Short: "Generate a shell completion script",
	Long: `Generate a shell completion script for cfctl.

  bash:        source <(cfctl completion bash)
               # or: cfctl completion bash > /etc/bash_completion.d/cfctl
  zsh:         cfctl completion zsh > "${fpath[1]}/_cfctl"   # then restart the shell
  fish:        cfctl completion fish > ~/.config/fish/completions/cfctl.fish
  powershell:  cfctl completion powershell | Out-String | Invoke-Expression

The generated 'cfctl api <tag> <op>' commands complete too.`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
	RunE: func(cmd *cobra.Command, args []string) error {
		root := cmd.Root()
		switch args[0] {
		case "bash":
			return root.GenBashCompletionV2(os.Stdout, true)
		case "zsh":
			return root.GenZshCompletion(os.Stdout)
		case "fish":
			return root.GenFishCompletion(os.Stdout, true)
		case "powershell", "pwsh":
			return root.GenPowerShellCompletionWithDesc(os.Stdout)
		}
		return fmt.Errorf("unsupported shell %q (want bash, zsh, fish, or powershell)", args[0])
	},
}

func init() {
	rootCmd.AddCommand(completionCmd)
}
