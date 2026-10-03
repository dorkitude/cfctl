package cmd

// Small command builders shared by the storage-area groups written on top of
// r2_common.go (queues, hyperdrive, vectorize, secrets-store, k2, basin,
// artifacts, agent-memory). Identifiers are prefixed "sb".

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

// sbPathFn builds a request path (and optional query) from the positional args.
type sbPathFn func(c *stClient, args []string) (string, url.Values, error)

// sbRun wraps a RunE that needs an authenticated storage client.
func sbRun(fn func(cmd *cobra.Command, c *stClient, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		return fn(cmd, c, args)
	}
}

// sbListCmd builds a list command: GET every page, table or --json.
// title is a Sprintf format taking the item count.
func sbListCmd(use, short, long string, args cobra.PositionalArgs, path sbPathFn, title, empty string, cols []stCol) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  args,
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			p, q, err := path(c, args)
			if err != nil {
				return err
			}
			raw, err := c.all(p, q)
			if err != nil {
				return err
			}
			return stEmit(raw, func() error {
				stList(title, empty, stItems(raw), cols)
				return nil
			})
		}),
	}
}

// sbGetCmd builds a "show one object" command.
func sbGetCmd(use, short, long, title string, args cobra.PositionalArgs, path sbPathFn) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  args,
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			p, q, err := path(c, args)
			if err != nil {
				return err
			}
			raw, err := c.get(p, q)
			if err != nil {
				return err
			}
			return stEmit(raw, func() error { return stDetail(title, raw) })
		}),
	}
}

// sbDeleteCmd builds a confirmed DELETE command. what describes the target
// for the confirmation prompt and success line.
func sbDeleteCmd(use, short, long string, args cobra.PositionalArgs, what func(args []string) string, path sbPathFn) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  args,
		RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
			p, q, err := path(c, args)
			if err != nil {
				return err
			}
			if err := confirm(cmd, "delete "+what(args)); err != nil {
				return err
			}
			raw, err := c.result(stReq{Method: "DELETE", Path: p, Query: q})
			if err != nil {
				return err
			}
			return stOK(raw, "Deleted "+what(args))
		}),
	}
	stYes(cmd)
	return cmd
}

// sbSend sends a JSON body and prints success (or the result with --json).
func sbSend(c *stClient, method, path string, q url.Values, body any, msg string) error {
	raw, err := c.result(stReq{Method: method, Path: path, Query: q, Body: body})
	if err != nil {
		return err
	}
	return stOK(raw, msg)
}

// sbSendShow sends a JSON body and shows the resulting object.
func sbSendShow(c *stClient, method, path string, q url.Values, body any, msg string) error {
	raw, err := c.result(stReq{Method: method, Path: path, Query: q, Body: body})
	if err != nil {
		return err
	}
	if jsonOutput {
		return printBody(stNonNull(raw), nil)
	}
	return stDetail("✓ "+msg, raw)
}

// sbField returns the first non-empty string field of an item.
func sbField(it map[string]any, names ...string) string {
	for _, n := range names {
		if s := stString(stGet(it, n)); s != "" {
			return s
		}
	}
	return ""
}

// sbFirst renders the first non-empty of several fields (for a table column
// whose name differs between API versions). Use with Path "".
func sbFirst(names ...string) func(v any) string {
	return func(v any) string {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		return sbField(m, names...)
	}
}

// sbJSONFlag parses a JSON flag value (literal, @file, or -).
func sbJSONFlag(cmd *cobra.Command, flag string) (any, bool, error) {
	if !cmd.Flags().Changed(flag) {
		return nil, false, nil
	}
	spec, _ := cmd.Flags().GetString(flag)
	data, err := stReadInput(spec)
	if err != nil {
		return nil, false, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, false, fmt.Errorf("--%s must be JSON: %w", flag, err)
	}
	return v, true, nil
}

// sbTrunc shortens a cell for tables.
func sbTrunc(n int) func(v any) string {
	return func(v any) string {
		s := strings.ReplaceAll(stString(v), "\n", " ")
		if len(s) > n {
			return s[:n-1] + "…"
		}
		return s
	}
}

// sbNoPath is a path function with no args.
func sbFixed(base string, segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		return c.p(base, segs...), nil, nil
	}
}
