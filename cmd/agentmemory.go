package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var agentMemoryCmd = &cobra.Command{
	Use:   "agent-memory",
	Short: "Manage Agent Memory namespaces, profiles, and memories [private beta]",
	Long: `Manage Agent Memory: namespaces, the profiles in them, and the memories
stored for each profile (remember, recall, ingest conversations).

Examples:
  cfctl agent-memory namespace list
  cfctl agent-memory namespace create support-bot
  cfctl agent-memory memories remember support-bot user-42 "Prefers email over phone"
  cfctl agent-memory memories recall support-bot user-42 "how should we contact them?"
  cfctl agent-memory memories ingest support-bot user-42 --file conversation.json
  cfctl agent-memory profile summary support-bot user-42

Generated equivalents: cfctl api namespaces <op>, cfctl api memory <op>.`,
}

func amNS(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("agent-memory/namespaces", append([]string{args[0]}, segs...)...), nil, nil
	}
}

func amProfile(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("agent-memory/namespaces", append([]string{args[0], "profiles", args[1]}, segs...)...), nil, nil
	}
}

// --- namespaces -----------------------------------------------------------------

var agentMemoryNamespaceCmd = &cobra.Command{
	Use:     "namespace",
	Aliases: []string{"namespaces", "ns"},
	Short:   "List, create, and delete Agent Memory namespaces",
}

var agentMemoryNSListCmd = sbListCmd("list", "List Agent Memory namespaces", "", cobra.NoArgs, sbFixed("agent-memory/namespaces"),
	"🧠 %d namespaces", "No Agent Memory namespaces found", []stCol{
		{Header: "NAME", Path: "name"},
		{Header: "ID", Path: "id"},
		{Header: "CREATED", Path: "", Fmt: sbFirst("created_at", "created_on")},
	})

var agentMemoryNSGetCmd = sbGetCmd("get <namespace>", "Show a namespace", "", "🧠 Namespace", cobra.ExactArgs(1), amNS())

var agentMemoryNSCreateCmd = &cobra.Command{
	Use:   "create <namespace>",
	Short: "Create a namespace",
	Args:  cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		return sbSendShow(c, "POST", c.p("agent-memory/namespaces"), nil, map[string]any{"name": args[0]}, "Created namespace "+args[0])
	}),
}

var agentMemoryNSDeleteCmd = sbDeleteCmd("delete <namespace>", "Delete a namespace and all its memories", "", cobra.ExactArgs(1),
	func(a []string) string { return "Agent Memory namespace " + a[0] }, amNS())

// --- profiles -------------------------------------------------------------------

var agentMemoryProfileCmd = &cobra.Command{
	Use:     "profile",
	Aliases: []string{"profiles"},
	Short:   "List, summarize, and delete profiles (one per user or agent)",
}

var agentMemoryProfileListCmd = sbListCmd("list <namespace>", "List profiles in a namespace", "", cobra.ExactArgs(1), amNS("profiles"),
	"👤 %d profiles", "No profiles", []stCol{
		{Header: "NAME", Path: "", Fmt: sbFirst("name", "profile_name", "id")},
		{Header: "MEMORIES", Path: "", Fmt: sbFirst("memory_count", "memories")},
		{Header: "UPDATED", Path: "", Fmt: sbFirst("updated_at", "last_updated", "created_at")},
	})

var agentMemoryProfileDeleteCmd = sbDeleteCmd("delete <namespace> <profile>", "Delete a profile and its memories", "", cobra.ExactArgs(2),
	func(a []string) string { return "profile " + a[1] + " in " + a[0] }, amProfile())

var agentMemoryProfileSummaryCmd = &cobra.Command{
	Use:   "summary <namespace> <profile>",
	Short: "Summarize what's remembered about a profile",
	Args:  cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body := map[string]any{}
		stFlagStr(cmd, body, "session", "sessionId")
		p, _, _ := amProfile("summary")(c, args)
		raw, err := c.result(stReq{Method: "POST", Path: p, Body: body})
		if err != nil {
			return err
		}
		return stEmit(raw, func() error { return amText("🧠 Summary of "+args[1], raw) })
	}),
}

// amText prints a text answer field if present, else the object.
func amText(title string, raw json.RawMessage) error {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		for _, k := range []string{"answer", "summary", "text", "result"} {
			if s, ok := m[k].(string); ok && s != "" {
				fmt.Println(title)
				fmt.Println()
				fmt.Println(s)
				return nil
			}
		}
	}
	return stDetail(title, raw)
}

// --- memories -------------------------------------------------------------------

var agentMemoryMemoriesCmd = &cobra.Command{
	Use:     "memories",
	Aliases: []string{"memory"},
	Short:   "List, add, recall, and delete a profile's memories",
}

var agentMemoryMemoriesListCmd = &cobra.Command{
	Use:   "list <namespace> <profile>",
	Short: "List a profile's memories",
	Args:  cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		q := url.Values{}
		if s, _ := cmd.Flags().GetString("session"); s != "" {
			q.Set("session_id", s)
		}
		if s, _ := cmd.Flags().GetString("type"); s != "" {
			q.Set("type", s)
		}
		p, _, _ := amProfile("memories")(c, args)
		raw, err := c.all(p, q)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("🧠 %d memories", "No memories", stItems(raw), []stCol{
				{Header: "ID", Path: "id"},
				{Header: "TYPE", Path: "type"},
				{Header: "CONTENT", Path: "", Fmt: func(v any) string { return sbTrunc(70)(sbFirst("content", "text", "memory")(v)) }},
				{Header: "CREATED", Path: "", Fmt: sbFirst("created_at", "createdAt")},
			})
			return nil
		})
	}),
}

var agentMemoryMemoriesGetCmd = sbGetCmd("get <namespace> <profile> <memory-id>", "Show one memory", "", "🧠 Memory", cobra.ExactArgs(3),
	func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("agent-memory/namespaces", args[0], "profiles", args[1], "memories", args[2]), nil, nil
	})

var agentMemoryMemoriesDeleteCmd = sbDeleteCmd("delete <namespace> <profile> <memory-id>", "Delete one memory", "", cobra.ExactArgs(3),
	func(a []string) string { return "memory " + a[2] },
	func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("agent-memory/namespaces", args[0], "profiles", args[1], "memories", args[2]), nil, nil
	})

var agentMemoryRememberCmd = &cobra.Command{
	Use:   "remember <namespace> <profile> <content>",
	Short: "Store a memory (use - to read the content from stdin)",
	Args:  cobra.ExactArgs(3),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		content, err := stReadInput(args[2])
		if err != nil {
			return err
		}
		body := map[string]any{"content": strings.TrimRight(string(content), "\n")}
		stFlagStr(cmd, body, "session", "sessionId")
		p, _, _ := amProfile("remember")(c, args)
		return sbSendShow(c, "POST", p, nil, body, "Remembered for "+args[1])
	}),
}

var agentMemoryRecallCmd = &cobra.Command{
	Use:   "recall <namespace> <profile> <query>",
	Short: "Ask a question answered from a profile's memories",
	Long: `Recall: answer a natural-language query from a profile's memories.

Examples:
  cfctl agent-memory memories recall support-bot user-42 "what plan are they on?"
  cfctl agent-memory memories recall support-bot user-42 "recent issues" --thinking high --length long`,
	Args: cobra.ExactArgs(3),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body := map[string]any{"query": args[2]}
		stFlagStr(cmd, body, "length", "responseLength")
		stFlagStr(cmd, body, "thinking", "thinkingLevel")
		stFlagStr(cmd, body, "reference-date", "referenceDate")
		p, _, _ := amProfile("recall")(c, args)
		raw, err := c.result(stReq{Method: "POST", Path: p, Body: body})
		if err != nil {
			return err
		}
		return stEmit(raw, func() error { return amText("🧠 "+args[2], raw) })
	}),
}

var agentMemoryIngestCmd = &cobra.Command{
	Use:   "ingest <namespace> <profile> --file <messages.json>",
	Short: "Extract memories from a conversation",
	Long: `Extract memories from conversation messages: a JSON file (or - for stdin)
holding an array of {"role": "user"|"assistant"|"system", "content": "...",
"timestamp": "..."} (or an object with a "messages" array).

Examples:
  cfctl agent-memory memories ingest support-bot user-42 --file chat.json --session s-1`,
	Args: cobra.ExactArgs(2),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		if file == "" {
			return fmt.Errorf("--file is required")
		}
		if file != "-" {
			file = "@" + file
		}
		data, err := stReadInput(file)
		if err != nil {
			return err
		}
		body := map[string]any{}
		var msgs []any
		if json.Unmarshal(data, &msgs) == nil {
			body["messages"] = msgs
		} else if err := json.Unmarshal(data, &body); err != nil || body["messages"] == nil {
			return fmt.Errorf("--file must be a JSON array of messages or an object with \"messages\"")
		}
		stFlagStr(cmd, body, "session", "sessionId")
		p, _, _ := amProfile("ingest")(c, args)
		return sbSendShow(c, "POST", p, nil, body, "Ingested conversation for "+args[1])
	}),
}

var agentMemorySessionCmd = &cobra.Command{
	Use:     "session",
	Aliases: []string{"sessions"},
	Short:   "Delete a session's memories",
}

var agentMemorySessionDeleteCmd = sbDeleteCmd("delete <namespace> <profile> <session-id>", "Delete the memories from one session", "", cobra.ExactArgs(3),
	func(a []string) string { return "session " + a[2] + " of " + a[1] },
	func(c *stClient, args []string) (string, url.Values, error) {
		return c.p("agent-memory/namespaces", args[0], "profiles", args[1], "sessions", args[2]), nil, nil
	})

func init() {
	agentMemoryNamespaceCmd.AddCommand(agentMemoryNSListCmd, agentMemoryNSGetCmd, agentMemoryNSCreateCmd, agentMemoryNSDeleteCmd)

	agentMemoryProfileSummaryCmd.Flags().String("session", "", "Only this session")
	agentMemoryProfileCmd.AddCommand(agentMemoryProfileListCmd, agentMemoryProfileDeleteCmd, agentMemoryProfileSummaryCmd)

	agentMemoryMemoriesListCmd.Flags().String("session", "", "Only memories from this session")
	agentMemoryMemoriesListCmd.Flags().String("type", "", "Only this type: fact, event, instruction, or task")
	agentMemoryRememberCmd.Flags().String("session", "", "Session identifier")
	agentMemoryRecallCmd.Flags().String("length", "", "Answer length: short, medium, or long")
	agentMemoryRecallCmd.Flags().String("thinking", "", "Search depth: low, medium, or high")
	agentMemoryRecallCmd.Flags().String("reference-date", "", "Anchor for relative dates in the query")
	agentMemoryIngestCmd.Flags().String("file", "", "JSON file of messages (or - for stdin)")
	agentMemoryIngestCmd.Flags().String("session", "", "Session identifier")
	agentMemoryMemoriesCmd.AddCommand(agentMemoryMemoriesListCmd, agentMemoryMemoriesGetCmd, agentMemoryMemoriesDeleteCmd,
		agentMemoryRememberCmd, agentMemoryRecallCmd, agentMemoryIngestCmd)
	agentMemorySessionCmd.AddCommand(agentMemorySessionDeleteCmd)

	agentMemoryCmd.AddCommand(agentMemoryNamespaceCmd, agentMemoryProfileCmd, agentMemoryMemoriesCmd, agentMemorySessionCmd)
	rootCmd.AddCommand(agentMemoryCmd)
}
