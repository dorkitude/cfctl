package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// docsFS holds README.md, CHANGELOG.md, ROADMAP.md, and docs/ (embedded by
// the main package; see docs_embed.go).
var docsFS fs.FS

// SetDocsFS registers the embedded documentation.
func SetDocsFS(f fs.FS) { docsFS = f }

// docsTopics maps topic names to embedded paths.
func docsTopics() map[string]string {
	t := map[string]string{}
	if docsFS == nil {
		return t
	}
	for name, p := range map[string]string{"readme": "README.md", "changelog": "CHANGELOG.md", "roadmap": "ROADMAP.md"} {
		if _, err := fs.Stat(docsFS, p); err == nil {
			t[name] = p
		}
	}
	_ = fs.WalkDir(docsFS, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		base := strings.ToLower(strings.TrimSuffix(path.Base(p), ".md"))
		dir := path.Base(path.Dir(p))
		switch dir {
		case "docs", "commands":
			if _, dup := t[base]; !dup {
				t[base] = p
			}
		default:
			t[dir+"/"+base] = p
		}
		return nil
	})
	return t
}

var docsCmd = &cobra.Command{
	Use:   "docs [topic]",
	Short: "Read cfctl's documentation in the terminal",
	Long: `Render cfctl's documentation (embedded in the binary, so it works offline).

With no topic, shows the README. Topics include readme, changelog,
architecture, and one per command reference in docs/commands (e.g. platform).
Output is styled on a terminal and plain Markdown when piped (or with --raw).

Examples:
  cfctl docs                 # README
  cfctl docs --list          # available topics
  cfctl docs platform        # Pages, AI, Workflows, Tunnels, ... reference
  cfctl docs readme --raw > cfctl.md
  cfctl docs --search tunnel # which topics mention a word`,
	Args: cobra.MaximumNArgs(1),
	ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		var names []string
		for n := range docsTopics() {
			names = append(names, n)
		}
		sort.Strings(names)
		return names, cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		topics := docsTopics()
		if len(topics) == 0 {
			return fmt.Errorf("this build has no embedded docs")
		}
		names := make([]string, 0, len(topics))
		for n := range topics {
			names = append(names, n)
		}
		sort.Strings(names)
		if list, _ := cmd.Flags().GetBool("list"); list {
			if jsonOutput {
				out := map[string]string{}
				for _, n := range names {
					out[n] = topics[n]
				}
				return printJSONValue(out)
			}
			fmt.Println(ui.TitleStyle.Render("📚 cfctl docs topics"))
			for _, n := range names {
				fmt.Printf("  %-28s %s\n", ui.AccentStyle.Render(n), ui.SubtleStyle.Render(topics[n]))
			}
			return nil
		}
		if q, _ := cmd.Flags().GetString("search"); q != "" {
			return docsSearch(topics, names, q)
		}
		topic := "readme"
		if len(args) == 1 {
			topic = strings.ToLower(strings.TrimSuffix(args[0], ".md"))
		}
		p, ok := topics[topic]
		if !ok {
			return fmt.Errorf("no docs topic %q; available: %s", topic, strings.Join(names, ", "))
		}
		data, err := fs.ReadFile(docsFS, p)
		if err != nil {
			return err
		}
		raw, _ := cmd.Flags().GetBool("raw")
		if raw || !term.IsTerminal(int(os.Stdout.Fd())) {
			_, err := os.Stdout.Write(data)
			return err
		}
		fmt.Print(docsRender(string(data)))
		return nil
	},
}

func docsSearch(topics map[string]string, names []string, q string) error {
	lq := strings.ToLower(q)
	found := 0
	for _, n := range names {
		data, err := fs.ReadFile(docsFS, topics[n])
		if err != nil {
			continue
		}
		var hits []string
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), lq) {
				hits = append(hits, fmt.Sprintf("    %4d  %s", i+1, platTrunc(line, 100)))
			}
		}
		if len(hits) == 0 {
			continue
		}
		found++
		fmt.Printf("%s %s\n", ui.AccentStyle.Render(n), ui.SubtleStyle.Render(fmt.Sprintf("(%d matches)", len(hits))))
		if len(hits) > 5 {
			hits = append(hits[:5], "    ...")
		}
		fmt.Println(strings.Join(hits, "\n"))
	}
	if found == 0 {
		fmt.Println(ui.Warn("No docs mention " + q))
	}
	return nil
}

var (
	mdCode   = regexp.MustCompile("`([^`]+)`")
	mdBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	mdBullet = regexp.MustCompile(`^(\s*)[-*] `)
)

// docsRender is a small Markdown-to-terminal renderer: headings, fenced
// code, bullets, inline code, bold, links, and quotes. Tables pass through.
func docsRender(md string) string {
	var b strings.Builder
	inCode := false
	for _, line := range strings.Split(md, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			b.WriteString("    " + ui.SubtleStyle.Render(line) + "\n")
			continue
		}
		switch {
		case strings.HasPrefix(line, "# "):
			b.WriteString(ui.TitleStyle.Render(strings.ToUpper(strings.TrimPrefix(line, "# "))) + "\n")
			continue
		case strings.HasPrefix(line, "## "):
			b.WriteString("\n" + ui.TitleStyle.UnsetMarginBottom().Render(strings.TrimPrefix(line, "## ")) + "\n")
			continue
		case strings.HasPrefix(line, "### "), strings.HasPrefix(line, "#### "):
			h := strings.TrimLeft(line, "# ")
			b.WriteString("\n" + ui.AccentStyle.Render(h) + "\n")
			continue
		case strings.HasPrefix(trim, "> "):
			line = "  │ " + strings.TrimPrefix(trim, "> ")
		}
		line = mdBullet.ReplaceAllString(line, "$1• ")
		line = mdLink.ReplaceAllStringFunc(line, func(m string) string {
			sm := mdLink.FindStringSubmatch(m)
			if sm[1] == sm[2] || strings.HasPrefix(sm[2], "#") {
				return sm[1]
			}
			return sm[1] + " " + ui.SubtleStyle.Render("("+sm[2]+")")
		})
		line = mdBold.ReplaceAllStringFunc(line, func(m string) string {
			return ui.SuccessStyle.UnsetForeground().Render(strings.Trim(m, "*"))
		})
		line = mdCode.ReplaceAllStringFunc(line, func(m string) string {
			return ui.AccentStyle.UnsetBold().Render(strings.Trim(m, "`"))
		})
		b.WriteString(line + "\n")
	}
	return b.String()
}

func init() {
	docsCmd.Flags().Bool("list", false, "List the available topics")
	docsCmd.Flags().Bool("raw", false, "Print plain Markdown")
	docsCmd.Flags().String("search", "", "Show which topics mention a word")
	rootCmd.AddCommand(docsCmd)
}
