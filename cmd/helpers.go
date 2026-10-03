package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/client"
	"github.com/dorkitude/cfctl/internal/output"
)

// getApp returns an authenticated App from stored credentials and flags.
func getApp(ctx context.Context) (*client.App, error) {
	return client.New(ctx)
}

// printJSON outputs v as JSON if --json flag is set, returns true if it did.
func printJSON(v interface{}) bool {
	if jsonOutput {
		output.JSON(v)
		return true
	}
	return false
}

// apiErr wraps an SDK error with context, without leaking request details.
func apiErr(what string, err error) error {
	return fmt.Errorf("%s: %w", what, client.APIError(err))
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// date formats a timestamp as YYYY-MM-DD ("" for zero).
func date(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

// timestamp formats a timestamp as RFC 3339 ("" for zero).
func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// relativeName turns a record FQDN into simple-style display form:
// "@" for the apex, "www" for www.example.com.
func relativeName(fqdn, zone string) string {
	fqdn = strings.TrimSuffix(fqdn, ".")
	if fqdn == "" || strings.EqualFold(fqdn, zone) {
		return "@"
	}
	if suffix := "." + zone; strings.HasSuffix(strings.ToLower(fqdn), strings.ToLower(suffix)) {
		return fqdn[:len(fqdn)-len(suffix)]
	}
	return fqdn
}

// fqdnName turns a user-supplied record name into the FQDN Cloudflare stores:
// "" or "@" → zone apex, "www" → www.example.com, "www.example.com" unchanged.
func fqdnName(name, zone string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" || name == "@" {
		return zone
	}
	lower, z := strings.ToLower(name), strings.ToLower(zone)
	if lower == z || strings.HasSuffix(lower, "."+z) {
		return name
	}
	return name + "." + zone
}

// ttlString renders a Cloudflare TTL, where 1 means "automatic".
func ttlString(ttl float64) string {
	if ttl == 1 {
		return "auto"
	}
	return fmt.Sprintf("%d", int64(ttl))
}
