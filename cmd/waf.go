package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const (
	wafCustomPhase    = "http_request_firewall_custom"
	wafManagedPhase   = "http_request_firewall_managed"
	wafRateLimitPhase = "http_ratelimit"
)

var wafCmd = &cobra.Command{
	Use:   "waf",
	Short: "WAF: managed rulesets, custom rules, and rate limiting rules",
	Long: `Web Application Firewall overview and custom rules.

Examples:
  cfctl waf overview example.com
  cfctl waf managed example.com
  cfctl waf custom list example.com
  cfctl waf custom add example.com --expression '(ip.src.country eq "T1")' --action block --description "Block Tor"
  cfctl waf custom delete example.com <rule-id>
  cfctl waf rate-limits example.com

Actions for custom rules: block, challenge, managed_challenge, js_challenge,
log, skip (skip needs --action-parameters).`,
}

var wafManagedCols = []col{{"ID", "id"}, {"On", "enabled"}, {"Action", "action"}, {"Ruleset", "managed_ruleset"}, {"Description", "description"}, {"Expression", "expression"}}

// wafManagedRun shows the managed rulesets deployed on a zone, with names.
func wafManagedRun(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	s, err := adminSession(cmd, args[0])
	if err != nil {
		return err
	}
	raw, found, err := getEntrypoint(ctx, s, true, wafManagedPhase, "WAF managed rules")
	if err != nil {
		return err
	}
	names := map[string]string{}
	if lr, err := fetch(ctx, s, "/zones/{zone_id}/rulesets", nil, nil, true, "rulesets"); err == nil {
		for _, it := range asList(lr, "") {
			names[jstr(it, "id")] = jstr(it, "name")
		}
	}
	var rules []any
	if found {
		rules, _ = jget(decodeAny(raw), "rules").([]any)
	}
	for _, r := range rules {
		if m, ok := r.(map[string]any); ok {
			id := jstr(r, "action_parameters.id")
			m["managed_ruleset"] = names[id]
			if names[id] == "" {
				m["managed_ruleset"] = id
			}
		}
	}
	if rules == nil {
		rules = []any{}
	}
	b, _ := json.Marshal(rules)
	return emit(b, func(any) { printTable("Deployed managed rulesets", rules, wafManagedCols) })
}

var wafManagedCmd = &cobra.Command{Use: "managed <zone>", Short: "Show deployed managed rulesets (Cloudflare Managed, OWASP, ...)", Args: cobra.ExactArgs(1), RunE: wafManagedRun}

var wafOverviewCmd = &cobra.Command{
	Use:   "overview <zone>",
	Short: "Show managed rulesets, custom rules, and rate limits in one view",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		out := map[string]any{}
		for _, ph := range []string{wafManagedPhase, wafCustomPhase, wafRateLimitPhase} {
			raw, found, err := getEntrypoint(ctx, s, true, ph, "WAF")
			if err != nil {
				out[ph] = map[string]any{"error": err.Error()}
				continue
			}
			rules := []any{}
			if found {
				if r, ok := jget(decodeAny(raw), "rules").([]any); ok {
					rules = r
				}
			}
			out[ph] = rules
		}
		if sl, err := fetch(ctx, s, "/zones/{zone_id}/settings/security_level", nil, nil, false, "zone settings"); err == nil {
			out["security_level"] = jget(decodeAny(sl), "value")
		}
		b, _ := json.Marshal(out)
		return emit(b, func(v any) {
			fmt.Println(ui.TitleStyle.Render("WAF for " + args[0]))
			fmt.Printf("  Security level: %s\n\n", jstr(v, "security_level"))
			for _, x := range []struct{ title, ph string }{{"Managed rulesets", wafManagedPhase}, {"Custom rules", wafCustomPhase}, {"Rate limiting rules", wafRateLimitPhase}} {
				if e := jstr(v, x.ph+".error"); e != "" {
					fmt.Println(ui.Warn(x.title + ": " + e))
					continue
				}
				rules, _ := jget(v, x.ph).([]any)
				printTable(x.title, rules, ruleCols)
				fmt.Println()
			}
		})
	},
}

var wafCustomAddCmd = &cobra.Command{
	Use:   "add <zone>",
	Short: "Add a WAF custom rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		rule, err := ruleFromFlags(cmd, true)
		if err != nil {
			return err
		}
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		raw, err := addPhaseRule(ctx, s, true, wafCustomPhase, rule, "WAF custom rules")
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			rules, _ := jget(v, "rules").([]any)
			id := ""
			if len(rules) > 0 {
				id = jstr(rules[len(rules)-1], "id")
			}
			fmt.Println(ui.Success("WAF custom rule " + id + " added"))
		})
	},
}

var wafCustomCmd = group("custom", "WAF custom rules", "", nil,
	phaseListCmd("list <zone>", "List WAF custom rules", "Custom rules", []string{wafCustomPhase}, ruleCols, "WAF custom rules"),
	wafCustomAddCmd,
	phaseDeleteCmd("delete <zone> <rule-id>", "Delete a WAF custom rule", []string{wafCustomPhase}, "WAF custom rules"),
)

var wafRateLimitsCmd = phaseListCmd("rate-limits <zone>", "List rate limiting rules", "Rate limiting rules",
	[]string{wafRateLimitPhase}, []col{{"ID", "id"}, {"On", "enabled"}, {"Action", "action"}, {"Period", "ratelimit.period"}, {"Requests", "ratelimit.requests_per_period"}, {"Description", "description"}, {"Expression", "expression"}}, "rate limiting rules")

func init() {
	rootCmd.AddCommand(wafCmd)
	wafCmd.AddCommand(wafOverviewCmd, wafManagedCmd, wafCustomCmd, wafRateLimitsCmd)
	addRuleFlags(wafCustomAddCmd)
}
