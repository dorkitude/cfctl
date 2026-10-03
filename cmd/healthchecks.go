package cmd

var healthcheckCols = []col{{"ID", "id"}, {"Name", "name"}, {"Address", "address"}, {"Type", "type"}, {"Status", "status"}, {"Suspended", "suspended"}, {"Interval", "interval"}, {"Failure reason", "failure_reason"}}

var healthchecksCmd = group("healthchecks", "Standalone health checks for origins", `Standalone Health Checks monitor an origin and alert through Notifications.

Examples:
  cfctl healthchecks list example.com
  cfctl healthchecks get example.com <id>
  cfctl healthchecks create example.com --data '{"name":"origin","address":"origin.example.com","type":"HTTPS","http_config":{"path":"/health"}}'
  cfctl healthchecks delete example.com <id>`, []string{"healthcheck"},
	readSpec{Use: "list <zone>", Short: "List health checks", Scope: scopeZone, Path: "/zones/{zone_id}/healthchecks", Title: "Health checks", Cols: healthcheckCols, Paginate: true, Feature: "Health Checks"}.build(),
	readSpec{Use: "get <zone> <id>", Short: "Show a health check", Scope: scopeZone, Path: "/zones/{zone_id}/healthchecks/{healthcheck_id}", Args: []string{"healthcheck_id"}, Title: "Health check", Feature: "Health Checks"}.build(),
	writeSpec{Use: "create <zone>", Short: "Create a health check (--data JSON)", Method: "POST", Scope: scopeZone, Path: "/zones/{zone_id}/healthchecks", DataFlag: true, Done: "Health check created", Feature: "Health Checks"}.build(),
	writeSpec{Use: "delete <zone> <id>", Short: "Delete a health check", Method: "DELETE", Scope: scopeZone, Path: "/zones/{zone_id}/healthchecks/{healthcheck_id}", Args: []string{"healthcheck_id"}, Confirm: "delete health check %s", Done: "Health check %s deleted", Feature: "Health Checks"}.build(),
)

func init() { rootCmd.AddCommand(healthchecksCmd) }
