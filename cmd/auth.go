package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/dorkitude/cfctl/internal/client"
	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// LoginResult is what `auth login --json` prints. It never includes the token.
type LoginResult struct {
	Authenticated bool   `json:"authenticated"`
	TokenID       string `json:"token_id"`
	TokenStatus   string `json:"token_status"`
	AccountID     string `json:"account_id"`
	AccountName   string `json:"account_name"`
	TokenPath     string `json:"token_path"`
	ConfigPath    string `json:"config_path"`
}

// AuthStatus is what `auth status --json` prints. It never includes the token.
type AuthStatus struct {
	Authenticated bool   `json:"authenticated"`
	TokenSource   string `json:"token_source,omitempty"`
	TokenPath     string `json:"token_path"`
	ConfigPath    string `json:"config_path"`
	AccountID     string `json:"account_id,omitempty"`
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate with Cloudflare",
	Long:  `Manage authentication with the Cloudflare API.`,
}

// readToken reads the API token from stdin: a hidden prompt on a terminal,
// otherwise the first non-empty line of piped input.
func readToken(in *os.File) (string, error) {
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprint(os.Stderr, "Enter your API token (input hidden): ")
		b, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("failed to read token: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}

	data, err := io.ReadAll(io.LimitReader(in, 64*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read token from stdin: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t, nil
		}
	}
	return "", nil
}

// promptTTY returns a reader for interactive prompts, or nil if there is no
// terminal (e.g. the token was piped over a non-interactive ssh session).
func promptTTY() (io.Reader, func()) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return os.Stdin, func() {}
	}
	if f, err := os.Open("/dev/tty"); err == nil {
		return f, func() { f.Close() }
	}
	return nil, func() {}
}

func accountList(accts []accounts.Account) string {
	var b strings.Builder
	for _, a := range accts {
		fmt.Fprintf(&b, "\n  %s  %s", a.ID, a.Name)
	}
	return b.String()
}

// pickAccount chooses the account to use at login.
//
// explicit (--account / CFCTL_ACCOUNT_ID) must match; cached (from an earlier
// login) is used if still visible; one account is used as-is; several are
// offered as a numbered prompt when a terminal is available.
func pickAccount(accts []accounts.Account, explicit, cached string, openTTY func() (io.Reader, func())) (accounts.Account, error) {
	if len(accts) == 0 {
		return accounts.Account{}, fmt.Errorf("this token cannot see any accounts; give it Account Settings: Read (see '%s auth setup')", BinName())
	}
	if explicit != "" {
		for _, a := range accts {
			if a.ID == explicit || strings.EqualFold(a.Name, explicit) {
				return a, nil
			}
		}
		return accounts.Account{}, fmt.Errorf("account '%s' is not visible to this token; available:%s", explicit, accountList(accts))
	}
	if len(accts) == 1 {
		return accts[0], nil
	}
	if cached != "" {
		for _, a := range accts {
			if a.ID == cached {
				return a, nil
			}
		}
	}
	tty, closeTTY := openTTY()
	defer closeTTY()
	if tty == nil {
		return accounts.Account{}, fmt.Errorf("token can see %d accounts; re-run with --account <id>:%s", len(accts), accountList(accts))
	}

	fmt.Fprintln(os.Stderr, "This token can see several accounts:")
	for i, a := range accts {
		fmt.Fprintf(os.Stderr, "  %d) %s  %s\n", i+1, a.Name, ui.SubtleStyle.Render(a.ID))
	}
	fmt.Fprintf(os.Stderr, "Choose [1-%d]: ", len(accts))
	line, _ := bufio.NewReader(tty).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(accts) {
		return accounts.Account{}, fmt.Errorf("invalid choice %q", strings.TrimSpace(line))
	}
	return accts[n-1], nil
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Login with a Cloudflare API token",
	Long: `Prompts for your API token (hidden input), or reads it from stdin when
piped, validates it against the Cloudflare API, discovers your account, and
stores the token locally.

Examples:
  cfctl auth login
  printf '%s' "$TOKEN" | cfctl auth login
  cfctl auth login --account 0123456789abcdef0123456789abcdef`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		force, _ := cmd.Flags().GetBool("force")

		if config.HasToken() && !force {
			fmt.Println(ui.Warn("Already authenticated. Use '" + BinName() + " auth logout' first (or --force) to re-authenticate."))
			return nil
		}

		if !jsonOutput && term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Println(ui.TitleStyle.Render("🔐 Cloudflare Authentication"))
		}

		token, err := readToken(os.Stdin)
		if err != nil {
			return err
		}
		if token == "" {
			return fmt.Errorf("token cannot be empty")
		}

		if !jsonOutput {
			fmt.Println(ui.SubtleStyle.Render("Validating token..."))
		}

		verify, err := client.ValidateToken(ctx, token)
		if err != nil {
			return err
		}

		accts, err := client.ListAccounts(ctx, client.NewClient(token))
		if err != nil {
			return fmt.Errorf("failed to list accounts: %w", err)
		}

		explicit := strings.TrimSpace(accountFlag)
		if explicit == "" {
			explicit = strings.TrimSpace(os.Getenv("CFCTL_ACCOUNT_ID"))
		}
		cached := ""
		if cfg, _ := config.Load(); cfg != nil {
			cached = cfg.AccountID
		}
		acct, err := pickAccount(accts, explicit, cached, promptTTY)
		if err != nil {
			return err
		}

		// Everything checks out: save token, then account.
		if err := config.SaveToken(token); err != nil {
			return fmt.Errorf("failed to save token: %w", err)
		}
		if err := config.Save(&config.Config{AccountID: acct.ID}); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		tokenPath, _ := config.TokenPath()
		configPath, _ := config.ConfigPath()

		if printJSON(LoginResult{
			Authenticated: true,
			TokenID:       verify.ID,
			TokenStatus:   string(verify.Status),
			AccountID:     acct.ID,
			AccountName:   acct.Name,
			TokenPath:     tokenPath,
			ConfigPath:    configPath,
		}) {
			return nil
		}

		fmt.Println()
		fmt.Println(ui.Success("Authenticated with Cloudflare! 🎉"))
		fmt.Println(ui.SubtleStyle.Render(fmt.Sprintf("Account: %s (ID: %s)", acct.Name, acct.ID)))
		fmt.Println(ui.SubtleStyle.Render("Token saved to: " + tokenPath))
		if env := config.TokenEnvVar(); env != "" {
			fmt.Println(ui.Warn(env + " is set and overrides the saved token"))
		}

		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored credentials",
	Long:  `Removes the locally stored API token.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.RemoveToken(); err != nil {
			return fmt.Errorf("failed to logout: %w", err)
		}
		fmt.Println(ui.Success("Logged out."))
		if env := config.TokenEnvVar(); env != "" {
			fmt.Println(ui.Warn(env + " is still set in your environment"))
		}
		return nil
	},
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check authentication status",
	Long:  `Check if you are currently authenticated with Cloudflare.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		tokenPath, _ := config.TokenPath()
		configPath, _ := config.ConfigPath()
		s := AuthStatus{TokenPath: tokenPath, ConfigPath: configPath, AccountID: config.AccountID()}
		if env := config.TokenEnvVar(); env != "" {
			s.Authenticated, s.TokenSource = true, env
		} else if config.HasToken() {
			s.Authenticated, s.TokenSource = true, "file"
		}

		if printJSON(s) {
			return nil
		}

		if s.Authenticated {
			fmt.Println(ui.Success("Authenticated"))
			if s.TokenSource == "file" {
				fmt.Println(ui.SubtleStyle.Render("Token location: " + tokenPath))
			} else {
				fmt.Println(ui.SubtleStyle.Render("Token from: $" + s.TokenSource))
			}
			if s.AccountID != "" {
				fmt.Println(ui.SubtleStyle.Render("Account ID: " + s.AccountID))
			}
		} else {
			fmt.Println(ui.Warn("Not authenticated"))
			fmt.Println(ui.SubtleStyle.Render("Run '" + BinName() + " auth login' to authenticate"))
		}
		return nil
	},
}

var authSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Instructions for getting a Cloudflare API token",
	Long:  `Prints instructions for creating a Cloudflare API token.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(ui.TitleStyle.Render("🔧 Cloudflare API Token Setup"))
		fmt.Println()
		fmt.Println(ui.SubtleStyle.Render("cfctl uses a Cloudflare API token (wrangler's OAuth login can't reach Registrar or DNS)."))
		fmt.Println()
		fmt.Println(ui.SuccessStyle.Render("1.") + " Log in at https://dash.cloudflare.com")
		fmt.Println()
		fmt.Println(ui.SuccessStyle.Render("2.") + " Go to My Profile → API Tokens")
		fmt.Println("   → https://dash.cloudflare.com/profile/api-tokens")
		fmt.Println()
		fmt.Println(ui.SuccessStyle.Render("3.") + " Click " + ui.AccentStyle.Render("\"Create Token\"") + " → " + ui.AccentStyle.Render("\"Create Custom Token\""))
		fmt.Println("   → Name it (e.g., 'cfctl')")
		fmt.Println("   → Permissions:")
		fmt.Println("       Account │ Account Settings     │ Read")
		fmt.Println("       Account │ Domain Registration  │ Edit   (a.k.a. Registrar: Domains)")
		fmt.Println("       Zone    │ Zone                 │ Edit")
		fmt.Println("       Zone    │ DNS                  │ Edit")
		fmt.Println("   → Account Resources: Include → your account")
		fmt.Println("   → Zone Resources:    Include → All zones")
		fmt.Println("   → Continue to summary → Create Token → copy it")
		fmt.Println()
		fmt.Println(ui.SuccessStyle.Render("4.") + " Run: " + ui.AccentStyle.Render(BinName()+" auth login"))
		fmt.Println("   → Paste your token when prompted (input is hidden)")
		fmt.Println("   → Or pipe it: " + ui.AccentStyle.Render("printf '%s' \"$TOKEN\" | "+BinName()+" auth login"))
		fmt.Println()
		fmt.Println(ui.SubtleStyle.Render("CFCTL_TOKEN or CLOUDFLARE_API_TOKEN, if set, override the saved token."))
	},
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authLoginCmd)
	authLoginCmd.Flags().Bool("force", false, "Replace an existing saved token")
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authSetupCmd)
}
