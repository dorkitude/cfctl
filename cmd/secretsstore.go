package cmd

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var secretsStoreCmd = &cobra.Command{
	Use:   "secrets-store",
	Short: "Manage Secrets Store stores and secrets",
	Long: `Manage the account-level Secrets Store: stores and the secrets in them.
Secret values are write-only: the API never returns them, and cfctl never
prints them. A store or secret can be given by name or ID.

Examples:
  cfctl secrets-store store list
  cfctl secrets-store secret list default_secrets_store
  cfctl secrets-store secret create default_secrets_store API_KEY --scopes workers
  echo -n "$TOKEN" | cfctl secrets-store secret create default_secrets_store API_KEY --scopes workers,ai_gateway
  cfctl secrets-store quota

Generated equivalents: cfctl api secrets-store <op>.`,
}

var secretsStoreStoreCmd = &cobra.Command{
	Use:     "store",
	Aliases: []string{"stores"},
	Short:   "List, create, and delete stores",
}

func secretsStoreID(c *stClient, arg string) (string, error) {
	return c.resolve("store", c.p("secrets_store/stores"), nil, arg, "id", "name")
}

func secretsStorePath(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		id, err := secretsStoreID(c, args[0])
		if err != nil {
			return "", nil, err
		}
		return c.p("secrets_store/stores", append([]string{id}, segs...)...), nil, nil
	}
}

var secretsStoreListCmd = sbListCmd("list", "List stores", "", cobra.NoArgs, sbFixed("secrets_store/stores"),
	"🔐 %d stores", "No stores found", []stCol{
		{Header: "NAME", Path: "name"},
		{Header: "ID", Path: "id"},
		{Header: "CREATED", Path: "created"},
		{Header: "MODIFIED", Path: "modified"},
	})

var secretsStoreGetCmd = sbGetCmd("get <store>", "Show a store", "", "🔐 Store", cobra.ExactArgs(1), secretsStorePath())

var secretsStoreCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a store",
	Args:  cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		return sbSendShow(c, "POST", c.p("secrets_store/stores"), nil, map[string]any{"name": args[0]}, "Created store "+args[0])
	}),
}

var secretsStoreDeleteCmd = &cobra.Command{
	Use:   "delete <store>",
	Short: "Delete a store (--force also deletes its secrets)",
	Args:  cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		id, err := secretsStoreID(c, args[0])
		if err != nil {
			return err
		}
		force, _ := cmd.Flags().GetBool("force")
		what := "store " + args[0]
		var q url.Values
		if force {
			what += " and every secret in it"
			q = url.Values{"force": {"true"}}
		}
		if err := confirm(cmd, "delete "+what); err != nil {
			return err
		}
		return sbSend(c, "DELETE", c.p("secrets_store/stores", id), q, nil, "Deleted "+what)
	}),
}

var secretsStoreQuotaCmd = sbGetCmd("quota", "Show secret usage against the account quota", "", "🔐 Secrets Store quota", cobra.NoArgs, sbFixed("secrets_store/quota"))

// --- secrets -----------------------------------------------------------------

var secretsStoreSecretCmd = &cobra.Command{
	Use:     "secret",
	Aliases: []string{"secrets"},
	Short:   "Manage secrets in a store (values are never printed)",
}

var secretCols = []stCol{
	{Header: "NAME", Path: "name"},
	{Header: "ID", Path: "id"},
	{Header: "SCOPES", Path: "scopes"},
	{Header: "STATUS", Path: "status"},
	{Header: "COMMENT", Path: "comment", Fmt: sbTrunc(40)},
	{Header: "MODIFIED", Path: "modified"},
}

var secretsStoreSecretListCmd = &cobra.Command{
	Use:   "list <store>",
	Short: "List secrets in a store (metadata only)",
	Long: `List secrets (names, scopes, status; never values).

Examples:
  cfctl secrets-store secret list default_secrets_store
  cfctl secrets-store secret list default_secrets_store --search api --scopes workers`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		id, err := secretsStoreID(c, args[0])
		if err != nil {
			return err
		}
		q := url.Values{}
		if s, _ := cmd.Flags().GetString("search"); s != "" {
			q.Set("search", s)
		}
		if sc, _ := cmd.Flags().GetStringSlice("scopes"); len(sc) > 0 {
			q.Set("scopes", strings.Join(sc, ","))
		}
		raw, err := c.all(c.p("secrets_store/stores", id, "secrets"), q)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("🔑 %d secrets", "No secrets found", stItems(raw), secretCols)
			return nil
		})
	}),
}

// secretID resolves a secret name or ID within a store.
func secretID(c *stClient, storeID, arg string) (string, error) {
	return c.resolve("secret", c.p("secrets_store/stores", storeID, "secrets"), url.Values{"search": {arg}}, arg, "id", "name")
}

// secretPath resolves <store> <secret> to the secret's path.
func secretPath(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		sid, err := secretsStoreID(c, args[0])
		if err != nil {
			return "", nil, err
		}
		id, err := secretID(c, sid, args[1])
		if err != nil {
			return "", nil, err
		}
		return c.p("secrets_store/stores", append([]string{sid, "secrets", id}, segs...)...), nil, nil
	}
}

var secretsStoreSecretGetCmd = sbGetCmd("get <store> <secret>", "Show a secret's metadata (never its value)", "", "🔑 Secret", cobra.ExactArgs(2), secretPath())

var secretsStoreSecretDeleteCmd = sbDeleteCmd("delete <store> <secret>", "Delete a secret", "", cobra.ExactArgs(2),
	func(a []string) string { return "secret " + a[1] }, secretPath())

// secretValue reads a secret value from --value, --value-file, a no-echo
// prompt (terminal), or stdin (piped; one trailing newline is dropped).
func secretValue(cmd *cobra.Command, required bool) (string, bool, error) {
	if cmd.Flags().Changed("value") {
		v, _ := cmd.Flags().GetString("value")
		return v, true, nil
	}
	if f, _ := cmd.Flags().GetString("value-file"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", false, err
		}
		return string(b), true, nil
	}
	if !required {
		return "", false, nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Secret value: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", false, err
		}
		return string(b), true, nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", false, err
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if v == "" {
		return "", false, fmt.Errorf("empty secret value: pass --value, --value-file, or pipe it on stdin")
	}
	return v, true, nil
}

func secretFlags(cmd *cobra.Command, withValue bool) {
	if withValue {
		cmd.Flags().String("value", "", "Secret value (visible in shell history; prefer stdin or the prompt)")
		cmd.Flags().String("value-file", "", "Read the secret value from this file (used as-is)")
	}
	cmd.Flags().StringSlice("scopes", nil, "Services that may use the secret: workers, ai_gateway, dex, access, containers, websearch")
	cmd.Flags().String("comment", "", "Description")
}

var secretsStoreSecretCreateCmd = &cobra.Command{
	Use:   "create <store> <name>",
	Short: "Create a secret (value from a prompt, stdin, --value-file, or --value)",
	Long: `Create a secret. The value is read without echo from the terminal, from
stdin when piped, or from --value-file / --value. It is never printed.

Examples:
  cfctl secrets-store secret create default_secrets_store API_KEY --scopes workers
  printf %s "$KEY" | cfctl secrets-store secret create default_secrets_store API_KEY --scopes workers --comment "prod key"`,
	Args: cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		scopes, _ := cmd.Flags().GetStringSlice("scopes")
		if len(scopes) == 0 {
			return fmt.Errorf("--scopes is required (e.g. --scopes workers)")
		}
		sid, err := secretsStoreID(c, args[0])
		if err != nil {
			return err
		}
		val, _, err := secretValue(cmd, true)
		if err != nil {
			return err
		}
		item := map[string]any{"name": args[1], "value": val, "scopes": scopes}
		stFlagStr(cmd, item, "comment", "comment")
		return sbSendShow(c, "POST", c.p("secrets_store/stores", sid, "secrets"), nil, []any{item}, "Created secret "+args[1])
	}),
}

var secretsStoreSecretUpdateCmd = &cobra.Command{
	Use:   "update <store> <secret>",
	Short: "Update a secret's value, scopes, or comment",
	Long: `Update a secret. Pass --value/--value-file (or --new-value to be prompted / read
stdin) to change the value; --scopes and --comment change metadata.

Examples:
  cfctl secrets-store secret update default_secrets_store API_KEY --comment rotated --new-value
  cfctl secrets-store secret update default_secrets_store API_KEY --scopes workers,ai_gateway`,
	Args: cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		newVal, _ := cmd.Flags().GetBool("new-value")
		body := map[string]any{}
		stFlagStr(cmd, body, "comment", "comment")
		stFlagList(cmd, body, "scopes", "scopes")
		p, _, err := secretPath()(c, args)
		if err != nil {
			return err
		}
		val, has, err := secretValue(cmd, newVal)
		if err != nil {
			return err
		}
		if has {
			body["value"] = val
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to update: pass --value/--value-file/--new-value, --scopes, or --comment")
		}
		return sbSendShow(c, "PATCH", p, nil, body, "Updated secret "+args[1])
	}),
}

var secretsStoreSecretDuplicateCmd = &cobra.Command{
	Use:   "duplicate <store> <secret> <new-name>",
	Short: "Copy a secret (value included) under a new name",
	Args:  cobra.ExactArgs(3),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		p, _, err := secretPath("duplicate")(c, args)
		if err != nil {
			return err
		}
		body := map[string]any{"name": args[2]}
		stFlagStr(cmd, body, "comment", "comment")
		scopes, _ := cmd.Flags().GetStringSlice("scopes")
		if len(scopes) == 0 {
			// Keep the original's scopes.
			orig, err := c.get(strings.TrimSuffix(p, "/duplicate"), nil)
			if err != nil {
				return err
			}
			if items := stItems(sbWrap1(orig)); len(items) == 1 {
				if s, ok := items[0]["scopes"]; ok {
					body["scopes"] = s
				}
			}
		} else {
			body["scopes"] = scopes
		}
		return sbSendShow(c, "POST", p, nil, body, fmt.Sprintf("Duplicated %s as %s", args[1], args[2]))
	}),
}

// sbWrap1 wraps a single object in an array (for stItems).
func sbWrap1(raw []byte) []byte {
	return append(append([]byte("["), raw...), ']')
}

func init() {
	stYes(secretsStoreDeleteCmd)
	secretsStoreDeleteCmd.Flags().Bool("force", false, "Also delete every secret in the store")
	secretsStoreStoreCmd.AddCommand(secretsStoreListCmd, secretsStoreGetCmd, secretsStoreCreateCmd, secretsStoreDeleteCmd)

	secretsStoreSecretListCmd.Flags().String("search", "", "Filter by name or comment")
	secretsStoreSecretListCmd.Flags().StringSlice("scopes", nil, "Only secrets with these scopes")
	secretFlags(secretsStoreSecretCreateCmd, true)
	secretFlags(secretsStoreSecretUpdateCmd, true)
	secretsStoreSecretUpdateCmd.Flags().Bool("new-value", false, "Read a new value from the prompt or stdin")
	secretFlags(secretsStoreSecretDuplicateCmd, false)
	secretsStoreSecretCmd.AddCommand(secretsStoreSecretListCmd, secretsStoreSecretGetCmd, secretsStoreSecretCreateCmd,
		secretsStoreSecretUpdateCmd, secretsStoreSecretDeleteCmd, secretsStoreSecretDuplicateCmd)

	secretsStoreCmd.AddCommand(secretsStoreStoreCmd, secretsStoreSecretCmd, secretsStoreQuotaCmd)
	rootCmd.AddCommand(secretsStoreCmd)
}
