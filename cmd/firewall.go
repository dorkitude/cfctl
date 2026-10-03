package cmd

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var accessRuleCols = []col{{"ID", "id"}, {"Mode", "mode"}, {"Target", "configuration.target"}, {"Value", "configuration.value"}, {"Scope", "scope.type"}, {"Notes", "notes"}, {"Created", "created_on"}}

var asnRE = regexp.MustCompile(`^(?i:AS)?\d+$`)
var countryRE = regexp.MustCompile(`^[A-Za-z]{2}$`)

// accessRuleTarget guesses the target type of a value: ip, ip6, ip_range,
// asn, or country.
func accessRuleTarget(v string) (string, string) {
	switch {
	case net.ParseIP(v) != nil && strings.Contains(v, ":"):
		return "ip6", v
	case net.ParseIP(v) != nil:
		return "ip", v
	case strings.Contains(v, "/"):
		if _, _, err := net.ParseCIDR(v); err == nil {
			return "ip_range", v
		}
	case asnRE.MatchString(v):
		// The API's ASN value is "AS<number>" (spec example: "AS12345").
		return "asn", "AS" + strings.TrimPrefix(strings.ToUpper(v), "AS")
	case countryRE.MatchString(v):
		return "country", strings.ToUpper(v)
	}
	return "", v
}

var firewallCmd = group("firewall", "IP Access Rules (allow/block/challenge by IP, range, ASN, or country)", `IP Access Rules, at zone level (--zone) or account level (default).

Examples:
  cfctl firewall access-rules list --zone example.com
  cfctl firewall access-rules create 192.0.2.1 --mode block --notes "abuse" --zone example.com
  cfctl firewall access-rules create 198.51.100.0/24 --mode challenge
  cfctl firewall access-rules create AS64496 --mode block
  cfctl firewall access-rules create T1 --mode block      (country code; T1 = Tor)
  cfctl firewall access-rules delete <rule-id> --zone example.com

For WAF custom rules see 'cfctl waf'; for IP lists see 'cfctl lists'.`, nil,
	group("access-rules", "IP Access Rules", "", []string{"access-rule", "ip-rules"},
		readSpec{
			Use: "list", Short: "List IP Access Rules", Scope: scopeEither,
			Path: "/zones/{zone_id}/firewall/access_rules/rules", AcctPath: "/accounts/{account_id}/firewall/access_rules/rules",
			Title: "IP Access Rules", Cols: accessRuleCols, Feature: "IP Access Rules", Paginate: true,
			Flags: func(c *cobra.Command) {
				c.Flags().String("mode", "", "Only rules with this mode")
				c.Flags().String("notes", "", "Only rules whose notes contain this")
			},
			Query: func(c *cobra.Command, q url.Values) error {
				m, _ := c.Flags().GetString("mode")
				n, _ := c.Flags().GetString("notes")
				setIf(q, "mode", m)
				setIf(q, "notes", n)
				return nil
			},
		}.build(),
		writeSpec{
			Use: "create <ip|cidr|asn|country>", Short: "Create an IP Access Rule", Method: "POST", Scope: scopeEither,
			Path: "/zones/{zone_id}/firewall/access_rules/rules", AcctPath: "/accounts/{account_id}/firewall/access_rules/rules",
			Args: []string{"value"},
			Body: func(c *cobra.Command, args []string) (any, error) {
				mode, _ := c.Flags().GetString("mode")
				switch mode {
				case "block", "challenge", "whitelist", "js_challenge", "managed_challenge":
				default:
					return nil, fmt.Errorf("--mode must be block, challenge, managed_challenge, js_challenge, or whitelist")
				}
				target, value := accessRuleTarget(args[0])
				if t, _ := c.Flags().GetString("target"); t != "" {
					target = t
				}
				if target == "" {
					return nil, fmt.Errorf("can't tell what %q is; pass --target ip|ip6|ip_range|asn|country", args[0])
				}
				notes, _ := c.Flags().GetString("notes")
				return map[string]any{"mode": mode, "configuration": map[string]any{"target": target, "value": value}, "notes": notes}, nil
			},
			Flags: func(c *cobra.Command) {
				c.Flags().String("mode", "block", "block, challenge, managed_challenge, js_challenge, or whitelist")
				c.Flags().String("target", "", "Override the detected target: ip, ip6, ip_range, asn, country")
				c.Flags().String("notes", "", "Notes")
			},
			Done: "Access rule created for %s", Feature: "IP Access Rules",
		}.build(),
		writeSpec{
			Use: "delete <rule-id>", Short: "Delete an IP Access Rule", Method: "DELETE", Scope: scopeEither,
			Path: "/zones/{zone_id}/firewall/access_rules/rules/{rule_id}", AcctPath: "/accounts/{account_id}/firewall/access_rules/rules/{rule_id}",
			Args: []string{"rule_id"}, Confirm: "delete access rule %s", Done: "Access rule %s deleted", Feature: "IP Access Rules",
		}.build(),
	),
)

func init() { rootCmd.AddCommand(firewallCmd) }
