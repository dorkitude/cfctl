package cmd

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var hyperdriveCmd = &cobra.Command{
	Use:   "hyperdrive",
	Short: "Manage Hyperdrive database configs",
	Long: `Manage Hyperdrive configs: connection pooling and query caching in front
of PostgreSQL and MySQL databases. A config can be given by ID or by name.

Passwords and Access client secrets are write-only: the API never returns
them and cfctl never prints them.

Examples:
  cfctl hyperdrive list
  cfctl hyperdrive create my-db --connection-string 'postgres://user:pass@db.example.com:5432/app'
  cfctl hyperdrive update my-db --max-age 120 --swr 30
  cfctl hyperdrive restart my-db

Generated equivalents: cfctl api hyperdrive <op>.`,
}

func hyperdriveID(c *stClient, arg string) (string, error) {
	return c.resolve("Hyperdrive config", c.p("hyperdrive/configs"), nil, arg, "id", "name")
}

func hyperdrivePath(segs ...string) sbPathFn {
	return func(c *stClient, args []string) (string, url.Values, error) {
		id, err := hyperdriveID(c, args[0])
		if err != nil {
			return "", nil, err
		}
		return c.p("hyperdrive/configs", append([]string{id}, segs...)...), nil, nil
	}
}

var hyperdriveListCmd = sbListCmd("list", "List Hyperdrive configs", "", cobra.NoArgs, sbFixed("hyperdrive/configs"),
	"🚀 %d Hyperdrive configs", "No Hyperdrive configs found", []stCol{
		{Header: "NAME", Path: "name"},
		{Header: "ID", Path: "id"},
		{Header: "SCHEME", Path: "origin.scheme"},
		{Header: "HOST", Path: "", Fmt: sbFirst("origin.host", "origin.service_id")},
		{Header: "DATABASE", Path: "origin.database"},
		{Header: "CACHING", Path: "caching.disabled", Fmt: func(v any) string {
			if v == true {
				return "off"
			}
			return "on"
		}},
	})

var hyperdriveGetCmd = sbGetCmd("get <config>", "Show a Hyperdrive config", "", "🚀 Hyperdrive config", cobra.ExactArgs(1), hyperdrivePath())

var hyperdriveDeleteCmd = sbDeleteCmd("delete <config>", "Delete a Hyperdrive config", "", cobra.ExactArgs(1),
	func(a []string) string { return "Hyperdrive config " + a[0] }, hyperdrivePath())

func hyperdriveFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("connection-string", "", "Origin as a URL: postgres://user:password@host:port/database (or mysql://...)")
	f.String("origin-host", "", "Origin database host")
	f.Int("origin-port", 0, "Origin database port")
	f.String("origin-scheme", "", "Origin scheme: postgres, postgresql, or mysql")
	f.String("database", "", "Origin database name")
	f.String("origin-user", "", "Origin database user")
	f.String("origin-password", "", "Origin database password (write-only)")
	f.String("access-client-id", "", "Cloudflare Access client ID (database behind a Tunnel)")
	f.String("access-client-secret", "", "Cloudflare Access client secret (write-only)")
	f.String("service-id", "", "Workers VPC service ID (database reachable through a Workers VPC)")
	f.Bool("caching-disabled", false, "Disable query caching")
	f.Int("max-age", 0, "Seconds a cached query result is fresh (default 60)")
	f.Int("swr", 0, "Seconds a stale result may be served while revalidating (default 15)")
	f.Int("origin-connection-limit", 0, "Soft maximum connections to the origin")
	f.String("sslmode", "", "TLS mode (postgres: require, verify-ca, verify-full; mysql: REQUIRED, VERIFY_CA, VERIFY_IDENTITY)")
	f.String("ca-certificate-id", "", "CA certificate ID for verify-ca/verify-full")
	f.String("mtls-certificate-id", "", "Client mTLS certificate ID")
	stDataFlag(cmd)
}

// hyperdriveParseConn turns a connection string into origin fields.
func hyperdriveParseConn(s string) (map[string]any, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid --connection-string: want scheme://user:password@host:port/database")
	}
	origin := map[string]any{"scheme": u.Scheme, "host": u.Hostname()}
	switch u.Scheme {
	case "postgres", "postgresql", "mysql":
	default:
		return nil, fmt.Errorf("invalid --connection-string: scheme must be postgres, postgresql, or mysql")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid --connection-string: bad port")
		}
		origin["port"] = n
	} else if u.Scheme == "mysql" {
		origin["port"] = 3306
	} else {
		origin["port"] = 5432
	}
	if u.User != nil {
		origin["user"] = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			origin["password"] = pw
		}
	}
	if db := strings.TrimPrefix(u.Path, "/"); db != "" {
		origin["database"] = db
	}
	return origin, nil
}

// hyperdriveBody builds a create/update body from --data and flags.
func hyperdriveBody(cmd *cobra.Command) (map[string]any, error) {
	body, err := stBody(cmd)
	if err != nil {
		return nil, err
	}
	if cs, _ := cmd.Flags().GetString("connection-string"); cs != "" {
		origin, err := hyperdriveParseConn(cs)
		if err != nil {
			return nil, err
		}
		for k, v := range origin {
			stSet(body, "origin."+k, v)
		}
	}
	stFlagStr(cmd, body, "origin-host", "origin.host")
	stFlagInt(cmd, body, "origin-port", "origin.port")
	stFlagStr(cmd, body, "origin-scheme", "origin.scheme")
	stFlagStr(cmd, body, "database", "origin.database")
	stFlagStr(cmd, body, "origin-user", "origin.user")
	stFlagStr(cmd, body, "origin-password", "origin.password")
	stFlagStr(cmd, body, "access-client-id", "origin.access_client_id")
	stFlagStr(cmd, body, "access-client-secret", "origin.access_client_secret")
	stFlagStr(cmd, body, "service-id", "origin.service_id")
	if stGet(body, "origin.access_client_id") != nil || stGet(body, "origin.service_id") != nil {
		// Tunnel and VPC origins have no port.
		if !cmd.Flags().Changed("origin-port") {
			if o, ok := body["origin"].(map[string]any); ok {
				delete(o, "port")
			}
		}
	}
	stFlagBool(cmd, body, "caching-disabled", "caching.disabled")
	stFlagInt(cmd, body, "max-age", "caching.max_age")
	stFlagInt(cmd, body, "swr", "caching.stale_while_revalidate")
	stFlagInt(cmd, body, "origin-connection-limit", "origin_connection_limit")
	stFlagStr(cmd, body, "sslmode", "mtls.sslmode")
	stFlagStr(cmd, body, "ca-certificate-id", "mtls.ca_certificate_id")
	stFlagStr(cmd, body, "mtls-certificate-id", "mtls.mtls_certificate_id")
	return body, nil
}

var hyperdriveCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a Hyperdrive config",
	Long: `Create a Hyperdrive config. Give the origin as --connection-string or with
the --origin-* flags.

Examples:
  cfctl hyperdrive create my-db --connection-string 'postgres://app:secret@db.example.com:5432/app'
  cfctl hyperdrive create my-db --origin-host db.example.com --origin-port 5432 --origin-scheme postgres \
      --database app --origin-user app --origin-password "$PGPASSWORD"
  cfctl hyperdrive create tunnel-db --origin-host db.internal --origin-scheme postgres --database app \
      --origin-user app --origin-password "$PW" --access-client-id ID.access --access-client-secret "$SECRET"
  cfctl hyperdrive create vpc-db --service-id SVC --origin-scheme mysql --database app --origin-user app --origin-password "$PW"
  cfctl hyperdrive create my-db --connection-string "$URL" --caching-disabled`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := hyperdriveBody(cmd)
		if err != nil {
			return err
		}
		body["name"] = args[0]
		if body["origin"] == nil && body["integration"] == nil {
			return fmt.Errorf("missing origin: pass --connection-string or --origin-host/--database/--origin-user/--origin-password")
		}
		return sbSendShow(c, "POST", c.p("hyperdrive/configs"), nil, body, "Created Hyperdrive config "+args[0])
	}),
}

var hyperdriveUpdateCmd = &cobra.Command{
	Use:   "update <config>",
	Short: "Update a Hyperdrive config (partial update)",
	Long: `Update a Hyperdrive config. Only the flags you pass change.

Examples:
  cfctl hyperdrive update my-db --max-age 120 --swr 30
  cfctl hyperdrive update my-db --origin-password "$NEW_PASSWORD"
  cfctl hyperdrive update my-db --name my-db-v2 --origin-connection-limit 40`,
	Args: cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		body, err := hyperdriveBody(cmd)
		if err != nil {
			return err
		}
		stFlagStr(cmd, body, "name", "name")
		if len(body) == 0 {
			return fmt.Errorf("nothing to update: pass flags or --data")
		}
		id, err := hyperdriveID(c, args[0])
		if err != nil {
			return err
		}
		return sbSendShow(c, "PATCH", c.p("hyperdrive/configs", id), nil, body, "Updated Hyperdrive config "+args[0])
	}),
}

var hyperdriveRestartCmd = &cobra.Command{
	Use:   "restart <config>",
	Short: "Restart a config's connection pool",
	Args:  cobra.ExactArgs(1),
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		id, err := hyperdriveID(c, args[0])
		if err != nil {
			return err
		}
		return sbSend(c, "POST", c.p("hyperdrive/configs", id, "restart"), nil, map[string]any{}, "Restarted the connection pool of "+args[0])
	}),
}

var hyperdrivePlanetscaleCmd = &cobra.Command{
	Use:   "planetscale",
	Short: "Cloudflare-billed PlanetScale databases",
}

var hyperdrivePlanetscaleSignatureCmd = &cobra.Command{
	Use:   "signature",
	Short: "Get a short-lived signed authorization for creating a Cloudflare-billed PlanetScale database",
	Long: `Get a short-lived signed authorization to pass to PlanetScale's CLI when
creating a database billed through Cloudflare. Prints the API result.`,
	Args: cobra.NoArgs,
	RunE: sbRun(func(cmd *cobra.Command, c *stClient, args []string) error {
		raw, err := c.result(stReq{Method: "POST", Path: c.p("hyperdrive/integrationsOperations/planetScale/createDatabaseSignature")})
		if err != nil {
			return err
		}
		return printBody(stNonNull(raw), nil)
	}),
}

func init() {
	hyperdriveFlags(hyperdriveCreateCmd)
	hyperdriveFlags(hyperdriveUpdateCmd)
	hyperdriveUpdateCmd.Flags().String("name", "", "Rename the config")
	hyperdrivePlanetscaleCmd.AddCommand(hyperdrivePlanetscaleSignatureCmd)
	hyperdriveCmd.AddCommand(hyperdriveListCmd, hyperdriveGetCmd, hyperdriveCreateCmd, hyperdriveUpdateCmd, hyperdriveDeleteCmd, hyperdriveRestartCmd, hyperdrivePlanetscaleCmd)
	rootCmd.AddCommand(hyperdriveCmd)
}
