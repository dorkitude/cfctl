package cmd

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var k2Cmd = &cobra.Command{
	Use:   "k2",
	Short: "Manage K2 streams [open beta]",
	Long: `Manage K2 streams: durable record streams that Workers and HTTP clients can
produce to. A stream can be given by ID or name.

Examples:
  cfctl k2 streams list
  cfctl k2 streams create clicks --retention 86400 --http --http-auth
  cfctl k2 streams update clicks --cors-origins https://example.com
  cfctl k2 streams subscriptions clicks

Generated equivalents: cfctl api workers-k2-other <op>.`,
}

var k2StreamsCmd = &cobra.Command{
	Use:     "streams",
	Aliases: []string{"stream"},
	Short:   "Create, inspect, update, and delete K2 streams",
}

func k2StreamID(c *stClient, arg string) (string, error) {
	return c.resolve("K2 stream", c.p("k2/streams"), url.Values{"name": {arg}}, arg, "id", "name")
}

func k2Path(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		id, err := k2StreamID(c, args[0])
		if err != nil {
			return "", nil, err
		}
		return c.p("k2/streams", append([]string{id}, segs...)...), nil, nil
	}
}

var k2ListCmd = &cobra.Command{
	Use:   "list",
	Short: "List K2 streams",
	Args:  cobra.NoArgs,
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		q := url.Values{}
		if n, _ := cmd.Flags().GetString("name"); n != "" {
			q.Set("name", n)
		}
		raw, err := c.all(c.p("k2/streams"), q)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			stList("⛰️  %d K2 streams", "No K2 streams found", stItems(raw), []stCol{
				{Header: "NAME", Path: "name"},
				{Header: "ID", Path: "id"},
				{Header: "RETENTION (s)", Path: "retention_seconds"},
				{Header: "HTTP", Path: "http.enabled"},
				{Header: "WORKER BINDING", Path: "worker_binding.enabled"},
				{Header: "CREATED", Path: "", Fmt: sbFirst("created_at", "created_on")},
			})
			return nil
		})
	}),
}

var k2GetCmd = sbGetCmd("get <stream>", "Show a K2 stream", "", "⛰️  K2 stream", cobra.ExactArgs(1), k2Path())

var k2DeleteCmd = sbDeleteCmd("delete <stream>", "Delete a K2 stream", "", cobra.ExactArgs(1),
	func(a []string) string { return "K2 stream " + a[0] }, k2Path())

var k2SubscriptionsCmd = sbListCmd("subscriptions <stream>", "List a stream's subscriptions", "", cobra.ExactArgs(1), k2Path("subscriptions"),
	"🔔 %d subscriptions", "No subscriptions", []stCol{
		{Header: "NAME", Path: "name"},
		{Header: "ID", Path: "id"},
		{Header: "TYPE", Path: "type"},
	})

func k2Flags(cmd *cobra.Command) {
	cmd.Flags().Int("retention", 0, "Record retention in seconds (3600-2592000)")
	cmd.Flags().Bool("http", false, "Accept records over the HTTP endpoint (--http=false to disable)")
	cmd.Flags().Bool("http-auth", false, "Require an API token with K2 produce permission on the HTTP endpoint")
	cmd.Flags().StringSlice("cors-origins", nil, "Allowed CORS origins for the HTTP endpoint")
	cmd.Flags().Bool("worker-binding", false, "Allow Workers bindings to produce records (--worker-binding=false to disable)")
	stDataFlag(cmd)
}

func k2Body(cmd *cobra.Command) (map[string]any, error) {
	body, err := stBody(cmd)
	if err != nil {
		return nil, err
	}
	stFlagInt(cmd, body, "retention", "retention_seconds")
	stFlagBool(cmd, body, "http", "http.enabled")
	if cmd.Flags().Changed("http-auth") || cmd.Flags().Changed("cors-origins") {
		stSet(body, "http.enabled", true)
	}
	stFlagBool(cmd, body, "http-auth", "http.authentication")
	stFlagList(cmd, body, "cors-origins", "http.cors.origins")
	stFlagBool(cmd, body, "worker-binding", "worker_binding.enabled")
	return body, nil
}

var k2CreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a K2 stream",
	Long: `Create a K2 stream. The HTTP endpoint is off unless --http is set.

Examples:
  cfctl k2 streams create clicks
  cfctl k2 streams create clicks --retention 604800 --http --http-auth --worker-binding
  cfctl k2 streams create clicks --http --cors-origins https://example.com`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := k2Body(cmd)
		if err != nil {
			return err
		}
		body["name"] = args[0]
		if body["http"] == nil {
			body["http"] = map[string]any{"enabled": false}
		}
		return sbSendShow(c, "POST", c.p("k2/streams"), nil, body, "Created K2 stream "+args[0])
	}),
}

var k2UpdateCmd = &cobra.Command{
	Use:   "update <stream>",
	Short: "Update a K2 stream",
	Long: `Update a stream's retention, HTTP endpoint, or Worker binding.

Examples:
  cfctl k2 streams update clicks --retention 3600
  cfctl k2 streams update clicks --http=false`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := k2Body(cmd)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to update: pass flags or --data")
		}
		id, err := k2StreamID(c, args[0])
		if err != nil {
			return err
		}
		return sbSendShow(c, "PATCH", c.p("k2/streams", id), nil, body, "Updated K2 stream "+args[0])
	}),
}

func init() {
	k2ListCmd.Flags().String("name", "", "Filter by name (substring)")
	k2Flags(k2CreateCmd)
	k2Flags(k2UpdateCmd)
	k2StreamsCmd.AddCommand(k2ListCmd, k2GetCmd, k2CreateCmd, k2UpdateCmd, k2DeleteCmd, k2SubscriptionsCmd)
	k2Cmd.AddCommand(k2StreamsCmd)
	rootCmd.AddCommand(k2Cmd)
}
