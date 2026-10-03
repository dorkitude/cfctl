package cmd

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var artifactsCmd = &cobra.Command{
	Use:   "artifacts",
	Short: "Manage Artifacts namespaces, Git repositories, and repo tokens [private beta]",
	Long: `Manage Artifacts: namespaces of hosted Git repositories, their contents and
history, and repo-scoped access tokens.

Examples:
  cfctl artifacts namespaces list
  cfctl artifacts repos list my-ns
  cfctl artifacts repos create my-ns site --description "landing page"
  cfctl artifacts repos import my-ns mirror --url https://github.com/org/repo.git
  cfctl artifacts repos log my-ns site --limit 10
  cfctl artifacts repos file my-ns site main README.md
  cfctl artifacts repos issue-token my-ns site --scope write --ttl 3600

Generated equivalents: cfctl api artifacts <op>.`,
}

var artifactsNamespacesCmd = &cobra.Command{
	Use:     "namespaces",
	Aliases: []string{"namespace", "ns"},
	Short:   "List, create, and delete Artifacts namespaces",
}

func artNS(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("artifacts/namespaces", append([]string{args[0]}, segs...)...), nil, nil
	}
}

func artRepo(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("artifacts/namespaces", append([]string{args[0], "repos", args[1]}, segs...)...), nil, nil
	}
}

var artifactsNSListCmd = sbListCmd("list", "List Artifacts namespaces", "", cobra.NoArgs, sbFixed("artifacts/namespaces"),
	"🧱 %d namespaces", "No Artifacts namespaces found", []stCol{
		{Header: "NAME", Path: "", Fmt: sbFirst("namespace", "name")},
		{Header: "JURISDICTION", Path: "jurisdiction"},
		{Header: "CREATED", Path: "created_at"},
	})

var artifactsNSGetCmd = sbGetCmd("get <namespace>", "Show an Artifacts namespace", "", "🧱 Namespace", cobra.ExactArgs(1), artNS())

var artifactsNSCreateCmd = &cobra.Command{
	Use:   "create <namespace>",
	Short: "Create an Artifacts namespace",
	Args:  cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body := map[string]any{"namespace": args[0]}
		stFlagStr(cmd, body, "jurisdiction", "jurisdiction")
		return sbSendShow(c, "POST", c.p("artifacts/namespaces"), nil, body, "Created namespace "+args[0])
	}),
}

var artifactsNSDeleteCmd = sbDeleteCmd("delete <namespace>", "Delete an Artifacts namespace", "", cobra.ExactArgs(1),
	func(a []string) string { return "Artifacts namespace " + a[0] }, artNS())

// --- repos --------------------------------------------------------------------

var artifactsReposCmd = &cobra.Command{
	Use:     "repos",
	Aliases: []string{"repo"},
	Short:   "Manage Git repositories in a namespace",
}

var artifactsReposListCmd = &cobra.Command{
	Use:   "list <namespace>",
	Short: "List repositories in a namespace",
	Args:  cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		q := url.Values{}
		if s, _ := cmd.Flags().GetString("search"); s != "" {
			q.Set("search", s)
		}
		if s, _ := cmd.Flags().GetString("sort"); s != "" {
			q.Set("sort", s)
		}
		raw, err := c.all(c.p("artifacts/namespaces", args[0], "repos"), q)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("📚 %d repositories", "No repositories found", stItems(raw), []stCol{
				{Header: "NAME", Path: "name"},
				{Header: "DEFAULT BRANCH", Path: "default_branch"},
				{Header: "READ-ONLY", Path: "read_only"},
				{Header: "LAST PUSH", Path: "last_push_at"},
				{Header: "DESCRIPTION", Path: "description", Fmt: sbTrunc(40)},
			})
			return nil
		})
	}),
}

var artifactsReposGetCmd = sbGetCmd("get <namespace> <repo>", "Show a repository", "", "📚 Repository", cobra.ExactArgs(2), artRepo())

var artifactsReposDeleteCmd = sbDeleteCmd("delete <namespace> <repo>", "Delete a repository", "", cobra.ExactArgs(2),
	func(a []string) string { return "repository " + a[0] + "/" + a[1] }, artRepo())

var artifactsReposCreateCmd = &cobra.Command{
	Use:   "create <namespace> <repo>",
	Short: "Create an empty repository",
	Args:  cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		body["name"] = args[1]
		stFlagStr(cmd, body, "description", "description")
		stFlagStr(cmd, body, "default-branch", "default_branch")
		stFlagBool(cmd, body, "read-only-repo", "read_only")
		return sbSendShow(c, "POST", c.p("artifacts/namespaces", args[0], "repos"), nil, body, "Created repository "+args[1])
	}),
}

var artifactsReposForkCmd = &cobra.Command{
	Use:   "fork <namespace> <repo> <new-name>",
	Short: "Fork a repository within the namespace",
	Args:  cobra.ExactArgs(3),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		body["name"] = args[2]
		stFlagStr(cmd, body, "description", "description")
		stFlagBool(cmd, body, "read-only-repo", "read_only")
		stFlagBool(cmd, body, "default-branch-only", "default_branch_only")
		p, _, _ := artRepo("fork")(c, args)
		return sbSendShow(c, "POST", p, nil, body, fmt.Sprintf("Forked %s as %s", args[1], args[2]))
	}),
}

var artifactsReposImportCmd = &cobra.Command{
	Use:   "import <namespace> <repo> --url <git-url>",
	Short: "Import a repository from a public Git URL",
	Long: `Create a repository by importing from a Git URL.

Examples:
  cfctl artifacts repos import my-ns mirror --url https://github.com/org/repo.git
  cfctl artifacts repos import my-ns mirror --url https://github.com/org/repo.git --branch main --depth 1`,
	Args: cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := stBody(cmd)
		if err != nil {
			return err
		}
		stFlagStr(cmd, body, "url", "url")
		stFlagStr(cmd, body, "branch", "branch")
		stFlagInt(cmd, body, "depth", "depth")
		stFlagBool(cmd, body, "read-only-repo", "read_only")
		if body["url"] == nil {
			return fmt.Errorf("--url is required")
		}
		p, _, _ := artRepo("import")(c, args)
		return sbSendShow(c, "POST", p, nil, body, "Importing into "+args[1])
	}),
}

var artifactsReposLogCmd = &cobra.Command{
	Use:   "log <namespace> <repo>",
	Short: "Show commit history",
	Args:  cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		q := url.Values{}
		if r, _ := cmd.Flags().GetString("ref"); r != "" {
			q.Set("ref", r)
		}
		if n, _ := cmd.Flags().GetInt("limit"); n > 0 {
			q.Set("limit", strconv.Itoa(n))
		}
		if n, _ := cmd.Flags().GetInt("offset"); n > 0 {
			q.Set("offset", strconv.Itoa(n))
		}
		p, _, _ := artRepo("log")(c, args)
		raw, err := c.get(p, q)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("📜 %d commits", "No commits", stItems(raw), []stCol{
				{Header: "COMMIT", Path: "", Fmt: func(v any) string { return artShort(sbFirst("hash", "sha", "oid", "id")(v)) }},
				{Header: "AUTHOR", Path: "", Fmt: sbFirst("author.name", "author", "author_name")},
				{Header: "DATE", Path: "", Fmt: sbFirst("author.date", "date", "timestamp", "committed_at")},
				{Header: "MESSAGE", Path: "message", Fmt: sbTrunc(60)},
			})
			return nil
		})
	}),
}

func artShort(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

var artifactsReposFileCmd = &cobra.Command{
	Use:   "file <namespace> <repo> <ref> <path>",
	Short: "Print a file at a ref (raw bytes; --output to save)",
	Long: `Print a file's contents at a branch, tag, or commit. With --json, prints the
API's JSON view of the file instead.

Examples:
  cfctl artifacts repos file my-ns site main README.md
  cfctl artifacts repos file my-ns site v1.2 assets/logo.png --output logo.png`,
	Args: cobra.ExactArgs(4),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		if jsonOutput {
			raw, err := c.get(c.p("artifacts/namespaces", args[0], "repos", args[1], "file"), url.Values{"ref": {args[2]}, "path": {args[3]}})
			if err != nil {
				return err
			}
			return printBody(stNonNull(raw), nil)
		}
		segs := append([]string{args[0], "repos", args[1], "raw", args[2]}, strings.Split(strings.TrimPrefix(args[3], "/"), "/")...)
		resp, err := c.do(stReq{Method: "GET", Path: c.p("artifacts/namespaces", segs...)})
		if err != nil {
			return err
		}
		if out, _ := cmd.Flags().GetString("output"); out != "" && out != "-" {
			if err := os.WriteFile(out, resp.Body, 0o644); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, ui.Success(fmt.Sprintf("Wrote %d bytes to %s", len(resp.Body), out)))
			return nil
		}
		_, err = os.Stdout.Write(resp.Body)
		return err
	}),
}

func artGitObject(kind string) *cobra.Command {
	return sbGetCmd(kind+" <namespace> <repo> <hash>", "Read a Git "+kind+" object", "", "🧱 Git "+kind, cobra.ExactArgs(3),
		func(c *stClient, args []string) (string, url.Values, error) {
			return c.p("artifacts/namespaces", args[0], "repos", args[1], kind, args[2]), nil, nil
		})
}

var artifactsReposTokensCmd = &cobra.Command{
	Use:   "tokens <namespace> <repo>",
	Short: "List a repository's access tokens",
	Args:  cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		q := url.Values{}
		if s, _ := cmd.Flags().GetString("state"); s != "" {
			q.Set("state", s)
		}
		p, _, _ := artRepo("tokens")(c, args)
		raw, err := c.all(p, q)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("🎟️  %d tokens", "No tokens", stItems(raw), []stCol{
				{Header: "ID", Path: "id"},
				{Header: "SCOPE", Path: "scope"},
				{Header: "STATE", Path: "state"},
				{Header: "EXPIRES", Path: "expires_at"},
				{Header: "CREATED", Path: "created_at"},
			})
			return nil
		})
	}),
}

var artifactsReposIssueTokenCmd = &cobra.Command{
	Use:   "issue-token <namespace> <repo>",
	Short: "Issue a repo-scoped access token (printed once)",
	Long: `Issue a token scoped to one repository. The token is printed once; store it
somewhere safe.

Examples:
  cfctl artifacts repos issue-token my-ns site --scope read --ttl 86400
  cfctl artifacts repos issue-token my-ns site --scope write --json`,
	Args: cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body := map[string]any{"repo": args[1]}
		stFlagStr(cmd, body, "scope", "scope")
		stFlagInt(cmd, body, "ttl", "ttl")
		raw, err := c.result(stReq{Method: "POST", Path: c.p("artifacts/namespaces", args[0], "tokens"), Body: body})
		if err != nil {
			return err
		}
		return stEmit(raw, func() error { return stDetail("🎟️  Token issued for "+args[1]+" (shown once)", raw) })
	}),
}

var artifactsReposRevokeTokenCmd = sbDeleteCmd("revoke-token <namespace> <token-id>", "Revoke a repo token", "", cobra.ExactArgs(2),
	func(a []string) string { return "token " + a[1] },
	func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("artifacts/namespaces", args[0], "tokens", args[1]), nil, nil
	})

func init() {
	artifactsNSCreateCmd.Flags().String("jurisdiction", "", "Data jurisdiction: unrestricted, us, eu, or fedramp")
	artifactsNamespacesCmd.AddCommand(artifactsNSListCmd, artifactsNSGetCmd, artifactsNSCreateCmd, artifactsNSDeleteCmd)

	artifactsReposListCmd.Flags().String("search", "", "Filter repositories by name")
	artifactsReposListCmd.Flags().String("sort", "", "Sort by created_at, updated_at, last_push_at, or name")
	for _, cmd := range []*cobra.Command{artifactsReposCreateCmd, artifactsReposForkCmd, artifactsReposImportCmd} {
		cmd.Flags().Bool("read-only-repo", false, "Make the repository read-only (not to be confused with the global --read-only guard)")
		stDataFlag(cmd)
	}
	artifactsReposCreateCmd.Flags().String("description", "", "Description")
	artifactsReposCreateCmd.Flags().String("default-branch", "", "Default branch name")
	artifactsReposForkCmd.Flags().String("description", "", "Description")
	artifactsReposForkCmd.Flags().Bool("default-branch-only", false, "Fork only the default branch")
	artifactsReposImportCmd.Flags().String("url", "", "Git URL to import from")
	artifactsReposImportCmd.Flags().String("branch", "", "Branch to import")
	artifactsReposImportCmd.Flags().Int("depth", 0, "Shallow-import this many commits")
	artifactsReposLogCmd.Flags().String("ref", "", "Branch, tag, or commit (default HEAD)")
	artifactsReposLogCmd.Flags().Int("limit", 0, "Maximum commits")
	artifactsReposLogCmd.Flags().Int("offset", 0, "Skip this many commits")
	artifactsReposFileCmd.Flags().StringP("output", "o", "", "Write the file here instead of stdout")
	artifactsReposTokensCmd.Flags().String("state", "", "Token state: active (default), expired, revoked, or all")
	artifactsReposIssueTokenCmd.Flags().String("scope", "", "Token scope: read or write")
	artifactsReposIssueTokenCmd.Flags().Int("ttl", 0, "Token lifetime in seconds")
	artifactsReposCmd.AddCommand(artifactsReposListCmd, artifactsReposGetCmd, artifactsReposCreateCmd, artifactsReposDeleteCmd,
		artifactsReposForkCmd, artifactsReposImportCmd, artifactsReposLogCmd, artifactsReposFileCmd,
		artGitObject("blob"), artGitObject("commit"), artGitObject("tree"),
		artifactsReposTokensCmd, artifactsReposIssueTokenCmd, artifactsReposRevokeTokenCmd)

	artifactsCmd.AddCommand(artifactsNamespacesCmd, artifactsReposCmd)
	rootCmd.AddCommand(artifactsCmd)
}
