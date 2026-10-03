package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/dns"
	"github.com/dorkitude/cfctl/internal/client"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Record is cfctl's stable JSON shape for a DNS record.
type Record struct {
	ID         string `json:"id"`
	Zone       string `json:"zone"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	TTL        int64  `json:"ttl"` // 1 = automatic
	Proxied    bool   `json:"proxied"`
	Proxiable  bool   `json:"proxiable"`
	Priority   *int64 `json:"priority,omitempty"`
	Comment    string `json:"comment,omitempty"`
	CreatedOn  string `json:"created_on,omitempty"`
	ModifiedOn string `json:"modified_on,omitempty"`
}

func toRecord(r dns.RecordResponse, zone string) Record {
	out := Record{
		ID:         r.ID,
		Zone:       zone,
		Type:       string(r.Type),
		Name:       r.Name,
		Content:    r.Content,
		TTL:        int64(r.TTL),
		Proxied:    r.Proxied,
		Proxiable:  r.Proxiable,
		Comment:    r.Comment,
		CreatedOn:  timestamp(r.CreatedOn),
		ModifiedOn: timestamp(r.ModifiedOn),
	}
	// The SDK's flattened union drops priority for some record types, so read
	// it from the raw response JSON when present.
	var raw struct {
		Priority *float64 `json:"priority"`
	}
	if err := json.Unmarshal([]byte(r.JSON.RawJSON()), &raw); err == nil && raw.Priority != nil {
		p := int64(*raw.Priority)
		out.Priority = &p
	} else if r.Priority != 0 {
		p := int64(r.Priority)
		out.Priority = &p
	}
	return out
}

var recordsCmd = &cobra.Command{
	Use:     "records",
	Aliases: []string{"record", "rec"},
	Short:   "Manage DNS records",
	Long:    `List, view, create, update, and delete DNS records for a zone.`,
}

var recordsListCmd = &cobra.Command{
	Use:   "list [zone]",
	Short: "List records for a zone",
	Long: `List all DNS records for a zone (by name or ID).

Examples:
  cfctl records list example.com
  cfctl records list example.com --type A
  cfctl records list example.com --name www
  cfctl records list example.com --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		z, err := client.ResolveZone(ctx, app, args[0])
		if err != nil {
			return err
		}

		nameFilter, _ := cmd.Flags().GetString("name")
		typeFilter, _ := cmd.Flags().GetString("type")
		page, _ := cmd.Flags().GetInt("page")
		perPage, _ := cmd.Flags().GetInt("per-page")

		params := dns.RecordListParams{ZoneID: cloudflare.F(z.ID)}
		if cmd.Flags().Changed("name") {
			params.Name = cloudflare.F(dns.RecordListParamsName{Exact: cloudflare.F(fqdnName(nameFilter, z.Name))})
		}
		if typeFilter != "" {
			params.Type = cloudflare.F(dns.RecordListParamsType(strings.ToUpper(typeFilter)))
		}
		if perPage > 0 {
			params.PerPage = cloudflare.F(float64(perPage))
		}

		var raw []dns.RecordResponse
		if page > 0 {
			params.Page = cloudflare.F(float64(page))
			resp, err := app.Client.DNS.Records.List(ctx, params)
			if err != nil {
				return apiErr("failed to list records", err)
			}
			raw = resp.Result
		} else {
			iter := app.Client.DNS.Records.ListAutoPaging(ctx, params)
			for iter.Next() {
				raw = append(raw, iter.Current())
			}
			if err := iter.Err(); err != nil {
				return apiErr("failed to list records", err)
			}
		}

		records := make([]Record, 0, len(raw))
		for _, r := range raw {
			records = append(records, toRecord(r, z.Name))
		}

		if printJSON(records) {
			return nil
		}

		if len(records) == 0 {
			fmt.Println(ui.Warn("No records found"))
			return nil
		}

		fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("📋 %d records for %s", len(records), z.Name)))
		fmt.Println()

		for _, r := range records {
			proxiedMarker := ""
			if r.Proxied {
				proxiedMarker = ui.WarningStyle.Render(" ☁ proxied")
			}

			priorityStr := ""
			if r.Priority != nil {
				priorityStr = fmt.Sprintf(" (pri: %d)", *r.Priority)
			}

			fmt.Printf("  %s %-20s %-6s %s%s%s  %s\n",
				ui.RecordTypeStyle.Render(r.Type),
				ui.AccentStyle.Render(relativeName(r.Name, z.Name)),
				ttlString(float64(r.TTL)),
				truncate(r.Content, 50),
				priorityStr,
				proxiedMarker,
				ui.SubtleStyle.Render(r.ID),
			)
		}

		return nil
	},
}

var recordsGetCmd = &cobra.Command{
	Use:   "get [zone] [record-id]",
	Short: "Get record details",
	Long:  `Display detailed information about a specific DNS record.`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		z, err := client.ResolveZone(ctx, app, args[0])
		if err != nil {
			return err
		}

		resp, err := app.Client.DNS.Records.Get(ctx, args[1], dns.RecordGetParams{ZoneID: cloudflare.F(z.ID)})
		if err != nil {
			return apiErr("failed to get record", err)
		}

		r := toRecord(*resp, z.Name)
		if printJSON(r) {
			return nil
		}

		name := relativeName(r.Name, z.Name)
		fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("📋 %s %s", r.Type, r.Name)))
		fmt.Println()
		fmt.Printf("  %-14s %s\n", "ID:", r.ID)
		fmt.Printf("  %-14s %s\n", "Type:", r.Type)
		fmt.Printf("  %-14s %s\n", "Name:", name)
		fmt.Printf("  %-14s %s\n", "Content:", r.Content)
		fmt.Printf("  %-14s %s\n", "TTL:", ttlString(float64(r.TTL)))
		if r.Priority != nil {
			fmt.Printf("  %-14s %d\n", "Priority:", *r.Priority)
		}
		fmt.Printf("  %-14s %v\n", "Proxied:", r.Proxied)
		if r.Comment != "" {
			fmt.Printf("  %-14s %s\n", "Comment:", r.Comment)
		}
		fmt.Printf("  %-14s %s\n", "Created:", r.CreatedOn)
		fmt.Printf("  %-14s %s\n", "Modified:", r.ModifiedOn)

		return nil
	},
}

var recordsCreateCmd = &cobra.Command{
	Use:   "create [zone]",
	Short: "Create a DNS record",
	Long: `Create a new DNS record in a zone.

Names may be relative ("www"), "@" / "" for the apex, or fully qualified.
TTL 1 (the default) means automatic.

Examples:
  cfctl records create example.com --type A --name www --content 1.2.3.4
  cfctl records create example.com --type A --name www --content 1.2.3.4 --proxied
  cfctl records create example.com --type CNAME --name blog --content example.com
  cfctl records create example.com --type MX --name "" --content mail.example.com --priority 10
  cfctl records create example.com --type TXT --name @ --content "v=spf1 include:_spf.google.com ~all"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		recordType, _ := cmd.Flags().GetString("type")
		name, _ := cmd.Flags().GetString("name")
		content, _ := cmd.Flags().GetString("content")
		ttl, _ := cmd.Flags().GetInt("ttl")
		priority, _ := cmd.Flags().GetInt("priority")
		proxied, _ := cmd.Flags().GetBool("proxied")
		comment, _ := cmd.Flags().GetString("comment")

		if recordType == "" || content == "" {
			return fmt.Errorf("--type and --content are required")
		}

		z, err := client.ResolveZone(ctx, app, args[0])
		if err != nil {
			return err
		}

		if ttl <= 0 {
			ttl = 1
		}
		body := dns.RecordNewParamsBody{
			Type:    cloudflare.F(dns.RecordNewParamsBodyType(strings.ToUpper(recordType))),
			Name:    cloudflare.F(fqdnName(name, z.Name)),
			Content: cloudflare.F(content),
			TTL:     cloudflare.F(dns.TTL(ttl)),
		}
		if cmd.Flags().Changed("proxied") {
			body.Proxied = cloudflare.F(proxied)
		}
		if cmd.Flags().Changed("priority") {
			body.Priority = cloudflare.F(float64(priority))
		}
		if comment != "" {
			body.Comment = cloudflare.F(comment)
		}

		resp, err := app.Client.DNS.Records.New(ctx, dns.RecordNewParams{ZoneID: cloudflare.F(z.ID), Body: body})
		if err != nil {
			return apiErr("failed to create record", err)
		}

		r := toRecord(*resp, z.Name)
		if printJSON(r) {
			return nil
		}

		fmt.Println(ui.Success(fmt.Sprintf("Created %s record '%s' → %s (ID: %s)",
			r.Type, r.Name, r.Content, r.ID)))

		return nil
	},
}

var recordsUpdateCmd = &cobra.Command{
	Use:   "update [zone] [record-id]",
	Short: "Update a DNS record",
	Long: `Update an existing DNS record. Only the flags you pass are changed.

Examples:
  cfctl records update example.com 023e105f4ecef8ad9ca31a8372d0c353 --content 5.6.7.8
  cfctl records update example.com 023e105f4ecef8ad9ca31a8372d0c353 --name www2 --ttl 600
  cfctl records update example.com 023e105f4ecef8ad9ca31a8372d0c353 --proxied=false`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		z, err := client.ResolveZone(ctx, app, args[0])
		if err != nil {
			return err
		}

		body := dns.RecordEditParamsBody{}
		changed := false

		if cmd.Flags().Changed("name") {
			name, _ := cmd.Flags().GetString("name")
			body.Name = cloudflare.F(fqdnName(name, z.Name))
			changed = true
		}
		if cmd.Flags().Changed("content") {
			content, _ := cmd.Flags().GetString("content")
			body.Content = cloudflare.F(content)
			changed = true
		}
		if cmd.Flags().Changed("ttl") {
			ttl, _ := cmd.Flags().GetInt("ttl")
			body.TTL = cloudflare.F(dns.TTL(ttl))
			changed = true
		}
		if cmd.Flags().Changed("priority") {
			priority, _ := cmd.Flags().GetInt("priority")
			body.Priority = cloudflare.F(float64(priority))
			changed = true
		}
		if cmd.Flags().Changed("proxied") {
			proxied, _ := cmd.Flags().GetBool("proxied")
			body.Proxied = cloudflare.F(proxied)
			changed = true
		}
		if cmd.Flags().Changed("comment") {
			comment, _ := cmd.Flags().GetString("comment")
			body.Comment = cloudflare.F(comment)
			changed = true
		}
		if !changed {
			return fmt.Errorf("nothing to update: pass at least one of --name, --content, --ttl, --priority, --proxied, --comment")
		}

		resp, err := app.Client.DNS.Records.Edit(ctx, args[1], dns.RecordEditParams{ZoneID: cloudflare.F(z.ID), Body: body})
		if err != nil {
			return apiErr("failed to update record", err)
		}

		r := toRecord(*resp, z.Name)
		if printJSON(r) {
			return nil
		}

		fmt.Println(ui.Success(fmt.Sprintf("Updated %s record '%s' → %s",
			r.Type, r.Name, r.Content)))

		return nil
	},
}

var recordsDeleteCmd = &cobra.Command{
	Use:   "delete [zone] [record-id]",
	Short: "Delete a DNS record",
	Long: `PERMANENTLY delete a DNS record from a zone.

Examples:
  cfctl records delete example.com 023e105f4ecef8ad9ca31a8372d0c353
  cfctl records delete example.com 023e105f4ecef8ad9ca31a8372d0c353 --yes

Asks for confirmation on a terminal; pass --yes (-y) to skip it. Without a
terminal, --yes is required.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		app, err := getApp(ctx)
		if err != nil {
			return err
		}

		z, err := client.ResolveZone(ctx, app, args[0])
		if err != nil {
			return err
		}

		recordID := args[1]
		if err := confirm(cmd, fmt.Sprintf("delete DNS record %s from zone '%s'", recordID, z.Name)); err != nil {
			return err
		}
		_, err = app.Client.DNS.Records.Delete(ctx, recordID, dns.RecordDeleteParams{ZoneID: cloudflare.F(z.ID)})
		if err != nil {
			return apiErr("failed to delete record", err)
		}

		if printJSON(map[string]interface{}{"id": recordID, "zone": z.Name, "deleted": true}) {
			return nil
		}

		fmt.Println(ui.Success(fmt.Sprintf("Record %s deleted from zone '%s'", recordID, z.Name)))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(recordsCmd)

	recordsCmd.AddCommand(recordsListCmd)
	recordsListCmd.Flags().String("name", "", "Filter by record name (relative or FQDN)")
	recordsListCmd.Flags().String("type", "", "Filter by record type (A, AAAA, CNAME, MX, etc.)")
	recordsListCmd.Flags().Int("page", 0, "Page number (default: fetch all pages)")
	recordsListCmd.Flags().Int("per-page", 0, "Results per page")

	recordsCmd.AddCommand(recordsGetCmd)

	recordsCmd.AddCommand(recordsCreateCmd)
	recordsCreateCmd.Flags().StringP("type", "t", "", "Record type (A, AAAA, CNAME, MX, TXT, etc.)")
	recordsCreateCmd.Flags().StringP("name", "n", "", "Record name (@ or empty for apex)")
	recordsCreateCmd.Flags().StringP("content", "c", "", "Record content/value")
	recordsCreateCmd.Flags().Int("ttl", 0, "Time to live in seconds (1 or unset = auto)")
	recordsCreateCmd.Flags().Int("priority", 0, "Record priority (for MX)")
	recordsCreateCmd.Flags().Bool("proxied", false, "Proxy traffic through Cloudflare (orange cloud)")
	recordsCreateCmd.Flags().String("comment", "", "Record comment")

	recordsCmd.AddCommand(recordsUpdateCmd)
	recordsUpdateCmd.Flags().StringP("name", "n", "", "New record name")
	recordsUpdateCmd.Flags().StringP("content", "c", "", "New record content")
	recordsUpdateCmd.Flags().Int("ttl", 0, "New TTL (1 = auto)")
	recordsUpdateCmd.Flags().Int("priority", 0, "New priority")
	recordsUpdateCmd.Flags().Bool("proxied", false, "Proxy through Cloudflare (--proxied=false to turn off)")
	recordsUpdateCmd.Flags().String("comment", "", "New comment")

	recordsCmd.AddCommand(recordsDeleteCmd)
	recordsDeleteCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
}
