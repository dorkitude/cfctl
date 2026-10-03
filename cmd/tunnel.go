package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

const tnBase = "/accounts/{account_id}/cfd_tunnel"

var tnCols = []platCol{
	{H: "id", Path: "id"}, {H: "name", Path: "name"}, {H: "status", Path: "status"}, {H: "config", Path: "remote_config"},
	{H: "connections", Path: "connections", W: 1}, {H: "created", Path: "created_at"},
}

// tnResolve turns a tunnel name in args[0] into its ID.
func tnResolve(ctx context.Context, s *apiSession, _ *cobra.Command, args []string) ([]string, error) {
	if len(args) == 0 || ctUUID.MatchString(args[0]) {
		return args, nil
	}
	path, _, err := platFill(ctx, s, tnBase, nil)
	if err != nil {
		return nil, err
	}
	raw, err := platDo(ctx, s, "GET", path, url.Values{"name": {args[0]}, "is_deleted": {"false"}}, nil)
	if err != nil {
		return nil, platErr("Cloudflare Tunnel", err)
	}
	var ts []struct{ ID, Name string }
	if err := platDecode(raw, &ts); err != nil {
		return nil, err
	}
	for _, t := range ts {
		if t.Name == args[0] {
			out := append([]string(nil), args...)
			out[0] = t.ID
			return out, nil
		}
	}
	return nil, fmt.Errorf("no tunnel named %q (see 'cfctl tunnel list')", args[0])
}

func tnPrintList(_ *cobra.Command, _ []string, raw json.RawMessage) error {
	var items []any
	if err := platDecode(raw, &items); err != nil {
		return err
	}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			conns, _ := m["connections"].([]any)
			m["connections"] = float64(len(conns))
		}
	}
	platTable("🚇 %d tunnels", items, tnCols)
	return nil
}

func init() {
	config := platGroup("config", "A remotely-managed tunnel's ingress configuration", "", nil,
		platSpecs(
			platSpec{Use: "get <tunnel>", Short: "Show the tunnel's configuration (ingress rules)", Path: tnBase + "/{tunnel_id}/configurations", ArgsHook: tnResolve,
				Print: tnPrintConfig, Product: "Cloudflare Tunnel"},
			platSpec{Use: "set <tunnel>", Short: "Replace the tunnel's configuration (--data or --file with {\"config\":{\"ingress\":[...]}})", Method: "PUT", Path: tnBase + "/{tunnel_id}/configurations", ArgsHook: tnResolve,
				Example: `  cfctl tunnel config set my-tunnel --data '{"config":{"ingress":[{"hostname":"app.example.com","service":"http://localhost:8080"},{"service":"http_status:404"}]}}'`,
				Data:    true, Done: "Updated tunnel configuration", Product: "Cloudflare Tunnel"},
		)...)

	routes := platGroup("route", "Private network (IP) and DNS routes", "", []string{"routes"},
		append(platSpecs(
			platSpec{Use: "list", Short: "List private network routes", Aliases: []string{"ls", "ip"}, Path: "/accounts/{account_id}/teamnet/routes", List: true,
				Flags: func(c *cobra.Command) { c.Flags().String("tunnel", "", "Only routes for this tunnel ID") },
				Query: func(c *cobra.Command, _ []string, q url.Values) error {
					platQueryStr(c, q, "tunnel", "tunnel_id")
					q.Set("is_deleted", "false")
					return nil
				},
				Cols:  []platCol{{H: "network", Path: "network"}, {H: "tunnel", Path: "tunnel_name|tunnel_id"}, {H: "vnet", Path: "virtual_network_name|virtual_network_id"}, {H: "comment", Path: "comment", W: 40}, {H: "id", Path: "id"}},
				Title: "🚇 %d routes", Product: "Cloudflare Tunnel"},
			platSpec{Use: "add <tunnel> <cidr>", Short: "Route a private network (CIDR) through a tunnel", Method: "POST", Path: "/accounts/{account_id}/teamnet/routes", ExtraArgs: 2,
				Flags: func(c *cobra.Command) {
					c.Flags().String("comment", "", "Comment")
					c.Flags().String("vnet", "", "Virtual network ID")
				},
				ArgsHook: tnResolve,
				Body: func(c *cobra.Command, args []string) (any, error) {
					b := map[string]any{"tunnel_id": args[0], "network": args[1]}
					platSetStr(c, b, "comment", "comment")
					platSetStr(c, b, "vnet", "virtual_network_id")
					return b, nil
				}, Done: "Added route", Product: "Cloudflare Tunnel"},
			platSpec{Use: "delete <route-id>", Short: "Delete a private network route", Aliases: []string{"rm"}, Method: "DELETE", Path: "/accounts/{account_id}/teamnet/routes/{route_id}",
				Confirm: "delete route %s", Done: "Deleted route %s", Product: "Cloudflare Tunnel"},
		), tnRouteDNSCmd())...)

	vnets := platGroup("vnet", "Virtual networks", "", []string{"vnets", "virtual-network"},
		platSpecs(
			platSpec{Use: "list", Short: "List virtual networks", Aliases: []string{"ls"}, Path: "/accounts/{account_id}/teamnet/virtual_networks",
				Query: func(_ *cobra.Command, _ []string, q url.Values) error { q.Set("is_deleted", "false"); return nil },
				Cols:  []platCol{{H: "id", Path: "id"}, {H: "name", Path: "name"}, {H: "default", Path: "is_default_network"}, {H: "comment", Path: "comment", W: 40}}, Title: "🚇 %d virtual networks", Product: "Cloudflare Tunnel"},
		)...)

	tnCmd := platGroup("tunnel", "Manage Cloudflare Tunnels (cloudflared)", `Manage Cloudflare Tunnels.

  cfctl tunnel list | info <tunnel> | create <name> | delete <tunnel>
  cfctl tunnel token <tunnel> --reveal          the connector token (a secret)
  cfctl tunnel config get|set <tunnel>          remotely-managed ingress rules
  cfctl tunnel connections <tunnel>             active connectors
  cfctl tunnel route list|add|delete|dns        private network and DNS routes
  cfctl tunnel vnet list                        virtual networks
  cfctl tunnel run <tunnel>                     run cloudflared with the tunnel's token
  cfctl tunnel quick-start <url>                a free trycloudflare.com tunnel (cloudflared)

Tunnels accept a name or an ID. Generated: 'cfctl api cloudflare-tunnel ...'.`, []string{"tunnels"},
		append(platSpecs(
			platSpec{Use: "list", Short: "List tunnels", Aliases: []string{"ls"}, Path: tnBase, List: true,
				Flags: func(c *cobra.Command) {
					c.Flags().Bool("include-deleted", false, "Include deleted tunnels")
					c.Flags().String("name", "", "Only tunnels with this name")
				},
				Query: func(c *cobra.Command, _ []string, q url.Values) error {
					if d, _ := c.Flags().GetBool("include-deleted"); !d {
						q.Set("is_deleted", "false")
					}
					platQueryStr(c, q, "name", "name")
					return nil
				},
				Print: tnPrintList, Product: "Cloudflare Tunnel"},
			platSpec{Use: "info <tunnel>", Short: "Show a tunnel", Aliases: []string{"get"}, Path: tnBase + "/{tunnel_id}", ArgsHook: tnResolve, Product: "Cloudflare Tunnel",
				Fields: []platCol{{H: "ID", Path: "id"}, {H: "Name", Path: "name"}, {H: "Status", Path: "status"}, {H: "Remote config", Path: "remote_config"},
					{H: "Type", Path: "tun_type"}, {H: "Created", Path: "created_at"}, {H: "Active since", Path: "conns_active_at"}, {H: "Inactive", Path: "conns_inactive_at"},
					{H: "Connections", Path: "connections", W: 300}},
				Title: "🚇 Tunnel"},
			platSpec{Use: "delete <tunnel>", Short: "Delete a tunnel", Aliases: []string{"rm"}, Method: "DELETE", Path: tnBase + "/{tunnel_id}", ArgsHook: tnResolve,
				Confirm: "delete tunnel %s", Done: "Deleted tunnel %s", Product: "Cloudflare Tunnel"},
			platSpec{Use: "token <tunnel>", Short: "Print the tunnel's connector token (secret; needs --reveal)", Path: tnBase + "/{tunnel_id}/token", ArgsHook: tnResolve,
				Secrets: []string{""}, Product: "Cloudflare Tunnel",
				Print: func(c *cobra.Command, _ []string, raw json.RawMessage) error {
					var t string
					if err := json.Unmarshal(raw, &t); err != nil {
						return fmt.Errorf("unexpected token response")
					}
					if reveal, _ := c.Flags().GetBool("reveal"); !reveal {
						fmt.Fprintln(os.Stderr, ui.Warn("The tunnel token is a secret; pass --reveal to print it"))
						return nil
					}
					fmt.Println(t)
					return nil
				}},
			platSpec{Use: "connections <tunnel>", Short: "List the tunnel's active connectors", Path: tnBase + "/{tunnel_id}/connections", ArgsHook: tnResolve,
				Cols:  []platCol{{H: "connector", Path: "id"}, {H: "version", Path: "client_version"}, {H: "arch", Path: "arch"}, {H: "origin ip", Path: "conns.0.origin_ip"}, {H: "colo", Path: "conns.0.colo_name"}, {H: "run at", Path: "run_at"}},
				Title: "🚇 %d connectors", Product: "Cloudflare Tunnel"},
			platSpec{Use: "cleanup <tunnel>", Short: "Remove stale connections", Method: "DELETE", Path: tnBase + "/{tunnel_id}/connections", ArgsHook: tnResolve,
				Confirm: "clean up connections of tunnel %s", Done: "Cleaned up connections", Product: "Cloudflare Tunnel"},
		), tnCreateCmd(), config, routes, vnets, tnRunCmd(), tnQuickStartCmd())...)
	rootCmd.AddCommand(tnCmd)
}

func tnPrintConfig(_ *cobra.Command, args []string, raw json.RawMessage) error {
	var r map[string]any
	if err := platDecode(raw, &r); err != nil {
		return err
	}
	fmt.Println(ui.TitleStyle.Render("🚇 Configuration of " + args[0]))
	fmt.Printf("  %s %s  %s %s\n", ui.SubtleStyle.Render("version:"), platStr(r["version"]), ui.SubtleStyle.Render("source:"), platStr(r["source"]))
	ingress, _ := platGet(r, "config.ingress").([]any)
	if len(ingress) == 0 {
		fmt.Println(ui.Warn("No ingress rules (locally-managed tunnels keep them in cloudflared's config.yml)"))
		return nil
	}
	platTable("", ingress, []platCol{{H: "hostname", Path: "hostname"}, {H: "path", Path: "path"}, {H: "service", Path: "service"}})
	return nil
}

func tnCreateCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "create <name>", Short: "Create a tunnel", Path: tnBase, ExtraArgs: 1,
		Long: `Create a Cloudflare Tunnel. By default it's remotely managed (config_src
"cloudflare"): configure ingress with 'cfctl tunnel config set' and run it with
'cfctl tunnel run <name>' or 'cloudflared tunnel run --token ...'.

With --local, it's locally managed and the credentials file cloudflared needs
is written to --credentials-file (default ~/.cloudflared/<id>.json, mode 0600).
The tunnel secret is generated locally and never printed.`,
		Flags: func(c *cobra.Command) {
			c.Flags().Bool("local", false, "Locally-managed tunnel (config in cloudflared's config.yml)")
			c.Flags().String("credentials-file", "", "Where to write the credentials JSON (--local)")
		},
		Run: func(c *cobra.Command, args []string) error {
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			local, _ := c.Flags().GetBool("local")
			src := "cloudflare"
			if local {
				src = "local"
			}
			body := map[string]any{"name": args[0], "config_src": src, "tunnel_secret": base64.StdEncoding.EncodeToString(secret)}
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			path, _, err := platFill(ctx, s, tnBase, nil)
			if err != nil {
				return err
			}
			raw, err := platDo(ctx, s, "POST", path, nil, body)
			if err != nil {
				return platErr("Cloudflare Tunnel", err)
			}
			var t map[string]any
			if err := platDecode(raw, &t); err != nil {
				return err
			}
			id := platStr(t["id"])
			credPath := ""
			if local {
				credPath, _ = c.Flags().GetString("credentials-file")
				if credPath == "" {
					home, _ := os.UserHomeDir()
					credPath = filepath.Join(home, ".cloudflared", id+".json")
				}
				acct, _ := s.account(ctx)
				cred, _ := json.Marshal(map[string]any{"AccountTag": acct, "TunnelSecret": body["tunnel_secret"], "TunnelID": id})
				if err := os.MkdirAll(filepath.Dir(credPath), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(credPath, cred, 0o600); err != nil {
					return err
				}
			}
			delete(t, "credentials_file")
			delete(t, "token")
			if jsonOutput {
				if credPath != "" {
					t["credentials_path"] = credPath
				}
				return printJSONValue(t)
			}
			fmt.Println(ui.Success(fmt.Sprintf("Created tunnel %s (%s)", args[0], id)))
			if credPath != "" {
				fmt.Println("  Credentials written to " + credPath)
			} else {
				fmt.Println("  Next: cfctl tunnel config set " + args[0] + " --data '{\"config\":{\"ingress\":[...]}}'")
				fmt.Println("        cfctl tunnel run " + args[0])
			}
			return nil
		},
	})
}

// tnRouteDNSCmd creates a proxied CNAME <hostname> → <id>.cfargotunnel.com,
// like 'cloudflared tunnel route dns'.
func tnRouteDNSCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "dns <tunnel> <hostname>", Short: "Point a hostname at a tunnel (proxied CNAME)", Path: tnBase + "/{tunnel_id}", ExtraArgs: 1,
		Flags: func(c *cobra.Command) {
			c.Flags().Bool("overwrite", false, "Replace an existing record for the hostname")
		},
		Run: func(c *cobra.Command, args []string) error {
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			args, err = tnResolve(ctx, s, c, args)
			if err != nil {
				return err
			}
			zid, _, err := emailResolveDomain(ctx, s, args[1])
			if err != nil {
				return err
			}
			target := args[0] + ".cfargotunnel.com"
			recPath := "/zones/" + zid + "/dns_records"
			raw, err := platDo(ctx, s, "GET", recPath, url.Values{"name": {args[1]}}, nil)
			if err != nil {
				return platErr("DNS", err)
			}
			var all []struct{ ID, Type, Name, Content string }
			_ = json.Unmarshal(raw, &all)
			var recs []struct{ ID, Type, Name, Content string }
			for _, r := range all {
				if strings.EqualFold(strings.TrimSuffix(r.Name, "."), args[1]) {
					recs = append(recs, r)
				}
			}
			body := map[string]any{"type": "CNAME", "name": args[1], "content": target, "proxied": true, "ttl": 1, "comment": "cfctl tunnel route dns"}
			if len(recs) > 0 {
				if recs[0].Type == "CNAME" && recs[0].Content == target {
					fmt.Println(ui.Info(args[1] + " already routes to the tunnel"))
					return nil
				}
				if ow, _ := c.Flags().GetBool("overwrite"); !ow {
					return fmt.Errorf("%s already has a %s record (%s); pass --overwrite to replace it", args[1], recs[0].Type, recs[0].Content)
				}
				raw, err = platDo(ctx, s, "PUT", recPath+"/"+recs[0].ID, nil, body)
			} else {
				raw, err = platDo(ctx, s, "POST", recPath, nil, body)
			}
			if err != nil {
				return platErr("DNS", err)
			}
			if jsonOutput {
				return printBody(raw, nil)
			}
			fmt.Println(ui.Success(fmt.Sprintf("%s → %s (proxied CNAME)", args[1], target)))
			return nil
		},
	})
}

func tnCloudflared() string {
	if p := os.Getenv("CFCTL_CLOUDFLARED"); p != "" {
		return p
	}
	return "cloudflared"
}

func tnExec(env []string, args ...string) error {
	cmd := exec.Command(tnCloudflared(), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cloudflared not found; install it (https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/) or set CFCTL_CLOUDFLARED")
		}
		return fmt.Errorf("cloudflared exited: %w", err)
	}
	return nil
}

func tnRunCmd() *cobra.Command {
	return platCommand(platSpec{
		Use: "run <tunnel>", Short: "Run a tunnel with cloudflared (token passed via the environment)", Path: tnBase + "/{tunnel_id}",
		Long: `Fetch the tunnel's connector token and exec 'cloudflared tunnel run'. The token is
passed in the TUNNEL_TOKEN environment variable, never on the command line.
Extra cloudflared flags go after --.`,
		OptionalArgs: 50,
		Run: func(c *cobra.Command, args []string) error {
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			args, err = tnResolve(ctx, s, c, args)
			if err != nil {
				return err
			}
			path, _, err := platFill(ctx, s, tnBase+"/{tunnel_id}/token", args[:1])
			if err != nil {
				return err
			}
			raw, err := platDo(ctx, s, "GET", path, nil, nil)
			if err != nil {
				return platErr("Cloudflare Tunnel", err)
			}
			var tok string
			if err := json.Unmarshal(raw, &tok); err != nil || tok == "" {
				return fmt.Errorf("unexpected token response")
			}
			return tnExec([]string{"TUNNEL_TOKEN=" + tok}, append([]string{"tunnel", "run"}, args[1:]...)...)
		},
	})
}

func tnQuickStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "quick-start <url>",
		Short: "Start a free, temporary tunnel to a local URL (trycloudflare.com, no account)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			u := args[0]
			if !strings.Contains(u, "://") {
				u = "http://" + u
			}
			return tnExec(nil, "tunnel", "--url", u)
		},
	}
}
