package cmd

import (
	"fmt"
	"sort"
	"time"

	"github.com/dorkitude/cfctl/internal/r2"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

// r2BucketUsage is one bucket's row in `r2 usage`.
type r2BucketUsage struct {
	Bucket       string             `json:"bucket"`
	Objects      float64            `json:"objects"`
	Bytes        float64            `json:"bytes"`
	ByClass      map[string]float64 `json:"bytes_by_class,omitempty"`
	ClassAThis   float64            `json:"class_a_this_month"`
	ClassBThis   float64            `json:"class_b_this_month"`
	ClassALast   float64            `json:"class_a_last_month"`
	ClassBLast   float64            `json:"class_b_last_month"`
	FreeOpsThis  float64            `json:"free_ops_this_month"`
	FreeOpsLast  float64            `json:"free_ops_last_month"`
	SizeSource   string             `json:"size_source"`
	StorageAsOf  string             `json:"storage_as_of,omitempty"`
	ScannedExact bool               `json:"-"`
}

// r2PeriodCost is an account-level cost estimate for one period.
type r2PeriodCost struct {
	Label   string              `json:"label"`
	Start   string              `json:"start"`
	End     string              `json:"end"`
	Usage   map[string]r2.Usage `json:"usage"` // by storage class
	Costs   map[string]r2.Cost  `json:"costs"`
	Total   float64             `json:"total_usd"`
	Partial bool                `json:"partial,omitempty"`
}

type r2OpsRow struct {
	Sum struct {
		Requests           float64 `json:"requests"`
		ResponseObjectSize float64 `json:"responseObjectSize"`
	} `json:"sum"`
	Dim struct {
		Action string `json:"actionType"`
		Bucket string `json:"bucketName"`
		Class  string `json:"storageClass"`
	} `json:"dimensions"`
}

type r2StorageDay struct {
	Max struct {
		PayloadSize float64 `json:"payloadSize"`
	} `json:"max"`
	Dim struct {
		Bucket string `json:"bucketName"`
		Class  string `json:"storageClass"`
		Date   string `json:"date"`
	} `json:"dimensions"`
}

// r2Ops returns operation counts in [start, end).
func r2Ops(c *stClient, start, end time.Time) ([]r2OpsRow, error) {
	q := `query($acct: String!, $start: Time!, $end: Time!) {
  viewer { accounts(filter: {accountTag: $acct}) {
    r2OperationsAdaptiveGroups(limit: 10000, filter: {datetime_geq: $start, datetime_lt: $end}) {
      sum { requests responseObjectSize }
      dimensions { actionType bucketName storageClass }
    } } } }`
	var data struct {
		Viewer struct {
			Accounts []struct {
				Rows []r2OpsRow `json:"r2OperationsAdaptiveGroups"`
			} `json:"accounts"`
		} `json:"viewer"`
	}
	err := c.graphql(q, map[string]any{"acct": c.acct, "start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339)}, &data)
	if err != nil {
		return nil, err
	}
	var out []r2OpsRow
	for _, a := range data.Viewer.Accounts {
		out = append(out, a.Rows...)
	}
	return out, nil
}

// r2StorageDays returns the daily max stored bytes per bucket and class for
// dates in [start, end).
func r2StorageDays(c *stClient, start, end time.Time) ([]r2StorageDay, error) {
	q := `query($acct: String!, $start: Date!, $end: Date!) {
  viewer { accounts(filter: {accountTag: $acct}) {
    r2StorageAdaptiveGroups(limit: 10000, filter: {date_geq: $start, date_lt: $end}) {
      max { payloadSize }
      dimensions { bucketName storageClass date }
    } } } }`
	var data struct {
		Viewer struct {
			Accounts []struct {
				Rows []r2StorageDay `json:"r2StorageAdaptiveGroups"`
			} `json:"accounts"`
		} `json:"viewer"`
	}
	err := c.graphql(q, map[string]any{"acct": c.acct, "start": start.Format("2006-01-02"), "end": end.Format("2006-01-02")}, &data)
	if err != nil {
		return nil, err
	}
	var out []r2StorageDay
	for _, a := range data.Viewer.Accounts {
		out = append(out, a.Rows...)
	}
	return out, nil
}

func r2Class(c string) string {
	if c == r2.InfrequentAccess {
		return c
	}
	return r2.Standard
}

// r2PeriodUsage sums one period's usage by storage class. Storage is the
// sum of daily maxima divided by the days in the month (GB-months accrued);
// with project=true, the observed daily average is assumed for the whole
// month and requests are scaled up to the full month.
func r2PeriodUsage(ops []r2OpsRow, days []r2StorageDay, monthDays, elapsedDays float64, project bool) map[string]r2.Usage {
	out := map[string]r2.Usage{r2.Standard: {}, r2.InfrequentAccess: {}}
	scale := 1.0
	if project && elapsedDays > 0 {
		scale = monthDays / elapsedDays
	}
	for _, o := range ops {
		cl := r2Class(o.Dim.Class)
		u := out[cl]
		switch r2.OpClass(o.Dim.Action) {
		case r2.ClassA:
			u.ClassA += o.Sum.Requests * scale
		case r2.ClassB:
			u.ClassB += o.Sum.Requests * scale
		}
		if cl == r2.InfrequentAccess && o.Dim.Action == "GetObject" {
			u.RetrievedBytes += o.Sum.ResponseObjectSize * scale
		}
		out[cl] = u
	}
	sum := map[string]float64{}
	dates := map[string]bool{}
	for _, d := range days {
		sum[r2Class(d.Dim.Class)] += d.Max.PayloadSize
		dates[d.Dim.Date] = true
	}
	for cl, s := range sum {
		u := out[cl]
		if project && len(dates) > 0 {
			u.StorageBytes = s / float64(len(dates)) // observed daily average, held for the month
		} else {
			u.StorageBytes = s / monthDays // accrued GB-months
		}
		out[cl] = u
	}
	return out
}

func r2PriceUsage(label string, start, end time.Time, usage map[string]r2.Usage, partial bool) r2PeriodCost {
	pc := r2PeriodCost{Label: label, Start: start.Format("2006-01-02"), End: end.Format("2006-01-02"), Usage: usage, Costs: map[string]r2.Cost{}, Partial: partial}
	for cl, u := range usage {
		cost := r2.Estimate(cl, u)
		pc.Costs[cl] = cost
		pc.Total += cost.Total
	}
	return pc
}

var r2UsageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Per-bucket objects, size, and Class A/B operations, with an estimated bill",
	Long: `Show every bucket's object count and size, Class A and Class B operations
this month and last month, and an estimated R2 bill.

Sizes come from the GraphQL Analytics API (r2StorageAdaptiveGroups; updated
every few minutes); --scan lists every object instead for exact numbers.
Operations come from r2OperationsAdaptiveGroups.

Pricing (` + r2.PricingSource + `):
  Standard:          $0.015/GB-month (10 GB free), Class A $4.50/M (1M free), Class B $0.36/M (10M free)
  Infrequent Access: $0.01/GB-month, Class A $9.00/M, Class B $0.90/M, retrieval $0.01/GB (no free tier)
  Egress is free. DeleteObject/DeleteBucket/AbortMultipartUpload are free.
  Class A: PutObject, CopyObject, multipart, List*, PutBucket*, lifecycle transitions; everything else is Class B.
  Partial units round up (1 request over the free tier bills a full million).

Storage is billed on the average stored over the month: "last month" uses the
daily maxima averaged over the month; "this month (projected)" assumes the
current average for the whole month and scales requests so far to 30/31 days.
Estimates only; your invoice is authoritative.

Examples:
  cfctl r2 usage
  cfctl r2 usage --scan
  cfctl r2 usage --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		scan, _ := cmd.Flags().GetBool("scan")
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if v, _ := cmd.Flags().GetString("now"); v != "" {
			if now, err = stParseTime(v); err != nil {
				return err
			}
		}
		som := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		prev := som.AddDate(0, -1, 0)
		nextMonth := som.AddDate(0, 1, 0)
		thisDays := nextMonth.Sub(som).Hours() / 24
		lastDays := som.Sub(prev).Hours() / 24
		elapsed := now.Sub(som).Hours() / 24
		if elapsed < 1.0/24 {
			elapsed = 1.0 / 24
		}

		buckets, _, err := r2ListBuckets(c, cmd)
		if err != nil {
			return err
		}
		opsThis, err := r2Ops(c, som, now)
		if err != nil {
			return err
		}
		opsLast, err := r2Ops(c, prev, som)
		if err != nil {
			return err
		}
		daysThis, err := r2StorageDays(c, som, now.AddDate(0, 0, 1))
		if err != nil {
			return err
		}
		daysLast, err := r2StorageDays(c, prev, som)
		if err != nil {
			return err
		}
		current, err := r2StorageNow(c, "")
		if err != nil {
			return err
		}

		rows := map[string]*r2BucketUsage{}
		var order []string
		get := func(name string) *r2BucketUsage {
			if rows[name] == nil {
				rows[name] = &r2BucketUsage{Bucket: name, ByClass: map[string]float64{}, SizeSource: "graphql"}
				order = append(order, name)
			}
			return rows[name]
		}
		for _, b := range buckets {
			get(b.Name)
		}
		// Account-level operations (ListBuckets, ...) have no bucket.
		const acctRow = "(account)"
		for _, s := range current {
			r := get(s.Bucket)
			r.Objects += s.Objects
			r.Bytes += s.PayloadBytes
			if s.PayloadBytes > 0 {
				r.ByClass[s.StorageClass] += s.PayloadBytes
			}
			if s.AsOf > r.StorageAsOf {
				r.StorageAsOf = s.AsOf
			}
		}
		bucketOf := func(name string) *r2BucketUsage {
			if name == "" {
				name = acctRow
			}
			return get(name)
		}
		for _, o := range opsThis {
			r := bucketOf(o.Dim.Bucket)
			switch r2.OpClass(o.Dim.Action) {
			case r2.ClassA:
				r.ClassAThis += o.Sum.Requests
			case r2.ClassB:
				r.ClassBThis += o.Sum.Requests
			default:
				r.FreeOpsThis += o.Sum.Requests
			}
		}
		for _, o := range opsLast {
			r := bucketOf(o.Dim.Bucket)
			switch r2.OpClass(o.Dim.Action) {
			case r2.ClassA:
				r.ClassALast += o.Sum.Requests
			case r2.ClassB:
				r.ClassBLast += o.Sum.Requests
			default:
				r.FreeOpsLast += o.Sum.Requests
			}
		}
		if scan {
			for _, name := range order {
				if name == acctRow {
					continue
				}
				r := rows[name]
				res, err := r2.List(c.ctx, c.c, c.acct, name, r2.ListOptions{Jurisdiction: r2Jurisdiction(cmd)}, nil)
				if err != nil {
					return fmt.Errorf("listing %s: %w", name, err)
				}
				r.Objects, r.Bytes, r.ByClass, r.SizeSource = 0, 0, map[string]float64{}, "scan"
				for _, o := range res.Objects {
					r.Objects++
					r.Bytes += float64(o.Size)
					r.ByClass[r2Class(o.StorageClass)] += float64(o.Size)
				}
			}
		}
		sort.Strings(order)

		last := r2PriceUsage(prev.Format("January 2006"), prev, som, r2PeriodUsage(opsLast, daysLast, lastDays, lastDays, false), false)
		toDate := r2PriceUsage(som.Format("January 2006")+" to date", som, now, r2PeriodUsage(opsThis, daysThis, thisDays, elapsed, false), true)
		projected := r2PriceUsage(som.Format("January 2006")+" (projected)", som, nextMonth, r2PeriodUsage(opsThis, daysThis, thisDays, elapsed, true), true)

		list := make([]r2BucketUsage, 0, len(order))
		var totObjects, totBytes, totAThis, totBThis, totALast, totBLast float64
		for _, name := range order {
			r := rows[name]
			list = append(list, *r)
			totObjects += r.Objects
			totBytes += r.Bytes
			totAThis += r.ClassAThis
			totBThis += r.ClassBThis
			totALast += r.ClassALast
			totBLast += r.ClassBLast
		}
		out := map[string]any{
			"as_of":          now.Format(time.RFC3339),
			"buckets":        list,
			"estimates":      []r2PeriodCost{last, toDate, projected},
			"prices":         r2.Prices,
			"pricing_source": r2.PricingSource,
		}
		return stEmitValue(out, func() error {
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🪣 R2 usage (%d buckets)", len(list))))
			trows := [][]string{}
			for _, r := range list {
				size := r2.HumanBytes(r.Bytes)
				if ia := r.ByClass[r2.InfrequentAccess]; ia > 0 {
					size += fmt.Sprintf(" (IA %s)", r2.HumanBytes(ia))
				}
				trows = append(trows, []string{r.Bucket, r2.HumanCount(r.Objects), size,
					r2.HumanCount(r.ClassAThis), r2.HumanCount(r.ClassBThis), r2.HumanCount(r.ClassALast), r2.HumanCount(r.ClassBLast)})
			}
			trows = append(trows, []string{"TOTAL", r2.HumanCount(totObjects), r2.HumanBytes(totBytes),
				r2.HumanCount(totAThis), r2.HumanCount(totBThis), r2.HumanCount(totALast), r2.HumanCount(totBLast)})
			stTable([]string{"BUCKET", "OBJECTS", "SIZE", "A THIS MO", "B THIS MO", "A LAST MO", "B LAST MO"}, trows)
			src := "GraphQL analytics"
			if scan {
				src = "full listing (--scan)"
			}
			fmt.Println(ui.SubtleStyle.Render("  sizes from " + src + "; operations from r2OperationsAdaptiveGroups"))
			fmt.Println()
			fmt.Println(ui.TitleStyle.Render("💵 Estimated cost"))
			crows := [][]string{}
			for _, p := range []r2PeriodCost{last, toDate, projected} {
				var gbmo, a, b float64
				for _, u := range p.Usage {
					gbmo += u.StorageBytes / r2.GB
					a += u.ClassA
					b += u.ClassB
				}
				crows = append(crows, []string{p.Label, fmt.Sprintf("%.3f", gbmo), r2.HumanCount(a), r2.HumanCount(b), fmt.Sprintf("$%.2f", p.Total)})
			}
			stTable([]string{"PERIOD", "GB-MONTHS", "CLASS A", "CLASS B", "ESTIMATE"}, crows)
			fmt.Println(ui.SubtleStyle.Render("  free tier applied (Standard: 10 GB-mo, 1M A, 10M B); egress free; prices: " + r2.PricingSource))
			return nil
		})
	},
}

func init() {
	r2UsageCmd.Flags().Bool("scan", false, "Count objects and bytes by listing every object (exact, slower)")
	r2UsageCmd.Flags().String("now", "", "Treat this time as now (for reproducible reports)")
	_ = r2UsageCmd.Flags().MarkHidden("now")
	r2Cmd.AddCommand(r2UsageCmd)
}
