package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Rulesets engine: the base for redirects, transform rules, WAF custom rules,
// rate limits, cache rules, origin rules, and config rules. Zone level with
// --zone, account level otherwise.

var rulesetCols = []col{{"ID", "id"}, {"Name", "name"}, {"Kind", "kind"}, {"Phase", "phase"}, {"Version", "version"}, {"Modified", "last_updated"}}
var ruleCols = []col{{"ID", "id"}, {"On", "enabled"}, {"Action", "action"}, {"Description", "description"}, {"Expression", "expression"}}

// phaseAliases are friendly names for common phases.
var phaseAliases = map[string]string{
	"redirect":          "http_request_dynamic_redirect",
	"redirects":         "http_request_dynamic_redirect",
	"bulk-redirect":     "http_request_redirect",
	"url-rewrite":       "http_request_transform",
	"request-headers":   "http_request_late_transform",
	"response-headers":  "http_response_headers_transform",
	"waf":               "http_request_firewall_custom",
	"custom":            "http_request_firewall_custom",
	"managed":           "http_request_firewall_managed",
	"rate-limit":        "http_ratelimit",
	"ratelimit":         "http_ratelimit",
	"cache":             "http_request_cache_settings",
	"origin":            "http_request_origin",
	"config":            "http_config_settings",
	"compression":       "http_response_compression",
	"custom-errors":     "http_custom_errors",
	"sbfm":              "http_request_sbfm",
	"single-redirect":   "http_request_dynamic_redirect",
	"late-transform":    "http_request_late_transform",
	"response-firewall": "http_response_firewall_managed",
}

func resolvePhase(p string) string {
	if v, ok := phaseAliases[strings.ToLower(p)]; ok {
		return v
	}
	return p
}

var rulesetsCmd = &cobra.Command{
	Use:     "rulesets",
	Aliases: []string{"ruleset"},
	Short:   "Rulesets engine: list rulesets, phase entrypoints, and add/update/delete rules",
	Long: `Work with Cloudflare's Rulesets engine (the base of redirect, transform,
WAF custom, rate limiting, cache, origin, and config rules).

Pass --zone <zone> for zone-level rulesets; without it, commands work at the
account level.

Phase names can be given in full (http_request_dynamic_redirect) or as an
alias: redirect, bulk-redirect, url-rewrite, request-headers,
response-headers, waf (custom), managed, rate-limit, cache, origin, config,
compression, custom-errors.

Examples:
  cfctl rulesets list --zone example.com
  cfctl rulesets get <ruleset-id> --zone example.com
  cfctl rulesets phase waf --zone example.com
  cfctl rulesets rules add --zone example.com --phase waf --expression '(ip.src.country eq "T1")' --action block --description "Block Tor"
  cfctl rulesets rules update <ruleset-id> <rule-id> --zone example.com --enabled=false
  cfctl rulesets rules delete <ruleset-id> <rule-id> --zone example.com

Generated equivalents: cfctl api zone-rulesets ..., cfctl api account-rulesets ...`,
}

var rulesetsListCmd = readSpec{
	Use: "list", Short: "List rulesets (zone with --zone, else account)", Scope: scopeEither,
	Path: "/zones/{zone_id}/rulesets", AcctPath: "/accounts/{account_id}/rulesets",
	Title: "Rulesets", Cols: rulesetCols, Feature: "rulesets",
	Flags: func(c *cobra.Command) {
		c.Flags().String("kind", "", "Only rulesets of this kind (zone, managed, custom, root)")
		c.Flags().String("phase", "", "Only rulesets in this phase (name or alias)")
	},
}.filtered()

// filtered applies --kind/--phase on the client side.
func (r readSpec) filtered() *cobra.Command {
	r.Human = nil
	c := r.build()
	inner := r
	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		s, path, vals, err := resolveScope(cmd, inner.Scope, inner.Path, inner.AcctPath, nil, args)
		if err != nil {
			return err
		}
		raw, err := fetch(ctx, s, path, vals, nil, true, inner.Feature)
		if err != nil {
			return err
		}
		kind, _ := cmd.Flags().GetString("kind")
		phase, _ := cmd.Flags().GetString("phase")
		phase = resolvePhase(phase)
		var keep []any
		for _, it := range asList(raw, "") {
			if (kind == "" || jstr(it, "kind") == kind) && (phase == "" || jstr(it, "phase") == phase) {
				keep = append(keep, it)
			}
		}
		if keep == nil {
			keep = []any{}
		}
		b, _ := json.Marshal(keep)
		return emit(b, func(v any) { printTable(inner.Title, keep, inner.Cols) })
	}
	return c
}

// printRuleset prints a ruleset header plus its rules.
func printRuleset(v any) {
	printDetail("Ruleset "+jstr(v, "name"), v, []col{{"ID", "id"}, {"Kind", "kind"}, {"Phase", "phase"}, {"Version", "version"}, {"Description", "description"}, {"Updated", "last_updated"}})
	rules, _ := jget(v, "rules").([]any)
	fmt.Println()
	printTable("Rules", rules, ruleCols)
}

var rulesetsGetCmd = readSpec{
	Use: "get <ruleset-id>", Short: "Show a ruleset and its rules", Scope: scopeEither,
	Path: "/zones/{zone_id}/rulesets/{ruleset_id}", AcctPath: "/accounts/{account_id}/rulesets/{ruleset_id}",
	Args: []string{"ruleset_id"}, Feature: "rulesets", Human: printRuleset,
}.build()

var rulesetsPhaseCmd = &cobra.Command{
	Use:   "phase <phase>",
	Short: "Show a phase's entrypoint ruleset (the rules that run in that phase)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		zone, _ := cmd.Flags().GetString("zone")
		s, err := adminSession(cmd, zone)
		if err != nil {
			return err
		}
		phase := resolvePhase(args[0])
		raw, found, err := getEntrypoint(ctx, s, zone != "", phase, "rulesets")
		if err != nil {
			return err
		}
		if !found {
			if jsonOutput {
				return printJSONValue(map[string]any{"phase": phase, "rules": []any{}})
			}
			fmt.Println(ui.Warn("No rules in phase " + phase))
			return nil
		}
		return emit(raw, printRuleset)
	},
}

// entrypointPath returns the phase entrypoint path at zone or account level.
func entrypointPath(zone bool) string {
	if zone {
		return "/zones/{zone_id}/rulesets/phases/{phase}/entrypoint"
	}
	return "/accounts/{account_id}/rulesets/phases/{phase}/entrypoint"
}

func rulesetBase(zone bool) string {
	if zone {
		return "/zones/{zone_id}/rulesets"
	}
	return "/accounts/{account_id}/rulesets"
}

// getEntrypoint fetches a phase entrypoint; found is false when the phase
// has no entrypoint yet (404).
func getEntrypoint(ctx context.Context, s *apiSession, zone bool, phase, feature string) (json.RawMessage, bool, error) {
	raw, err := fetch(ctx, s, entrypointPath(zone), map[string]string{"phase": phase}, nil, false, feature)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == 404 {
			return nil, false, nil
		}
		return nil, false, err
	}
	return raw, true, nil
}

// addPhaseRule appends a rule to a phase's entrypoint, creating the
// entrypoint if the phase has none yet.
func addPhaseRule(ctx context.Context, s *apiSession, zone bool, phase string, rule map[string]any, feature string) (json.RawMessage, error) {
	raw, found, err := getEntrypoint(ctx, s, zone, phase, feature)
	if err != nil {
		return nil, err
	}
	if !found {
		body := map[string]any{"rules": []any{rule}}
		return send(ctx, s, "PUT", entrypointPath(zone), map[string]string{"phase": phase}, nil, body, feature)
	}
	id := jstr(decodeAny(raw), "id")
	return send(ctx, s, "POST", rulesetBase(zone)+"/{ruleset_id}/rules", map[string]string{"ruleset_id": id}, nil, rule, feature)
}

// phaseRuleID finds the entrypoint ruleset of a phase and checks a rule is in it.
func phaseRulesetID(ctx context.Context, s *apiSession, zone bool, phase, ruleID, feature string) (string, error) {
	raw, found, err := getEntrypoint(ctx, s, zone, phase, feature)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("no rules in phase %s", phase)
	}
	v := decodeAny(raw)
	rules, _ := jget(v, "rules").([]any)
	for _, r := range rules {
		if jstr(r, "id") == ruleID {
			return jstr(v, "id"), nil
		}
	}
	return "", fmt.Errorf("rule %s not found in phase %s", ruleID, phase)
}

// ruleFromFlags builds a rule body from the common rule flags.
func ruleFromFlags(c *cobra.Command, requireCore bool) (map[string]any, error) {
	rule := map[string]any{}
	if v, _ := c.Flags().GetString("expression"); v != "" {
		rule["expression"] = v
	}
	if v, _ := c.Flags().GetString("action"); v != "" {
		rule["action"] = v
	}
	if v, _ := c.Flags().GetString("description"); v != "" {
		rule["description"] = v
	}
	if c.Flags().Changed("enabled") {
		v, _ := c.Flags().GetBool("enabled")
		rule["enabled"] = v
	}
	if v, _ := c.Flags().GetString("action-parameters"); v != "" {
		raw, err := api.ReadData(v, os.Stdin)
		if err != nil {
			return nil, err
		}
		var ap any
		if err := json.Unmarshal(raw, &ap); err != nil {
			return nil, fmt.Errorf("--action-parameters is not valid JSON: %w", err)
		}
		rule["action_parameters"] = ap
	}
	merged, err := mergeData(c, rule)
	if err != nil {
		return nil, err
	}
	rule, _ = merged.(map[string]any)
	if requireCore && (rule["expression"] == nil || rule["action"] == nil) {
		return nil, fmt.Errorf("a rule needs --expression and --action (or --data)")
	}
	return rule, nil
}

func addRuleFlags(c *cobra.Command) {
	c.Flags().String("expression", "", "Rule expression, e.g. '(http.host eq \"example.com\")'")
	c.Flags().String("action", "", "Rule action (block, challenge, managed_challenge, js_challenge, log, skip, redirect, rewrite, route, set_config, set_cache_settings, execute, ...)")
	c.Flags().String("description", "", "Rule description")
	c.Flags().Bool("enabled", true, "Whether the rule is enabled")
	c.Flags().String("action-parameters", "", "action_parameters as JSON (or @file)")
	c.Flags().String("data", "", "Full rule JSON (or @file, or -); merged over the flags")
}

var rulesetsRulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Add, update, and delete rules in a ruleset",
}

var rulesetsRulesAddCmd = &cobra.Command{
	Use:   "add [ruleset-id]",
	Short: "Add a rule to a ruleset, or to a phase entrypoint with --phase",
	Long: `Add a rule. Give a ruleset ID, or --phase to add to that phase's entrypoint
(created if the phase has none yet).

Examples:
  cfctl rulesets rules add --zone example.com --phase waf --expression '(cf.threat_score gt 50)' --action managed_challenge
  cfctl rulesets rules add <ruleset-id> --zone example.com --expression '(http.request.uri.path eq "/old")' --action redirect --action-parameters '{"from_value":{"target_url":{"value":"https://example.com/new"},"status_code":301}}'`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		rule, err := ruleFromFlags(cmd, true)
		if err != nil {
			return err
		}
		zone, _ := cmd.Flags().GetString("zone")
		phase, _ := cmd.Flags().GetString("phase")
		if (len(args) == 0) == (phase == "") {
			return fmt.Errorf("give either a ruleset ID or --phase")
		}
		s, err := adminSession(cmd, zone)
		if err != nil {
			return err
		}
		var raw json.RawMessage
		if phase != "" {
			raw, err = addPhaseRule(ctx, s, zone != "", resolvePhase(phase), rule, "rulesets")
		} else {
			raw, err = send(ctx, s, "POST", rulesetBase(zone != "")+"/{ruleset_id}/rules", map[string]string{"ruleset_id": args[0]}, nil, rule, "rulesets")
		}
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			rules, _ := jget(v, "rules").([]any)
			id := ""
			if len(rules) > 0 {
				id = jstr(rules[len(rules)-1], "id")
			}
			fmt.Println(ui.Success(fmt.Sprintf("Rule %s added to ruleset %s (version %s)", id, jstr(v, "id"), jstr(v, "version"))))
		})
	},
}

var rulesetsRulesUpdateCmd = &cobra.Command{
	Use:   "update <ruleset-id> <rule-id>",
	Short: "Update a rule (only the fields you pass change; the rest are kept)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		rule, err := ruleFromFlags(cmd, false)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("enabled") {
			delete(rule, "enabled")
		}
		if len(rule) == 0 {
			return fmt.Errorf("nothing to update: pass --expression, --action, --description, --enabled, --action-parameters, or --data")
		}
		zone, _ := cmd.Flags().GetString("zone")
		s, err := adminSession(cmd, zone)
		if err != nil {
			return err
		}
		// PATCH replaces the whole rule definition, so merge the changes
		// over the current rule (fields left out would otherwise be reset).
		vals := map[string]string{"ruleset_id": args[0], "rule_id": args[1]}
		cur, err := fetch(ctx, s, rulesetBase(zone != "")+"/{ruleset_id}", vals, nil, false, "rulesets")
		if err != nil {
			return err
		}
		full, err := mergeRule(cur, args[1], rule)
		if err != nil {
			return err
		}
		raw, err := send(ctx, s, "PATCH", rulesetBase(zone != "")+"/{ruleset_id}/rules/{rule_id}", vals, nil, full, "rulesets")
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			fmt.Println(ui.Success(fmt.Sprintf("Rule %s updated (ruleset %s version %s)", args[1], args[0], jstr(v, "version"))))
		})
	},
}

// mergeRule finds ruleID in a ruleset and returns it with changes applied,
// minus the read-only fields.
func mergeRule(ruleset json.RawMessage, ruleID string, changes map[string]any) (map[string]any, error) {
	var rs struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(ruleset, &rs); err != nil {
		return nil, fmt.Errorf("unexpected ruleset response: %w", err)
	}
	for _, r := range rs.Rules {
		if id, _ := r["id"].(string); id == ruleID {
			for _, k := range []string{"id", "version", "last_updated"} {
				delete(r, k)
			}
			for k, v := range changes {
				r[k] = v
			}
			return r, nil
		}
	}
	return nil, fmt.Errorf("rule %s not found in ruleset", ruleID)
}

var rulesetsRulesDeleteCmd = writeSpec{
	Use: "delete <ruleset-id> <rule-id>", Short: "Delete a rule from a ruleset", Method: "DELETE", Scope: scopeEither,
	Path: "/zones/{zone_id}/rulesets/{ruleset_id}/rules/{rule_id}", AcctPath: "/accounts/{account_id}/rulesets/{ruleset_id}/rules/{rule_id}",
	Args: []string{"ruleset_id", "rule_id"}, Confirm: "delete rule %s", Feature: "rulesets",
	Human: func(v any) {
		fmt.Println(ui.Success("Rule deleted (ruleset " + jstr(v, "id") + " now at version " + jstr(v, "version") + ")"))
	},
}.build()

var rulesetsDeleteCmd = writeSpec{
	Use: "delete <ruleset-id>", Short: "Delete a custom ruleset", Method: "DELETE", Scope: scopeEither,
	Path: "/zones/{zone_id}/rulesets/{ruleset_id}", AcctPath: "/accounts/{account_id}/rulesets/{ruleset_id}",
	Args: []string{"ruleset_id"}, Confirm: "delete ruleset %s and all its rules", Done: "Ruleset %s deleted", Feature: "rulesets",
}.build()

var rulesetsVersionsCmd = readSpec{
	Use: "versions <ruleset-id>", Short: "List a ruleset's versions", Scope: scopeEither,
	Path: "/zones/{zone_id}/rulesets/{ruleset_id}/versions", AcctPath: "/accounts/{account_id}/rulesets/{ruleset_id}/versions",
	Args: []string{"ruleset_id"}, Title: "Versions", Cols: []col{{"Version", "version"}, {"Updated", "last_updated"}, {"Description", "description"}}, Feature: "rulesets",
}.build()

// phaseListCmd lists the rules of one or more phases (redirects, transform, waf).
func phaseListCmd(use, short, title string, phases []string, cols []col, feature string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			s, err := adminSession(cmd, args[0])
			if err != nil {
				return err
			}
			var all []any
			for _, ph := range phases {
				raw, found, err := getEntrypoint(ctx, s, true, ph, feature)
				if err != nil {
					return err
				}
				if !found {
					continue
				}
				v := decodeAny(raw)
				rules, _ := jget(v, "rules").([]any)
				for _, r := range rules {
					if m, ok := r.(map[string]any); ok {
						m["phase"] = ph
						m["ruleset_id"] = jstr(v, "id")
					}
					all = append(all, r)
				}
			}
			if all == nil {
				all = []any{}
			}
			b, _ := json.Marshal(all)
			return emit(b, func(any) { printTable(title, all, cols) })
		},
	}
}

// phaseDeleteCmd deletes a rule from a zone phase entrypoint by rule ID.
func phaseDeleteCmd(use, short string, phases []string, feature string) *cobra.Command {
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			s, err := adminSession(cmd, args[0])
			if err != nil {
				return err
			}
			var rsID string
			for _, ph := range phases {
				if id, err := phaseRulesetID(ctx, s, true, ph, args[1], feature); err == nil {
					rsID = id
					break
				}
			}
			if rsID == "" {
				return fmt.Errorf("rule %s not found", args[1])
			}
			if err := confirm(cmd, fmt.Sprintf("delete rule %s (zone %s)", args[1], args[0])); err != nil {
				return err
			}
			raw, err := send(ctx, s, "DELETE", "/zones/{zone_id}/rulesets/{ruleset_id}/rules/{rule_id}", map[string]string{"ruleset_id": rsID, "rule_id": args[1]}, nil, nil, feature)
			if err != nil {
				return err
			}
			return emit(raw, func(any) { fmt.Println(ui.Success("Rule " + args[1] + " deleted")) })
		},
	}
	c.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	return c
}

// queryFlag is a small helper for building query strings from flags.
func setIf(q url.Values, key, val string) {
	if val != "" {
		q.Set(key, val)
	}
}

func init() {
	rootCmd.AddCommand(rulesetsCmd)
	rulesetsCmd.AddCommand(rulesetsListCmd, rulesetsGetCmd, rulesetsPhaseCmd, rulesetsRulesCmd, rulesetsDeleteCmd, rulesetsVersionsCmd)
	rulesetsRulesCmd.AddCommand(rulesetsRulesAddCmd, rulesetsRulesUpdateCmd, rulesetsRulesDeleteCmd)
	for _, c := range []*cobra.Command{rulesetsPhaseCmd, rulesetsRulesAddCmd, rulesetsRulesUpdateCmd} {
		c.Flags().String("zone", "", "Zone name or ID (default: account level)")
	}
	addRuleFlags(rulesetsRulesAddCmd)
	addRuleFlags(rulesetsRulesUpdateCmd)
	rulesetsRulesAddCmd.Flags().String("phase", "", "Add to this phase's entrypoint (name or alias) instead of a ruleset ID")
}
