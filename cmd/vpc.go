package cmd

import (
	"fmt"
	"net"
	"strings"

	"github.com/spf13/cobra"
)

const vpcBase = "/accounts/{account_id}/connectivity/directory/services"

func vpcFlags(c *cobra.Command) {
	c.Flags().String("name", "", "Service name")
	c.Flags().String("type", "", "Service type: tcp or http")
	c.Flags().String("tunnel-id", "", "UUID of the Cloudflare Tunnel that reaches the service")
	c.Flags().Int("tcp-port", 0, "TCP port (type tcp)")
	c.Flags().String("app-protocol", "", "TCP application protocol: postgresql or mysql")
	c.Flags().Int("http-port", 0, "HTTP port (type http; default 80)")
	c.Flags().Int("https-port", 0, "HTTPS port (type http; default 443)")
	c.Flags().String("ipv4", "", "IPv4 address of the host")
	c.Flags().String("ipv6", "", "IPv6 address of the host")
	c.Flags().String("hostname", "", "Hostname of the host (resolved inside the network)")
	c.Flags().StringSlice("resolver-ips", nil, "Resolver IPs for --hostname")
	c.Flags().String("cert-verification-mode", "", "TLS verification to the origin: verify_full, verify_ca, disabled")
}

// vpcBody builds a service like wrangler's buildRequest.
func vpcBody(c *cobra.Command, _ []string) (any, error) {
	get := func(f string) string { v, _ := c.Flags().GetString(f); return v }
	geti := func(f string) int { v, _ := c.Flags().GetInt(f); return v }
	name, typ, tunnel := get("name"), get("type"), get("tunnel-id")
	if c.Flags().Changed("data") && name == "" && typ == "" && tunnel == "" {
		return nil, nil // the whole service comes from --data
	}
	if name == "" || tunnel == "" || (typ != "tcp" && typ != "http") {
		return nil, fmt.Errorf("--name, --type (tcp|http), and --tunnel-id are required")
	}
	ipv4, ipv6, host := get("ipv4"), get("ipv6"), get("hostname")
	if ipv4 == "" && ipv6 == "" && host == "" {
		return nil, fmt.Errorf("give --ipv4/--ipv6 or --hostname")
	}
	if host != "" && (ipv4 != "" || ipv6 != "") {
		return nil, fmt.Errorf("--hostname conflicts with --ipv4/--ipv6")
	}
	if ipv4 != "" && (net.ParseIP(ipv4) == nil || !strings.Contains(ipv4, ".")) {
		return nil, fmt.Errorf("invalid IPv4 address %q", ipv4)
	}
	if ipv6 != "" && (net.ParseIP(ipv6) == nil || !strings.Contains(ipv6, ":")) {
		return nil, fmt.Errorf("invalid IPv6 address %q", ipv6)
	}
	h := map[string]any{}
	if ipv4 != "" {
		h["ipv4"] = ipv4
	}
	if ipv6 != "" {
		h["ipv6"] = ipv6
	}
	if host != "" {
		h["hostname"] = host
		rn := map[string]any{"tunnel_id": tunnel}
		if ips, _ := c.Flags().GetStringSlice("resolver-ips"); len(ips) > 0 {
			for _, ip := range ips {
				if net.ParseIP(ip) == nil {
					return nil, fmt.Errorf("invalid resolver IP %q", ip)
				}
			}
			rn["resolver_ips"] = ips
		}
		h["resolver_network"] = rn
	} else {
		h["network"] = map[string]any{"tunnel_id": tunnel}
	}
	b := map[string]any{"name": name, "type": typ, "host": h}
	if typ == "tcp" {
		if geti("tcp-port") == 0 {
			return nil, fmt.Errorf("tcp services need --tcp-port")
		}
		b["tcp_port"] = geti("tcp-port")
		if p := get("app-protocol"); p != "" {
			b["app_protocol"] = p
		}
	} else {
		if p := geti("http-port"); p > 0 {
			b["http_port"] = p
		}
		if p := geti("https-port"); p > 0 {
			b["https_port"] = p
		}
	}
	if m := get("cert-verification-mode"); m != "" {
		b["tls_settings"] = map[string]any{"cert_verification_mode": m}
	}
	return b, nil
}

func init() {
	cols := []platCol{
		{H: "id", Path: "service_id|id"}, {H: "name", Path: "name"}, {H: "type", Path: "type"},
		{H: "host", Path: "host.hostname|host.ipv4|host.ipv6"}, {H: "tcp", Path: "tcp_port"}, {H: "http", Path: "http_port"}, {H: "https", Path: "https_port"},
		{H: "tunnel", Path: "host.network.tunnel_id|host.resolver_network.tunnel_id"},
	}
	service := platGroup("service", "Manage VPC services (private origins reached through a tunnel)", "", []string{"services"},
		platSpecs(
			platSpec{Use: "list", Short: "List VPC services", Aliases: []string{"ls"}, Path: vpcBase, List: true, Cols: cols, Title: "🔗 %d VPC services", Product: "Workers VPC"},
			platSpec{Use: "get <service-id>", Short: "Show a VPC service", Path: vpcBase + "/{service_id}", Title: "🔗 VPC service", Product: "Workers VPC"},
			platSpec{Use: "create", Short: "Create a VPC service", Method: "POST", Path: vpcBase, Flags: vpcFlags, Body: vpcBody, Data: true,
				Example: `  cfctl vpc service create --name db --type tcp --tcp-port 5432 --app-protocol postgresql --ipv4 10.0.0.5 --tunnel-id <uuid>
  cfctl vpc service create --name api --type http --hostname api.internal --tunnel-id <uuid>`,
				Title: "🔗 Created VPC service", Product: "Workers VPC"},
			platSpec{Use: "update <service-id>", Short: "Replace a VPC service", Method: "PUT", Path: vpcBase + "/{service_id}", Flags: vpcFlags, Body: vpcBody, Data: true,
				Title: "🔗 Updated VPC service", Product: "Workers VPC"},
			platSpec{Use: "delete <service-id>", Short: "Delete a VPC service", Aliases: []string{"rm"}, Method: "DELETE", Path: vpcBase + "/{service_id}",
				Confirm: "delete VPC service %s", Done: "Deleted VPC service %s", Product: "Workers VPC"},
		)...)
	rootCmd.AddCommand(platGroup("vpc", "Workers VPC services", `Manage Workers VPC services: private origins (an IP or hostname behind a
Cloudflare Tunnel) that Workers can reach through a binding.

  cfctl vpc service list|get|create|update|delete

Generated: 'cfctl api connectivity-services ...'.`, nil, service))
}
