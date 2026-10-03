package cmd

var userCmd = group("user", "Your user: details, invites, memberships (needs a user-owned token)", `The user that owns the API token. These endpoints need a user-owned token;
account-owned tokens get a friendly error.

Examples:
  cfctl user get
  cfctl user invites
  cfctl user memberships`, nil,
	readSpec{Use: "get", Short: "Show your user details", Path: "/user", Title: "User", Feature: "user details",
		Fields: []col{{"ID", "id"}, {"Email", "email"}, {"First name", "first_name"}, {"Last name", "last_name"}, {"Country", "country"}, {"2FA", "two_factor_authentication_enabled"}, {"Suspended", "suspended"}, {"Created", "created_on"}}}.build(),
	readSpec{Use: "invites", Short: "List invitations to other accounts", Path: "/user/invites", Title: "Invites", Feature: "user invites",
		Cols: []col{{"ID", "id"}, {"Organization", "organization_name"}, {"Invited by", "invited_by"}, {"Status", "status"}, {"Roles", "#names:roles"}, {"Expires", "expires_on"}}}.build(),
	readSpec{Use: "memberships", Short: "List your account memberships", Path: "/memberships", Title: "Memberships", Paginate: true, Feature: "memberships",
		Cols: []col{{"ID", "id"}, {"Account", "account.name"}, {"Status", "status"}, {"Roles", "roles"}}}.build(),
)

func init() { rootCmd.AddCommand(userCmd) }
