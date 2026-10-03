package cmd

var lbCols = []col{{"ID", "id"}, {"Name", "name"}, {"On", "enabled"}, {"Proxied", "proxied"}, {"Steering", "steering_policy"}, {"Default pools", "#len:default_pools"}, {"Fallback", "fallback_pool"}}
var poolCols = []col{{"ID", "id"}, {"Name", "name"}, {"On", "enabled"}, {"Healthy", "healthy"}, {"Origins", "#len:origins"}, {"Monitor", "monitor"}, {"Min origins", "minimum_origins"}}
var monitorCols = []col{{"ID", "id"}, {"Type", "type"}, {"Method", "method"}, {"Path", "path"}, {"Expected", "expected_codes"}, {"Interval", "interval"}, {"Description", "description"}}

var lbCmd = group("lb", "Load balancers, pools, and health monitors", `Load Balancing: load balancers (per zone), pools and monitors (per account).

Examples:
  cfctl lb list example.com
  cfctl lb get example.com <lb-id>
  cfctl lb create example.com --data @lb.json
  cfctl lb delete example.com <lb-id>
  cfctl lb pools list
  cfctl lb pools get <pool-id>
  cfctl lb pools health <pool-id>
  cfctl lb pools create --data @pool.json
  cfctl lb monitors list
  cfctl lb monitors create --data '{"type":"https","path":"/health","expected_codes":"200"}'

Create/update bodies follow the API (see 'cfctl api describe load-balancers create-load-balancer').`,
	[]string{"load-balancers", "loadbalancers"},
	readSpec{Use: "list <zone>", Short: "List load balancers on a zone", Scope: scopeZone, Path: "/zones/{zone_id}/load_balancers", Title: "Load balancers", Cols: lbCols, Feature: "Load Balancing"}.build(),
	readSpec{Use: "get <zone> <lb-id>", Short: "Show a load balancer", Scope: scopeZone, Path: "/zones/{zone_id}/load_balancers/{load_balancer_id}", Args: []string{"load_balancer_id"}, Title: "Load balancer", Feature: "Load Balancing"}.build(),
	writeSpec{Use: "create <zone>", Short: "Create a load balancer (--data JSON)", Method: "POST", Scope: scopeZone, Path: "/zones/{zone_id}/load_balancers", DataFlag: true, Done: "Load balancer created", Feature: "Load Balancing"}.build(),
	writeSpec{Use: "update <zone> <lb-id>", Short: "Patch a load balancer (--data JSON)", Method: "PATCH", Scope: scopeZone, Path: "/zones/{zone_id}/load_balancers/{load_balancer_id}", Args: []string{"load_balancer_id"}, DataFlag: true, Done: "Load balancer %s updated", Feature: "Load Balancing"}.build(),
	writeSpec{Use: "delete <zone> <lb-id>", Short: "Delete a load balancer", Method: "DELETE", Scope: scopeZone, Path: "/zones/{zone_id}/load_balancers/{load_balancer_id}", Args: []string{"load_balancer_id"}, Confirm: "delete load balancer %s", Done: "Load balancer %s deleted", Feature: "Load Balancing"}.build(),
	group("pools", "Origin pools (account level)", "", []string{"pool"},
		readSpec{Use: "list", Short: "List pools", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/pools", Title: "Pools", Cols: poolCols, Feature: "Load Balancing"}.build(),
		readSpec{Use: "get <pool-id>", Short: "Show a pool and its origins", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/pools/{pool_id}", Args: []string{"pool_id"}, Title: "Pool", Feature: "Load Balancing",
			Human: func(v any) {
				printDetail("Pool "+jstr(v, "name"), v, []col{{"ID", "id"}, {"Enabled", "enabled"}, {"Healthy", "healthy"}, {"Monitor", "monitor"}, {"Minimum origins", "minimum_origins"}, {"Check regions", "check_regions"}, {"Notification", "notification_email"}, {"Description", "description"}})
				origins, _ := jget(v, "origins").([]any)
				printTable("Origins", origins, []col{{"Name", "name"}, {"Address", "address"}, {"On", "enabled"}, {"Weight", "weight"}})
			}}.build(),
		readSpec{Use: "health <pool-id>", Short: "Show health check results for a pool's origins", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/pools/{pool_id}/health", Args: []string{"pool_id"}, Title: "Pool health", Feature: "Load Balancing"}.build(),
		writeSpec{Use: "create", Short: "Create a pool (--data JSON)", Method: "POST", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/pools", DataFlag: true, Done: "Pool created", Feature: "Load Balancing"}.build(),
		writeSpec{Use: "update <pool-id>", Short: "Patch a pool (--data JSON)", Method: "PATCH", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/pools/{pool_id}", Args: []string{"pool_id"}, DataFlag: true, Done: "Pool %s updated", Feature: "Load Balancing"}.build(),
		writeSpec{Use: "delete <pool-id>", Short: "Delete a pool", Method: "DELETE", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/pools/{pool_id}", Args: []string{"pool_id"}, Confirm: "delete pool %s", Done: "Pool %s deleted", Feature: "Load Balancing"}.build(),
	),
	group("monitors", "Health monitors (account level)", "", []string{"monitor"},
		readSpec{Use: "list", Short: "List monitors", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/monitors", Title: "Monitors", Cols: monitorCols, Feature: "Load Balancing"}.build(),
		readSpec{Use: "get <monitor-id>", Short: "Show a monitor", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/monitors/{monitor_id}", Args: []string{"monitor_id"}, Title: "Monitor", Feature: "Load Balancing"}.build(),
		writeSpec{Use: "create", Short: "Create a monitor (--data JSON)", Method: "POST", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/monitors", DataFlag: true, Done: "Monitor created", Feature: "Load Balancing"}.build(),
		writeSpec{Use: "delete <monitor-id>", Short: "Delete a monitor", Method: "DELETE", Scope: scopeAccount, Path: "/accounts/{account_id}/load_balancers/monitors/{monitor_id}", Args: []string{"monitor_id"}, Confirm: "delete monitor %s", Done: "Monitor %s deleted", Feature: "Load Balancing"}.build(),
	),
)

func init() {
	rootCmd.AddCommand(lbCmd)
}
