package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const wfBase = "/accounts/{account_id}/workflows"

var wfInstanceCols = []platCol{
	{H: "id", Path: "id"}, {H: "status", Path: "status"}, {H: "version", Path: "version_id", W: 12},
	{H: "created", Path: "created_on"}, {H: "started", Path: "started_on"}, {H: "ended", Path: "ended_on"},
}

// wfLatest resolves an instance ID of "latest" (args[1]) to the newest instance.
func wfLatest(ctx context.Context, s *apiSession, _ *cobra.Command, args []string) ([]string, error) {
	if len(args) < 2 || args[1] != "latest" {
		return args, nil
	}
	path, _, err := platFill(ctx, s, wfBase+"/{workflow_name}/instances", args[:1])
	if err != nil {
		return nil, err
	}
	raw, err := platDo(ctx, s, "GET", path, url.Values{"per_page": {"100"}}, nil)
	if err != nil {
		return nil, platErr("Workflows", err)
	}
	var ins []struct {
		ID        string `json:"id"`
		CreatedOn string `json:"created_on"`
	}
	if err := platDecode(raw, &ins); err != nil {
		return nil, err
	}
	if len(ins) == 0 {
		return nil, fmt.Errorf("workflow %q has no instances", args[0])
	}
	sort.Slice(ins, func(i, j int) bool { return ins[i].CreatedOn > ins[j].CreatedOn })
	out := append([]string(nil), args...)
	out[1] = ins[0].ID
	return out, nil
}

func wfStatusSpec(verb, status, short string, confirmIt bool) platSpec {
	sp := platSpec{
		Use: verb + " <workflow> <instance-id|latest>", Short: short, Method: "PATCH",
		Path: wfBase + "/{workflow_name}/instances/{instance_id}/status", ArgsHook: wfLatest,
		Body: func(c *cobra.Command, _ []string) (any, error) {
			b := map[string]any{"status": status}
			if c.Flags().Lookup("rollback") != nil {
				if rb, _ := c.Flags().GetBool("rollback"); rb {
					b["rollback"] = true
				}
			}
			if c.Flags().Lookup("from") != nil {
				platSetStr(c, b, "from", "from")
			}
			return b, nil
		},
		Done: fmt.Sprintf("%s requested for instance %%s", status), Product: "Workflows",
	}
	if confirmIt {
		sp.Confirm = verb + " workflow instance %s"
	}
	switch status {
	case "terminate":
		sp.Flags = func(c *cobra.Command) { c.Flags().Bool("rollback", false, "Run the workflow's rollback steps") }
	case "restart":
		sp.Flags = func(c *cobra.Command) { c.Flags().String("from", "", "Restart from this step name") }
	}
	return sp
}

func init() {
	instances := platGroup("instances", "Manage workflow instances", "", []string{"instance"},
		platSpecs(
			platSpec{Use: "list <workflow>", Short: "List a workflow's instances", Aliases: []string{"ls"}, Path: wfBase + "/{workflow_name}/instances", List: true,
				Flags: func(c *cobra.Command) {
					c.Flags().String("status", "", "Only instances with this status (queued, running, paused, errored, terminated, complete, waiting)")
					c.Flags().Int("per-page", 50, "Page size")
				},
				Query: func(c *cobra.Command, _ []string, q url.Values) error {
					platQueryStr(c, q, "status", "status")
					platQueryInt(c, q, "per-page", "per_page")
					return nil
				},
				Cols: wfInstanceCols, Title: "🔁 %d instances", Product: "Workflows"},
			platSpec{Use: "describe <workflow> <instance-id|latest>", Short: "Show an instance: status, steps, output, errors", Aliases: []string{"get", "info"},
				Path: wfBase + "/{workflow_name}/instances/{instance_id}", ArgsHook: wfLatest, Print: wfPrintInstance, Product: "Workflows"},
			platSpec{Use: "send-event <workflow> <instance-id|latest> <event-type>", Short: "Send an event to a waiting instance", Method: "POST",
				Path: wfBase + "/{workflow_name}/instances/{instance_id}/events/{event_type}", ArgsHook: wfLatest,
				Flags: func(c *cobra.Command) { c.Flags().String("payload", "", "Event payload: JSON string, @file, or -") },
				Body: func(c *cobra.Command, _ []string) (any, error) {
					p, _ := c.Flags().GetString("payload")
					if p == "" {
						return map[string]any{}, nil
					}
					b, err := api.ReadData(p, os.Stdin)
					if err != nil {
						return nil, err
					}
					var v any
					if err := json.Unmarshal(b, &v); err != nil {
						return nil, fmt.Errorf("--payload is not valid JSON: %w", err)
					}
					return v, nil
				},
				Done: "Sent event", Product: "Workflows"},
			wfStatusSpec("pause", "pause", "Pause a running instance", false),
			wfStatusSpec("resume", "resume", "Resume a paused instance", false),
			wfStatusSpec("terminate", "terminate", "Terminate an instance", true),
			wfStatusSpec("restart", "restart", "Restart an instance", true),
			platSpec{Use: "delete <workflow> <instance-id>...", Short: "Delete instances (up to 100)", Aliases: []string{"rm"}, Method: "POST",
				Path: wfBase + "/{workflow_name}/instances/batch/delete", ExtraArgs: 1, OptionalArgs: 99,
				ArgsHook: wfLatest,
				Body: func(_ *cobra.Command, args []string) (any, error) {
					return map[string]any{"instances": args[1:]}, nil
				},
				Confirm: "delete workflow instances %s", Done: "Deleted instances", Product: "Workflows"},
		)...)

	versions := platGroup("versions", "Workflow versions", "", []string{"version"},
		platSpecs(
			platSpec{Use: "list <workflow>", Short: "List a workflow's versions", Aliases: []string{"ls"}, Path: wfBase + "/{workflow_name}/versions", List: true,
				Cols: []platCol{{H: "id", Path: "id"}, {H: "class", Path: "class_name"}, {H: "created", Path: "created_on"}}, Title: "🔁 %d versions", Product: "Workflows"},
			platSpec{Use: "get <workflow> <version-id>", Short: "Show a version", Path: wfBase + "/{workflow_name}/versions/{version_id}", Product: "Workflows"},
			platSpec{Use: "graph <workflow> <version-id>", Short: "Show a version's step graph (JSON)", Path: wfBase + "/{workflow_name}/versions/{version_id}/graph",
				Print: func(_ *cobra.Command, _ []string, raw json.RawMessage) error { return printBody(raw, nil) }, Product: "Workflows"},
		)...)

	wfCmd := platGroup("workflows", "Manage Workflows and their instances", `Manage Workflows (durable, multi-step Workers).

  cfctl workflows list | describe <name> | delete <name>
  cfctl workflows trigger <name> [params-json]
  cfctl workflows instances list|describe|pause|resume|terminate|restart|send-event|delete
  cfctl workflows versions list|get|graph

Instance IDs accept "latest" (the newest instance). Generated: 'cfctl api workflows ...'.`,
		[]string{"workflow", "wf"},
		append(platSpecs(
			platSpec{Use: "list", Short: "List workflows", Aliases: []string{"ls"}, Path: wfBase, List: true,
				Cols: []platCol{{H: "name", Path: "name"}, {H: "script", Path: "script_name"}, {H: "class", Path: "class_name"},
					{H: "running", Path: "instances.running"}, {H: "complete", Path: "instances.complete"}, {H: "errored", Path: "instances.errored"},
					{H: "modified", Path: "modified_on"}},
				Title: "🔁 %d workflows", Product: "Workflows"},
			platSpec{Use: "describe <workflow>", Short: "Show a workflow", Aliases: []string{"get", "info"}, Path: wfBase + "/{workflow_name}", Title: "🔁 Workflow %s", Product: "Workflows"},
			platSpec{Use: "delete <workflow>", Short: "Delete a workflow and all its instances", Aliases: []string{"rm"}, Method: "DELETE", Path: wfBase + "/{workflow_name}",
				Confirm: "delete workflow %s and all its instances", Done: "Deleted workflow %s", Product: "Workflows"},
			platSpec{Use: "trigger <workflow> [params-json]", Short: "Start a new instance", Method: "POST", Path: wfBase + "/{workflow_name}/instances", OptionalArgs: 1,
				Flags: func(c *cobra.Command) { c.Flags().String("id", "", "Custom instance ID (default: generated)") },
				Body: func(c *cobra.Command, args []string) (any, error) {
					b := map[string]any{}
					platSetStr(c, b, "id", "instance_id")
					if len(args) > 1 {
						data, err := api.ReadData(args[1], os.Stdin)
						if err != nil {
							return nil, err
						}
						var params any
						if err := json.Unmarshal(data, &params); err != nil {
							return nil, fmt.Errorf("params must be JSON: %w", err)
						}
						b["params"] = params
					}
					return b, nil
				},
				Print: func(_ *cobra.Command, args []string, raw json.RawMessage) error {
					var r map[string]any
					_ = json.Unmarshal(raw, &r)
					fmt.Println(ui.Success(fmt.Sprintf("Started instance %s of %s (%s)", platStr(r["id"]), args[0], platStr(r["status"]))))
					return nil
				},
				Product: "Workflows"},
		), instances, versions)...)
	rootCmd.AddCommand(wfCmd)
}

func wfPrintInstance(_ *cobra.Command, args []string, raw json.RawMessage) error {
	var m map[string]any
	if err := platDecode(raw, &m); err != nil {
		return err
	}
	platDetail("🔁 Instance "+args[1], m, []platCol{
		{H: "Status", Path: "status"}, {H: "Queued", Path: "queued"}, {H: "Start", Path: "start"}, {H: "End", Path: "end"},
		{H: "Success", Path: "success"}, {H: "Trigger", Path: "trigger.source"}, {H: "Version", Path: "versionId"},
		{H: "Params", Path: "params", W: 200}, {H: "Output", Path: "output", W: 200}, {H: "Error", Path: "error.message", W: 200},
	})
	steps, _ := m["steps"].([]any)
	if len(steps) > 0 {
		fmt.Println()
		fmt.Println("  " + ui.AccentStyle.Render(fmt.Sprintf("%d steps", len(steps))))
		for _, st := range steps {
			name := platStr(platGet(st, "name"))
			typ := platStr(platGet(st, "type"))
			ok := platGet(st, "success")
			mark := ui.SubtleStyle.Render("…")
			if ok == true {
				mark = ui.SuccessStyle.Render("✓")
			} else if ok == false {
				mark = ui.ErrorStyle.Render("✗")
			}
			attempts := 0
			if a, ok := platGet(st, "attempts").([]any); ok {
				attempts = len(a)
			}
			line := fmt.Sprintf("    %s %-10s %s", mark, typ, name)
			if attempts > 1 {
				line += ui.SubtleStyle.Render(fmt.Sprintf("  (%d attempts)", attempts))
			}
			fmt.Println(line)
			if e := platStr(platGet(st, "error.message")); e != "" {
				fmt.Println("      " + ui.ErrorStyle.Render(platTrunc(e, 200)))
			}
		}
	}
	return nil
}
