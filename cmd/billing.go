package cmd

var billingSubscriptionCols = []col{{"ID", "id"}, {"Plan", "rate_plan.public_name"}, {"Product", "product.name"}, {"State", "state"}, {"Price", "price"}, {"Currency", "currency"}, {"Frequency", "frequency"}, {"Period end", "current_period_end"}}

var billingCmd = group("billing", "Billing: subscriptions, billing profile, and history (read-only views)", `Billing information. These are views; changes happen in the dashboard.

Examples:
  cfctl billing subscriptions
  cfctl billing zone example.com
  cfctl billing profile
  cfctl billing history`, nil,
	readSpec{Use: "subscriptions", Short: "List the account's subscriptions", Scope: scopeAccount, Path: "/accounts/{account_id}/subscriptions", Title: "Subscriptions", Cols: billingSubscriptionCols, Feature: "billing"}.build(),
	readSpec{Use: "zone <zone>", Short: "Show a zone's plan subscription", Scope: scopeZone, Path: "/zones/{zone_id}/subscription", Title: "Zone subscription", Feature: "zone subscriptions",
		Fields: []col{{"ID", "id"}, {"Plan", "rate_plan.public_name"}, {"State", "state"}, {"Price", "price"}, {"Currency", "currency"}, {"Frequency", "frequency"}, {"Period start", "current_period_start"}, {"Period end", "current_period_end"}}}.build(),
	readSpec{Use: "profile", Short: "Show the billing profile", Path: "/user/billing/profile", Title: "Billing profile", Feature: "billing",
		Fields: []col{{"ID", "id"}, {"Name", "first_name"}, {"Last name", "last_name"}, {"Email", "billing_email"}, {"Company", "company"}, {"Country", "country"}, {"Payment", "payment_gateway"}, {"Updated", "edited_on"}}}.build(),
	readSpec{Use: "history", Short: "Show billing history (user-owned tokens only)", Path: "/user/billing/history", Title: "Billing history", Paginate: true, Feature: "billing",
		Cols: []col{{"ID", "id"}, {"Date", "occurred_at"}, {"Type", "type"}, {"Status", "status|action"}, {"Details", "description|receipt_id"}, {"Amount", "amount|amount_to_pay"}, {"Currency", "currency"}}}.build(),
)

func init() { rootCmd.AddCommand(billingCmd) }
