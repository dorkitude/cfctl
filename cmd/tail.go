package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/dorkitude/cfctl/internal/version"
	"github.com/dorkitude/cfctl/internal/workers"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// tailPingInterval is how often cfctl pings the tail socket (a var for tests).
var tailPingInterval = 10 * time.Second

func newTailCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "tail [name]",
		Short: "Stream a Worker's live logs",
		Long: `Start a log tail on a Worker and stream its events (requests, crons,
queues, console.log output, exceptions) until Ctrl-C, which deletes the tail.

Filters run on Cloudflare's side, like wrangler's.
--format pretty (default on a terminal) prints one summary line per event plus
its logs; --format json prints one JSON object per line (default when piped).

Examples:
  cfctl tail my-worker
  cfctl tail my-worker --status error
  cfctl tail my-worker --method POST --search "checkout" --sampling-rate 0.1
  cfctl tail my-worker --header "x-debug:1" --ip 203.0.113.7
  cfctl tail my-worker --format json | jq .logs`,
		Args: cobra.MaximumNArgs(1),
		RunE: runTail,
	}
	wkAddScriptFlags(c)
	c.Flags().String("format", "", "Output format: pretty or json (default: pretty on a terminal, else json)")
	c.Flags().StringArray("status", nil, "Only events with this outcome: ok, error, canceled (repeatable)")
	c.Flags().StringArray("method", nil, "Only requests with this HTTP method (repeatable)")
	c.Flags().StringArray("header", nil, "Only requests with this header: NAME or NAME:value (repeatable)")
	c.Flags().StringArray("ip", nil, "Only requests from this client IP (repeatable)")
	c.Flags().String("search", "", "Only events whose console.log messages contain this text")
	c.Flags().Float64("sampling-rate", 0, "Keep only this fraction of events (0-1)")
	return c
}

func runTail(cmd *cobra.Command, args []string) error {
	name, err := wkScriptName(cmd, args)
	if err != nil {
		return err
	}
	format, _ := cmd.Flags().GetString("format")
	if format == "" {
		format = "json"
		if term.IsTerminal(int(os.Stdout.Fd())) && !jsonOutput {
			format = "pretty"
		}
	}
	if jsonOutput {
		format = "json"
	}
	if format != "pretty" && format != "json" {
		return fmt.Errorf("--format must be pretty or json")
	}
	var o workers.TailFilterOptions
	o.Status, _ = cmd.Flags().GetStringArray("status")
	o.Methods, _ = cmd.Flags().GetStringArray("method")
	o.Headers, _ = cmd.Flags().GetStringArray("header")
	o.ClientIP, _ = cmd.Flags().GetStringArray("ip")
	o.Search, _ = cmd.Flags().GetString("search")
	o.Sampling, _ = cmd.Flags().GetFloat64("sampling-rate")
	filters, err := o.Filters()
	if err != nil {
		return err
	}

	s, err := newAPISession(cmd)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var tail struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	if _, err := wkCall(ctx, s, "POST", wkScriptPath(name, "/tails"), nil, map[string]any{"filters": filters}, &tail); err != nil {
		return fmt.Errorf("failed to start a tail on %s: %w", name, err)
	}
	if tail.ID == "" || tail.URL == "" {
		return fmt.Errorf("the API did not return a tail URL")
	}
	// Always delete the tail, even on Ctrl-C (with a fresh context).
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := wkCall(dctx, s, "DELETE", wkScriptPath(name, "/tails/"+url.PathEscape(tail.ID)), nil, nil, nil); err != nil {
			fmt.Fprintln(os.Stderr, ui.Warn("failed to delete tail "+tail.ID+": "+err.Error()))
		} else if format == "pretty" {
			fmt.Fprintln(os.Stderr, ui.Info("tail closed"))
		}
	}()

	hdr := http.Header{"User-Agent": {"cfctl/" + version.Version}}
	conn, err := workers.DialWS(ctx, tail.URL, workers.TailProtocol, hdr, api.Timeout)
	if api.Debug() {
		host := tail.URL
		if u, perr := url.Parse(tail.URL); perr == nil {
			host = u.Host
		}
		status := "101"
		if err != nil {
			status = "error"
		}
		fmt.Fprintf(api.DebugOut, "debug: GET %s (tail websocket) -> %s\n", host, status)
	}
	if err != nil {
		return fmt.Errorf("failed to connect to the tail: %w", err)
	}
	defer conn.Close()
	_ = conn.WriteText([]byte(`{"debug":false}`))
	if format == "pretty" {
		fmt.Fprintln(os.Stderr, ui.Success("Connected to "+name+", waiting for logs... (Ctrl-C to stop)"))
	}

	msgs := make(chan []byte)
	errc := make(chan error, 1)
	go func() {
		for {
			m, err := conn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			select {
			case msgs <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	ping := time.NewTicker(tailPingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ping.C:
			_ = conn.Ping()
		case err := <-errc:
			if errors.Is(err, workers.ErrWSClosed) || ctx.Err() != nil || strings.Contains(err.Error(), "EOF") {
				if format == "pretty" {
					fmt.Fprintln(os.Stderr, ui.Info("tail connection closed by Cloudflare"))
				}
				return nil
			}
			return fmt.Errorf("tail connection: %w", err)
		case m := <-msgs:
			if format == "json" {
				var v any
				if json.Unmarshal(m, &v) == nil {
					b, _ := json.Marshal(v)
					fmt.Println(string(b))
				} else {
					fmt.Println(strings.TrimSpace(string(m)))
				}
				continue
			}
			fmt.Println(workers.FormatTailPretty(m, nil))
		}
	}
}

func init() {
	workersCmd.AddCommand(newTailCmd())
	rootCmd.AddCommand(newTailCmd())
}
