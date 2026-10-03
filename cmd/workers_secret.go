package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Secret values are read from stdin or a no-echo prompt, sent in request
// bodies only, and never printed, logged, or included in --json output.

// wkReadSecret reads one secret value: a no-echo prompt on a terminal,
// otherwise all of stdin (minus one trailing newline).
func wkReadSecret(name string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "Enter a secret value for %s: ", name)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if len(b) == 0 {
			return "", fmt.Errorf("empty secret value")
		}
		return string(b), nil
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 5<<20))
	if err != nil {
		return "", err
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if v == "" {
		return "", fmt.Errorf("empty secret value on stdin")
	}
	return v, nil
}

// wkParseSecretsFile parses {"NAME": "value"|null} JSON or KEY=value .env
// content. A null JSON value means delete.
func wkParseSecretsFile(data []byte) (map[string]*string, error) {
	out := map[string]*string{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var m map[string]any
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return nil, fmt.Errorf("secrets file is not valid JSON: %w", err)
		}
		for k, v := range m {
			switch t := v.(type) {
			case nil:
				out[k] = nil
			case string:
				s := t
				out[k] = &s
			default:
				b, _ := json.Marshal(t)
				s := string(b)
				out[k] = &s
			}
		}
		return out, nil
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 5<<20)
	line := 0
	for sc.Scan() {
		line++
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		l = strings.TrimPrefix(l, "export ")
		k, v, ok := strings.Cut(l, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("line %d: want KEY=value", line)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		s := v
		out[k] = &s
	}
	return out, sc.Err()
}

// wkSecretsPatch turns parsed secrets into a secrets-bulk / env merge patch.
func wkSecretsPatch(secrets map[string]*string) (map[string]any, []string, []string) {
	patch := map[string]any{}
	var set, del []string
	for k, v := range secrets {
		if v == nil {
			patch[k] = nil
			del = append(del, k)
			continue
		}
		patch[k] = map[string]string{"type": "secret_text", "name": k, "text": *v}
		set = append(set, k)
	}
	sort.Strings(set)
	sort.Strings(del)
	return patch, set, del
}

func wkReadSecretsInput(args []string) (map[string]*string, error) {
	var data []byte
	var err error
	if len(args) == 0 || args[0] == "-" {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			return nil, fmt.Errorf("pass a JSON or .env file, or pipe one on stdin")
		}
		data, err = io.ReadAll(io.LimitReader(os.Stdin, 50<<20))
	} else {
		data, err = os.ReadFile(args[0])
	}
	if err != nil {
		return nil, err
	}
	secrets, err := wkParseSecretsFile(data)
	if err != nil {
		return nil, err
	}
	if len(secrets) == 0 {
		return nil, fmt.Errorf("no secrets found in the input")
	}
	if len(secrets) > 100 {
		return nil, fmt.Errorf("%d secrets; the API takes at most 100 per request", len(secrets))
	}
	return secrets, nil
}

func newSecretCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "secret",
		Short: "Manage a Worker's secrets",
		Long: `Create, list, and delete secrets on a Worker. Each change creates and
deploys a new version. Values come from a hidden prompt or stdin and are
never printed.

Examples:
  cfctl secret put API_KEY --name my-worker                # prompts, no echo
  printf %s "$TOKEN" | cfctl secret put API_KEY --name my-worker
  cfctl secret list --name my-worker
  cfctl secret delete API_KEY --name my-worker
  cfctl secret bulk secrets.json --name my-worker          # {"A":"1","OLD":null}
  cfctl secret bulk .env.production --name my-worker       # KEY=value lines`,
	}

	put := &cobra.Command{
		Use:   "put <KEY>",
		Short: "Create or update a secret (value from prompt or stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			val, err := wkReadSecret(args[0])
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			body := map[string]string{"name": args[0], "text": val, "type": "secret_text"}
			if _, err := wkCall(cmd.Context(), s, "PUT", wkScriptPath(name, "/secrets"), nil, body, nil); err != nil {
				return fmt.Errorf("failed to set secret %s on %s: %w", args[0], name, err)
			}
			if printJSON(map[string]any{"worker": name, "secret": args[0], "updated": true}) {
				return nil
			}
			fmt.Println(ui.Success(fmt.Sprintf("Set secret %s on %s", args[0], name)))
			return nil
		},
	}
	wkAddScriptFlags(put)

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List secret names (values are never returned)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var secs []map[string]any
			if _, err := wkCall(cmd.Context(), s, "GET", wkScriptPath(name, "/secrets"), nil, nil, &secs); err != nil {
				return fmt.Errorf("failed to list secrets of %s: %w", name, err)
			}
			if jsonOutput {
				if secs == nil {
					secs = []map[string]any{}
				}
				return printJSONValue(secs)
			}
			if len(secs) == 0 {
				fmt.Println(ui.Warn("No secrets on " + name))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🤫 %d secrets on %s", len(secs), name)))
			for _, sec := range secs {
				fmt.Printf("  %-32s %s\n", ui.AccentStyle.Render(fmt.Sprint(sec["name"])), ui.SubtleStyle.Render(fmt.Sprint(sec["type"])))
			}
			return nil
		},
	}
	wkAddScriptFlags(list)

	del := &cobra.Command{
		Use:     "delete <KEY>",
		Aliases: []string{"rm"},
		Short:   "Delete a secret",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			if err := confirm(cmd, fmt.Sprintf("delete secret %s from %s", args[0], name)); err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			if _, err := wkCall(cmd.Context(), s, "DELETE", wkScriptPath(name, "/secrets/"+url.PathEscape(args[0])), nil, nil, nil); err != nil {
				return fmt.Errorf("failed to delete secret %s from %s: %w", args[0], name, err)
			}
			if printJSON(map[string]any{"worker": name, "secret": args[0], "deleted": true}) {
				return nil
			}
			fmt.Println(ui.Success(fmt.Sprintf("Deleted secret %s from %s", args[0], name)))
			return nil
		},
	}
	wkAddScriptFlags(del)
	wkAddYesFlag(del)

	bulk := &cobra.Command{
		Use:   "bulk [file|-]",
		Short: "Set (or delete, with null) many secrets in one version",
		Long: `Create, update, or delete up to 100 secrets in a single new version.
Input is a JSON object ({"KEY": "value", "OLD_KEY": null}) or .env lines
(KEY=value), from a file or stdin.

Examples:
  cfctl secret bulk secrets.json --name my-worker
  cat .env | cfctl secret bulk --name my-worker`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			secrets, err := wkReadSecretsInput(args)
			if err != nil {
				return err
			}
			patch, set, del := wkSecretsPatch(secrets)
			if len(del) > 0 {
				if err := confirm(cmd, fmt.Sprintf("delete %d secrets (%s) from %s", len(del), strings.Join(del, ", "), name)); err != nil {
					return err
				}
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			if _, err := wkCall(cmd.Context(), s, "PATCH", wkScriptPath(name, "/secrets-bulk"), nil, map[string]any{"secrets": patch}, nil); err != nil {
				return fmt.Errorf("failed to update secrets on %s: %w", name, err)
			}
			if printJSON(map[string]any{"worker": name, "set": wkNonNil(set), "deleted": wkNonNil(del)}) {
				return nil
			}
			if len(set) > 0 {
				fmt.Println(ui.Success(fmt.Sprintf("Set %d secrets on %s: %s", len(set), name, strings.Join(set, ", "))))
			}
			if len(del) > 0 {
				fmt.Println(ui.Success(fmt.Sprintf("Deleted %d secrets from %s: %s", len(del), name, strings.Join(del, ", "))))
			}
			return nil
		},
	}
	wkAddScriptFlags(bulk)
	wkAddYesFlag(bulk)

	c.AddCommand(put, list, del, bulk)
	return c
}

func wkNonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func init() {
	workersCmd.AddCommand(newSecretCmd())
	rootCmd.AddCommand(newSecretCmd())
}

// ---- versions secret ------------------------------------------------------

// wkLatestVersion fetches the latest version through the Workers Versions API.
func wkLatestVersion(cmd *cobra.Command, s *apiSession, name string) (map[string]any, error) {
	var v map[string]any
	if _, err := wkCall(cmd.Context(), s, "GET", wkWorkerPath(name, "/versions/latest"), nil, nil, &v); err != nil {
		return nil, fmt.Errorf("failed to get the latest version of %s: %w", name, err)
	}
	return v, nil
}

// wkPatchVersionSecrets creates a new (undeployed) version from the latest
// one: every existing binding is inherited by name except the ones being
// changed; set holds new secret values, del the names to drop.
func wkPatchVersionSecrets(cmd *cobra.Command, s *apiSession, name string, set map[string]string, del []string, message string) (map[string]any, error) {
	latest, err := wkLatestVersion(cmd, s, name)
	if err != nil {
		return nil, err
	}
	drop := map[string]bool{}
	for _, d := range del {
		drop[d] = true
	}
	for k := range set {
		drop[k] = true
	}
	var bindings []map[string]any
	existing := map[string]bool{}
	if bs, ok := latest["bindings"].([]any); ok {
		for _, b := range bs {
			bm, _ := b.(map[string]any)
			n := fmt.Sprint(bm["name"])
			existing[n] = true
			if drop[n] {
				continue
			}
			bindings = append(bindings, map[string]any{"type": "inherit", "name": n})
		}
	}
	for _, d := range del {
		if !existing[d] {
			return nil, fmt.Errorf("%s has no binding named %s", name, d)
		}
	}
	for _, k := range wkSortedKeys(set) {
		bindings = append(bindings, map[string]any{"type": "secret_text", "name": k, "text": set[k]})
	}
	if bindings == nil {
		bindings = []map[string]any{}
	}
	body := map[string]any{"bindings": bindings}
	if message != "" {
		body["annotations"] = map[string]string{"workers/message": message}
	}
	var v map[string]any
	if _, err := wkCall(cmd.Context(), s, "PATCH", wkWorkerPath(name, "/versions/latest"), nil, body, &v); err != nil {
		return nil, fmt.Errorf("failed to create a new version of %s: %w", name, err)
	}
	return v, nil
}

func newVersionsSecretCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "secret",
		Short: "Change secrets in a new version without deploying it",
		Long: `Like 'secret', but each change creates a new version (from the latest
one, inheriting every other binding) and does NOT deploy it. Deploy it with
'versions deploy' (e.g. gradually).

Examples:
  cfctl versions secret put API_KEY --name my-worker
  cfctl versions secret bulk secrets.json --name my-worker --message "rotate keys"
  cfctl versions secret delete OLD_KEY --name my-worker
  cfctl versions secret list --name my-worker`,
	}
	done := func(name string, v map[string]any) error {
		if jsonOutput {
			return printJSONValue(map[string]any{"worker": name, "version_id": v["id"], "number": v["number"]})
		}
		fmt.Println(ui.Success(fmt.Sprintf("Created version %v of %s (not deployed)", v["id"], name)))
		fmt.Println(ui.SubtleStyle.Render(fmt.Sprintf("  Deploy it: %s versions deploy %v --name %s", BinName(), v["id"], name)))
		return nil
	}
	put := &cobra.Command{
		Use: "put <KEY>", Short: "Set a secret in a new version (value from prompt or stdin)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			val, err := wkReadSecret(args[0])
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			msg, _ := cmd.Flags().GetString("message")
			v, err := wkPatchVersionSecrets(cmd, s, name, map[string]string{args[0]: val}, nil, msg)
			if err != nil {
				return err
			}
			return done(name, v)
		},
	}
	del := &cobra.Command{
		Use: "delete <KEY>", Aliases: []string{"rm"}, Short: "Remove a secret in a new version", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			if err := confirm(cmd, fmt.Sprintf("remove secret %s in a new version of %s", args[0], name)); err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			msg, _ := cmd.Flags().GetString("message")
			v, err := wkPatchVersionSecrets(cmd, s, name, nil, []string{args[0]}, msg)
			if err != nil {
				return err
			}
			return done(name, v)
		},
	}
	bulk := &cobra.Command{
		Use: "bulk [file|-]", Short: "Set (or delete, with null) many secrets in one new version", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			secrets, err := wkReadSecretsInput(args)
			if err != nil {
				return err
			}
			set := map[string]string{}
			var dels []string
			for k, v := range secrets {
				if v == nil {
					dels = append(dels, k)
				} else {
					set[k] = *v
				}
			}
			sort.Strings(dels)
			if len(dels) > 0 {
				if err := confirm(cmd, fmt.Sprintf("remove %d secrets (%s) in a new version of %s", len(dels), strings.Join(dels, ", "), name)); err != nil {
					return err
				}
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			msg, _ := cmd.Flags().GetString("message")
			v, err := wkPatchVersionSecrets(cmd, s, name, set, dels, msg)
			if err != nil {
				return err
			}
			return done(name, v)
		},
	}
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List secret names in the latest version", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := wkScriptName(cmd, nil)
			if err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			v, err := wkLatestVersion(cmd, s, name)
			if err != nil {
				return err
			}
			secs := wkSecretNames(v["bindings"])
			if jsonOutput {
				return printJSONValue(secs)
			}
			if len(secs) == 0 {
				fmt.Println(ui.Warn("No secrets in the latest version of " + name))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🤫 %d secrets in version %v of %s", len(secs), v["id"], name)))
			for _, sec := range secs {
				fmt.Printf("  %-32s %s\n", ui.AccentStyle.Render(fmt.Sprint(sec["name"])), ui.SubtleStyle.Render(fmt.Sprint(sec["type"])))
			}
			return nil
		},
	}
	for _, x := range []*cobra.Command{put, del, bulk, list} {
		wkAddScriptFlags(x)
		if x != list {
			x.Flags().String("message", "", "Version message (workers/message)")
		}
	}
	wkAddYesFlag(del)
	wkAddYesFlag(bulk)
	c.AddCommand(put, del, bulk, list)
	return c
}
