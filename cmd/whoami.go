package cmd

import (
	"context"
	"fmt"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Whoami is the identity behind the current token.
type Whoami struct {
	Token struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		Type      string `json:"type"` // "user" or "account"
		ExpiresOn string `json:"expires_on,omitempty"`
	} `json:"token"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name,omitempty"`
		Type string `json:"type,omitempty"`
	} `json:"account"`
	// User is only filled in if the token has User Details: Read.
	User *struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user,omitempty"`
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show current identity",
	Long:  `Display information about the current API token, account, and (if permitted) user.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		tok, err := app.VerifyToken(ctx)
		if err != nil {
			return fmt.Errorf("whoami failed: %w", err)
		}
		if tok.AccountID != "" {
			app.AccountID = tok.AccountID
		}

		var w Whoami
		w.Token.ID = tok.ID
		w.Token.Status = tok.Status
		w.Token.Type = tok.Type
		w.Token.ExpiresOn = timestamp(tok.ExpiresOn)
		w.Account.ID = app.AccountID

		acct, err := app.Client.Accounts.Get(ctx, accounts.AccountGetParams{AccountID: cloudflare.F(app.AccountID)})
		if err != nil {
			return apiErr("failed to get account "+app.AccountID, err)
		}
		w.Account.Name = acct.Name
		w.Account.Type = string(acct.Type)

		// GET /user needs User Details: Read, which cfctl doesn't require
		// (and account-owned tokens never have).
		if w.Token.Type != config.TokenTypeAccount {
			if u, err := app.Client.User.Get(ctx); err == nil {
				w.User = &struct {
					ID    string `json:"id"`
					Email string `json:"email"`
				}{u.ID, u.Email}
			}
		}

		if printJSON(w) {
			return nil
		}

		fmt.Println(ui.TitleStyle.Render("☁️  Cloudflare Identity"))
		fmt.Println()

		fmt.Println(ui.SuccessStyle.Render("API Token"))
		fmt.Printf("  %-14s %s\n", "ID:", w.Token.ID)
		fmt.Printf("  %-14s %s\n", "Status:", w.Token.Status)
		fmt.Printf("  %-14s %s\n", "Type:", tokenTypeLabel(w.Token.Type, w.Account.ID, w.Account.Name))
		if w.Token.ExpiresOn != "" {
			fmt.Printf("  %-14s %s\n", "Expires:", w.Token.ExpiresOn)
		}

		fmt.Println(ui.SuccessStyle.Render("Account"))
		fmt.Printf("  %-14s %s\n", "ID:", w.Account.ID)
		fmt.Printf("  %-14s %s\n", "Name:", w.Account.Name)
		if w.Account.Type != "" {
			fmt.Printf("  %-14s %s\n", "Type:", w.Account.Type)
		}

		if w.User != nil {
			fmt.Println(ui.SuccessStyle.Render("User"))
			fmt.Printf("  %-14s %s\n", "ID:", w.User.ID)
			fmt.Printf("  %-14s %s\n", "Email:", w.User.Email)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(whoamiCmd)
}
