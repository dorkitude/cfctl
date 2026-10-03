package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// Analytics presets over the GraphQL Analytics API. Every query is a
// read (POST /graphql is allowed in read-only mode).

var analyticsCmd = &cobra.Command{
	Use:   "analytics",
	Short: "Analytics presets: zone traffic, top paths/countries, firewall, Workers, R2 (GraphQL)",
	Long: `Ready-made analytics over Cloudflare's GraphQL Analytics API.

--since takes 30m, 24h, 3d, 7d, 30d, YYYY-MM-DD, or RFC 3339 (default 24h).
Ranges up to 3 days use hourly data; longer ranges use daily data. Free plans
limit how far back some datasets go; Cloudflare's error is shown when a range
is too wide. Every command supports --json.

Examples:
  cfctl analytics zone example.com
  cfctl analytics zone example.com --since 30d --json
  cfctl analytics paths example.com --since 7d --limit 20
  cfctl analytics countries example.com --since 7d
  cfctl analytics firewall example.com --since 24h
  cfctl analytics workers --since 7d
  cfctl analytics r2 --since 30d

For anything else: cfctl graphql '<query>'`,
}

// timeRange returns the window for --since/--until.
func timeRange(cmd *cobra.Command) (time.Time, time.Time, error) {
	now := time.Now().UTC().Truncate(time.Minute)
	sinceS, _ := cmd.Flags().GetString("since")
	since, err := parseSince(sinceS, now)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	until := now
	if u, _ := cmd.Flags().GetString("until"); u != "" {
		if until, err = parseSince(u, now); err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if !since.Before(until) {
		return time.Time{}, time.Time{}, fmt.Errorf("--since must be before --until")
	}
	return since, until, nil
}

// gql runs a query and decodes data, turning GraphQL errors into a Go error.
func gql(ctx context.Context, s *apiSession, query string, vars map[string]any) (any, error) {
	body, err := s.c.GraphQL(ctx, query, vars)
	if body != nil {
		var resp struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if json.Unmarshal(body, &resp) == nil {
			if len(resp.Errors) > 0 {
				var msgs []string
				for _, e := range resp.Errors {
					msgs = append(msgs, e.Message)
				}
				msg := strings.Join(msgs, "; ")
				hint := ""
				switch {
				case strings.Contains(msg, "does not have access"):
					hint = "\n  hint: this dataset isn't available on this zone's or account's plan"
				case strings.Contains(msg, "time range"):
					hint = "\n  hint: this plan limits how far back this dataset goes; try a shorter --since"
				case strings.Contains(strings.ToLower(msg), "not authorized") || strings.Contains(strings.ToLower(msg), "access"):
					hint = "\n  hint: the token needs the Analytics Read (account or zone) permission"
				}
				return nil, fmt.Errorf("GraphQL: %s%s", msg, hint)
			}
			if err == nil {
				return decodeAny(resp.Data), nil
			}
		}
	}
	if err != nil {
		return nil, friendly("analytics", err)
	}
	return nil, fmt.Errorf("unexpected GraphQL response")
}

func num(v any, path string) float64 {
	f, _ := strconv.ParseFloat(jstr(v, path), 64)
	return f
}

func humanBytes(b float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", b, units[i])
	}
	return fmt.Sprintf("%.1f %s", b, units[i])
}

func humanCount(n float64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.2fB", n/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.2fM", n/1e6)
	case n >= 1e4:
		return fmt.Sprintf("%.1fK", n/1e3)
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

func pct(a, b float64) string {
	if b == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", 100*a/b)
}

// kv is a name/count pair for top-N tables.
type kv struct {
	Name  string  `json:"name"`
	Count float64 `json:"requests"`
	Bytes float64 `json:"bytes,omitempty"`
	Extra float64 `json:"threats,omitempty"`
}

func topN(m map[string]*kv, n int) []kv {
	var out []kv
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// zoneTraffic fetches the hourly (≤3 days) or daily groups for a zone.
func zoneTraffic(ctx context.Context, s *apiSession, since, until time.Time) ([]any, error) {
	zid, err := s.zone(ctx)
	if err != nil {
		return nil, err
	}
	const fields = `sum { requests cachedRequests bytes cachedBytes threats pageViews encryptedRequests responseStatusMap { edgeResponseStatus requests } countryMap { clientCountryName requests bytes threats } } uniq { uniques }`
	var q string
	vars := map[string]any{"zone": zid}
	if until.Sub(since) <= 72*time.Hour {
		q = `query($zone: String!, $since: Time!, $until: Time!) { viewer { zones(filter: {zoneTag: $zone}) { groups: httpRequests1hGroups(limit: 1000, filter: {datetime_geq: $since, datetime_lt: $until}) { dimensions { ts: datetime } ` + fields + ` } } } }`
		vars["since"], vars["until"] = since.Format(time.RFC3339), until.Format(time.RFC3339)
	} else {
		q = `query($zone: String!, $since: Date!, $until: Date!) { viewer { zones(filter: {zoneTag: $zone}) { groups: httpRequests1dGroups(limit: 1000, filter: {date_geq: $since, date_leq: $until}) { dimensions { ts: date } ` + fields + ` } } } }`
		vars["since"], vars["until"] = since.Format("2006-01-02"), until.Format("2006-01-02")
	}
	data, err := gql(ctx, s, q, vars)
	if err != nil {
		return nil, err
	}
	groups, _ := jget(data, "viewer.zones.0.groups").([]any)
	return groups, nil
}

var analyticsZoneCmd = &cobra.Command{
	Use:   "zone <zone>",
	Short: "Traffic summary: requests, cache ratio, bandwidth, threats, status codes, countries",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		since, until, err := timeRange(cmd)
		if err != nil {
			return err
		}
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		groups, err := zoneTraffic(ctx, s, since, until)
		if err != nil {
			return err
		}
		var req, cached, bytes, cachedBytes, threats, views, uniques, encrypted float64
		status := map[string]*kv{}
		countries := map[string]*kv{}
		var series []map[string]any
		for _, g := range groups {
			r := num(g, "sum.requests")
			req += r
			cached += num(g, "sum.cachedRequests")
			bytes += num(g, "sum.bytes")
			cachedBytes += num(g, "sum.cachedBytes")
			threats += num(g, "sum.threats")
			views += num(g, "sum.pageViews")
			encrypted += num(g, "sum.encryptedRequests")
			uniques += num(g, "uniq.uniques")
			series = append(series, map[string]any{"time": jstr(g, "dimensions.ts"), "requests": r, "bytes": num(g, "sum.bytes")})
			for _, x := range asAny(jget(g, "sum.responseStatusMap")) {
				k := jstr(x, "edgeResponseStatus")
				if status[k] == nil {
					status[k] = &kv{Name: k}
				}
				status[k].Count += num(x, "requests")
			}
			for _, x := range asAny(jget(g, "sum.countryMap")) {
				k := jstr(x, "clientCountryName")
				if countries[k] == nil {
					countries[k] = &kv{Name: k}
				}
				countries[k].Count += num(x, "requests")
				countries[k].Bytes += num(x, "bytes")
				countries[k].Extra += num(x, "threats")
			}
		}
		sort.Slice(series, func(i, j int) bool { return fmt.Sprint(series[i]["time"]) < fmt.Sprint(series[j]["time"]) })
		limit, _ := cmd.Flags().GetInt("limit")
		summary := map[string]any{
			"zone": args[0], "since": since.Format(time.RFC3339), "until": until.Format(time.RFC3339),
			"requests": req, "cached_requests": cached, "cache_ratio": ratio(cached, req),
			"bytes": bytes, "cached_bytes": cachedBytes, "threats": threats, "page_views": views,
			"encrypted_requests": encrypted, "uniques_sum": uniques,
			"status_codes": topN(status, 0), "top_countries": topN(countries, limit), "series": series,
		}
		if jsonOutput {
			return printJSONValue(summary)
		}
		fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("Traffic for %s, %s → %s", args[0], since.Format("2006-01-02 15:04"), until.Format("2006-01-02 15:04 UTC"))))
		rows := [][2]string{
			{"Requests", humanCount(req)},
			{"Cached", humanCount(cached) + " (" + pct(cached, req) + ")"},
			{"Bandwidth", humanBytes(bytes) + " (" + pct(cachedBytes, bytes) + " cached)"},
			{"Encrypted", pct(encrypted, req)},
			{"Page views", humanCount(views)},
			{"Threats", humanCount(threats)},
			{"Unique visitors", humanCount(uniques) + " (sum of buckets)"},
		}
		for _, r := range rows {
			fmt.Printf("  %-16s %s\n", r[0]+":", r[1])
		}
		fmt.Println()
		printKVTable("Status codes", topN(status, 0), req, false)
		fmt.Println()
		printKVTable("Top countries", topN(countries, limit), req, true)
		return nil
	},
}

func ratio(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func asAny(v any) []any {
	a, _ := v.([]any)
	return a
}

func printKVTable(title string, rows []kv, total float64, withBytes bool) {
	var items []any
	for _, r := range rows {
		m := map[string]any{"name": r.Name, "requests": humanCount(r.Count), "share": pct(r.Count, total)}
		if withBytes {
			m["bytes"] = humanBytes(r.Bytes)
			m["threats"] = humanCount(r.Extra)
		}
		items = append(items, m)
	}
	cols := []col{{"Name", "name"}, {"Requests", "requests"}, {"Share", "share"}}
	if withBytes {
		cols = append(cols, col{"Bandwidth", "bytes"}, col{"Threats", "threats"})
	}
	printTable(title, items, cols)
}

var analyticsCountriesCmd = &cobra.Command{
	Use:   "countries <zone>",
	Short: "Top countries by requests",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		since, until, err := timeRange(cmd)
		if err != nil {
			return err
		}
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		groups, err := zoneTraffic(ctx, s, since, until)
		if err != nil {
			return err
		}
		countries := map[string]*kv{}
		var total float64
		for _, g := range groups {
			for _, x := range asAny(jget(g, "sum.countryMap")) {
				k := jstr(x, "clientCountryName")
				if countries[k] == nil {
					countries[k] = &kv{Name: k}
				}
				countries[k].Count += num(x, "requests")
				countries[k].Bytes += num(x, "bytes")
				countries[k].Extra += num(x, "threats")
				total += num(x, "requests")
			}
		}
		limit, _ := cmd.Flags().GetInt("limit")
		top := topN(countries, limit)
		if jsonOutput {
			return printJSONValue(top)
		}
		printKVTable("Top countries for "+args[0]+" since "+since.Format("2006-01-02 15:04"), top, total, true)
		return nil
	},
}

// adaptiveTop runs a top-N query over httpRequestsAdaptiveGroups by one dimension.
func adaptiveTop(cmd *cobra.Command, zone, dim, title string, extraFilter map[string]any) error {
	ctx := context.Background()
	since, until, err := timeRange(cmd)
	if err != nil {
		return err
	}
	s, err := adminSession(cmd, zone)
	if err != nil {
		return err
	}
	zid, err := s.zone(ctx)
	if err != nil {
		return err
	}
	limit, _ := cmd.Flags().GetInt("limit")
	filter := map[string]any{"datetime_geq": since.Format(time.RFC3339), "datetime_lt": until.Format(time.RFC3339)}
	for k, v := range extraFilter {
		filter[k] = v
	}
	q := `query($zone: String!, $limit: Int!, $filter: ZoneHttpRequestsAdaptiveGroupsFilter_InputObject) { viewer { zones(filter: {zoneTag: $zone}) { top: httpRequestsAdaptiveGroups(limit: $limit, orderBy: [count_DESC], filter: $filter) { count sum { edgeResponseBytes } dimensions { key: ` + dim + ` } } } } }`
	data, err := gql(ctx, s, q, map[string]any{"zone": zid, "limit": limit, "filter": filter})
	if err != nil {
		return err
	}
	var rows []kv
	var total float64
	for _, g := range asAny(jget(data, "viewer.zones.0.top")) {
		r := kv{Name: jstr(g, "dimensions.key"), Count: num(g, "count"), Bytes: num(g, "sum.edgeResponseBytes")}
		total += r.Count
		rows = append(rows, r)
	}
	if jsonOutput {
		if rows == nil {
			rows = []kv{}
		}
		return printJSONValue(rows)
	}
	var items []any
	for _, r := range rows {
		items = append(items, map[string]any{"name": r.Name, "requests": humanCount(r.Count), "share": pct(r.Count, total), "bytes": humanBytes(r.Bytes)})
	}
	printTable(title+" for "+zone+" since "+since.Format("2006-01-02 15:04"), items, []col{{"Name", "name"}, {"Requests", "requests"}, {"Share of top", "share"}, {"Bandwidth", "bytes"}})
	return nil
}

var analyticsPathsCmd = &cobra.Command{
	Use:   "paths <zone>",
	Short: "Top request paths (optionally only one status code)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var extra map[string]any
		if st, _ := cmd.Flags().GetInt("status"); st > 0 {
			extra = map[string]any{"edgeResponseStatus": st}
		}
		if h, _ := cmd.Flags().GetString("host"); h != "" {
			if extra == nil {
				extra = map[string]any{}
			}
			extra["clientRequestHTTPHost"] = h
		}
		return adaptiveTop(cmd, args[0], "clientRequestPath", "Top paths", extra)
	},
}

var analyticsFirewallCmd = &cobra.Command{
	Use:   "firewall <zone>",
	Short: "Security events by action, source, and country",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		since, until, err := timeRange(cmd)
		if err != nil {
			return err
		}
		s, err := adminSession(cmd, args[0])
		if err != nil {
			return err
		}
		zid, err := s.zone(ctx)
		if err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		q := `query($zone: String!, $limit: Int!, $since: Time!, $until: Time!) { viewer { zones(filter: {zoneTag: $zone}) { events: firewallEventsAdaptiveGroups(limit: $limit, orderBy: [count_DESC], filter: {datetime_geq: $since, datetime_lt: $until}) { count dimensions { action source clientCountryName } } } } }`
		vars := map[string]any{"zone": zid, "limit": limit, "since": since.Format(time.RFC3339), "until": until.Format(time.RFC3339)}
		data, err := gql(ctx, s, q, vars)
		var events []any
		if err != nil && strings.Contains(err.Error(), "does not have access") {
			// Plans without the grouped dataset: aggregate raw events.
			raw := `query($zone: String!, $since: Time!, $until: Time!) { viewer { zones(filter: {zoneTag: $zone}) { events: firewallEventsAdaptive(limit: 10000, orderBy: [datetime_DESC], filter: {datetime_geq: $since, datetime_lt: $until}) { action source clientCountryName } } } }`
			data, err = gql(ctx, s, raw, vars)
			if err != nil {
				return err
			}
			counts := map[string]map[string]any{}
			for _, e := range asAny(jget(data, "viewer.zones.0.events")) {
				k := jstr(e, "action") + "|" + jstr(e, "source") + "|" + jstr(e, "clientCountryName")
				if counts[k] == nil {
					counts[k] = map[string]any{"count": 0.0, "dimensions": map[string]any{"action": jstr(e, "action"), "source": jstr(e, "source"), "clientCountryName": jstr(e, "clientCountryName")}}
				}
				counts[k]["count"] = counts[k]["count"].(float64) + 1
			}
			for _, v := range counts {
				events = append(events, v)
			}
			sort.Slice(events, func(i, j int) bool { return num(events[i], "count") > num(events[j], "count") })
			if len(events) > limit {
				events = events[:limit]
			}
		} else if err != nil {
			return err
		} else {
			events = asAny(jget(data, "viewer.zones.0.events"))
		}
		if events == nil {
			events = []any{}
		}
		b, _ := json.Marshal(events)
		return emit(b, func(any) {
			printTable("Security events for "+args[0]+" since "+since.Format("2006-01-02 15:04"), events, []col{{"Events", "count"}, {"Action", "dimensions.action"}, {"Source", "dimensions.source"}, {"Country", "dimensions.clientCountryName"}})
		})
	},
}

var analyticsWorkersCmd = &cobra.Command{
	Use:   "workers",
	Short: "Workers invocations, errors, subrequests, and CPU time per script",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		since, until, err := timeRange(cmd)
		if err != nil {
			return err
		}
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		acct, err := s.account(ctx)
		if err != nil {
			return err
		}
		filter := map[string]any{"datetime_geq": since.Format(time.RFC3339), "datetime_lt": until.Format(time.RFC3339)}
		if sc, _ := cmd.Flags().GetString("script"); sc != "" {
			filter["scriptName"] = sc
		}
		q := `query($acct: String!, $filter: AccountWorkersInvocationsAdaptiveFilter_InputObject) { viewer { accounts(filter: {accountTag: $acct}) { w: workersInvocationsAdaptive(limit: 10000, filter: $filter) { sum { requests errors subrequests } quantiles { cpuTimeP50 cpuTimeP99 } dimensions { scriptName status } } } } }`
		data, err := gql(ctx, s, q, map[string]any{"acct": acct, "filter": filter})
		if err != nil {
			return err
		}
		type agg struct {
			Script      string  `json:"script"`
			Requests    float64 `json:"requests"`
			Errors      float64 `json:"errors"`
			Subrequests float64 `json:"subrequests"`
			CPUP50      float64 `json:"cpu_time_p50_us"`
			CPUP99      float64 `json:"cpu_time_p99_us"`
		}
		by := map[string]*agg{}
		for _, g := range asAny(jget(data, "viewer.accounts.0.w")) {
			name := jstr(g, "dimensions.scriptName")
			a := by[name]
			if a == nil {
				a = &agg{Script: name}
				by[name] = a
			}
			a.Requests += num(g, "sum.requests")
			a.Errors += num(g, "sum.errors")
			a.Subrequests += num(g, "sum.subrequests")
			if p := num(g, "quantiles.cpuTimeP50"); p > a.CPUP50 {
				a.CPUP50 = p
			}
			if p := num(g, "quantiles.cpuTimeP99"); p > a.CPUP99 {
				a.CPUP99 = p
			}
		}
		out := []agg{}
		for _, a := range by {
			out = append(out, *a)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Requests > out[j].Requests })
		if jsonOutput {
			return printJSONValue(out)
		}
		var items []any
		for _, a := range out {
			items = append(items, map[string]any{"script": a.Script, "requests": humanCount(a.Requests), "errors": humanCount(a.Errors), "rate": pct(a.Errors, a.Requests), "sub": humanCount(a.Subrequests), "p50": fmt.Sprintf("%.1f ms", a.CPUP50/1000), "p99": fmt.Sprintf("%.1f ms", a.CPUP99/1000)})
		}
		printTable("Workers since "+since.Format("2006-01-02 15:04"), items, []col{{"Script", "script"}, {"Requests", "requests"}, {"Errors", "errors"}, {"Error rate", "rate"}, {"Subrequests", "sub"}, {"CPU p50 (max)", "p50"}, {"CPU p99 (max)", "p99"}})
		return nil
	},
}

// analyticsR2Class maps an R2 action to its billing class.
func analyticsR2Class(action string) string {
	switch action {
	case "DeleteObject", "DeleteObjects", "DeleteBucket", "AbortMultipartUpload":
		return "free"
	case "HeadBucket", "HeadObject", "GetObject", "UsageSummary", "GetBucketEncryption", "GetBucketLocation", "GetBucketCors", "GetBucketLifecycleConfiguration":
		return "B"
	}
	return "A"
}

var analyticsR2Cmd = &cobra.Command{
	Use:   "r2",
	Short: "R2 storage and operations (Class A/B) per bucket",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		since, until, err := timeRange(cmd)
		if err != nil {
			return err
		}
		s, err := adminSession(cmd, "")
		if err != nil {
			return err
		}
		acct, err := s.account(ctx)
		if err != nil {
			return err
		}
		opsFilter := map[string]any{"datetime_geq": since.Format(time.RFC3339), "datetime_lt": until.Format(time.RFC3339)}
		// Storage: the latest sample per bucket in the last day of the window.
		stFrom := until.Add(-24 * time.Hour)
		if stFrom.Before(since) {
			stFrom = since
		}
		stFilter := map[string]any{"datetime_geq": stFrom.Format(time.RFC3339), "datetime_lt": until.Format(time.RFC3339)}
		if b, _ := cmd.Flags().GetString("bucket"); b != "" {
			opsFilter["bucketName"] = b
			stFilter["bucketName"] = b
		}
		q := `query($acct: String!, $ops: AccountR2OperationsAdaptiveGroupsFilter_InputObject, $st: AccountR2StorageAdaptiveGroupsFilter_InputObject) { viewer { accounts(filter: {accountTag: $acct}) {
  ops: r2OperationsAdaptiveGroups(limit: 10000, filter: $ops) { sum { requests responseObjectSize } dimensions { bucketName actionType } }
  st: r2StorageAdaptiveGroups(limit: 10000, orderBy: [datetime_DESC], filter: $st) { max { payloadSize metadataSize objectCount uploadCount } dimensions { bucketName datetime } } } } }`
		data, err := gql(ctx, s, q, map[string]any{"acct": acct, "ops": opsFilter, "st": stFilter})
		if err != nil {
			return err
		}
		type bucket struct {
			Name       string  `json:"bucket"`
			ClassA     float64 `json:"class_a_ops"`
			ClassB     float64 `json:"class_b_ops"`
			Free       float64 `json:"free_ops"`
			Egress     float64 `json:"response_bytes"`
			Storage    float64 `json:"storage_bytes"`
			Objects    float64 `json:"objects"`
			StorageAt  string  `json:"storage_at,omitempty"`
			MetaBytes  float64 `json:"metadata_bytes"`
			Multiparts float64 `json:"pending_uploads"`
		}
		by := map[string]*bucket{}
		get := func(n string) *bucket {
			if by[n] == nil {
				by[n] = &bucket{Name: n}
			}
			return by[n]
		}
		for _, g := range asAny(jget(data, "viewer.accounts.0.ops")) {
			b := get(jstr(g, "dimensions.bucketName"))
			n := num(g, "sum.requests")
			switch analyticsR2Class(jstr(g, "dimensions.actionType")) {
			case "A":
				b.ClassA += n
			case "B":
				b.ClassB += n
			default:
				b.Free += n
			}
			b.Egress += num(g, "sum.responseObjectSize")
		}
		for _, g := range asAny(jget(data, "viewer.accounts.0.st")) {
			b := get(jstr(g, "dimensions.bucketName"))
			if b.StorageAt != "" {
				continue // ordered newest first: keep the latest sample
			}
			b.StorageAt = jstr(g, "dimensions.datetime")
			b.Storage = num(g, "max.payloadSize")
			b.MetaBytes = num(g, "max.metadataSize")
			b.Objects = num(g, "max.objectCount")
			b.Multiparts = num(g, "max.uploadCount")
		}
		out := []bucket{}
		for _, b := range by {
			out = append(out, *b)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		if jsonOutput {
			return printJSONValue(out)
		}
		var items []any
		var ta, tb, ts float64
		for _, b := range out {
			ta += b.ClassA
			tb += b.ClassB
			ts += b.Storage
			name := b.Name
			if name == "" {
				name = "(account-level)"
			}
			items = append(items, map[string]any{"bucket": name, "storage": humanBytes(b.Storage), "objects": humanCount(b.Objects), "a": humanCount(b.ClassA), "b": humanCount(b.ClassB), "free": humanCount(b.Free), "egress": humanBytes(b.Egress)})
		}
		printTable("R2 since "+since.Format("2006-01-02 15:04"), items, []col{{"Bucket", "bucket"}, {"Storage", "storage"}, {"Objects", "objects"}, {"Class A", "a"}, {"Class B", "b"}, {"Free ops", "free"}, {"Egress", "egress"}})
		fmt.Fprintf(os.Stdout, "\n  Total: %s stored, %s Class A, %s Class B operations\n", humanBytes(ts), humanCount(ta), humanCount(tb))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(analyticsCmd)
	analyticsCmd.AddCommand(analyticsZoneCmd, analyticsPathsCmd, analyticsCountriesCmd, analyticsFirewallCmd, analyticsWorkersCmd, analyticsR2Cmd)
	for _, c := range analyticsCmd.Commands() {
		c.Flags().String("since", "24h", "Start: 30m, 24h, 3d, 7d, 30d, YYYY-MM-DD, or RFC 3339")
		c.Flags().String("until", "", "End (default: now), same formats")
	}
	for _, c := range []*cobra.Command{analyticsZoneCmd, analyticsPathsCmd, analyticsCountriesCmd, analyticsFirewallCmd} {
		c.Flags().Int("limit", 10, "How many top entries to show")
	}
	analyticsPathsCmd.Flags().Int("status", 0, "Only requests with this edge status code (e.g. 404)")
	analyticsPathsCmd.Flags().String("host", "", "Only requests for this hostname")
	analyticsWorkersCmd.Flags().String("script", "", "Only this Worker script")
	analyticsR2Cmd.Flags().String("bucket", "", "Only this bucket")
}
