package cmd

import (
	"context"
	"fmt"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Whoami is the identity behind the current token.
type Whoami struct {
	Token struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
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

		tok, err := app.Client.User.Tokens.Verify(ctx)
		if err != nil {
			return apiErr("whoami failed", err)
		}

		var w Whoami
		w.Token.ID = tok.ID
		w.Token.Status = string(tok.Status)
		w.Token.ExpiresOn = timestamp(tok.ExpiresOn)
		w.Account.ID = app.AccountID

		acct, err := app.Client.Accounts.Get(ctx, accounts.AccountGetParams{AccountID: cloudflare.F(app.AccountID)})
		if err != nil {
			return apiErr("failed to get account "+app.AccountID, err)
		}
		w.Account.Name = acct.Name
		w.Account.Type = string(acct.Type)

		// GET /user needs User Details: Read, which cfctl doesn't require.
		if u, err := app.Client.User.Get(ctx); err == nil {
			w.User = &struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			}{u.ID, u.Email}
		}

		if printJSON(w) {
			return nil
		}

		fmt.Println(ui.TitleStyle.Render("☁️  Cloudflare Identity"))
		fmt.Println()

		fmt.Println(ui.SuccessStyle.Render("API Token"))
		fmt.Printf("  %-14s %s\n", "ID:", w.Token.ID)
		fmt.Printf("  %-14s %s\n", "Status:", w.Token.Status)
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
