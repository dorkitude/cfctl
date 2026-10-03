package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/apispec"
	"github.com/spf13/cobra"
)

// `cfctl api smoke` is a hidden maintainer tool: a live, read-only sweep of
// the generated GET operations that need no IDs beyond the account and zone.
// It always forces read-only mode (in this process and in every child), so it
// can't change anything in the account. See docs/smoke/README.md.

var apiSmokeCmd = &cobra.Command{
	Use:    "smoke",
	Short:  "Live read-only sweep of generated GET operations (maintainers)",
	Hidden: true,
	Args:   cobra.NoArgs,
	Long: `Run every generated GET operation whose only path parameters are
{account_id} and/or {zone_id} (plus those with no path parameters, like
/user/...) against the real account, through the real CLI (one child
'cfctl api <tag> <op>' per operation), and record the HTTP status and outcome
of each in a TSV file.

Read-only mode is forced: CFCTL_READONLY=1 is set for this process and every
child, so a non-GET request can't be sent.

Outcomes:
  ok            2xx, exit 0, output well formed
  expected-4xx  401/403/404/400... with a friendly error (product not enabled,
                token type, plan, missing setup)
  rate-limited  429 after cfctl's retries
  5xx           Cloudflare server error (after retries)
  client-bug    cfctl failed: panic, decode error, 2xx with a non-zero exit,
                malformed --json output, raw JSON in an error, request refused
  skipped       a required parameter can't be satisfied from the spec`,
	RunE: runAPISmoke,
}

func init() {
	apiSmokeCmd.Flags().String("out", "docs/smoke/generated-get-sweep.tsv", "Write results here (TSV)")
	apiSmokeCmd.Flags().Float64("rate", 2.5, "Maximum operations per second")
	apiSmokeCmd.Flags().String("filter", "", "Only operations whose operationId, tag, or path matches this regexp")
	apiSmokeCmd.Flags().Int("limit", 0, "Stop after this many operations (0: all)")
	apiSmokeCmd.Flags().Bool("dry-run", false, "Print the plan (commands and skips) without calling the API")
	apiSmokeCmd.Flags().Bool("check-all", true, "Re-run paginated 2xx operations with --all --max-pages 3")
	apiCmd.AddCommand(apiSmokeCmd)
}

// smokeAccountAliases are path params that name the account under another name;
// the sweep fills them with the account ID (generated commands take them as
// positional arguments).
var smokeAccountAliases = map[string]bool{"account_identifier": true, "accountId": true, "account_tag": true, "accountID": true}
var smokeZoneAliases = map[string]bool{"zone": true, "zoneId": true, "zone_tag": true, "zoneID": true}

// smokePlan is how one operation is called (or why it's skipped).
type smokePlan struct {
	op       *apispec.Op
	args     []string // after "api <tag> <op>"
	zone     bool
	skip     string
	paginate bool
}

// smokeQueryValue picks a value for a required query parameter: its enum's
// first value, its default, or a value for well-known names.
func smokeQueryValue(p apispec.Param, env smokeEnv) (string, bool) {
	if len(p.Enum) > 0 {
		return p.Enum[0], true
	}
	if p.Default != "" {
		return p.Default, true
	}
	name := strings.ToLower(p.Name)
	day := env.now.UTC().Truncate(time.Hour)
	switch {
	case name == "account_tag" || name == "account_id" || name == "accountid":
		return env.accountID, true
	case name == "zone_id" || name == "zone_tag" || name == "zoneid":
		return env.zoneID, true
	case name == "host" || name == "hostname":
		return env.zoneName, true
	case name == "url":
		return "https://" + env.zoneName + "/", true
	case name == "ip":
		return "1.1.1.1", true
	case name == "model":
		return "@cf/meta/llama-3.1-8b-instruct", true
	case name == "q":
		return "cfctl", true
	case name == "before":
		return day.Format(time.RFC3339), true
	case name == "since" || name == "start" || name == "from" || name == "date_from" || name == "start_date" ||
		name == "datetime_start" || name == "starttime" || name == "start_time":
		return day.Add(-24 * time.Hour).Format(time.RFC3339), true
	case name == "until" || name == "end" || name == "to" || name == "date_to" || name == "end_date" ||
		name == "datetime_end" || name == "endtime" || name == "end_time":
		return day.Format(time.RFC3339), true
	case name == "page":
		return "1", true
	case name == "per_page" || name == "limit":
		return "10", true
	}
	if p.Type == "boolean" {
		return "false", true
	}
	return "", false
}

// smokeEnv is what the sweep knows for filling parameters.
type smokeEnv struct {
	accountID, zoneID, zoneName string
	now                         time.Time
}

func planSmoke(o *apispec.Op, env smokeEnv) (smokePlan, bool) {
	pl := smokePlan{op: o}
	if o.Method != "GET" {
		return pl, false
	}
	for _, p := range o.PathParams() {
		switch {
		case isAccountParam(p.Name):
		case isZoneParam(p.Name):
			pl.zone = true
		case smokeAccountAliases[p.Name]:
			pl.args = append(pl.args, env.accountID)
		default:
			return pl, false // needs an ID we don't have
		}
	}
	qflags, unmapped := queryFlags(o)
	for _, qf := range qflags {
		switch qf.param.Name {
		case "page", "cursor", "per_page", "page_size", "pageSize", "page_token":
			pl.paginate = true
		}
		if !qf.param.Required {
			// Radar answers 400 "send either range or start & end dates"
			// without one, although the spec marks dateRange optional.
			if qf.param.Name == "dateRange" {
				v := "1d"
				if len(qf.param.Enum) > 0 && !contains(qf.param.Enum, v) {
					v = qf.param.Enum[0]
				}
				pl.args = append(pl.args, "--"+qf.flag+"="+v)
			}
			continue
		}
		v, ok := smokeQueryValue(qf.param, env)
		if !ok {
			pl.skip = fmt.Sprintf("required query param %s has no enum/default", qf.param.Name)
			return pl, true
		}
		pl.args = append(pl.args, "--"+qf.flag+"="+v)
	}
	for _, p := range unmapped {
		if p.Required {
			v, ok := smokeQueryValue(p, env)
			if !ok {
				pl.skip = fmt.Sprintf("required query param %s has no enum/default", p.Name)
				return pl, true
			}
			pl.args = append(pl.args, "--query", p.Name+"="+v)
		}
	}
	for _, p := range o.HeaderParams() {
		if p.Required {
			pl.skip = fmt.Sprintf("required header %s", p.Name)
			return pl, true
		}
	}
	if o.BodyRequired {
		pl.skip = "GET with a required body"
		return pl, true
	}
	return pl, true
}

var (
	smokeDebugRE = regexp.MustCompile(`(?m)^debug: (\S+) (\S+) -> (\d{3}|refused|[^(]+?) \(`)
	smokePanicRE = regexp.MustCompile(`(?m)^panic:|goroutine \d+ \[|runtime error:`)
)

// smokeResult is the outcome of one child run.
type smokeResult struct {
	status  string // HTTP status of the last request, or "-"
	outcome string
	detail  string
}

// classifySmoke turns a child run's exit code and output into an outcome.
func classifySmoke(exit int, stdout, stderr []byte, wantPath string) smokeResult {
	r := smokeResult{status: "-"}
	ms := smokeDebugRE.FindAllSubmatch(stderr, -1)
	// The status of the operation's own request (the last one to its path;
	// zone lookups go to /zones first).
	for _, m := range ms {
		r.status = string(m[3])
	}
	for _, m := range ms {
		if strings.HasSuffix(string(m[2]), wantPath) || wantPath == "" {
			r.status = string(m[3])
		}
	}
	msg := firstErrLine(stderr)
	switch {
	case smokePanicRE.Match(stderr) || smokePanicRE.Match(stdout):
		r.outcome, r.detail = "client-bug", "panic"
	case exit == -1:
		r.outcome, r.detail = "client-bug", "timeout"
	case strings.HasPrefix(r.status, "refused") || bytes.Contains(stderr, []byte("read-only mode")):
		r.outcome, r.detail = "client-bug", "GET refused by the read-only guard"
	case r.status == "-":
		if exit == 0 {
			r.outcome, r.detail = "client-bug", "no request sent"
		} else {
			r.outcome, r.detail = "client-bug", "failed before sending: "+msg
		}
	case strings.HasPrefix(r.status, "2"):
		trimmed := bytes.TrimSpace(stdout)
		switch {
		case exit != 0 && strings.HasPrefix(msg, "API error:"):
			// HTTP 200 with success:false in the envelope: an API-side
			// failure, reported as one.
			r.outcome, r.detail = "expected-4xx", msg
		case exit != 0:
			r.outcome, r.detail = "client-bug", "2xx but exit "+strconv.Itoa(exit)+": "+msg
		case len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && !json.Valid(trimmed) && !isNDJSON(trimmed):
			r.outcome, r.detail = "client-bug", "output looks like JSON but doesn't parse"
		default:
			r.outcome = "ok"
		}
	case r.status == "429":
		r.outcome, r.detail = "rate-limited", msg
	case strings.HasPrefix(r.status, "5"):
		r.outcome, r.detail = "5xx", msg
	case strings.HasPrefix(r.status, "4"):
		switch {
		case exit == 0:
			r.outcome, r.detail = "client-bug", "4xx but exit 0"
		case msg == "":
			r.outcome, r.detail = "client-bug", "4xx with no error message"
		case strings.Contains(msg, `{"`) || strings.Contains(msg, `"errors"`) || strings.HasSuffix(msg, "{") || strings.HasSuffix(msg, "["):
			r.outcome, r.detail = "client-bug", "raw JSON in error: "+msg
		default:
			r.outcome, r.detail = "expected-4xx", msg
		}
	default:
		r.outcome, r.detail = "client-bug", "unexpected status "+r.status+": "+msg
	}
	if len(r.detail) > 160 {
		r.detail = r.detail[:157] + "..."
	}
	r.detail = strings.NewReplacer("\t", " ", "\n", " ").Replace(r.detail)
	return r
}

// isNDJSON reports whether b is newline-delimited JSON values.
func isNDJSON(b []byte) bool {
	lines := bytes.Split(b, []byte("\n"))
	if len(lines) < 2 {
		return false
	}
	for _, l := range lines {
		if l = bytes.TrimSpace(l); len(l) > 0 && !json.Valid(l) {
			return false
		}
	}
	return true
}

// firstErrLine returns the first non-debug stderr line (the error message).
func firstErrLine(stderr []byte) string {
	for _, l := range strings.Split(string(stderr), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "debug:") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(l, "✗"))
	}
	return ""
}

func runAPISmoke(cmd *cobra.Command, args []string) error {
	// Force read-only everywhere: this process and every child.
	api.SetReadOnly(true)
	if err := os.Setenv("CFCTL_READONLY", "1"); err != nil {
		return err
	}
	if !api.ReadOnly() {
		return fmt.Errorf("refusing to run: read-only mode could not be enabled")
	}

	outPath, _ := cmd.Flags().GetString("out")
	rate, _ := cmd.Flags().GetFloat64("rate")
	filter, _ := cmd.Flags().GetString("filter")
	limit, _ := cmd.Flags().GetInt("limit")
	dry, _ := cmd.Flags().GetBool("dry-run")
	checkAll, _ := cmd.Flags().GetBool("check-all")
	var filterRE *regexp.Regexp
	if filter != "" {
		var err error
		if filterRE, err = regexp.Compile(filter); err != nil {
			return fmt.Errorf("invalid --filter: %w", err)
		}
	}
	if rate <= 0 {
		rate = 2.5
	}

	ctx := context.Background()
	accountID := "{account_id}"
	zoneID := ""
	zoneArg, _ := cmd.Flags().GetString("zone")
	if !dry {
		s, err := newAPISession(cmd)
		if err != nil {
			return err
		}
		if accountID, err = s.account(ctx); err != nil {
			return err
		}
		if zoneArg == "" {
			return fmt.Errorf("pass --zone <name-or-id>: the zone to run zone-level operations against")
		}
		if zoneID, err = s.zone(ctx); err != nil {
			return err
		}
	}

	env := smokeEnv{accountID: accountID, zoneID: zoneID, zoneName: zoneArg, now: time.Now()}
	if env.zoneID == "" {
		env.zoneID = "{zone_id}"
	}
	var plans []smokePlan
	all := apispec.Ops()
	for i := range all {
		o := &all[i]
		if filterRE != nil && !filterRE.MatchString(o.ID+" "+o.Tag+" "+o.Path) {
			continue
		}
		if pl, ok := planSmoke(o, env); ok {
			plans = append(plans, pl)
		}
	}
	sort.SliceStable(plans, func(i, j int) bool { return plans[i].op.Path < plans[j].op.Path })
	if limit > 0 && len(plans) > limit {
		plans = plans[:limit]
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}

	if dry {
		for _, pl := range plans {
			if pl.skip != "" {
				fmt.Printf("SKIP  %s  %s (%s)\n", pl.op.ID, pl.op.Path, pl.skip)
				continue
			}
			fmt.Printf("RUN   %s\n", strings.Join(smokeArgv(pl, "<zone>"), " "))
		}
		return nil
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "operationId\ttag\tpath\tstatus\toutcome\tall_check\tdetail")

	counts := map[string]int{}
	allCounts := map[string]int{}
	interval := time.Duration(float64(time.Second) / rate)
	last := time.Time{}
	pace := func() {
		if wait := interval - time.Since(last); wait > 0 {
			time.Sleep(wait)
		}
		last = time.Now()
	}
	for i, pl := range plans {
		o := pl.op
		var r smokeResult
		allCheck := "-"
		if pl.skip != "" {
			r = smokeResult{status: "-", outcome: "skipped", detail: pl.skip}
		} else {
			pace()
			argv := smokeArgv(pl, zoneID)
			exit, stdout, stderr := runSmokeChild(self, argv)
			wantPath := smokeWantPath(o.Path)
			r = classifySmoke(exit, stdout, stderr, wantPath)
			if r.outcome == "ok" && pl.paginate && checkAll {
				pace()
				exit, stdout, stderr = runSmokeChild(self, append(argv, "--all", "--max-pages", "3"))
				ar := classifySmoke(exit, stdout, stderr, wantPath)
				allCheck = ar.outcome
				if ar.outcome != "ok" {
					allCheck = ar.outcome + ": " + ar.detail
				}
				allCounts[ar.outcome]++
			}
		}
		counts[r.outcome]++
		fmt.Fprintf(f, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", o.ID, o.Tag, o.Path, r.status, r.outcome, allCheck, r.detail)
		fmt.Fprintf(os.Stderr, "[%d/%d] %-12s %-4s %s %s\n", i+1, len(plans), r.outcome, r.status, o.ID, o.Path)
	}

	ran := len(plans) - counts["skipped"]
	fmt.Printf("\nGenerated GET sweep: %d operations (%d ran, %d skipped), zone %s\n", len(plans), ran, counts["skipped"], zoneArg)
	for _, k := range []string{"ok", "expected-4xx", "rate-limited", "5xx", "client-bug", "skipped"} {
		fmt.Printf("  %-13s %d\n", k, counts[k])
	}
	if len(allCounts) > 0 {
		fmt.Printf("--all re-runs on paginated ops: ")
		var parts []string
		for k, v := range allCounts {
			parts = append(parts, fmt.Sprintf("%s %d", k, v))
		}
		sort.Strings(parts)
		fmt.Println(strings.Join(parts, ", "))
	}
	fmt.Printf("Results: %s\n", outPath)
	if counts["client-bug"] > 0 || allCounts["client-bug"] > 0 {
		return fmt.Errorf("%d client-side failures", counts["client-bug"]+allCounts["client-bug"])
	}
	return nil
}

// smokeArgv is the child's argv: api <tag> <op> [args] [--zone Z] --debug.
func smokeArgv(pl smokePlan, zone string) []string {
	argv := []string{"api", pl.op.TagSlug, pl.op.Slug}
	argv = append(argv, pl.args...)
	if pl.zone {
		argv = append(argv, "--zone", zone)
	}
	return append(argv, "--debug")
}

// smokeWantPath is the literal tail of the op's path after its last
// placeholder, used to pick the op's own request out of the debug log.
func smokeWantPath(p string) string {
	if i := strings.LastIndex(p, "}"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func runSmokeChild(self string, argv []string) (int, []byte, []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, self, argv...)
	c.Env = append(os.Environ(), "CFCTL_READONLY=1", "NO_COLOR=1")
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	c.Stdin = nil
	err := c.Run()
	if ctx.Err() != nil {
		return -1, out.Bytes(), errb.Bytes()
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), out.Bytes(), errb.Bytes()
		}
		return 1, out.Bytes(), append(errb.Bytes(), []byte(err.Error())...)
	}
	return 0, out.Bytes(), errb.Bytes()
}
