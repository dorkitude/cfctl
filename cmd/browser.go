package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const brBase = "/accounts/{account_id}/browser-rendering"

// brDevtoolsURL picks the DevTools URL of the page target (or the first).
func brDevtoolsURL(targets []map[string]any, selector string) (string, error) {
	var pick map[string]any
	for _, t := range targets {
		if selector != "" {
			if t["id"] == selector || strings.Contains(platStr(t["url"]), selector) || strings.Contains(platStr(t["title"]), selector) {
				pick = t
				break
			}
			continue
		}
		if t["type"] == "page" {
			pick = t
			break
		}
	}
	if pick == nil && selector == "" && len(targets) > 0 {
		pick = targets[0]
	}
	if pick == nil {
		return "", fmt.Errorf("no matching target")
	}
	u := platStr(pick["devtoolsFrontendUrl"])
	if u == "" {
		return "", fmt.Errorf("target %s has no DevTools URL", platStr(pick["id"]))
	}
	return u, nil
}

func init() {
	sessionCols := []platCol{
		{H: "session", Path: "sessionId"}, {H: "start", Path: "startTime"}, {H: "connection", Path: "connectionId"},
		{H: "connected", Path: "connectionStartTime"},
	}
	brCmd := platGroup("browser", "Browser Run (Browser Rendering): sessions and quick actions", `Manage Browser Run (Browser Rendering) sessions, and run one-shot rendering actions.

  cfctl browser list | get <session> | create | view <session> | close <session>
  cfctl browser screenshot|pdf <url> -o file
  cfctl browser markdown|content|links <url>

Quick actions are billed browser time. Generated: 'cfctl api browser-rendering ...'.`, []string{"browser-rendering"},
		platSpecs(
			platSpec{Use: "list", Short: "List active browser sessions", Aliases: []string{"ls"}, Path: brBase + "/devtools/session",
				Cols: sessionCols, Title: "🌐 %d browser sessions", Product: "Browser Run"},
			platSpec{Use: "get <session-id>", Short: "Show a browser session", Path: brBase + "/devtools/session/{session_id}", Product: "Browser Run"},
			platSpec{Use: "create", Short: "Start a browser session and print its DevTools URL", Method: "POST", Path: brBase + "/devtools/browser",
				Flags: func(c *cobra.Command) {
					c.Flags().IntP("keep-alive", "k", 0, "Keep-alive in seconds (default: service default)")
					c.Flags().Bool("lab", false, "Lab session with experimental Chrome features")
				},
				Query: func(c *cobra.Command, _ []string, q url.Values) error {
					q.Set("targets", "true")
					if lab, _ := c.Flags().GetBool("lab"); lab {
						q.Set("lab", "true")
					}
					if k, _ := c.Flags().GetInt("keep-alive"); k > 0 {
						q.Set("keep_alive", strconv.Itoa(k*1000))
					}
					return nil
				},
				Print: func(_ *cobra.Command, _ []string, raw json.RawMessage) error {
					var r struct {
						SessionID string           `json:"sessionId"`
						Targets   []map[string]any `json:"targets"`
					}
					if err := platDecode(raw, &r); err != nil {
						return err
					}
					fmt.Println(ui.Success("Session created: " + r.SessionID))
					if u, err := brDevtoolsURL(r.Targets, ""); err == nil {
						fmt.Println("  DevTools: " + u)
					}
					return nil
				}, Product: "Browser Run"},
			platSpec{Use: "view <session-id>", Short: "Print the DevTools URL for a live session", Path: brBase + "/devtools/browser/{session_id}/json",
				Flags: func(c *cobra.Command) {
					c.Flags().String("target", "", "Target ID, or a substring of its URL or title")
				},
				Print: func(c *cobra.Command, _ []string, raw json.RawMessage) error {
					var targets []map[string]any
					if err := platDecode(raw, &targets); err != nil {
						return err
					}
					sel, _ := c.Flags().GetString("target")
					u, err := brDevtoolsURL(targets, sel)
					if err != nil {
						return err
					}
					fmt.Println(u)
					return nil
				}, Product: "Browser Run"},
			platSpec{Use: "close <session-id>", Short: "Close a browser session", Method: "DELETE", Path: brBase + "/devtools/browser/{session_id}",
				Done: "Closed session %s", Product: "Browser Run"},
		)...)
	for _, a := range []struct {
		name, short string
		binary      bool
	}{
		{"screenshot", "Take a screenshot of a page (PNG)", true},
		{"pdf", "Render a page as PDF", true},
		{"markdown", "Fetch a page as Markdown", false},
		{"content", "Fetch a page's rendered HTML", false},
		{"links", "List the links on a page", false},
	} {
		brCmd.AddCommand(brActionCmd(a.name, a.short, a.binary))
	}
	rootCmd.AddCommand(brCmd)
}

// brActionCmd is a one-shot rendering action (POST /browser-rendering/<action>).
func brActionCmd(action, short string, binary bool) *cobra.Command {
	c := &cobra.Command{
		Use:   action + " <url>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			body := map[string]any{}
			if c.Flags().Changed("data") {
				d, _ := c.Flags().GetString("data")
				b, err := api.ReadData(d, os.Stdin)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(b, &body); err != nil {
					return fmt.Errorf("--data must be a JSON object: %w", err)
				}
			}
			if strings.HasPrefix(args[0], "<") {
				body["html"] = args[0]
			} else {
				body["url"] = args[0]
			}
			out, _ := c.Flags().GetString("output")
			if binary && out == "" {
				return fmt.Errorf("--output <file> is required for %s", action)
			}
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			path, _, err := platFill(ctx, s, brBase+"/"+action, nil)
			if err != nil {
				return err
			}
			b, _ := json.Marshal(body)
			resp, err := s.c.Do(ctx, api.Request{Method: "POST", Path: path, Body: b, ContentType: "application/json"})
			if err != nil {
				return platErr("Browser Run", err)
			}
			if binary {
				if err := os.WriteFile(out, resp.Body, 0o644); err != nil {
					return err
				}
				if jsonOutput {
					return printJSONValue(map[string]any{"output": out, "bytes": len(resp.Body)})
				}
				fmt.Println(ui.Success(fmt.Sprintf("Wrote %s (%d bytes)", out, len(resp.Body))))
				return nil
			}
			raw := json.RawMessage(resp.Body)
			if resp.Envelope != nil {
				raw = resp.Envelope.Result
			}
			if jsonOutput {
				return printBody(raw, nil)
			}
			var v any
			if json.Unmarshal(raw, &v) == nil {
				switch t := v.(type) {
				case string:
					raw = json.RawMessage(t)
				case []any:
					for _, x := range t {
						fmt.Println(platStr(x))
					}
					return nil
				}
			}
			if out != "" {
				return os.WriteFile(out, raw, 0o644)
			}
			fmt.Println(string(raw))
			return nil
		},
	}
	c.Flags().StringP("output", "o", "", "Write the result to this file")
	c.Flags().String("data", "", "Extra request options as JSON (e.g. '{\"viewport\":{\"width\":1280,\"height\":720}}')")
	return c
}
