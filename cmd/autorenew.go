package cmd

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/registrar"
	"github.com/dorkitude/cfctl/internal/client"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// AutoRenewStatus is the result of an auto-renew query or change.
type AutoRenewStatus struct {
	Domain    string `json:"domain"`
	State     string `json:"state,omitempty"`
	AutoRenew bool   `json:"auto_renew"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Changed   *bool  `json:"changed,omitempty"`
	// Pending is set when Cloudflare accepted the change but is still applying it.
	Pending bool `json:"pending,omitempty"`
}

// parseOnOff turns a user-supplied toggle word into a bool.
func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "enable", "enabled", "true", "yes", "1":
		return true, nil
	case "off", "disable", "disabled", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("invalid auto-renew setting %q (use on or off)", s)
}

// autoRenewError turns registrar API errors into friendlier messages.
func autoRenewError(domain, action string, err error) error {
	switch client.StatusCode(err) {
	case http.StatusNotFound:
		return fmt.Errorf("domain '%s' is not registered with Cloudflare Registrar in this account (API: %s)", domain, client.Messages(err))
	case http.StatusForbidden:
		return fmt.Errorf("failed to %s auto-renew for '%s': %s (does the token have Domain Registration: Edit?)", action, domain, client.Messages(err))
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
		return fmt.Errorf("failed to %s auto-renew for '%s': %s", action, domain, client.Messages(err))
	}
	return fmt.Errorf("failed to %s auto-renew for '%s': %w", action, domain, client.APIError(err))
}

// getAutoRenewStatus fetches the current auto-renew status for a domain.
func getAutoRenewStatus(ctx context.Context, c *cloudflare.Client, accountID, domain string) (*AutoRenewStatus, error) {
	d, err := getRegistration(ctx, c, accountID, domain)
	if err != nil {
		return nil, err
	}
	return &AutoRenewStatus{
		Domain:    d.DomainName,
		State:     string(d.Status),
		AutoRenew: d.AutoRenew,
		ExpiresAt: timestamp(d.ExpiresAt),
	}, nil
}

// setAutoRenew enables or disables auto-renewal via the Registrar API
// (PATCH /accounts/{id}/registrar/registrations/{domain}). It returns
// pending=true if Cloudflare is still applying the change.
func setAutoRenew(ctx context.Context, c *cloudflare.Client, accountID, domain string, enable bool) (bool, error) {
	action := "disable"
	if enable {
		action = "enable"
	}
	wf, err := c.Registrar.Registrations.Edit(ctx, normalizeDomain(domain), registrar.RegistrationEditParams{
		AccountID: cloudflare.F(accountID),
		AutoRenew: cloudflare.F(enable),
	})
	if err != nil {
		return false, autoRenewError(domain, action, err)
	}
	switch wf.State {
	case registrar.WorkflowStatusStateFailed, registrar.WorkflowStatusStateBlocked, registrar.WorkflowStatusStateActionRequired:
		msg := string(wf.State)
		if wf.Error.Message != "" {
			msg += ": " + wf.Error.Message
		}
		return false, fmt.Errorf("failed to %s auto-renew for '%s': %s", action, domain, msg)
	}
	return !wf.Completed, nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func printAutoRenewStatus(s *AutoRenewStatus) {
	fmt.Println(ui.TitleStyle.Render("↻ " + s.Domain))
	fmt.Println()
	style := ui.WarningStyle
	if s.AutoRenew {
		style = ui.SuccessStyle
	}
	fmt.Printf("  %-14s %s\n", "Auto-Renew:", style.Render(onOff(s.AutoRenew)))
	if s.State != "" {
		fmt.Printf("  %-14s %s\n", "State:", s.State)
	}
	if s.ExpiresAt != "" {
		fmt.Printf("  %-14s %s\n", "Expires:", s.ExpiresAt)
	}
	if s.State != "" && s.State != string(registrar.RegistrationStatusActive) {
		fmt.Println()
		fmt.Println(ui.Warn(fmt.Sprintf("'%s' is %s, not active; auto-renew may not apply", s.Domain, s.State)))
	}
}

var domainsAutoRenewCmd = &cobra.Command{
	Use:     "autorenew [domain] [on|off]",
	Aliases: []string{"auto-renew", "renewal"},
	Short:   "Show or set auto-renew for a registered domain",
	Long: `Show or change auto-renewal for a domain registered with Cloudflare Registrar.

With only a domain, shows the current auto-renew status.
With on/off, enables or disables auto-renewal via the Registrar API.

Examples:
  cfctl domains autorenew example.com
  cfctl domains autorenew example.com on
  cfctl domains autorenew example.com off
  cfctl domains autorenew example.com --json`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		domain := args[0]

		var want *bool
		if len(args) == 2 {
			v, err := parseOnOff(args[1])
			if err != nil {
				return err
			}
			want = &v
		}

		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		if want == nil {
			status, err := getAutoRenewStatus(ctx, app.Client, app.AccountID, domain)
			if err != nil {
				return err
			}
			if printJSON(status) {
				return nil
			}
			printAutoRenewStatus(status)
			return nil
		}

		before, err := getAutoRenewStatus(ctx, app.Client, app.AccountID, domain)
		if err != nil {
			return err
		}

		changed := before.AutoRenew != *want
		pending := false
		if changed {
			if pending, err = setAutoRenew(ctx, app.Client, app.AccountID, domain, *want); err != nil {
				return err
			}
		}

		after := *before
		after.AutoRenew = *want
		after.Changed = &changed
		after.Pending = pending

		if printJSON(after) {
			return nil
		}

		switch {
		case changed && pending:
			fmt.Println(ui.Success(fmt.Sprintf("Auto-renew change to %s accepted for '%s' (Cloudflare is still applying it)", onOff(*want), after.Domain)))
		case changed:
			fmt.Println(ui.Success(fmt.Sprintf("Auto-renew turned %s for '%s'", onOff(*want), after.Domain)))
		default:
			fmt.Println(ui.Info(fmt.Sprintf("Auto-renew already %s for '%s'; nothing to do", onOff(*want), after.Domain)))
		}
		return nil
	},
}

func init() {
	domainsCmd.AddCommand(domainsAutoRenewCmd)
}
