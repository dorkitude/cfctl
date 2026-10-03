package cmd

var spectrumCols = []col{{"ID", "id"}, {"Protocol", "protocol"}, {"DNS name", "dns.name"}, {"Origin", "origin_direct"}, {"Proxy protocol", "proxy_protocol"}, {"TLS", "tls"}, {"Created", "created_on"}}

var spectrumCmd = group("spectrum", "Spectrum applications (TCP/UDP proxying)", `Spectrum proxies arbitrary TCP/UDP through Cloudflare (paid plans).

Examples:
  cfctl spectrum apps list example.com
  cfctl spectrum apps get example.com <app-id>
  cfctl spectrum apps create example.com --data @app.json
  cfctl spectrum apps delete example.com <app-id>`, nil,
	group("apps", "Spectrum applications", "", []string{"app"},
		readSpec{Use: "list <zone>", Short: "List Spectrum apps", Scope: scopeZone, Path: "/zones/{zone_id}/spectrum/apps", Title: "Spectrum apps", Cols: spectrumCols, Paginate: true, Feature: "Spectrum"}.build(),
		readSpec{Use: "get <zone> <app-id>", Short: "Show a Spectrum app", Scope: scopeZone, Path: "/zones/{zone_id}/spectrum/apps/{app_id}", Args: []string{"app_id"}, Title: "Spectrum app", Feature: "Spectrum"}.build(),
		writeSpec{Use: "create <zone>", Short: "Create a Spectrum app (--data JSON)", Method: "POST", Scope: scopeZone, Path: "/zones/{zone_id}/spectrum/apps", DataFlag: true, Done: "Spectrum app created", Feature: "Spectrum"}.build(),
		writeSpec{Use: "delete <zone> <app-id>", Short: "Delete a Spectrum app", Method: "DELETE", Scope: scopeZone, Path: "/zones/{zone_id}/spectrum/apps/{app_id}", Args: []string{"app_id"}, Confirm: "delete Spectrum app %s", Done: "Spectrum app %s deleted", Feature: "Spectrum"}.build(),
	),
)

func init() { rootCmd.AddCommand(spectrumCmd) }
