package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var sslCmd = &cobra.Command{
	Use:     "ssl",
	Aliases: []string{"tls"},
	Short:   "SSL/TLS: encryption mode, certificate packs, origin CA and custom certificates",
	Long: `SSL/TLS for a zone: the encryption mode and TLS settings, edge certificate
packs (Universal SSL and Advanced Certificates), Origin CA certificates, and
custom (uploaded) certificates.

Examples:
  cfctl ssl status example.com
  cfctl ssl mode example.com strict
  cfctl ssl packs list example.com
  cfctl ssl packs order example.com --host example.com --host '*.example.com'
  cfctl ssl universal example.com
  cfctl ssl verification example.com
  cfctl ssl origin list example.com
  cfctl ssl origin create example.com --host example.com --host '*.example.com' --key-out origin.key --cert-out origin.pem
  cfctl ssl custom list example.com`,
}

// tlsSettings are the zone settings shown by `ssl status`.
var tlsSettings = []string{"ssl", "min_tls_version", "tls_1_3", "always_use_https", "automatic_https_rewrites", "opportunistic_encryption", "tls_client_auth", "ciphers", "0rtt", "security_header"}

var sslStatusCmd = &cobra.Command{
	Use:   "status <zone>",
	Short: "Show encryption mode, TLS settings, Universal SSL, and certificate status",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		raw, err := fetch(ctx, s, "/zones/{zone_id}/settings", nil, nil, false, "zone settings")
		if err != nil {
			return err
		}
		out := map[string]any{}
		for _, it := range asList(raw, "") {
			id := jstr(it, "id")
			for _, n := range tlsSettings {
				if id == n {
					out[id] = jget(it, "value")
				}
			}
		}
		if r, err := fetch(ctx, s, "/zones/{zone_id}/ssl/universal/settings", nil, nil, false, "Universal SSL"); err == nil {
			out["universal_ssl"] = decodeAny(r)
		}
		if r, err := fetch(ctx, s, "/zones/{zone_id}/ssl/verification", nil, nil, false, "SSL verification"); err == nil {
			out["verification"] = decodeAny(r)
		}
		if r, err := fetch(ctx, s, "/zones/{zone_id}/ssl/certificate_packs", nil, url.Values{"status": {"all"}}, false, "certificate packs"); err == nil {
			out["certificate_packs"] = decodeAny(r)
		}
		b, _ := json.Marshal(out)
		return emit(b, func(v any) {
			var fields []col
			for _, n := range tlsSettings {
				fields = append(fields, col{n, n})
			}
			fields = append(fields, col{"universal_ssl", "universal_ssl.enabled"}, col{"universal_ca", "universal_ssl.certificate_authority"})
			printDetail("SSL/TLS for "+args[0], v, fields)
			if packs, ok := jget(v, "certificate_packs").([]any); ok && len(packs) > 0 {
				fmt.Println()
				printTable("Certificate packs", packs, packCols)
			}
			if vs, ok := jget(v, "verification").([]any); ok && len(vs) > 0 {
				fmt.Println()
				printTable("Verification", vs, verificationCols)
			}
		})
	},
}

var sslModeCmd = &cobra.Command{
	Use:   "mode <zone> [off|flexible|full|strict]",
	Short: "Show or set the SSL/TLS encryption mode",
	Long: `Show or set the SSL/TLS encryption mode between Cloudflare and your origin:
off, flexible (HTTP to origin), full (HTTPS, any cert), strict (HTTPS, valid cert).`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		var raw json.RawMessage
		if len(args) == 1 {
			raw, err = fetch(ctx, s, "/zones/{zone_id}/settings/ssl", nil, nil, false, "zone settings")
		} else {
			m := strings.ToLower(args[1])
			switch m {
			case "off", "flexible", "full", "strict":
			default:
				return fmt.Errorf("mode must be off, flexible, full, or strict")
			}
			raw, err = send(ctx, s, "PATCH", "/zones/{zone_id}/settings/ssl", nil, nil, map[string]any{"value": m}, "zone settings")
		}
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			fmt.Println(ui.Success("SSL mode: " + jstr(v, "value")))
		})
	},
}

var packCols = []col{{"ID", "id"}, {"Type", "type"}, {"Status", "status"}, {"Hosts", "hosts"}, {"CA", "certificate_authority"}, {"Validity", "validity_days"}}
var verificationCols = []col{{"Host", "hostname"}, {"Status", "certificate_status"}, {"Method", "validation_method"}, {"Pack", "cert_pack_uuid"}}
var originCols = []col{{"ID", "id"}, {"Hosts", "hostnames"}, {"Type", "request_type"}, {"Expires", "expires_on"}, {"Validity", "requested_validity"}}
var customCols = []col{{"ID", "id"}, {"Hosts", "hosts"}, {"Status", "status"}, {"Issuer", "issuer"}, {"Expires", "expires_on"}, {"Bundle", "bundle_method"}}

var sslPacksCmd = group("packs", "Edge certificate packs (Universal SSL, Advanced Certificates)", "", []string{"pack", "certificate-packs"},
	readSpec{
		Use: "list <zone>", Short: "List certificate packs", Scope: scopeZone, Path: "/zones/{zone_id}/ssl/certificate_packs",
		Title: "Certificate packs", Cols: packCols, Feature: "certificate packs", Paginate: true,
		Flags: func(c *cobra.Command) { c.Flags().Bool("all-statuses", false, "Include inactive and pending packs") },
		Query: func(c *cobra.Command, q url.Values) error {
			if a, _ := c.Flags().GetBool("all-statuses"); a {
				q.Set("status", "all")
			}
			return nil
		},
	}.build(),
	readSpec{
		Use: "get <zone> <pack-id>", Short: "Show a certificate pack", Scope: scopeZone,
		Path: "/zones/{zone_id}/ssl/certificate_packs/{id}", Args: []string{"id"}, Title: "Certificate pack", Feature: "certificate packs",
	}.build(),
	writeSpec{
		Use: "order <zone>", Short: "Order an Advanced Certificate pack",
		Long: `Order an Advanced Certificate Manager pack (needs ACM on the zone).

Example:
  cfctl ssl packs order example.com --host example.com --host '*.example.com' --ca lets_encrypt --validity 90 --method txt`,
		Method: "POST", Scope: scopeZone, Path: "/zones/{zone_id}/ssl/certificate_packs/order",
		Body: func(c *cobra.Command, args []string) (any, error) {
			hs, _ := c.Flags().GetStringArray("host")
			hosts := splitList(hs)
			if len(hosts) == 0 {
				return nil, fmt.Errorf("pass at least one --host")
			}
			ca, _ := c.Flags().GetString("ca")
			days, _ := c.Flags().GetInt("validity")
			method, _ := c.Flags().GetString("method")
			return map[string]any{"type": "advanced", "hosts": hosts, "certificate_authority": ca, "validity_days": days, "validation_method": method}, nil
		},
		Flags: func(c *cobra.Command) {
			c.Flags().StringArray("host", nil, "Hostname to cover (repeatable or comma-separated)")
			c.Flags().String("ca", "lets_encrypt", "Certificate authority: lets_encrypt, google, ssl_com")
			c.Flags().Int("validity", 90, "Validity in days: 14, 30, 90, or 365")
			c.Flags().String("method", "txt", "Validation method: txt, http, or email")
		},
		Done: "Certificate pack ordered", Feature: "Advanced Certificate Manager",
	}.build(),
	writeSpec{
		Use: "delete <zone> <pack-id>", Short: "Delete an advanced certificate pack", Method: "DELETE", Scope: scopeZone,
		Path: "/zones/{zone_id}/ssl/certificate_packs/{id}", Args: []string{"id"},
		Confirm: "delete certificate pack %s", Done: "Certificate pack %s deleted", Feature: "certificate packs",
	}.build(),
)

var sslUniversalCmd = &cobra.Command{
	Use:   "universal <zone>",
	Short: "Show or change Universal SSL (--enable/--disable, --ca)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		body := map[string]any{}
		if cmd.Flags().Changed("enable") || cmd.Flags().Changed("disable") {
			en, _ := cmd.Flags().GetBool("enable")
			dis, _ := cmd.Flags().GetBool("disable")
			if en == dis {
				return fmt.Errorf("pass either --enable or --disable")
			}
			body["enabled"] = en
		}
		if ca, _ := cmd.Flags().GetString("ca"); ca != "" {
			body["certificate_authority"] = ca
		}
		var raw json.RawMessage
		if len(body) == 0 {
			raw, err = fetch(ctx, s, "/zones/{zone_id}/ssl/universal/settings", nil, nil, false, "Universal SSL")
		} else {
			if body["enabled"] == false {
				if err := confirm(cmd, "disable Universal SSL on "+args[0]+" (HTTPS stops working without another certificate)"); err != nil {
					return err
				}
			}
			raw, err = send(ctx, s, "PATCH", "/zones/{zone_id}/ssl/universal/settings", nil, nil, body, "Universal SSL")
		}
		if err != nil {
			return err
		}
		return emit(raw, func(v any) { printDetail("Universal SSL for "+args[0], v, nil) })
	},
}

var sslVerificationCmd = readSpec{
	Use: "verification <zone>", Short: "Show certificate validation status per hostname", Scope: scopeZone,
	Path: "/zones/{zone_id}/ssl/verification", Title: "Verification", Cols: verificationCols, Feature: "SSL verification",
}.build()

var sslOriginCmd = group("origin", "Origin CA certificates (for Full (strict) between Cloudflare and your origin)", "", []string{"origin-ca"},
	readSpec{
		Use: "list <zone>", Short: "List Origin CA certificates for a zone", Scope: scopeZone, Path: "/certificates",
		Title: "Origin CA certificates", Cols: originCols, Feature: "Origin CA", Paginate: true,
		Query: func(c *cobra.Command, q url.Values) error { return nil },
	}.originZone(),
	readSpec{
		Use: "get <certificate-id>", Short: "Show an Origin CA certificate (the certificate is public; no private key is stored)",
		Path: "/certificates/{id}", Args: []string{"id"}, Title: "Origin CA certificate", Feature: "Origin CA",
		Fields: []col{{"ID", "id"}, {"Hosts", "hostnames"}, {"Type", "request_type"}, {"Validity", "requested_validity"}, {"Expires", "expires_on"}, {"Certificate", "certificate"}},
	}.build(),
	sslOriginCreateCmd(),
	writeSpec{
		Use: "revoke <certificate-id>", Short: "Revoke an Origin CA certificate", Method: "DELETE",
		Path: "/certificates/{id}", Args: []string{"id"}, Confirm: "revoke Origin CA certificate %s (origins using it will fail Full (strict))",
		Done: "Origin CA certificate %s revoked", Feature: "Origin CA",
	}.build(),
)

// originZone resolves the zone positional into ?zone_id= (the endpoint isn't
// zone-scoped in its path).
func (r readSpec) originZone() *cobra.Command {
	c := r.build()
	c.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		zid, err := s.zone(ctx)
		if err != nil {
			return err
		}
		raw, err := fetch(ctx, s, r.Path, nil, url.Values{"zone_id": {zid}}, true, r.Feature)
		if err != nil {
			return err
		}
		return emit(raw, func(v any) {
			b, _ := json.Marshal(v)
			printTable(r.Title, asList(b, ""), r.Cols)
		})
	}
	return c
}

func sslOriginCreateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "create <zone>",
		Short: "Create an Origin CA certificate (generates the key and CSR locally)",
		Long: `Create an Origin CA certificate. By default cfctl generates a private key
and CSR locally: the key is written to --key-out (mode 0600) and is never
printed or sent anywhere. Pass --csr @file to use your own CSR instead.

Examples:
  cfctl ssl origin create example.com --host example.com --host '*.example.com' --key-out origin.key --cert-out origin.pem
  cfctl ssl origin create example.com --host example.com --csr @origin.csr --cert-out origin.pem
  cfctl ssl origin create example.com --host example.com --key-out k.pem --type origin-ecc --days 365`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			hs, _ := cmd.Flags().GetStringArray("host")
			hosts := splitList(hs)
			if len(hosts) == 0 {
				hosts = []string{args[0], "*." + args[0]}
			}
			typ, _ := cmd.Flags().GetString("type")
			days, _ := cmd.Flags().GetInt("days")
			csrArg, _ := cmd.Flags().GetString("csr")
			keyOut, _ := cmd.Flags().GetString("key-out")
			certOut, _ := cmd.Flags().GetString("cert-out")
			force, _ := cmd.Flags().GetBool("force")

			var csrPEM, keyPEM []byte
			if csrArg != "" {
				b, err := api.ReadData(csrArg, os.Stdin)
				if err != nil {
					return err
				}
				csrPEM = b
			} else {
				if keyOut == "" {
					return fmt.Errorf("pass --key-out <file> for the generated private key (or --csr @file)")
				}
				if _, err := os.Stat(keyOut); err == nil && !force {
					return fmt.Errorf("%s exists; pass --force to overwrite", keyOut)
				}
				var err error
				csrPEM, keyPEM, err = makeCSR(hosts, typ)
				if err != nil {
					return err
				}
			}
			s, err := adminSession(cmd, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"hostnames": hosts, "request_type": typ, "requested_validity": days, "csr": string(csrPEM)}
			raw, err := send(ctx, s, "POST", "/certificates", nil, nil, body, "Origin CA")
			if err != nil {
				return err
			}
			// Write the key only after the API accepted the CSR.
			if keyPEM != nil {
				if err := os.WriteFile(keyOut, keyPEM, 0o600); err != nil {
					return fmt.Errorf("certificate created but writing the key failed: %w", err)
				}
			}
			v := decodeAny(raw)
			if certOut != "" {
				if err := os.WriteFile(certOut, []byte(jstr(v, "certificate")), 0o644); err != nil {
					return err
				}
			}
			if jsonOutput {
				return printBody(raw, nil)
			}
			fmt.Println(ui.Success(fmt.Sprintf("Origin CA certificate %s created for %s (expires %s)", jstr(v, "id"), strings.Join(hosts, ", "), jstr(v, "expires_on"))))
			if keyPEM != nil {
				fmt.Println("  Private key: " + keyOut + " (keep it secret)")
			}
			if certOut != "" {
				fmt.Println("  Certificate: " + certOut)
			} else {
				fmt.Println(jstr(v, "certificate"))
			}
			return nil
		},
	}
	c.Flags().StringArray("host", nil, "Hostname to cover (repeatable; default: the zone and its wildcard)")
	c.Flags().String("type", "origin-rsa", "Key type: origin-rsa or origin-ecc")
	c.Flags().Int("days", 5475, "Validity in days: 7, 30, 90, 365, 730, 1095, or 5475")
	c.Flags().String("csr", "", "Use this CSR (PEM, @file, or -) instead of generating a key")
	c.Flags().String("key-out", "", "Write the generated private key here (mode 0600)")
	c.Flags().String("cert-out", "", "Write the certificate here (default: print it)")
	c.Flags().Bool("force", false, "Overwrite an existing --key-out file")
	return c
}

// makeCSR generates a private key and a CSR for hosts.
func makeCSR(hosts []string, typ string) (csrPEM, keyPEM []byte, err error) {
	var key any
	switch typ {
	case "origin-ecc":
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "origin-rsa":
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	default:
		return nil, nil, fmt.Errorf("--type must be origin-rsa or origin-ecc")
	}
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hosts[0]},
		DNSNames: hosts,
	}, key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), nil
}

var sslCustomCmd = group("custom", "Custom (uploaded) edge certificates (Business and Enterprise plans)", "", []string{"custom-certificates"},
	readSpec{
		Use: "list <zone>", Short: "List custom certificates", Scope: scopeZone, Path: "/zones/{zone_id}/custom_certificates",
		Title: "Custom certificates", Cols: customCols, Feature: "custom certificates", Paginate: true,
	}.build(),
	readSpec{
		Use: "get <zone> <certificate-id>", Short: "Show a custom certificate", Scope: scopeZone,
		Path: "/zones/{zone_id}/custom_certificates/{id}", Args: []string{"id"}, Title: "Custom certificate", Feature: "custom certificates",
	}.build(),
	writeSpec{
		Use: "upload <zone>", Short: "Upload a custom certificate and key",
		Long: `Upload a custom certificate. The key is read from a file and sent to
Cloudflare; it is never printed.

Example:
  cfctl ssl custom upload example.com --cert @cert.pem --key @key.pem --bundle-method ubiquitous`,
		Method: "POST", Scope: scopeZone, Path: "/zones/{zone_id}/custom_certificates",
		Body: func(c *cobra.Command, args []string) (any, error) {
			cert, _ := c.Flags().GetString("cert")
			key, _ := c.Flags().GetString("key")
			if cert == "" || key == "" {
				return nil, fmt.Errorf("pass --cert @file and --key @file")
			}
			cb, err := api.ReadData(cert, os.Stdin)
			if err != nil {
				return nil, err
			}
			kb, err := api.ReadData(key, os.Stdin)
			if err != nil {
				return nil, err
			}
			bm, _ := c.Flags().GetString("bundle-method")
			return map[string]any{"certificate": string(cb), "private_key": string(kb), "bundle_method": bm}, nil
		},
		Flags: func(c *cobra.Command) {
			c.Flags().String("cert", "", "Certificate PEM (@file)")
			c.Flags().String("key", "", "Private key PEM (@file)")
			c.Flags().String("bundle-method", "ubiquitous", "ubiquitous, optimal, or force")
		},
		Done: "Custom certificate uploaded", Feature: "custom certificates",
		Human: func(v any) {
			fmt.Println(ui.Success(fmt.Sprintf("Custom certificate %s uploaded (hosts %s, expires %s)", jstr(v, "id"), jstr(v, "hosts"), jstr(v, "expires_on"))))
		},
	}.build(),
	writeSpec{
		Use: "delete <zone> <certificate-id>", Short: "Delete a custom certificate", Method: "DELETE", Scope: scopeZone,
		Path: "/zones/{zone_id}/custom_certificates/{id}", Args: []string{"id"},
		Confirm: "delete custom certificate %s", Done: "Custom certificate %s deleted", Feature: "custom certificates",
	}.build(),
)

func init() {
	rootCmd.AddCommand(sslCmd)
	sslCmd.AddCommand(sslStatusCmd, sslModeCmd, sslPacksCmd, sslUniversalCmd, sslVerificationCmd, sslOriginCmd, sslCustomCmd)
	sslUniversalCmd.Flags().Bool("enable", false, "Turn Universal SSL on")
	sslUniversalCmd.Flags().Bool("disable", false, "Turn Universal SSL off (asks for confirmation)")
	sslUniversalCmd.Flags().String("ca", "", "Certificate authority: lets_encrypt, google, ssl_com")
	sslUniversalCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
}
