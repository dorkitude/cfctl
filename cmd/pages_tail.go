package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/dorkitude/cfctl/internal/version"
	"github.com/spf13/cobra"
)

func pagesTailSpec() platSpec {
	return platSpec{
		Use:   "tail <project> [deployment-id]",
		Short: "Stream live logs from a deployment's Functions",
		Long: `Start a tail session for a Pages deployment and stream its Functions logs
(console.log, exceptions, request outcomes) until Ctrl-C. Without a deployment
ID, the latest deployment for --env is used.`,
		Example: `  cfctl pages deployment tail my-site
  cfctl pages deployment tail my-site --env preview --status error
  cfctl pages deployment tail my-site <deployment-id> --format json`,
		Path: pagesBase + "/{project_name}", OptionalArgs: 1, Product: "Pages",
		Flags: func(c *cobra.Command) {
			c.Flags().String("env", "production", "Environment of the latest deployment to tail: production or preview")
			c.Flags().String("format", "pretty", "Output format: pretty or json")
			c.Flags().StringSlice("status", nil, "Only requests with this outcome: ok, error, canceled")
			c.Flags().StringSlice("method", nil, "Only requests with this HTTP method")
			c.Flags().String("header", "", "Only requests with this header (name or name:value)")
			c.Flags().StringSlice("ip", nil, "Only requests from these client IPs ('self' for yours)")
			c.Flags().String("search", "", "Only events whose logs contain this text")
			c.Flags().Float64("sampling-rate", 0, "Fraction of events to receive (0-1; 0 = all)")
			c.Flags().Bool("once", false, "Exit after the first event (useful for scripts and tests)")
		},
		Run: runPagesTail,
	}
}

// platTailFilters builds the tail filter body from flags (same shape as
// wrangler's).
func platTailFilters(c *cobra.Command) (map[string]any, error) {
	var filters []any
	if st, _ := c.Flags().GetStringSlice("status"); len(st) > 0 {
		var outcomes []string
		for _, s := range st {
			switch s {
			case "ok":
				outcomes = append(outcomes, "ok")
			case "error":
				outcomes = append(outcomes, "exception", "exceededCpu", "exceededMemory", "unknown")
			case "canceled":
				outcomes = append(outcomes, "canceled")
			default:
				return nil, fmt.Errorf("--status must be ok, error, or canceled")
			}
		}
		filters = append(filters, map[string]any{"outcome": outcomes})
	}
	if r, _ := c.Flags().GetFloat64("sampling-rate"); r > 0 {
		filters = append(filters, map[string]any{"sampling_rate": r})
	}
	if m, _ := c.Flags().GetStringSlice("method"); len(m) > 0 {
		filters = append(filters, map[string]any{"method": m})
	}
	if h, _ := c.Flags().GetString("header"); h != "" {
		k, v, ok := strings.Cut(h, ":")
		hf := map[string]any{"key": strings.TrimSpace(k)}
		if ok {
			hf["query"] = strings.TrimSpace(v)
		}
		filters = append(filters, map[string]any{"header": hf})
	}
	if ips, _ := c.Flags().GetStringSlice("ip"); len(ips) > 0 {
		filters = append(filters, map[string]any{"client_ip": ips})
	}
	if q, _ := c.Flags().GetString("search"); q != "" {
		filters = append(filters, map[string]any{"query": q})
	}
	if filters == nil {
		filters = []any{}
	}
	return map[string]any{"filters": filters}, nil
}

func runPagesTail(c *cobra.Command, args []string) error {
	format, _ := c.Flags().GetString("format")
	if format != "pretty" && format != "json" {
		return fmt.Errorf("--format must be pretty or json")
	}
	if jsonOutput {
		format = "json"
	}
	filters, err := platTailFilters(c)
	if err != nil {
		return err
	}
	ctx := context.Background()
	s, err := newAPISession(c)
	if err != nil {
		return err
	}
	projPath, _, err := platFill(ctx, s, pagesBase+"/{project_name}", args[:1])
	if err != nil {
		return err
	}
	depID := ""
	if len(args) > 1 {
		depID = args[1]
	} else {
		env, _ := c.Flags().GetString("env")
		raw, err := platDo(ctx, s, "GET", projPath+"/deployments", url.Values{"env": {env}, "per_page": {"1"}}, nil)
		if err != nil {
			return platErr("Pages", err)
		}
		var deps []struct {
			ID string `json:"id"`
		}
		if err := platDecode(raw, &deps); err != nil {
			return err
		}
		if len(deps) == 0 {
			return fmt.Errorf("no %s deployments for %s", env, args[0])
		}
		depID = deps[0].ID
	}
	tailBase := projPath + "/deployments/" + url.PathEscape(depID) + "/tails"
	raw, err := platDo(ctx, s, "POST", tailBase, nil, filters)
	if err != nil {
		return platErr("Pages", err)
	}
	var tail struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := platDecode(raw, &tail); err != nil || tail.URL == "" {
		return fmt.Errorf("Pages didn't return a tail URL")
	}
	defer func() {
		// Best-effort cleanup with a fresh context (the main one may be canceled).
		dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = platDo(dctx, s, "DELETE", tailBase+"/"+url.PathEscape(tail.ID), nil, nil)
	}()
	once, _ := c.Flags().GetBool("once")
	return platStreamTail(ctx, tail.URL, format, once, fmt.Sprintf("Connected to deployment %s, waiting for logs... (Ctrl-C to stop)", depID))
}

// platStreamTail connects to a trace-v1 tail WebSocket and prints events.
// The tail URL is pre-authorized; no API token is sent to it.
func platStreamTail(ctx context.Context, wsURL, format string, once bool, banner string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"trace-v1"},
		HTTPHeader:   map[string][]string{"User-Agent": {"cfctl/" + version.Version}},
	})
	if err != nil {
		return fmt.Errorf("failed to connect to the tail: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(32 << 20)
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"debug":false}`)); err != nil {
		return fmt.Errorf("failed to start the tail: %w", err)
	}
	if format == "pretty" {
		fmt.Fprintln(os.Stderr, ui.Info(banner))
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return fmt.Errorf("tail closed: %w", err)
		}
		if format == "json" {
			fmt.Println(strings.TrimSpace(string(data)))
		} else {
			platPrintTailEvent(data)
		}
		if once {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return nil
		}
	}
}

// platPrintTailEvent renders one trace event like wrangler's pretty format.
func platPrintTailEvent(data []byte) {
	var ev map[string]any
	if json.Unmarshal(data, &ev) != nil {
		fmt.Println(string(data))
		return
	}
	outcome := platStr(ev["outcome"])
	when := ""
	if ts, ok := ev["eventTimestamp"].(float64); ok {
		when = time.UnixMilli(int64(ts)).Format("15:04:05")
	}
	head := ""
	if req, ok := platGet(ev, "event.request").(map[string]any); ok {
		head = fmt.Sprintf("%s %s", platStr(req["method"]), platStr(req["url"]))
	} else if cron := platStr(platGet(ev, "event.cron")); cron != "" {
		head = "cron " + cron
	} else {
		head = "event"
	}
	style := ui.SuccessStyle
	if outcome != "ok" {
		style = ui.ErrorStyle
	}
	fmt.Printf("%s %s %s\n", ui.SubtleStyle.Render(when), head, style.Render("- "+outcome))
	if logs, ok := ev["logs"].([]any); ok {
		for _, l := range logs {
			lvl := platStr(platGet(l, "level"))
			msg := platStr(platGet(l, "message"))
			fmt.Printf("  (%s) %s\n", lvl, msg)
		}
	}
	if exs, ok := ev["exceptions"].([]any); ok {
		for _, e := range exs {
			fmt.Println("  " + ui.ErrorStyle.Render(fmt.Sprintf("✘ %s: %s", platStr(platGet(e, "name")), platStr(platGet(e, "message")))))
		}
	}
}
