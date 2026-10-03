package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/r2"
	"github.com/spf13/cobra"
)

// Bucket settings: cors, lifecycle, lock, domain, dev-url, notification,
// sippy, local-uploads, jobs. Each lives under `cfctl r2 buckets <setting>`.

// r2GetConfig fetches a bucket sub-resource (…/buckets/{b}/<sub>).
func r2GetConfig(c *stClient, cmd *cobra.Command, bucket string, sub ...string) (json.RawMessage, error) {
	return c.result(stReq{Method: "GET", Path: r2.BucketPath(c.acct, bucket, sub...), Header: r2Header(cmd)})
}

// r2PutConfig replaces a bucket sub-resource.
func r2PutConfig(c *stClient, cmd *cobra.Command, bucket string, body any, sub ...string) (json.RawMessage, error) {
	return c.result(stReq{Method: "PUT", Path: r2.BucketPath(c.acct, bucket, sub...), Header: r2Header(cmd), Body: body})
}

// r2Rules decodes {"rules": [...]} into maps.
func r2Rules(raw json.RawMessage) []map[string]any {
	var w struct {
		Rules []map[string]any `json:"rules"`
	}
	_ = json.Unmarshal(raw, &w)
	if w.Rules == nil {
		w.Rules = []map[string]any{}
	}
	return w.Rules
}

// r2ReadRulesFile reads a rules file: {"rules": [...]} or a bare array.
func r2ReadRulesFile(spec string) ([]any, error) {
	if spec != "-" {
		spec = "@" + strings.TrimPrefix(spec, "@")
	}
	data, err := stReadInput(spec)
	if err != nil {
		return nil, err
	}
	var arr []any
	if json.Unmarshal(data, &arr) == nil {
		return arr, nil
	}
	var obj struct {
		Rules []any `json:"rules"`
	}
	if err := json.Unmarshal(data, &obj); err != nil || obj.Rules == nil {
		return nil, fmt.Errorf("want a JSON array of rules or {\"rules\": [...]}")
	}
	return obj.Rules, nil
}

// r2Days converts days to seconds (for Age conditions).
func r2Days(d int) int { return d * 86400 }

func r2AgeOrDate(days int, date string) (map[string]any, error) {
	if date != "" {
		t, err := stParseTime(date)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "Date", "date": t.Format(time.RFC3339)}, nil
	}
	return map[string]any{"type": "Age", "maxAge": r2Days(days)}, nil
}

func r2Duration(sec any) string {
	f, ok := sec.(float64)
	if !ok {
		return ""
	}
	if int64(f)%86400 == 0 {
		return fmt.Sprintf("%d days", int64(f)/86400)
	}
	return (time.Duration(f) * time.Second).String()
}

func r2CondString(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	switch m["type"] {
	case "Age":
		if d := r2Duration(m["maxAge"]); d != "" {
			return "after " + d
		}
		return "after " + r2Duration(m["maxAgeSeconds"])
	case "Date":
		return "on " + stString(m["date"])
	case "Indefinite":
		return "indefinitely"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// --- CORS -------------------------------------------------------------------

var r2CorsCmd = &cobra.Command{Use: "cors", Short: "Show, set, or delete a bucket's CORS rules"}

var r2CorsListCmd = &cobra.Command{
	Use:     "list <bucket>",
	Aliases: []string{"get"},
	Short:   "Show CORS rules",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2GetConfig(c, cmd, args[0], "cors")
		if err != nil {
			if r2IsCode(err, 10059) || r2IsStatus(err, 404) {
				raw = json.RawMessage(`{"rules":[]}`)
			} else {
				return err
			}
		}
		return stEmit(raw, func() error {
			stList("🌐 %d CORS rules for "+args[0], "No CORS rules for "+args[0], r2Rules(raw), []stCol{
				{Header: "ID", Path: "id"},
				{Header: "ORIGINS", Path: "allowed.origins"},
				{Header: "METHODS", Path: "allowed.methods"},
				{Header: "HEADERS", Path: "allowed.headers"},
				{Header: "EXPOSE", Path: "exposeHeaders"},
				{Header: "MAX AGE", Path: "maxAgeSeconds"},
			})
			return nil
		})
	},
}

var r2CorsSetCmd = &cobra.Command{
	Use:   "set <bucket> --file rules.json",
	Short: "Replace CORS rules from a JSON file",
	Long: `Replace a bucket's CORS rules. The file holds {"rules": [...]} (the API and
wrangler format) or a bare array of rules:

  {"rules": [{"allowed": {"origins": ["https://example.com"], "methods": ["GET", "HEAD"], "headers": ["*"]},
              "exposeHeaders": ["ETag"], "maxAgeSeconds": 3600}]}`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		if file == "" {
			return fmt.Errorf("--file is required")
		}
		rules, err := r2ReadRulesFile(file)
		if err != nil {
			return err
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		if err := confirm(cmd, fmt.Sprintf("replace the CORS rules of %s with %d rules", args[0], len(rules))); err != nil {
			return err
		}
		raw, err := r2PutConfig(c, cmd, args[0], map[string]any{"rules": rules}, "cors")
		if err != nil {
			return err
		}
		return stOK(raw, fmt.Sprintf("Set %d CORS rules on %s", len(rules), args[0]))
	},
}

var r2CorsDeleteCmd = &cobra.Command{
	Use:   "delete <bucket>",
	Short: "Delete all CORS rules",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		if err := confirm(cmd, "delete the CORS rules of "+args[0]); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "DELETE", Path: r2.BucketPath(c.acct, args[0], "cors"), Header: r2Header(cmd)})
		if err != nil {
			return err
		}
		return stOK(raw, "Deleted the CORS rules of "+args[0])
	},
}

func r2IsStatus(err error, status int) bool {
	var e *api.Error
	return errors.As(err, &e) && e.Status == status
}

func r2IsCode(err error, code int) bool {
	var e *api.Error
	if !errors.As(err, &e) {
		return false
	}
	for _, m := range e.Errors {
		if m.Code == code {
			return true
		}
	}
	return false
}

// --- lifecycle --------------------------------------------------------------

var r2LifecycleCmd = &cobra.Command{Use: "lifecycle", Short: "List, add, remove, or replace object lifecycle rules"}

var r2LifecycleListCmd = &cobra.Command{
	Use:     "list <bucket>",
	Aliases: []string{"get"},
	Short:   "List lifecycle rules",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2GetConfig(c, cmd, args[0], "lifecycle")
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			rules := r2Rules(raw)
			for _, r := range rules {
				var actions []string
				if t, ok := r["deleteObjectsTransition"].(map[string]any); ok {
					actions = append(actions, "delete objects "+r2CondString(t["condition"]))
				}
				if t, ok := r["abortMultipartUploadsTransition"].(map[string]any); ok {
					actions = append(actions, "abort uploads "+r2CondString(t["condition"]))
				}
				if ts, ok := r["storageClassTransitions"].([]any); ok {
					for _, x := range ts {
						if t, ok := x.(map[string]any); ok {
							actions = append(actions, fmt.Sprintf("→ %v %s", t["storageClass"], r2CondString(t["condition"])))
						}
					}
				}
				r["_actions"] = strings.Join(actions, "; ")
			}
			stList("♻️  %d lifecycle rules for "+args[0], "No lifecycle rules for "+args[0], rules, []stCol{
				{Header: "ID", Path: "id"},
				{Header: "ENABLED", Path: "enabled"},
				{Header: "PREFIX", Path: "conditions.prefix"},
				{Header: "ACTIONS", Path: "_actions"},
			})
			return nil
		})
	},
}

var r2LifecycleAddCmd = &cobra.Command{
	Use:   "add <bucket>",
	Short: "Add a lifecycle rule",
	Long: `Add a lifecycle rule (the existing rules are kept).

Examples:
  cfctl r2 buckets lifecycle add my-bucket --id expire-tmp --prefix tmp/ --expire-days 7
  cfctl r2 buckets lifecycle add my-bucket --id to-ia --prefix logs/ --ia-transition-days 30
  cfctl r2 buckets lifecycle add my-bucket --id cleanup --abort-multipart-days 1
  cfctl r2 buckets lifecycle add my-bucket --id eol --expire-date 2027-01-01`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		prefix, _ := cmd.Flags().GetString("prefix")
		expDays, _ := cmd.Flags().GetInt("expire-days")
		expDate, _ := cmd.Flags().GetString("expire-date")
		iaDays, _ := cmd.Flags().GetInt("ia-transition-days")
		iaDate, _ := cmd.Flags().GetString("ia-transition-date")
		abortDays, _ := cmd.Flags().GetInt("abort-multipart-days")
		disabled, _ := cmd.Flags().GetBool("disabled")
		if id == "" {
			id = fmt.Sprintf("rule-%d", time.Now().Unix())
		}
		rule := map[string]any{"id": id, "enabled": !disabled, "conditions": map[string]any{"prefix": prefix}}
		if expDays > 0 || expDate != "" {
			cond, err := r2AgeOrDate(expDays, expDate)
			if err != nil {
				return err
			}
			rule["deleteObjectsTransition"] = map[string]any{"condition": cond}
		}
		if iaDays > 0 || iaDate != "" {
			cond, err := r2AgeOrDate(iaDays, iaDate)
			if err != nil {
				return err
			}
			rule["storageClassTransitions"] = []any{map[string]any{"condition": cond, "storageClass": r2.InfrequentAccess}}
		}
		if abortDays > 0 {
			rule["abortMultipartUploadsTransition"] = map[string]any{"condition": map[string]any{"type": "Age", "maxAge": r2Days(abortDays)}}
		}
		if len(rule) == 3 {
			return fmt.Errorf("give at least one action: --expire-days/--expire-date, --ia-transition-days/--ia-transition-date, or --abort-multipart-days")
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2GetConfig(c, cmd, args[0], "lifecycle")
		if err != nil {
			return err
		}
		rules := r2Rules(raw)
		for _, r := range rules {
			if r["id"] == id {
				return fmt.Errorf("a rule with id %q already exists; remove it first", id)
			}
		}
		rules = append(rules, rule)
		res, err := r2PutConfig(c, cmd, args[0], map[string]any{"rules": rules}, "lifecycle")
		if err != nil {
			return err
		}
		return stOK(res, fmt.Sprintf("Added lifecycle rule %s to %s", id, args[0]))
	},
}

// r2RemoveRule implements `<setting> remove <bucket> --id X` for rule lists.
func r2RemoveRule(setting, title string) *cobra.Command {
	c := &cobra.Command{
		Use:   "remove <bucket> --id <rule-id>",
		Short: "Remove a " + title + " rule by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("id")
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			c, err := newST(cmd)
			if err != nil {
				return err
			}
			raw, err := r2GetConfig(c, cmd, args[0], setting)
			if err != nil {
				return err
			}
			var kept []map[string]any
			found := false
			for _, r := range r2Rules(raw) {
				if r["id"] == id {
					found = true
					continue
				}
				kept = append(kept, r)
			}
			if !found {
				return fmt.Errorf("no %s rule with id %q on %s", title, id, args[0])
			}
			if kept == nil {
				kept = []map[string]any{}
			}
			if err := confirm(cmd, fmt.Sprintf("remove %s rule %s from %s", title, id, args[0])); err != nil {
				return err
			}
			res, err := r2PutConfig(c, cmd, args[0], map[string]any{"rules": kept}, setting)
			if err != nil {
				return err
			}
			return stOK(res, fmt.Sprintf("Removed %s rule %s from %s", title, id, args[0]))
		},
	}
	c.Flags().String("id", "", "Rule ID")
	stYes(c)
	return c
}

// r2SetRules implements `<setting> set <bucket> --file rules.json`.
func r2SetRules(setting, title string) *cobra.Command {
	c := &cobra.Command{
		Use:   "set <bucket> --file rules.json",
		Short: "Replace all " + title + " rules from a JSON file ({\"rules\": [...]} or an array)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			rules, err := r2ReadRulesFile(file)
			if err != nil {
				return err
			}
			c, err := newST(cmd)
			if err != nil {
				return err
			}
			if err := confirm(cmd, fmt.Sprintf("replace the %s rules of %s with %d rules", title, args[0], len(rules))); err != nil {
				return err
			}
			res, err := r2PutConfig(c, cmd, args[0], map[string]any{"rules": rules}, setting)
			if err != nil {
				return err
			}
			return stOK(res, fmt.Sprintf("Set %d %s rules on %s", len(rules), title, args[0]))
		},
	}
	c.Flags().String("file", "", "JSON file with the rules (- for stdin)")
	stYes(c)
	return c
}

// --- lock -------------------------------------------------------------------

var r2LockCmd = &cobra.Command{Use: "lock", Short: "List, add, remove, or replace bucket lock (retention) rules"}

var r2LockListCmd = &cobra.Command{
	Use:     "list <bucket>",
	Aliases: []string{"get"},
	Short:   "List lock rules",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2GetConfig(c, cmd, args[0], "lock")
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("🔒 %d lock rules for "+args[0], "No lock rules for "+args[0], r2Rules(raw), []stCol{
				{Header: "ID", Path: "id"},
				{Header: "ENABLED", Path: "enabled"},
				{Header: "PREFIX", Path: "prefix"},
				{Header: "RETENTION", Path: "condition", Fmt: r2CondString},
			})
			return nil
		})
	},
}

var r2LockAddCmd = &cobra.Command{
	Use:   "add <bucket>",
	Short: "Add a lock rule (objects can't be overwritten or deleted while locked)",
	Long: `Add a lock rule (the existing rules are kept).

Examples:
  cfctl r2 buckets lock add my-bucket --id keep-30d --prefix backups/ --retention-days 30
  cfctl r2 buckets lock add my-bucket --id legal --prefix contracts/ --retention-indefinite
  cfctl r2 buckets lock add my-bucket --id until --retention-date 2027-06-01`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("id")
		prefix, _ := cmd.Flags().GetString("prefix")
		days, _ := cmd.Flags().GetInt("retention-days")
		date, _ := cmd.Flags().GetString("retention-date")
		indef, _ := cmd.Flags().GetBool("retention-indefinite")
		disabled, _ := cmd.Flags().GetBool("disabled")
		var cond map[string]any
		switch {
		case indef:
			cond = map[string]any{"type": "Indefinite"}
		case date != "":
			t, err := stParseTime(date)
			if err != nil {
				return err
			}
			cond = map[string]any{"type": "Date", "date": t.Format(time.RFC3339)}
		case days > 0:
			cond = map[string]any{"type": "Age", "maxAgeSeconds": r2Days(days)}
		default:
			return fmt.Errorf("give --retention-days, --retention-date, or --retention-indefinite")
		}
		if id == "" {
			id = fmt.Sprintf("lock-%d", time.Now().Unix())
		}
		rule := map[string]any{"id": id, "enabled": !disabled, "condition": cond}
		if prefix != "" {
			rule["prefix"] = prefix
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2GetConfig(c, cmd, args[0], "lock")
		if err != nil {
			return err
		}
		rules := r2Rules(raw)
		for _, r := range rules {
			if r["id"] == id {
				return fmt.Errorf("a lock rule with id %q already exists; remove it first", id)
			}
		}
		if err := confirm(cmd, fmt.Sprintf("lock objects under %q in %s %s", prefix, args[0], r2CondString(cond))); err != nil {
			return err
		}
		res, err := r2PutConfig(c, cmd, args[0], map[string]any{"rules": append(rules, rule)}, "lock")
		if err != nil {
			return err
		}
		return stOK(res, fmt.Sprintf("Added lock rule %s to %s", id, args[0]))
	},
}

// --- custom domains ---------------------------------------------------------

var r2DomainCmd = &cobra.Command{Use: "domain", Aliases: []string{"domains"}, Short: "List, add, update, or remove custom domains"}

var r2DomainListCmd = &cobra.Command{
	Use:   "list <bucket>",
	Short: "List custom domains",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2GetConfig(c, cmd, args[0], "domains", "custom")
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("🌍 %d custom domains for "+args[0], "No custom domains for "+args[0], stItems(raw, "domains"), []stCol{
				{Header: "DOMAIN", Path: "domain"},
				{Header: "ENABLED", Path: "enabled"},
				{Header: "SSL", Path: "status.ssl"},
				{Header: "OWNERSHIP", Path: "status.ownership"},
				{Header: "MIN TLS", Path: "minTLS"},
				{Header: "ZONE", Path: "zoneName"},
			})
			return nil
		})
	},
}

var r2DomainGetCmd = &cobra.Command{
	Use:   "get <bucket> <domain>",
	Short: "Show a custom domain's settings",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2GetConfig(c, cmd, args[0], "domains", "custom", args[1])
		if err != nil {
			return err
		}
		return stEmit(raw, func() error { return stDetail("🌍 "+args[1], raw) })
	},
}

var r2DomainAddCmd = &cobra.Command{
	Use:   "add <bucket> --domain <domain>",
	Short: "Connect a custom domain (public access through it)",
	Long: `Connect a custom domain to a bucket. The domain's zone must be in this
account; it is found from the domain unless --zone is given.

Examples:
  cfctl r2 buckets domain add my-bucket --domain assets.example.com
  cfctl r2 buckets domain add my-bucket --domain cdn.example.com --zone example.com --min-tls 1.2`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		domain, _ := cmd.Flags().GetString("domain")
		if domain == "" {
			return fmt.Errorf("--domain is required")
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		zoneID, err := r2ZoneFor(c, cmd, domain)
		if err != nil {
			return err
		}
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		body["domain"] = domain
		body["zoneId"] = zoneID
		disabled, _ := cmd.Flags().GetBool("disabled")
		body["enabled"] = !disabled
		stFlagStr(cmd, body, "min-tls", "minTLS")
		stFlagList(cmd, body, "ciphers", "ciphers")
		raw, err := c.result(stReq{Method: "POST", Path: r2.BucketPath(c.acct, args[0], "domains", "custom"), Header: r2Header(cmd), Body: body})
		if err != nil {
			return err
		}
		return stOK(raw, fmt.Sprintf("Connected %s to %s (it may take a few minutes to become active)", domain, args[0]))
	},
}

// r2ZoneFor finds the zone ID for a domain: --zone (name or ID), else the
// longest zone name that the domain ends with.
func r2ZoneFor(c *stClient, cmd *cobra.Command, domain string) (string, error) {
	if z, _ := cmd.Flags().GetString("zone"); z != "" {
		c.s.zoneArg = z
		return c.s.zone(c.ctx)
	}
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(domain), "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		c.s.zoneArg, c.s.zoneID = strings.Join(labels[i:], "."), ""
		if id, err := c.s.zone(c.ctx); err == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("no zone in this account matches %s; pass --zone", domain)
}

var r2DomainUpdateCmd = &cobra.Command{
	Use:   "update <bucket> <domain>",
	Short: "Enable/disable a custom domain or change its TLS settings",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		stFlagBool(cmd, body, "enabled", "enabled")
		stFlagStr(cmd, body, "min-tls", "minTLS")
		stFlagList(cmd, body, "ciphers", "ciphers")
		if len(body) == 0 {
			return fmt.Errorf("nothing to change: pass --enabled, --min-tls, --ciphers, or --data")
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "PUT", Path: r2.BucketPath(c.acct, args[0], "domains", "custom", args[1]), Header: r2Header(cmd), Body: body})
		if err != nil {
			return err
		}
		return stOK(raw, "Updated "+args[1])
	},
}

var r2DomainRemoveCmd = &cobra.Command{
	Use:   "remove <bucket> <domain>",
	Short: "Disconnect a custom domain",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		if err := confirm(cmd, fmt.Sprintf("disconnect %s from %s", args[1], args[0])); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "DELETE", Path: r2.BucketPath(c.acct, args[0], "domains", "custom", args[1]), Header: r2Header(cmd)})
		if err != nil {
			return err
		}
		return stOK(raw, fmt.Sprintf("Disconnected %s from %s", args[1], args[0]))
	},
}

// --- r2.dev URL and local uploads (enabled flags) --------------------------

// r2ToggleCmds builds get|enable|disable for a {"enabled": bool} setting.
func r2ToggleCmds(parent *cobra.Command, title string, sub ...string) {
	get := &cobra.Command{
		Use:   "get <bucket>",
		Short: "Show the " + title + " setting",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newST(cmd)
			if err != nil {
				return err
			}
			raw, err := r2GetConfig(c, cmd, args[0], sub...)
			if err != nil {
				return err
			}
			return stEmit(raw, func() error { return stDetail(title+" for "+args[0], raw) })
		},
	}
	set := func(on bool) *cobra.Command {
		verb := map[bool]string{true: "enable", false: "disable"}[on]
		c := &cobra.Command{
			Use:   verb + " <bucket>",
			Short: strings.ToUpper(verb[:1]) + verb[1:] + " " + title,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := newST(cmd)
				if err != nil {
					return err
				}
				if err := confirm(cmd, fmt.Sprintf("%s %s for %s", verb, title, args[0])); err != nil {
					return err
				}
				raw, err := r2PutConfig(c, cmd, args[0], map[string]any{"enabled": on}, sub...)
				if err != nil {
					return err
				}
				return stOK(raw, fmt.Sprintf("%sd %s for %s", strings.ToUpper(verb[:1])+verb[1:], title, args[0]))
			},
		}
		stYes(c)
		return c
	}
	parent.AddCommand(get, set(true), set(false))
}

var r2DevURLCmd = &cobra.Command{Use: "dev-url", Short: "Show, enable, or disable public access via the bucket's r2.dev URL"}
var r2LocalUploadsCmd = &cobra.Command{Use: "local-uploads", Short: "Show, enable, or disable Local Uploads"}

// --- event notifications ----------------------------------------------------

var r2NotificationCmd = &cobra.Command{Use: "notification", Aliases: []string{"notifications"}, Short: "List, create, or delete event notification rules (to a Queue)"}

func r2NotificationPath(c *stClient, bucket string, queueID ...string) string {
	p := c.p("event_notifications/r2", bucket, "configuration")
	if len(queueID) > 0 {
		p += "/queues/" + url.PathEscape(queueID[0])
	}
	return p
}

// r2QueueID resolves a queue name or ID.
func r2QueueID(c *stClient, arg string) (string, error) {
	return c.resolve("queue", c.p("queues"), nil, arg, "queue_id", "queue_name")
}

var r2NotificationListCmd = &cobra.Command{
	Use:     "list <bucket>",
	Aliases: []string{"get"},
	Short:   "List event notification rules",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "GET", Path: r2NotificationPath(c, args[0]), Header: r2Header(cmd)})
		if err != nil {
			if r2IsCode(err, 11015) {
				raw = json.RawMessage(`{"queues":[]}`)
			} else {
				return err
			}
		}
		return stEmit(raw, func() error {
			var cfg struct {
				Queues []struct {
					QueueID   string           `json:"queueId"`
					QueueName string           `json:"queueName"`
					Rules     []map[string]any `json:"rules"`
				} `json:"queues"`
			}
			_ = json.Unmarshal(raw, &cfg)
			var items []map[string]any
			for _, q := range cfg.Queues {
				for _, r := range q.Rules {
					r["_queue"] = q.QueueName
					if r["_queue"] == "" {
						r["_queue"] = q.QueueID
					}
					items = append(items, r)
				}
			}
			stList("🔔 %d notification rules for "+args[0], "No event notification rules for "+args[0], items, []stCol{
				{Header: "QUEUE", Path: "_queue"},
				{Header: "RULE ID", Path: "ruleId"},
				{Header: "ACTIONS", Path: "actions"},
				{Header: "PREFIX", Path: "prefix"},
				{Header: "SUFFIX", Path: "suffix"},
				{Header: "DESCRIPTION", Path: "description"},
			})
			return nil
		})
	},
}

// r2EventActions maps wrangler's --event-types to R2 actions.
var r2EventActions = map[string][]string{
	"object-create": {"PutObject", "CopyObject", "CompleteMultipartUpload"},
	"object-delete": {"DeleteObject", "LifecycleDeletion"},
}

var r2NotificationCreateCmd = &cobra.Command{
	Use:   "create <bucket> --queue <queue> --event-types object-create,object-delete",
	Short: "Send matching object events to a Queue",
	Long: `Create an event notification rule that sends object events to a Queue.
--event-types takes wrangler's names (object-create, object-delete) or raw R2
actions (PutObject, CopyObject, CompleteMultipartUpload, DeleteObject,
LifecycleDeletion).

Examples:
  cfctl r2 buckets notification create my-bucket --queue uploads --event-types object-create
  cfctl r2 buckets notification create my-bucket --queue q --event-types object-delete --prefix img/ --suffix .png`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		queue, _ := cmd.Flags().GetString("queue")
		types, _ := cmd.Flags().GetStringSlice("event-types")
		if queue == "" || len(types) == 0 {
			return fmt.Errorf("--queue and --event-types are required")
		}
		var actions []string
		for _, t := range types {
			if a, ok := r2EventActions[t]; ok {
				actions = append(actions, a...)
			} else {
				actions = append(actions, t)
			}
		}
		rule := map[string]any{"actions": actions}
		for _, f := range []string{"prefix", "suffix", "description"} {
			if v, _ := cmd.Flags().GetString(f); v != "" {
				rule[f] = v
			}
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		qid, err := r2QueueID(c, queue)
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "PUT", Path: r2NotificationPath(c, args[0], qid), Header: r2Header(cmd), Body: map[string]any{"rules": []any{rule}}})
		if err != nil {
			return err
		}
		return stOK(raw, fmt.Sprintf("Events %s on %s now go to queue %s", strings.Join(actions, ","), args[0], queue))
	},
}

var r2NotificationDeleteCmd = &cobra.Command{
	Use:   "delete <bucket> --queue <queue> [--rule <id>...]",
	Short: "Delete notification rules for a Queue (all, or --rule IDs)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		queue, _ := cmd.Flags().GetString("queue")
		if queue == "" {
			return fmt.Errorf("--queue is required")
		}
		ruleIDs, _ := cmd.Flags().GetStringSlice("rule")
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		qid, err := r2QueueID(c, queue)
		if err != nil {
			return err
		}
		what := fmt.Sprintf("delete every notification rule from %s to queue %s", args[0], queue)
		var body any
		if len(ruleIDs) > 0 {
			what = fmt.Sprintf("delete %d notification rules from %s", len(ruleIDs), args[0])
			body = map[string]any{"ruleIds": ruleIDs}
		}
		if err := confirm(cmd, what); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "DELETE", Path: r2NotificationPath(c, args[0], qid), Header: r2Header(cmd), Body: body})
		if err != nil {
			return err
		}
		return stOK(raw, "Deleted notification rules on "+args[0])
	},
}

// --- Sippy ------------------------------------------------------------------

var r2SippyCmd = &cobra.Command{Use: "sippy", Short: "Show, enable, or disable Sippy incremental migration"}

var r2SippyGetCmd = &cobra.Command{
	Use:   "get <bucket>",
	Short: "Show the Sippy configuration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2GetConfig(c, cmd, args[0], "sippy")
		if err != nil {
			return err
		}
		return stEmit(raw, func() error { return stDetail("Sippy for "+args[0], raw) })
	},
}

var r2SippyEnableCmd = &cobra.Command{
	Use:   "enable <bucket> --provider aws|gcs|s3|azure ...",
	Short: "Enable Sippy: serve missing objects from another provider and copy them in",
	Long: `Enable Sippy on-demand migration. Objects missing from R2 are fetched from
the source bucket, served, and copied into R2.

The destination credentials are an R2 API token's access key ID and secret
(--r2-access-key-id / --r2-secret-access-key).

Examples:
  cfctl r2 buckets sippy enable my-bucket --provider aws --bucket src --region us-east-1 \
    --access-key-id AKIA... --secret-access-key ... --r2-access-key-id ... --r2-secret-access-key ...
  cfctl r2 buckets sippy enable my-bucket --provider gcs --bucket src --service-account-key-file key.json \
    --r2-access-key-id ... --r2-secret-access-key ...
  cfctl r2 buckets sippy enable my-bucket --provider s3 --bucket-url https://s3.example.com/src ...
  cfctl r2 buckets sippy enable my-bucket --data @sippy.json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		if p, _ := cmd.Flags().GetString("provider"); p != "" {
			stSet(body, "source.provider", p)
			switch p {
			case "aws":
				stFlagStr(cmd, body, "bucket", "source.bucket")
				stFlagStr(cmd, body, "region", "source.region")
				stFlagStr(cmd, body, "access-key-id", "source.accessKeyId")
				stFlagStr(cmd, body, "secret-access-key", "source.secretAccessKey")
			case "s3":
				stFlagStr(cmd, body, "bucket-url", "source.bucketUrl")
				stFlagStr(cmd, body, "access-key-id", "source.accessKeyId")
				stFlagStr(cmd, body, "secret-access-key", "source.secretAccessKey")
			case "gcs":
				stFlagStr(cmd, body, "bucket", "source.bucket")
				stFlagStr(cmd, body, "client-email", "source.clientEmail")
				stFlagStr(cmd, body, "private-key", "source.privateKey")
				if f, _ := cmd.Flags().GetString("service-account-key-file"); f != "" {
					data, err := stReadInput("@" + f)
					if err != nil {
						return err
					}
					var key struct {
						ClientEmail string `json:"client_email"`
						PrivateKey  string `json:"private_key"`
					}
					if err := json.Unmarshal(data, &key); err != nil {
						return fmt.Errorf("--service-account-key-file: %w", err)
					}
					stSet(body, "source.clientEmail", key.ClientEmail)
					stSet(body, "source.privateKey", key.PrivateKey)
				}
			case "azure":
				stFlagStr(cmd, body, "account-name", "source.accountName")
				stFlagStr(cmd, body, "container", "source.container")
				stFlagStr(cmd, body, "account-key", "source.accountKey")
				stFlagStr(cmd, body, "sas-token", "source.sasToken")
			default:
				return fmt.Errorf("--provider must be aws, gcs, s3, or azure")
			}
		}
		stSet(body, "destination.provider", "r2")
		stFlagStr(cmd, body, "r2-access-key-id", "destination.accessKeyId")
		stFlagStr(cmd, body, "r2-secret-access-key", "destination.secretAccessKey")
		if _, ok := body["source"]; !ok {
			return fmt.Errorf("give --provider and its flags, or --data")
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2PutConfig(c, cmd, args[0], body, "sippy")
		if err != nil {
			return err
		}
		return stOK(raw, "Enabled Sippy for "+args[0])
	},
}

var r2SippyDisableCmd = &cobra.Command{
	Use:   "disable <bucket>",
	Short: "Disable Sippy",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		if err := confirm(cmd, "disable Sippy for "+args[0]); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "DELETE", Path: r2.BucketPath(c.acct, args[0], "sippy"), Header: r2Header(cmd)})
		if err != nil {
			return err
		}
		return stOK(raw, "Disabled Sippy for "+args[0])
	},
}

// --- background jobs ----------------------------------------------------------

var r2JobsCmd = &cobra.Command{Use: "jobs", Short: "List, inspect, or start background jobs (prefix delete, storage-class migration)"}

var r2JobsListCmd = &cobra.Command{
	Use:   "list <bucket>",
	Short: "List background jobs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		q := url.Values{}
		if v, _ := cmd.Flags().GetString("type"); v != "" {
			q.Set("jobType", v)
		}
		if v, _ := cmd.Flags().GetString("status"); v != "" {
			q.Set("status", v)
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "GET", Path: r2.BucketPath(c.acct, args[0], "jobs"), Query: q, Header: r2Header(cmd)})
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("⚙️  %d jobs for "+args[0], "No jobs for "+args[0], stItems(raw, "jobs"), []stCol{
				{Header: "ID", Path: "id"},
				{Header: "TYPE", Path: "jobType"},
				{Header: "STATUS", Path: "status"},
				{Header: "PREFIX", Path: "prefix"},
				{Header: "CREATED", Path: "createdAt"},
			})
			return nil
		})
	},
}

var r2JobsGetCmd = &cobra.Command{
	Use:   "get <bucket> <job-id>",
	Short: "Show a background job",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := r2GetConfig(c, cmd, args[0], "jobs", args[1])
		if err != nil {
			return err
		}
		return stEmit(raw, func() error { return stDetail("⚙️  job "+args[1], raw) })
	},
}

var r2JobsCreateCmd = &cobra.Command{
	Use:   "create <bucket> --type prefix-delete|storage-class-migration",
	Short: "Start a prefix-delete or storage-class migration job",
	Long: `Start a background job.

  --type prefix-delete --prefix logs/      delete everything under logs/ (prefix must end in /;
                                            --prefix "" empties the bucket)
  --type storage-class-migration [--from InfrequentAccess --to Standard]

Examples:
  cfctl r2 buckets jobs create my-bucket --type prefix-delete --prefix tmp/
  cfctl r2 buckets jobs create my-bucket --type storage-class-migration --from Standard --to InfrequentAccess`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		typ, _ := cmd.Flags().GetString("type")
		body := map[string]any{}
		var what string
		switch typ {
		case "prefix-delete", "prefixDelete":
			if !cmd.Flags().Changed("prefix") {
				return fmt.Errorf("--prefix is required (use --prefix \"\" to empty the bucket)")
			}
			prefix, _ := cmd.Flags().GetString("prefix")
			if prefix != "" && !strings.HasSuffix(prefix, "/") {
				return fmt.Errorf("--prefix must end in /")
			}
			body["jobType"], body["prefix"] = "prefixDelete", prefix
			what = fmt.Sprintf("delete every object under %q in %s", prefix, args[0])
			if prefix == "" {
				what = "delete every object in " + args[0]
			}
		case "storage-class-migration", "storageClassMigration":
			body["jobType"] = "storageClassMigration"
			stFlagStr(cmd, body, "from", "sourceStorageClass")
			stFlagStr(cmd, body, "to", "destinationStorageClass")
			what = "migrate the storage class of objects in " + args[0]
		default:
			return fmt.Errorf("--type must be prefix-delete or storage-class-migration")
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		if err := confirm(cmd, what); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "POST", Path: r2.BucketPath(c.acct, args[0], "jobs"), Header: r2Header(cmd), Body: body})
		if err != nil {
			return err
		}
		var job struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &job)
		return stOK(raw, "Started job "+job.ID+" (check it with 'cfctl r2 buckets jobs get "+args[0]+" "+job.ID+"')")
	},
}

func init() {
	r2CorsSetCmd.Flags().String("file", "", "JSON file with the rules (- for stdin)")
	stYes(r2CorsSetCmd)
	stYes(r2CorsDeleteCmd)
	r2CorsCmd.AddCommand(r2CorsListCmd, r2CorsSetCmd, r2CorsDeleteCmd)

	f := r2LifecycleAddCmd.Flags()
	f.String("id", "", "Rule ID (default: generated)")
	f.String("prefix", "", "Apply to keys starting with this prefix (default: all)")
	f.Int("expire-days", 0, "Delete objects this many days after upload")
	f.String("expire-date", "", "Delete objects on this date")
	f.Int("ia-transition-days", 0, "Move objects to Infrequent Access this many days after upload")
	f.String("ia-transition-date", "", "Move objects to Infrequent Access on this date")
	f.Int("abort-multipart-days", 0, "Abort incomplete multipart uploads after this many days")
	f.Bool("disabled", false, "Add the rule disabled")
	r2LifecycleCmd.AddCommand(r2LifecycleListCmd, r2LifecycleAddCmd, r2RemoveRule("lifecycle", "lifecycle"), r2SetRules("lifecycle", "lifecycle"))

	f = r2LockAddCmd.Flags()
	f.String("id", "", "Rule ID (default: generated)")
	f.String("prefix", "", "Apply to keys starting with this prefix (default: all)")
	f.Int("retention-days", 0, "Lock objects for this many days after upload")
	f.String("retention-date", "", "Lock objects until this date")
	f.Bool("retention-indefinite", false, "Lock objects indefinitely")
	f.Bool("disabled", false, "Add the rule disabled")
	stYes(r2LockAddCmd)
	r2LockCmd.AddCommand(r2LockListCmd, r2LockAddCmd, r2RemoveRule("lock", "lock"), r2SetRules("lock", "lock"))

	r2DomainAddCmd.Flags().String("domain", "", "Custom domain (its zone must be in this account)")
	r2DomainAddCmd.Flags().String("zone", "", "Zone name or ID (default: found from the domain)")
	r2DomainAddCmd.Flags().String("min-tls", "", "Minimum TLS version: 1.0, 1.1, 1.2, or 1.3")
	r2DomainAddCmd.Flags().StringSlice("ciphers", nil, "Allowed TLS ciphers (BoringSSL names)")
	r2DomainAddCmd.Flags().Bool("disabled", false, "Connect without enabling public access yet")
	stDataFlag(r2DomainAddCmd)
	r2DomainUpdateCmd.Flags().Bool("enabled", true, "Enable (true) or disable (false) public access through the domain")
	r2DomainUpdateCmd.Flags().String("min-tls", "", "Minimum TLS version: 1.0, 1.1, 1.2, or 1.3")
	r2DomainUpdateCmd.Flags().StringSlice("ciphers", nil, "Allowed TLS ciphers (BoringSSL names)")
	stDataFlag(r2DomainUpdateCmd)
	stYes(r2DomainRemoveCmd)
	r2DomainCmd.AddCommand(r2DomainListCmd, r2DomainGetCmd, r2DomainAddCmd, r2DomainUpdateCmd, r2DomainRemoveCmd)

	r2ToggleCmds(r2DevURLCmd, "r2.dev URL", "domains", "managed")
	r2ToggleCmds(r2LocalUploadsCmd, "Local Uploads", "local-uploads")

	r2NotificationCreateCmd.Flags().String("queue", "", "Queue name or ID")
	r2NotificationCreateCmd.Flags().StringSlice("event-types", nil, "object-create, object-delete, or R2 action names")
	r2NotificationCreateCmd.Flags().String("prefix", "", "Only keys with this prefix")
	r2NotificationCreateCmd.Flags().String("suffix", "", "Only keys with this suffix")
	r2NotificationCreateCmd.Flags().String("description", "", "Rule description")
	r2NotificationDeleteCmd.Flags().String("queue", "", "Queue name or ID")
	r2NotificationDeleteCmd.Flags().StringSlice("rule", nil, "Rule IDs to delete (default: all rules for the queue)")
	stYes(r2NotificationDeleteCmd)
	r2NotificationCmd.AddCommand(r2NotificationListCmd, r2NotificationCreateCmd, r2NotificationDeleteCmd)

	f = r2SippyEnableCmd.Flags()
	f.String("provider", "", "Source provider: aws, gcs, s3 (S3-compatible), or azure")
	f.String("bucket", "", "Source bucket (aws, gcs)")
	f.String("region", "", "Source region (aws)")
	f.String("bucket-url", "", "Source bucket URL (s3)")
	f.String("access-key-id", "", "Source access key ID (aws, s3)")
	f.String("secret-access-key", "", "Source secret access key (aws, s3)")
	f.String("client-email", "", "Service account client email (gcs)")
	f.String("private-key", "", "Service account private key (gcs)")
	f.String("service-account-key-file", "", "GCS service account key JSON file (sets client email and private key)")
	f.String("account-name", "", "Storage account name (azure)")
	f.String("container", "", "Container (azure)")
	f.String("account-key", "", "Storage account key (azure)")
	f.String("sas-token", "", "SAS token (azure)")
	f.String("r2-access-key-id", "", "R2 API token access key ID (destination)")
	f.String("r2-secret-access-key", "", "R2 API token secret access key (destination)")
	stDataFlag(r2SippyEnableCmd)
	stYes(r2SippyDisableCmd)
	r2SippyCmd.AddCommand(r2SippyGetCmd, r2SippyEnableCmd, r2SippyDisableCmd)

	r2JobsListCmd.Flags().String("type", "", "Only jobs of this type: prefixDelete or storageClassMigration")
	r2JobsListCmd.Flags().String("status", "", "Only jobs with this status (needs --type)")
	r2JobsCreateCmd.Flags().String("type", "", "prefix-delete or storage-class-migration")
	r2JobsCreateCmd.Flags().String("prefix", "", "Prefix to delete (must end in /; \"\" empties the bucket)")
	r2JobsCreateCmd.Flags().String("from", "", "Source storage class (storage-class-migration)")
	r2JobsCreateCmd.Flags().String("to", "", "Destination storage class (storage-class-migration)")
	stYes(r2JobsCreateCmd)
	r2JobsCmd.AddCommand(r2JobsListCmd, r2JobsGetCmd, r2JobsCreateCmd)

	r2BucketsCmd.AddCommand(newCatalogCmd("catalog", "r2-catalog", "Manage R2 Data Catalog (Apache Iceberg) for a bucket"), r2CorsCmd, r2LifecycleCmd, r2LockCmd, r2DomainCmd, r2DevURLCmd, r2LocalUploadsCmd, r2NotificationCmd, r2SippyCmd, r2JobsCmd)
}
