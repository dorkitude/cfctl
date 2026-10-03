package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Workers for Platforms dispatch namespaces.

func wkNSPath(name string) string {
	return "/accounts/{account_id}/workers/dispatch/namespaces/" + url.PathEscape(name)
}

func newDispatchNamespaceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "dispatch-namespace",
		Aliases: []string{"dispatch-namespaces", "dispatch"},
		Short:   "Manage Workers for Platforms dispatch namespaces",
		Long: `List, inspect, create, rename, and delete dispatch namespaces
(Workers for Platforms; the account needs the add-on).

Examples:
  cfctl dispatch-namespace list
  cfctl dispatch-namespace get customers
  cfctl dispatch-namespace create customers
  cfctl dispatch-namespace rename customers tenants
  cfctl dispatch-namespace scripts tenants
  cfctl dispatch-namespace delete tenants`,
	}
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List dispatch namespaces", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			p, err := s.fillPath(ctx, "/accounts/{account_id}/workers/dispatch/namespaces", nil)
			if err != nil {
				return err
			}
			res, err := s.c.All(ctx, api.Request{Method: "GET", Path: p}, 0)
			if err != nil {
				return fmt.Errorf("failed to list dispatch namespaces: %w", err)
			}
			var ns []map[string]any
			_ = json.Unmarshal(res.Result, &ns)
			if jsonOutput {
				if ns == nil {
					ns = []map[string]any{}
				}
				return printJSONValue(ns)
			}
			if len(ns) == 0 {
				fmt.Println(ui.Warn("No dispatch namespaces"))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🏗  %d dispatch namespaces", len(ns))))
			for _, n := range ns {
				fmt.Printf("  %-32s %-8v %s\n", ui.AccentStyle.Render(wkStr(n, "namespace_name")), wkFirst(n["script_count"], 0), ui.SubtleStyle.Render(wkStr(n, "namespace_id")))
			}
			return nil
		},
	}
	get := &cobra.Command{
		Use: "get <name>", Short: "Show a dispatch namespace", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var n map[string]any
			if _, err := wkCall(cmd.Context(), s, "GET", wkNSPath(args[0]), nil, nil, &n); err != nil {
				return fmt.Errorf("failed to get dispatch namespace %s: %w", args[0], err)
			}
			if jsonOutput {
				return printJSONValue(n)
			}
			fmt.Println(ui.TitleStyle.Render("🏗  " + wkStr(n, "namespace_name")))
			wkKV("ID", wkStr(n, "namespace_id"))
			wkKV("Scripts", wkFirst(n["script_count"], 0))
			wkKV("Trusted workers", wkFirst(n["trusted_workers"], false))
			wkKV("Created", wkTime(wkStr(n, "created_on")))
			wkKV("Modified", wkTime(wkStr(n, "modified_on")))
			return nil
		},
	}
	create := &cobra.Command{
		Use: "create <name>", Short: "Create a dispatch namespace", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var n map[string]any
			if _, err := wkCall(cmd.Context(), s, "POST", "/accounts/{account_id}/workers/dispatch/namespaces", nil, map[string]string{"name": args[0]}, &n); err != nil {
				return fmt.Errorf("failed to create dispatch namespace %s: %w", args[0], err)
			}
			if jsonOutput {
				return printJSONValue(n)
			}
			fmt.Println(ui.Success(fmt.Sprintf("Created dispatch namespace %s (%s)", args[0], wkStr(n, "namespace_id"))))
			return nil
		},
	}
	rename := &cobra.Command{
		Use: "rename <old-name> <new-name>", Short: "Rename a dispatch namespace", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			var n map[string]any
			if _, err := wkCall(cmd.Context(), s, "PATCH", wkNSPath(args[0]), nil, map[string]string{"name": args[1]}, &n); err != nil {
				return fmt.Errorf("failed to rename dispatch namespace %s: %w", args[0], err)
			}
			if jsonOutput {
				return printJSONValue(n)
			}
			fmt.Println(ui.Success(fmt.Sprintf("Renamed dispatch namespace %s to %s", args[0], args[1])))
			return nil
		},
	}
	del := &cobra.Command{
		Use: "delete <name>", Aliases: []string{"rm"}, Short: "Delete a dispatch namespace and its scripts", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirm(cmd, "delete dispatch namespace "+args[0]+" and every script in it"); err != nil {
				return err
			}
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			if _, err := wkCall(cmd.Context(), s, "DELETE", wkNSPath(args[0]), nil, nil, nil); err != nil {
				return fmt.Errorf("failed to delete dispatch namespace %s: %w", args[0], err)
			}
			if printJSON(map[string]any{"name": args[0], "deleted": true}) {
				return nil
			}
			fmt.Println(ui.Success("Deleted dispatch namespace " + args[0]))
			return nil
		},
	}
	wkAddYesFlag(del)
	scripts := &cobra.Command{
		Use: "scripts <name>", Short: "List the scripts in a dispatch namespace", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := newAPISession(cmd)
			if err != nil {
				return err
			}
			p, err := s.fillPath(ctx, wkNSPath(args[0])+"/scripts", nil)
			if err != nil {
				return err
			}
			res, err := s.c.All(ctx, api.Request{Method: "GET", Path: p}, 0)
			if err != nil {
				return fmt.Errorf("failed to list scripts in %s: %w", args[0], err)
			}
			var list []map[string]any
			_ = json.Unmarshal(res.Result, &list)
			if jsonOutput {
				if list == nil {
					list = []map[string]any{}
				}
				return printJSONValue(list)
			}
			if len(list) == 0 {
				fmt.Println(ui.Warn("No scripts in " + args[0]))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🏗  %d scripts in %s", len(list), args[0])))
			for _, sc := range list {
				id := wkStr(sc, "id")
				if id == "" {
					id = wkStr(sc, "script", "id")
				}
				mod := wkStr(sc, "modified_on")
				if mod == "" {
					mod = wkStr(sc, "script", "modified_on")
				}
				fmt.Printf("  %-40s %s\n", ui.AccentStyle.Render(id), wkTime(mod))
			}
			return nil
		},
	}
	c.AddCommand(list, get, create, rename, del, scripts)
	return c
}

func init() {
	workersCmd.AddCommand(newDispatchNamespaceCmd())
	rootCmd.AddCommand(newDispatchNamespaceCmd())
}
