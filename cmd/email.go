package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const (
	erZone = "/zones/{zone_id}/email/routing"
	erAcct = "/accounts/{account_id}/email/routing"
)

var erRuleCols = []platCol{
	{H: "id", Path: "id|tag"}, {H: "name", Path: "name", W: 30}, {H: "enabled", Path: "enabled"},
	{H: "matchers", Path: "matchers", W: 50}, {H: "actions", Path: "actions", W: 60}, {H: "priority", Path: "priority"},
}

// emailResolveDomain finds the zone for a domain or subdomain (walking up
// labels), like wrangler's resolveDomain.
func emailResolveDomain(ctx context.Context, s *apiSession, domain string) (zoneID, zoneName string, err error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	labels := strings.Split(domain, ".")
	for i := 0; i <= len(labels)-2; i++ {
		cand := strings.Join(labels[i:], ".")
		q := url.Values{"name": {cand}}
		if id := config.AccountID(); id != "" {
			q.Set("account.id", id)
		}
		raw, err := platDo(ctx, s, "GET", "/zones", q, nil)
		if err != nil {
			return "", "", err
		}
		var zs []struct{ ID, Name string }
		_ = json.Unmarshal(raw, &zs)
		for _, z := range zs {
			if strings.EqualFold(z.Name, cand) {
				return z.ID, z.Name, nil
			}
		}
	}
	return "", "", fmt.Errorf("no zone for %q in this account", domain)
}

// erAction builds the actions list from --forward/--worker/--drop.
func erAction(c *cobra.Command) ([]any, error) {
	fwd, _ := c.Flags().GetStringSlice("forward")
	worker, _ := c.Flags().GetString("worker")
	drop, _ := c.Flags().GetBool("drop")
	n := 0
	if len(fwd) > 0 {
		n++
	}
	if worker != "" {
		n++
	}
	if drop {
		n++
	}
	if n != 1 {
		return nil, fmt.Errorf("give exactly one action: --forward <address>, --worker <name>, or --drop")
	}
	switch {
	case len(fwd) > 0:
		return []any{map[string]any{"type": "forward", "value": fwd}}, nil
	case worker != "":
		return []any{map[string]any{"type": "worker", "value": []string{worker}}}, nil
	default:
		return []any{map[string]any{"type": "drop"}}, nil
	}
}

func erActionFlags(c *cobra.Command) {
	c.Flags().StringSlice("forward", nil, "Forward to these verified destination addresses")
	c.Flags().String("worker", "", "Send to this Email Worker")
	c.Flags().Bool("drop", false, "Drop the message")
}

func erRuleFlags(c *cobra.Command) {
	erActionFlags(c)
	c.Flags().String("to", "", "Match messages sent to this address (literal)")
	c.Flags().String("name", "", "Rule name")
	c.Flags().Int("priority", 0, "Priority (lower runs first)")
	c.Flags().Bool("disabled", false, "Create/leave the rule disabled")
}

func erRuleBody(c *cobra.Command, _ []string) (any, error) {
	to, _ := c.Flags().GetString("to")
	if to == "" {
		return nil, fmt.Errorf("--to <address> is required")
	}
	actions, err := erAction(c)
	if err != nil {
		return nil, err
	}
	dis, _ := c.Flags().GetBool("disabled")
	b := map[string]any{
		"enabled":  !dis,
		"matchers": []any{map[string]any{"type": "literal", "field": "to", "value": to}},
		"actions":  actions,
	}
	platSetStr(c, b, "name", "name")
	platSetInt(c, b, "priority", "priority")
	return b, nil
}

func init() {
	rules := platGroup("rules", "Manage routing rules for a zone", "", []string{"rule"},
		platSpecs(
			platSpec{Use: "list <zone>", Short: "List routing rules", Aliases: []string{"ls"}, Path: erZone + "/rules", List: true,
				Cols: erRuleCols, Title: "📧 %d routing rules", Product: "Email Routing"},
			platSpec{Use: "get <zone> <rule-id>", Short: "Show a routing rule", Path: erZone + "/rules/{rule_identifier}", Product: "Email Routing"},
			platSpec{Use: "create <zone>", Short: "Create a routing rule", Method: "POST", Path: erZone + "/rules", Flags: erRuleFlags, Body: erRuleBody, Data: true,
				Example: `  cfctl email routing rules create example.com --to hi@example.com --forward me@gmail.com
  cfctl email routing rules create example.com --to bot@example.com --worker inbox-worker`,
				Title: "📧 Created rule", Product: "Email Routing"},
			platSpec{Use: "update <zone> <rule-id>", Short: "Replace a routing rule", Method: "PUT", Path: erZone + "/rules/{rule_identifier}", Flags: erRuleFlags, Body: erRuleBody, Data: true,
				Done: "Updated rule", Product: "Email Routing"},
			platSpec{Use: "delete <zone> <rule-id>", Short: "Delete a routing rule", Aliases: []string{"rm"}, Method: "DELETE", Path: erZone + "/rules/{rule_identifier}",
				Confirm: "delete routing rule %s", Done: "Deleted rule", Product: "Email Routing"},
		)...)

	catchAll := platGroup("catch-all", "Show or set the catch-all rule", "", []string{"catchall"},
		platSpecs(
			platSpec{Use: "get <zone>", Short: "Show the catch-all rule", Path: erZone + "/rules/catch_all", Product: "Email Routing",
				Fields: []platCol{{H: "Enabled", Path: "enabled"}, {H: "Name", Path: "name"}, {H: "Actions", Path: "actions", W: 200}}, Title: "📧 Catch-all"},
			platSpec{Use: "set <zone>", Short: "Set the catch-all rule", Method: "PUT", Path: erZone + "/rules/catch_all",
				Flags: func(c *cobra.Command) {
					erActionFlags(c)
					c.Flags().Bool("disabled", false, "Disable the catch-all")
				},
				Body: func(c *cobra.Command, _ []string) (any, error) {
					actions, err := erAction(c)
					if err != nil {
						return nil, err
					}
					dis, _ := c.Flags().GetBool("disabled")
					return map[string]any{"enabled": !dis, "matchers": []any{map[string]any{"type": "all"}}, "actions": actions}, nil
				},
				Done: "Updated the catch-all rule", Product: "Email Routing"},
		)...)

	addresses := platGroup("addresses", "Manage destination addresses (account-wide)", "", []string{"address"},
		platSpecs(
			platSpec{Use: "list", Short: "List destination addresses", Aliases: []string{"ls"}, Path: erAcct + "/addresses", List: true,
				Cols: []platCol{{H: "id", Path: "id|tag"}, {H: "email", Path: "email"}, {H: "verified", Path: "verified"}, {H: "created", Path: "created"}}, Title: "📧 %d destination addresses", Product: "Email Routing"},
			platSpec{Use: "get <address-id>", Short: "Show a destination address", Path: erAcct + "/addresses/{destination_address_identifier}", Product: "Email Routing"},
			platSpec{Use: "create <email>", Short: "Add a destination address (sends a verification email)", Method: "POST", Path: erAcct + "/addresses", ExtraArgs: 1,
				Body: func(_ *cobra.Command, args []string) (any, error) { return map[string]any{"email": args[0]}, nil },
				Done: "Added %s; check its inbox for the verification email", Product: "Email Routing"},
			platSpec{Use: "delete <address-id>", Short: "Remove a destination address", Aliases: []string{"rm"}, Method: "DELETE", Path: erAcct + "/addresses/{destination_address_identifier}",
				Confirm: "delete destination address %s", Done: "Deleted %s", Product: "Email Routing"},
		)...)

	dns := platGroup("dns", "Email Routing DNS records", "", nil,
		platSpecs(
			platSpec{Use: "get <zone>", Short: "Show the DNS records Email Routing needs", Path: erZone + "/dns",
				Cols: []platCol{{H: "type", Path: "type"}, {H: "name", Path: "name"}, {H: "content", Path: "content", W: 70}, {H: "priority", Path: "priority"}}, Title: "📧 Required DNS records", Product: "Email Routing"},
			platSpec{Use: "unlock <zone>", Short: "Unlock the MX records so they can be edited", Method: "POST", Path: erZone + "/unlock",
				Body: func(*cobra.Command, []string) (any, error) { return map[string]any{}, nil }, Done: "Unlocked Email Routing DNS records for %s", Product: "Email Routing"},
		)...)

	routing := platGroup("routing", "Email Routing: settings, rules, addresses, DNS", "", nil,
		append(platSpecs(
			platSpec{Use: "list", Short: "List zones with Email Routing", Aliases: []string{"ls"}, Path: erAcct + "/zones", List: true,
				Cols: []platCol{{H: "zone", Path: "name|zone_name"}, {H: "enabled", Path: "enabled"}, {H: "status", Path: "status"}, {H: "id", Path: "id|zone_id"}}, Title: "📧 %d zones", Product: "Email Routing"},
			platSpec{Use: "settings <zone>", Short: "Show Email Routing settings for a zone", Aliases: []string{"get", "status"}, Path: erZone,
				Fields: []platCol{{H: "Zone", Path: "name"}, {H: "Enabled", Path: "enabled"}, {H: "Status", Path: "status"}, {H: "Skip wizard", Path: "skip_wizard"}, {H: "Created", Path: "created"}, {H: "Modified", Path: "modified"}, {H: "Tag", Path: "tag|id"}},
				Title:  "📧 Email Routing for %s", Product: "Email Routing"},
			platSpec{Use: "enable <zone>", Short: "Enable Email Routing (adds MX/SPF records)", Method: "POST", Path: erZone + "/enable",
				Body: func(*cobra.Command, []string) (any, error) { return map[string]any{}, nil }, Done: "Enabled Email Routing for %s", Product: "Email Routing"},
			platSpec{Use: "disable <zone>", Short: "Disable Email Routing", Method: "POST", Path: erZone + "/disable",
				Body:    func(*cobra.Command, []string) (any, error) { return map[string]any{}, nil },
				Confirm: "disable Email Routing for %s", Done: "Disabled Email Routing for %s", Product: "Email Routing"},
		), rules, catchAll, addresses, dns)...)

	sending := platGroup("sending", "Email Sending: subdomains, DNS, send", "", nil,
		esListCmd(), esSettingsCmd(), esEnableCmd(), esDisableCmd(), esDNSCmd(), esSendCmd(), esSendRawCmd())

	emailCmd := platGroup("email", "Email Routing and Email Sending", `Manage Cloudflare Email services.

  cfctl email routing list | settings|enable|disable <zone>
  cfctl email routing rules list|get|create|update|delete <zone>
  cfctl email routing catch-all get|set <zone>
  cfctl email routing addresses list|get|create|delete
  cfctl email routing dns get|unlock <zone>
  cfctl email sending list [domain] | settings|enable|disable|dns <domain>
  cfctl email sending send --from --to --subject --text|--html
  cfctl email sending send-raw --from --to --mime-file

Zones accept a name or ID. Generated: 'cfctl api email-routing-settings ...' and friends.`, nil, routing, sending)
	rootCmd.AddCommand(emailCmd)
}

// Email Sending -----------------------------------------------------------

var esCols = []platCol{
	{H: "name", Path: "name"}, {H: "enabled", Path: "enabled"}, {H: "id", Path: "tag|id"},
	{H: "dkim selector", Path: "dkim_selector"}, {H: "return path", Path: "return_path_domain"}, {H: "created", Path: "created"},
}

// esSubdomain finds the sending subdomain object for a domain.
func esSubdomain(ctx context.Context, s *apiSession, domain string) (string, map[string]any, error) {
	zid, _, err := emailResolveDomain(ctx, s, domain)
	if err != nil {
		return "", nil, platErr("Email Sending", err)
	}
	raw, err := platDo(ctx, s, "GET", "/zones/"+zid+"/email/sending/subdomains", nil, nil)
	if err != nil {
		return "", nil, platErr("Email Sending", err)
	}
	var subs []map[string]any
	if err := platDecode(raw, &subs); err != nil {
		return "", nil, err
	}
	for _, sd := range subs {
		if strings.EqualFold(platStr(sd["name"]), domain) {
			return zid, sd, nil
		}
	}
	return zid, nil, fmt.Errorf("Email Sending isn't enabled for %s (see 'cfctl email sending enable %s')", domain, domain)
}

func esRun(c *cobra.Command, fn func(ctx context.Context, s *apiSession) error) error {
	s, err := newAPISession(c)
	if err != nil {
		return err
	}
	return fn(context.Background(), s)
}

func esListCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "list [domain]", Short: "List sending subdomains (all zones when no domain)", Aliases: []string{"ls"}, Path: "/accounts/{account_id}/email/sending", OptionalArgs: 1,
		Run: func(c *cobra.Command, args []string) error {
			return esRun(c, func(ctx context.Context, s *apiSession) error {
				var raw json.RawMessage
				var err error
				if len(args) == 1 {
					zid, _, rerr := emailResolveDomain(ctx, s, args[0])
					if rerr != nil {
						return rerr
					}
					raw, err = platDo(ctx, s, "GET", "/zones/"+zid+"/email/sending/subdomains", nil, nil)
				} else {
					path, _, ferr := platFill(ctx, s, "/accounts/{account_id}/email/sending/zones", nil)
					if ferr != nil {
						return ferr
					}
					raw, err = platAll(ctx, s, path, nil)
				}
				if err != nil {
					return platErr("Email Sending", err)
				}
				if jsonOutput {
					return printBody(raw, nil)
				}
				var items []any
				if err := platDecode(raw, &items); err != nil {
					return err
				}
				platTable("📤 %d sending domains", items, esCols)
				return nil
			})
		},
	})
}

func esSettingsCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "settings <domain>", Short: "Show Email Sending settings for a domain", Path: "/accounts/{account_id}/email/sending", ExtraArgs: 1,
		Run: func(c *cobra.Command, args []string) error {
			return esRun(c, func(ctx context.Context, s *apiSession) error {
				_, sd, err := esSubdomain(ctx, s, args[0])
				if err != nil {
					return err
				}
				if jsonOutput {
					return printJSONValue(sd)
				}
				platDetail("📤 Email Sending for "+args[0], sd, []platCol{{H: "Name", Path: "name"}, {H: "Enabled", Path: "enabled"}, {H: "Tag", Path: "tag|id"},
					{H: "Created", Path: "created"}, {H: "Modified", Path: "modified"}, {H: "DKIM selector", Path: "dkim_selector"}, {H: "Return path", Path: "return_path_domain"}})
				return nil
			})
		},
	})
}

func esEnableCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "enable <domain>", Short: "Enable Email Sending for a zone or subdomain", Path: "/accounts/{account_id}/email/sending", ExtraArgs: 1,
		Run: func(c *cobra.Command, args []string) error {
			return esRun(c, func(ctx context.Context, s *apiSession) error {
				zid, _, err := emailResolveDomain(ctx, s, args[0])
				if err != nil {
					return platErr("Email Sending", err)
				}
				raw, err := platDo(ctx, s, "POST", "/zones/"+zid+"/email/sending/subdomains", nil, map[string]any{"name": args[0]})
				if err != nil {
					return platErr("Email Sending", err)
				}
				if jsonOutput {
					return printBody(raw, nil)
				}
				fmt.Println(ui.Success("Email Sending enabled for " + args[0]))
				return nil
			})
		},
	})
}

func esDisableCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "disable <domain>", Short: "Disable Email Sending for a zone or subdomain", Path: "/accounts/{account_id}/email/sending", ExtraArgs: 1, Confirm: "disable Email Sending for %s",
		Run: func(c *cobra.Command, args []string) error {
			return esRun(c, func(ctx context.Context, s *apiSession) error {
				zid, sd, err := esSubdomain(ctx, s, args[0])
				if err != nil {
					return err
				}
				if err := confirm(c, "disable Email Sending for "+args[0]); err != nil {
					return err
				}
				if _, err := platDo(ctx, s, "DELETE", "/zones/"+zid+"/email/sending/subdomains/"+url.PathEscape(platStr(platGet(sd, "tag|id"))), nil, nil); err != nil {
					return platErr("Email Sending", err)
				}
				if jsonOutput {
					return printJSONValue(map[string]any{"domain": args[0], "disabled": true})
				}
				fmt.Println(ui.Success("Email Sending disabled for " + args[0]))
				return nil
			})
		},
	})
}

func esDNSCmd() *cobra.Command {
	get := platCommand(platSpec{
		Use: "get <domain>", Short: "Show the DNS records Email Sending needs", Path: "/accounts/{account_id}/email/sending", ExtraArgs: 1,
		Run: func(c *cobra.Command, args []string) error {
			return esRun(c, func(ctx context.Context, s *apiSession) error {
				zid, sd, err := esSubdomain(ctx, s, args[0])
				if err != nil {
					return err
				}
				raw, err := platDo(ctx, s, "GET", "/zones/"+zid+"/email/sending/subdomains/"+url.PathEscape(platStr(platGet(sd, "tag|id")))+"/dns", nil, nil)
				if err != nil {
					return platErr("Email Sending", err)
				}
				if jsonOutput {
					return printBody(raw, nil)
				}
				var items []any
				if err := platDecode(raw, &items); err != nil {
					return err
				}
				platTable("📤 DNS records for "+args[0], items, []platCol{{H: "type", Path: "type"}, {H: "name", Path: "name"}, {H: "content", Path: "content", W: 80}, {H: "priority", Path: "priority"}})
				return nil
			})
		},
	})
	return platGroup("dns", "Email Sending DNS records", "", nil, get)
}

func esSendCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "send", Short: "Send an email", Method: "POST", Path: "/accounts/{account_id}/email/sending/send",
		Example: `  cfctl email sending send --from me@example.com --to you@example.org --subject Hi --text "Hello"`,
		Flags: func(c *cobra.Command) {
			c.Flags().String("from", "", "Sender address (required)")
			c.Flags().String("from-name", "", "Sender display name")
			c.Flags().StringSlice("to", nil, "Recipient(s) (required)")
			c.Flags().StringSlice("cc", nil, "CC recipient(s)")
			c.Flags().StringSlice("bcc", nil, "BCC recipient(s)")
			c.Flags().String("subject", "", "Subject (required)")
			c.Flags().String("text", "", "Plain-text body")
			c.Flags().String("html", "", "HTML body")
			c.Flags().String("reply-to", "", "Reply-To address")
			c.Flags().String("reply-to-name", "", "Reply-To display name")
			c.Flags().StringArray("header", nil, "Custom header 'Name:Value' (repeatable)")
			c.Flags().StringArray("attachment", nil, "File to attach (repeatable)")
		},
		Body: esSendBody, Print: esPrintSend, Product: "Email Sending",
	})
}

func esSendBody(c *cobra.Command, _ []string) (any, error) {
	from, _ := c.Flags().GetString("from")
	to, _ := c.Flags().GetStringSlice("to")
	subject, _ := c.Flags().GetString("subject")
	text, _ := c.Flags().GetString("text")
	html, _ := c.Flags().GetString("html")
	if from == "" || len(to) == 0 || subject == "" {
		return nil, fmt.Errorf("--from, --to, and --subject are required")
	}
	if text == "" && html == "" {
		return nil, fmt.Errorf("at least one of --text or --html is required")
	}
	b := map[string]any{"subject": subject}
	if fn, _ := c.Flags().GetString("from-name"); fn != "" {
		b["from"] = map[string]any{"address": from, "name": fn}
	} else {
		b["from"] = from
	}
	if len(to) == 1 {
		b["to"] = to[0]
	} else {
		b["to"] = to
	}
	platSetStr(c, b, "text", "text")
	platSetStr(c, b, "html", "html")
	platSetStrs(c, b, "cc", "cc")
	platSetStrs(c, b, "bcc", "bcc")
	if rt, _ := c.Flags().GetString("reply-to"); rt != "" {
		if rn, _ := c.Flags().GetString("reply-to-name"); rn != "" {
			b["reply_to"] = map[string]any{"address": rt, "name": rn}
		} else {
			b["reply_to"] = rt
		}
	}
	if hs, _ := c.Flags().GetStringArray("header"); len(hs) > 0 {
		h := map[string]string{}
		for _, x := range hs {
			k, v, ok := strings.Cut(x, ":")
			if !ok || strings.TrimSpace(k) == "" {
				return nil, fmt.Errorf("invalid --header %q: want Name:Value", x)
			}
			h[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		b["headers"] = h
	}
	if as, _ := c.Flags().GetStringArray("attachment"); len(as) > 0 {
		var atts []any
		for _, p := range as {
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, fmt.Errorf("failed to read attachment %s: %w", p, err)
			}
			ct := mime.TypeByExtension(filepath.Ext(p))
			if i := strings.Index(ct, ";"); i >= 0 {
				ct = ct[:i]
			}
			if ct == "" {
				ct = "application/octet-stream"
			}
			atts = append(atts, map[string]any{"content": base64.StdEncoding.EncodeToString(data), "filename": filepath.Base(p), "type": ct, "disposition": "attachment"})
		}
		b["attachments"] = atts
	}
	return b, nil
}

func esPrintSend(_ *cobra.Command, _ []string, raw json.RawMessage) error {
	var r struct {
		Delivered        []string `json:"delivered"`
		Queued           []string `json:"queued"`
		PermanentBounces []string `json:"permanent_bounces"`
	}
	_ = json.Unmarshal(raw, &r)
	if len(r.Delivered) > 0 {
		fmt.Println(ui.Success("Delivered to: " + strings.Join(r.Delivered, ", ")))
	}
	if len(r.Queued) > 0 {
		fmt.Println(ui.Info("Queued for: " + strings.Join(r.Queued, ", ")))
	}
	if len(r.PermanentBounces) > 0 {
		fmt.Println(ui.Warn("Permanently bounced: " + strings.Join(r.PermanentBounces, ", ")))
	}
	if len(r.Delivered)+len(r.Queued)+len(r.PermanentBounces) == 0 {
		fmt.Println(ui.Success("Email sent"))
	}
	return nil
}

func esSendRawCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "send-raw", Short: "Send a raw MIME message", Method: "POST", Path: "/accounts/{account_id}/email/sending/send_raw",
		Flags: func(c *cobra.Command) {
			c.Flags().String("from", "", "Envelope sender (required)")
			c.Flags().StringSlice("to", nil, "Envelope recipient(s) (required)")
			c.Flags().String("mime", "", "Raw MIME message")
			c.Flags().String("mime-file", "", "File containing the raw MIME message (- for stdin)")
		},
		Body: func(c *cobra.Command, _ []string) (any, error) {
			from, _ := c.Flags().GetString("from")
			to, _ := c.Flags().GetStringSlice("to")
			msg, _ := c.Flags().GetString("mime")
			file, _ := c.Flags().GetString("mime-file")
			if from == "" || len(to) == 0 {
				return nil, fmt.Errorf("--from and --to are required")
			}
			if (msg == "") == (file == "") {
				return nil, fmt.Errorf("give exactly one of --mime or --mime-file")
			}
			if file != "" {
				var data []byte
				var err error
				if file == "-" {
					data, err = io.ReadAll(os.Stdin)
				} else {
					data, err = os.ReadFile(file)
				}
				if err != nil {
					return nil, err
				}
				msg = string(data)
			}
			return map[string]any{"from": from, "recipients": to, "mime_message": msg}, nil
		},
		Print: esPrintSend, Product: "Email Sending",
	})
}
