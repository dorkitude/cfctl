package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go/v7"
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
	TokenType     string `json:"token_type"`
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
	AccountName   string `json:"account_name,omitempty"`
	// TokenType is "user" or "account" ("" if unknown without --verify).
	TokenType string `json:"token_type,omitempty"`
	// Verified/TokenStatus are only set with --verify.
	Verified    *bool  `json:"verified,omitempty"`
	TokenStatus string `json:"token_status,omitempty"`
}

// tokenTypeLabel renders "user" or "account (owned by Name, ID)".
func tokenTypeLabel(kind, accountID, accountName string) string {
	if kind != config.TokenTypeAccount {
		return kind
	}
	owner := accountID
	if accountName != "" {
		owner = accountName + ", " + accountID
	}
	return "account (owned by " + owner + ")"
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

var accountIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// validAccountID reports whether id looks like a Cloudflare account ID.
func validAccountID(id string) bool {
	return accountIDPattern.MatchString(id)
}

// promptAccountID asks for an account ID (visible input) until it gets a
// 32-character lowercase hex string. Prompts go to w.
func promptAccountID(r *bufio.Reader, w io.Writer) (string, error) {
	fmt.Fprintln(w, ui.SubtleStyle.Render("Find it at dash.cloudflare.com → your account home → \"Account ID\" (right sidebar, API section),"))
	fmt.Fprintln(w, ui.SubtleStyle.Render("or the 32-hex id in the dashboard URL, or `wrangler whoami`."))
	for attempt := 0; attempt < 5; attempt++ {
		fmt.Fprint(w, "Cloudflare account ID: ")
		line, err := r.ReadString('\n')
		id := strings.ToLower(strings.TrimSpace(line))
		if validAccountID(id) {
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("no account ID given; pass --account <id>")
		}
		fmt.Fprintln(w, ui.Warn("An account ID is 32 hex characters (0-9, a-f). Try again."))
	}
	return "", fmt.Errorf("no valid account ID given; pass --account <id>")
}

// loginTimeout bounds the whole verify step of `auth login`.
var loginTimeout = 45 * time.Second

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Login with a Cloudflare API token",
	Long: `Asks for your Cloudflare account ID first (unless --account is given or one
is cached), then for your API token (hidden input, or piped stdin). Verifies the
token (user or account-owned) and stores it locally.

Piped tokens need a known account ID (--account), since there is no terminal
to ask on.

Examples:
  cfctl auth login
  cfctl auth login --account 0123456789abcdef0123456789abcdef
  printf '%s' "$TOKEN" | cfctl auth login --account 0123456789abcdef0123456789abcdef`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		force, _ := cmd.Flags().GetBool("force")

		if config.HasToken() && !force {
			fmt.Println(ui.Warn("Already authenticated. Use '" + BinName() + " auth logout' first (or --force) to re-authenticate."))
			return nil
		}

		interactive := term.IsTerminal(int(os.Stdin.Fd()))

		// Account ID first: --account (or CFCTL_ACCOUNT_ID), then the cached one.
		accountID := strings.ToLower(config.AccountID())
		if accountID == "" {
			if !interactive {
				return fmt.Errorf("no account ID known: re-run with --account <account-id> (token not read)")
			}
			if !jsonOutput {
				fmt.Println(ui.TitleStyle.Render("🔐 Cloudflare Authentication"))
			}
			id, err := promptAccountID(bufio.NewReader(os.Stdin), os.Stderr)
			if err != nil {
				return err
			}
			accountID = id
		} else if !validAccountID(accountID) {
			return fmt.Errorf("invalid account ID %q: expected 32 hex characters", accountID)
		} else if interactive && !jsonOutput {
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
			fmt.Println(ui.SubtleStyle.Render("Validating token for account " + accountID + "..."))
		}

		vctx, cancel := context.WithTimeout(ctx, loginTimeout)
		defer cancel()
		verify, err := client.ValidateToken(vctx, token, []string{accountID}, "")
		if err != nil {
			if client.IsTimeout(err) || vctx.Err() != nil {
				return fmt.Errorf("timed out verifying the token after %s; check connectivity, and that --account %s is right", loginTimeout, accountID)
			}
			return err
		}

		// Best-effort account name (needs Account Settings: Read).
		acct := accounts.Account{ID: accountID}
		if a, err := client.NewClient(token).Accounts.Get(vctx, accounts.AccountGetParams{AccountID: cloudflare.F(accountID)}); err == nil {
			acct.Name = a.Name
		}

		// Everything checks out: save token, then account.
		if err := config.SaveToken(token); err != nil {
			return fmt.Errorf("failed to save token: %w", err)
		}
		if err := config.Save(&config.Config{AccountID: acct.ID, AccountName: acct.Name, TokenType: verify.Type}); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		tokenPath, _ := config.TokenPath()
		configPath, _ := config.ConfigPath()

		if printJSON(LoginResult{
			Authenticated: true,
			TokenID:       verify.ID,
			TokenStatus:   verify.Status,
			TokenType:     verify.Type,
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
		fmt.Println(ui.SubtleStyle.Render("Token type: " + tokenTypeLabel(verify.Type, acct.ID, acct.Name)))
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
			if tok, _ := config.LoadToken(); strings.HasPrefix(tok, client.AccountTokenPrefix) {
				s.TokenType = config.TokenTypeAccount
			}
		} else if config.HasToken() {
			s.Authenticated, s.TokenSource = true, "file"
			s.TokenType = config.TokenType()
			if cfg, _ := config.Load(); cfg != nil && cfg.AccountID == s.AccountID {
				s.AccountName = cfg.AccountName
			}
		}

		verifyFlag, _ := cmd.Flags().GetBool("verify")
		if verifyFlag && s.Authenticated {
			ctx := context.Background()
			ok := false
			app, err := getApp(ctx)
			if err == nil {
				var info *client.TokenInfo
				if info, err = app.VerifyToken(ctx); err == nil {
					ok = true
					s.TokenType, s.TokenStatus = info.Type, info.Status
					if info.AccountID != "" {
						s.AccountID = info.AccountID
					}
				}
			}
			s.Verified = &ok
			if err != nil && !jsonOutput {
				defer fmt.Println(ui.Err(err.Error()))
			}
		}

		if printJSON(s) {
			return nil
		}

		if s.Authenticated {
			if s.Verified != nil && !*s.Verified {
				fmt.Println(ui.Warn("Token present but failed verification"))
			} else {
				fmt.Println(ui.Success("Authenticated"))
			}
			if s.TokenSource == "file" {
				fmt.Println(ui.SubtleStyle.Render("Token location: " + tokenPath))
			} else {
				fmt.Println(ui.SubtleStyle.Render("Token from: $" + s.TokenSource))
			}
			if s.TokenType != "" {
				fmt.Println(ui.SubtleStyle.Render("Token type: " + tokenTypeLabel(s.TokenType, s.AccountID, s.AccountName)))
			}
			if s.TokenStatus != "" {
				fmt.Println(ui.SubtleStyle.Render("Token status: " + s.TokenStatus))
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
		fmt.Println(ui.SuccessStyle.Render("2.") + " Create the token on either page (both kinds work):")
		fmt.Println("   → User token:    My Profile → API Tokens")
		fmt.Println("     https://dash.cloudflare.com/profile/api-tokens")
		fmt.Println("   → Account token: Manage Account → Account API Tokens (starts with cfat_)")
		fmt.Println()
		fmt.Println(ui.SuccessStyle.Render("3.") + " Click " + ui.AccentStyle.Render("\"Create Token\"") + " → " + ui.AccentStyle.Render("\"Create Custom Token\""))
		fmt.Println("   → Name it (e.g., 'cfctl')")
		fmt.Println("   → Permissions:")
		fmt.Println("       Account │ Account Settings     │ Read")
		fmt.Println("       Account │ Domain Registration  │ Edit   (a.k.a. Registrar: Domains)")
		fmt.Println("       Zone    │ Zone                 │ Edit")
		fmt.Println("       Zone    │ DNS                  │ Edit")
		fmt.Println("   → Account Resources: Include → your account (user tokens only)")
		fmt.Println("   → Zone Resources:    Include → All zones")
		fmt.Println("   → Continue to summary → Create Token → copy it")
		fmt.Println()
		fmt.Println(ui.SuccessStyle.Render("4.") + " Run: " + ui.AccentStyle.Render(BinName()+" auth login"))
		fmt.Println("   → You'll be asked for your account ID first (32 hex chars: dashboard")
		fmt.Println("     account home → \"Account ID\", the id in the dashboard URL, or `wrangler whoami`)")
		fmt.Println("   → Then paste your token when prompted (input is hidden)")
		fmt.Println("   → Or pipe it (account ID required): " + ui.AccentStyle.Render("printf '%s' \"$TOKEN\" | "+BinName()+" auth login --account <id>"))
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
	authStatusCmd.Flags().Bool("verify", false, "Also verify the token against the API")
	authCmd.AddCommand(authSetupCmd)
}
