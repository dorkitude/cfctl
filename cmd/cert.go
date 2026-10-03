package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// cert and mtls-certificate: account mTLS certificates and CA chains, as
// used by Workers mtls_certificates bindings and Hyperdrive.

const mtlsBase = "/accounts/{account_id}/mtls_certificates"

var mtlsCols = []platCol{
	{H: "id", Path: "id"}, {H: "name", Path: "name"}, {H: "ca", Path: "ca"}, {H: "issuer", Path: "issuer", W: 40},
	{H: "expires", Path: "expires_on"}, {H: "uploaded", Path: "uploaded_on"},
}

var mtlsFields = []platCol{
	{H: "ID", Path: "id"}, {H: "Name", Path: "name"}, {H: "CA", Path: "ca"}, {H: "Issuer", Path: "issuer"},
	{H: "Serial", Path: "serial_number"}, {H: "Signature", Path: "signature"}, {H: "Expires", Path: "expires_on"}, {H: "Uploaded", Path: "uploaded_on"},
}

// mtlsUpload builds an upload command; ca selects a CA chain upload.
func mtlsUpload(use string, ca bool) *cobra.Command {
	short := "Upload an mTLS client certificate and private key"
	if ca {
		short = "Upload a CA certificate chain"
	}
	return platCommand(platSpec{
		Use: use, Short: short, Method: "POST", Path: mtlsBase,
		Flags: func(c *cobra.Command) {
			c.Flags().String("cert", "", "PEM certificate (chain) file (required)")
			if !ca {
				c.Flags().String("key", "", "PEM private key file (required)")
			} else {
				c.Flags().String("ca-cert", "", "Alias for --cert")
			}
			c.Flags().String("name", "", "Name for the certificate")
		},
		Body: func(c *cobra.Command, _ []string) (any, error) {
			certPath, _ := c.Flags().GetString("cert")
			if ca && certPath == "" {
				certPath, _ = c.Flags().GetString("ca-cert")
			}
			if certPath == "" {
				return nil, fmt.Errorf("--cert <file> is required")
			}
			pem, err := os.ReadFile(certPath)
			if err != nil {
				return nil, err
			}
			b := map[string]any{"certificates": string(pem), "ca": ca}
			if !ca {
				keyPath, _ := c.Flags().GetString("key")
				if keyPath == "" {
					return nil, fmt.Errorf("--key <file> is required")
				}
				key, err := os.ReadFile(keyPath)
				if err != nil {
					return nil, err
				}
				b["private_key"] = string(key)
			}
			platSetStr(c, b, "name", "name")
			return b, nil
		},
		Print: func(_ *cobra.Command, _ []string, raw json.RawMessage) error {
			var m map[string]any
			if err := platDecode(raw, &m); err != nil {
				return err
			}
			fmt.Println(ui.Success(fmt.Sprintf("Uploaded %s (%s)", platStr(m["name"]), platStr(m["id"]))))
			platDetail("", m, mtlsFields)
			return nil
		},
		Secrets: []string{"private_key"}, Product: "mTLS certificates",
	})
}

func mtlsCommon() []*cobra.Command {
	return platSpecs(
		platSpec{Use: "list", Short: "List uploaded mTLS certificates", Aliases: []string{"ls"}, Path: mtlsBase, List: true,
			Cols: mtlsCols, Title: "🪪 %d mTLS certificates", Product: "mTLS certificates"},
		platSpec{Use: "get <id>", Short: "Show a certificate", Path: mtlsBase + "/{mtls_certificate_id}", Fields: mtlsFields, Title: "🪪 Certificate %s", Product: "mTLS certificates"},
		platSpec{Use: "associations <id>", Short: "List services using a certificate", Path: mtlsBase + "/{mtls_certificate_id}/associations",
			Cols: []platCol{{H: "service", Path: "service"}, {H: "status", Path: "status"}}, Product: "mTLS certificates"},
		platSpec{Use: "delete <id>", Short: "Delete a certificate", Aliases: []string{"rm"}, Method: "DELETE", Path: mtlsBase + "/{mtls_certificate_id}",
			Confirm: "delete mTLS certificate %s", Done: "Deleted certificate %s", Product: "mTLS certificates"},
	)
}

func init() {
	upload := platGroup("upload", "Upload a client certificate or a CA chain", "", nil,
		mtlsUpload("mtls-certificate", false), mtlsUpload("certificate-authority", true))
	certCmd := platGroup("cert", "Manage mTLS client certificates and CA chains", `Manage the account's mTLS certificates and CA certificate chains (used by Workers
mtls_certificates bindings and Hyperdrive).

  cfctl cert upload mtls-certificate --cert cert.pem --key key.pem [--name n]
  cfctl cert upload certificate-authority --cert ca.pem [--name n]
  cfctl cert list | get <id> | associations <id> | delete <id>

Generated: 'cfctl api mtls-certificate-management ...'.`, nil, append([]*cobra.Command{upload}, mtlsCommon()...)...)
	mtlsCmd := platGroup("mtls-certificate", "Manage certificates used for mTLS connections", `Manage mTLS certificates (wrangler mtls-certificate). Same as 'cfctl cert'.

  cfctl mtls-certificate upload --cert cert.pem --key key.pem [--name n]
  cfctl mtls-certificate list | get <id> | delete <id>`, []string{"mtls"},
		append([]*cobra.Command{mtlsUpload("upload", false)}, mtlsCommon()...)...)
	rootCmd.AddCommand(certCmd, mtlsCmd)
}
