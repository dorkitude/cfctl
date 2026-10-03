package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/config"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/dorkitude/cfctl/internal/workers"
	"github.com/spf13/cobra"
)

// bindingFlagKinds are the shorthand binding flags shared by deploy,
// versions upload and preview deploy.
var bindingFlagKinds = []struct{ name, usage string }{
	{"var", "Plain-text variable NAME=value (repeatable)"},
	{"kv", "KV namespace binding NAME=namespace_id (repeatable)"},
	{"r2", "R2 bucket binding NAME=bucket (repeatable)"},
	{"d1", "D1 database binding NAME=database_id (repeatable)"},
	{"service", "Service binding NAME=worker[#entrypoint] (repeatable)"},
	{"queue", "Queue producer binding NAME=queue (repeatable)"},
	{"ai", "Workers AI binding NAME (repeatable)"},
	{"binding", `Any binding as JSON, e.g. '{"type":"browser","name":"B"}' (repeatable)`},
}

// wkAddUploadFlags adds the flags shared by every script upload.
func wkAddUploadFlags(c *cobra.Command) {
	wkAddScriptFlags(c)
	f := c.Flags()
	f.String("compatibility-date", "", "Workers runtime compatibility date YYYY-MM-DD (default: config, else today)")
	f.StringArray("compatibility-flag", nil, "Compatibility flag, e.g. nodejs_compat (repeatable)")
	for _, k := range bindingFlagKinds {
		f.StringArray(k.name, nil, k.usage)
	}
	f.String("bindings-file", "", "JSON file with an array of binding objects")
	f.StringArray("module", nil, "Extra module file to upload next to the main module (repeatable)")
	f.Bool("esm", false, "Force ES module format (default: detected from the script)")
	f.Bool("service-worker", false, "Force service-worker format (default: detected from the script)")
	f.Bool("bundle", false, "Bundle the entry point with esbuild (must be on PATH) before uploading")
	f.Bool("minify", false, "Minify when bundling (with --bundle)")
	f.Bool("upload-source-maps", false, "Upload <main>.map next to the main module")
	f.String("assets", "", "Static assets directory to upload (default: config [assets].directory)")
	f.Bool("keep-vars", false, "Keep plain-text/JSON vars set in the dashboard instead of replacing them")
	f.String("message", "", "Message to attach to the version (workers/message)")
	f.String("tag", "", "Tag to attach to the version (workers/tag)")
	f.Bool("dry-run", false, "Build and print the upload metadata, but send nothing")
}

// uploadPlan is a fully resolved script upload.
type uploadPlan struct {
	Name     string
	Config   *workers.Config
	Modules  []workers.Module
	Metadata map[string]any
	Assets   *workers.Assets
	cleanup  func()
}

func (p *uploadPlan) Close() {
	if p != nil && p.cleanup != nil {
		p.cleanup()
	}
}

// wkPrepareUpload resolves the entry point, modules, bindings, and metadata
// from args, flags, and the wrangler config.
func wkPrepareUpload(cmd *cobra.Command, args []string) (*uploadPlan, error) {
	ctx := cmd.Context()
	f := cmd.Flags()
	cfg, err := wkConfig(cmd)
	if err != nil {
		return nil, err
	}
	p := &uploadPlan{Config: cfg, Metadata: map[string]any{}}

	p.Name, _ = f.GetString("name")
	if p.Name == "" && cfg != nil {
		p.Name = cfg.Name
	}
	if p.Name == "" {
		return nil, fmt.Errorf("no Worker name: pass --name or set name in the wrangler config")
	}

	main := ""
	if len(args) > 0 {
		main = args[0]
	} else if cfg != nil {
		main = cfg.MainPath()
	}

	assetsDir, _ := f.GetString("assets")
	if assetsDir == "" && cfg != nil {
		assetsDir = cfg.AssetsDir()
	}
	if main == "" && assetsDir == "" {
		return nil, fmt.Errorf("no entry point: pass the built script path, or set main (or assets) in the wrangler config")
	}

	compatFlags, _ := f.GetStringArray("compatibility-flag")
	if cfg != nil && !f.Changed("compatibility-flag") {
		compatFlags = cfg.CompatibilityFlags
	}

	if main != "" {
		if bundle, _ := f.GetBool("bundle"); bundle {
			dir, err := os.MkdirTemp("", "cfctl-bundle-")
			if err != nil {
				return nil, err
			}
			p.cleanup = func() { os.RemoveAll(dir) }
			minify, _ := f.GetBool("minify")
			maps, _ := f.GetBool("upload-source-maps")
			nodeCompat := false
			for _, fl := range compatFlags {
				if strings.HasPrefix(fl, "nodejs_compat") {
					nodeCompat = true
				}
			}
			out, err := workers.Bundle(ctx, main, dir, minify, nodeCompat, maps)
			if err != nil {
				p.Close()
				return nil, err
			}
			main = out
		}
		extra, _ := f.GetStringArray("module")
		var esm *bool
		if v, _ := f.GetBool("esm"); v {
			t := true
			esm = &t
		} else if v, _ := f.GetBool("service-worker"); v {
			fl := false
			esm = &fl
		}
		maps, _ := f.GetBool("upload-source-maps")
		mods, isESM, err := workers.LoadModules(main, extra, esm, maps)
		if err != nil {
			p.Close()
			return nil, err
		}
		p.Modules = mods
		if isESM {
			p.Metadata["main_module"] = mods[0].Name
		} else {
			p.Metadata["body_part"] = mods[0].Name
		}
	}

	date, _ := f.GetString("compatibility-date")
	if date == "" && cfg != nil {
		date = cfg.CompatibilityDate
	}
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
		fmt.Fprintln(os.Stderr, ui.Warn("no compatibility_date set; using today ("+date+")"))
	}
	p.Metadata["compatibility_date"] = date
	if len(compatFlags) > 0 {
		p.Metadata["compatibility_flags"] = compatFlags
	}

	bindings, err := cfg.Bindings()
	if err != nil {
		p.Close()
		return nil, err
	}
	extra, err := wkBindingFlags(cmd)
	if err != nil {
		p.Close()
		return nil, err
	}
	bindings = workers.MergeBindings(bindings, extra)
	if bindings == nil {
		bindings = []workers.Binding{}
	}
	p.Metadata["bindings"] = bindings

	keep := []string{"secret_text", "secret_key"}
	keepVars, _ := f.GetBool("keep-vars")
	if keepVars || (cfg != nil && cfg.KeepVars) {
		keep = append(keep, "plain_text", "json")
	}
	p.Metadata["keep_bindings"] = keep

	ann := map[string]any{}
	if m, _ := f.GetString("message"); m != "" {
		ann["workers/message"] = m
	}
	if t, _ := f.GetString("tag"); t != "" {
		ann["workers/tag"] = t
	}
	if len(ann) > 0 {
		p.Metadata["annotations"] = ann
	}

	if cfg != nil {
		if cfg.UsageModel != "" {
			p.Metadata["usage_model"] = cfg.UsageModel
		}
		if cfg.Logpush != nil {
			p.Metadata["logpush"] = *cfg.Logpush
		}
		if cfg.Limits != nil {
			p.Metadata["limits"] = cfg.Limits
		}
		if cfg.Placement != nil {
			p.Metadata["placement"] = cfg.Placement
		}
		if cfg.Observability != nil {
			p.Metadata["observability"] = cfg.Observability
		}
		if cfg.TailConsumers != nil {
			p.Metadata["tail_consumers"] = cfg.TailConsumers
		}
	}

	if assetsDir != "" {
		a, err := workers.ScanAssets(assetsDir)
		if err != nil {
			p.Close()
			return nil, err
		}
		p.Assets = a
	}
	return p, nil
}

// wkBindingFlags collects --var/--kv/.../--binding and --bindings-file.
func wkBindingFlags(cmd *cobra.Command) ([]workers.Binding, error) {
	var out []workers.Binding
	if file, _ := cmd.Flags().GetString("bindings-file"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var bs []workers.Binding
		if err := json.Unmarshal(workers.StripJSONC(data), &bs); err != nil {
			return nil, fmt.Errorf("--bindings-file %s: want a JSON array of binding objects: %w", file, err)
		}
		out = append(out, bs...)
	}
	for _, k := range bindingFlagKinds {
		vals, _ := cmd.Flags().GetStringArray(k.name)
		for _, v := range vals {
			b, err := workers.ParseBindingFlag(k.name, v)
			if err != nil {
				return nil, err
			}
			out = append(out, b)
		}
	}
	return out, nil
}

// wkAssetsConfig builds metadata.assets.config from the wrangler config
// and the scanned _headers / _redirects.
func wkAssetsConfig(p *uploadPlan) map[string]any {
	conf := map[string]any{}
	if p.Config != nil && p.Config.Assets != nil {
		a := p.Config.Assets
		if a.HTMLHandling != "" {
			conf["html_handling"] = a.HTMLHandling
		}
		if a.NotFoundHandling != "" {
			conf["not_found_handling"] = a.NotFoundHandling
		}
		if a.RunWorkerFirst != nil {
			conf["run_worker_first"] = a.RunWorkerFirst
		}
	}
	if p.Assets != nil {
		if p.Assets.Headers != "" {
			conf["_headers"] = p.Assets.Headers
		}
		if p.Assets.Redirects != "" {
			conf["_redirects"] = p.Assets.Redirects
		}
	}
	return conf
}

// wkUploadAssets runs the assets upload-session flow and returns the
// completion token to put in metadata.assets.jwt. sessionPath is the
// .../assets-upload-session endpoint for the script.
func wkUploadAssets(ctx context.Context, s *apiSession, sessionPath string, a *workers.Assets) (string, error) {
	var sess struct {
		JWT     string     `json:"jwt"`
		Buckets [][]string `json:"buckets"`
	}
	if _, err := wkCall(ctx, s, "POST", sessionPath, nil, map[string]any{"manifest": a.Payload()}, &sess); err != nil {
		return "", fmt.Errorf("failed to start the assets upload session: %w", err)
	}
	total := 0
	for _, b := range sess.Buckets {
		total += len(b)
	}
	if total == 0 {
		fmt.Fprintln(os.Stderr, ui.Info(fmt.Sprintf("assets: %d files, all already uploaded", a.Count())))
		return sess.JWT, nil
	}
	fmt.Fprintln(os.Stderr, ui.Info(fmt.Sprintf("assets: uploading %d of %d files", total, a.Count())))
	acct, err := s.account(ctx)
	if err != nil {
		return "", err
	}
	// The upload endpoint authenticates with the session JWT, not the API
	// token. api.New still uses the shared transport (read-only guard etc.).
	up := api.New(sess.JWT, config.APIBaseURL())
	completion := ""
	if wkSingleAssetUploads(sess.JWT) {
		// The session asked for one request per file (wrangler's
		// "single asset upload" mode): raw bytes, Content-Type = the file's.
		n := 0
		for _, bucket := range sess.Buckets {
			for _, h := range bucket {
				n++
				path, ct, ok := a.FileFor(h)
				if !ok {
					return "", fmt.Errorf("assets: server asked for unknown hash %s", h)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return "", err
				}
				resp, err := up.Do(ctx, api.Request{Method: "POST", Path: "/accounts/" + url.PathEscape(acct) + "/workers/assets/upload/" + url.PathEscape(h), Body: data, ContentType: ct})
				if err != nil {
					return "", fmt.Errorf("assets: file %d/%d upload failed: %w", n, total, err)
				}
				if jwt := wkEnvelopeJWT(resp); jwt != "" {
					completion = jwt
				}
			}
		}
		if completion == "" {
			return "", fmt.Errorf("assets: upload finished without a completion token")
		}
		return completion, nil
	}
	for i, bucket := range sess.Buckets {
		if len(bucket) == 0 {
			continue
		}
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for _, h := range bucket {
			path, ct, ok := a.FileFor(h)
			if !ok {
				return "", fmt.Errorf("assets: server asked for unknown hash %s", h)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			hdr := textproto.MIMEHeader{}
			hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, h, h))
			hdr.Set("Content-Type", ct)
			pw, err := w.CreatePart(hdr)
			if err != nil {
				return "", err
			}
			pw.Write([]byte(base64.StdEncoding.EncodeToString(data)))
		}
		w.Close()
		resp, err := up.Do(ctx, api.Request{Method: "POST", Path: "/accounts/" + url.PathEscape(acct) + "/workers/assets/upload", Query: url.Values{"base64": {"true"}}, Body: buf.Bytes(), ContentType: w.FormDataContentType()})
		if err != nil {
			return "", fmt.Errorf("assets: bucket %d/%d upload failed: %w", i+1, len(sess.Buckets), err)
		}
		if jwt := wkEnvelopeJWT(resp); jwt != "" {
			completion = jwt
		}
	}
	if completion == "" {
		return "", fmt.Errorf("assets: upload finished without a completion token")
	}
	return completion, nil
}

// wkEnvelopeJWT returns result.jwt of an assets upload response, if any.
func wkEnvelopeJWT(resp *api.Response) string {
	if resp == nil || resp.Envelope == nil {
		return ""
	}
	var r struct {
		JWT string `json:"jwt"`
	}
	if json.Unmarshal(resp.Envelope.Result, &r) != nil {
		return ""
	}
	return r.JWT
}

// wkSingleAssetUploads reports whether an upload-session JWT asks for one
// upload request per file (claim wrangler_single_asset_uploads, which
// wrangler honors the same way). The JWT is only decoded, not verified.
func wkSingleAssetUploads(jwt string) bool {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	var claims struct {
		Single bool `json:"wrangler_single_asset_uploads"`
	}
	return json.Unmarshal(raw, &claims) == nil && claims.Single
}

// wkApplyMigrations sets metadata.migrations from the config's Durable
// Object migrations, starting after the script's current migration tag.
func wkApplyMigrations(ctx context.Context, s *apiSession, p *uploadPlan) error {
	if p.Config == nil || len(p.Config.Migrations) == 0 {
		return nil
	}
	var scripts []wkScript
	if _, err := wkCall(ctx, s, "GET", "/accounts/{account_id}/workers/scripts", nil, nil, &scripts); err != nil {
		return err
	}
	old := ""
	for _, sc := range scripts {
		if sc.ID == p.Name {
			old = sc.MigrationTag
		}
	}
	steps := p.Config.Migrations
	if old != "" {
		for i, m := range steps {
			if m.Tag == old {
				steps = steps[i+1:]
				break
			}
		}
	}
	if len(steps) == 0 {
		return nil
	}
	mig := map[string]any{"new_tag": steps[len(steps)-1].Tag, "steps": steps}
	if old != "" {
		mig["old_tag"] = old
	}
	p.Metadata["migrations"] = mig
	return nil
}

func wkPrintDryRun(p *uploadPlan, extra ...map[string]any) error {
	out := map[string]any{"name": p.Name, "metadata": p.Metadata}
	for _, e := range extra {
		for k, v := range e {
			out[k] = v
		}
	}
	var mods []map[string]any
	for _, m := range p.Modules {
		mods = append(mods, map[string]any{"name": m.Name, "content_type": m.ContentType, "size": len(m.Data)})
	}
	out["modules"] = mods
	if p.Assets != nil {
		out["assets"] = map[string]any{"directory": p.Assets.Dir, "files": p.Assets.Count()}
	}
	fmt.Fprintln(os.Stderr, ui.Info("dry run: nothing was uploaded"))
	return printJSONValue(out)
}

func wkTotalSize(mods []workers.Module) string {
	n := 0
	for _, m := range mods {
		n += len(m.Data)
	}
	return fmt.Sprintf("%.2f KiB", float64(n)/1024)
}

// wkUpload sends the plan to path (PUT script or POST versions) and returns
// the decoded result.
func wkUpload(ctx context.Context, s *apiSession, method, path string, p *uploadPlan) (map[string]any, error) {
	if p.Assets != nil {
		jwt, err := wkUploadAssets(ctx, s, wkScriptPath(p.Name, "/assets-upload-session"), p.Assets)
		if err != nil {
			return nil, err
		}
		p.Metadata["assets"] = map[string]any{"jwt": jwt, "config": wkAssetsConfig(p)}
	}
	body, ct, err := workers.BuildUpload(p.Metadata, p.Modules)
	if err != nil {
		return nil, err
	}
	fp, err := s.fillPath(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.c.Do(ctx, api.Request{Method: method, Path: fp, Body: body, ContentType: ct})
	if err != nil {
		return nil, fmt.Errorf("upload of %s failed: %w", p.Name, err)
	}
	var res map[string]any
	if resp.Envelope != nil {
		_ = json.Unmarshal(resp.Envelope.Result, &res)
	}
	return res, nil
}

// ---- deploy ---------------------------------------------------------------

func newDeployCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "deploy [script]",
		Short: "Upload a Worker and deploy it to 100% of traffic",
		Long: `Upload a built Worker script (ES module or service worker) with its
bindings and static assets, deploy it, and apply its triggers (cron
schedules, routes, custom domains, workers.dev).

Settings come from flags, falling back to wrangler.toml / wrangler.json(c)
in the current directory (name, main, compatibility_date/flags, vars and
bindings, assets, triggers, routes, migrations, observability, ...).
cfctl doesn't bundle by default: point it at a built file, or pass --bundle
to run esbuild (if it's on PATH). Existing secrets are always kept.

Examples:
  # Deploy using ./wrangler.toml (name, main, bindings, routes, crons)
  cfctl deploy

  # Deploy a pre-built module with flags only
  cfctl deploy dist/index.js --name my-worker --compatibility-date 2025-01-01 \
    --var API_BASE=https://api.example.com --kv CACHE=0f2ac74b498b48028cb68387c421e279

  # Bundle with esbuild first, use the [env.staging] section
  cfctl deploy --bundle --env staging

  # Static assets only
  cfctl deploy --name my-site --assets ./public

  # See exactly what would be uploaded
  cfctl deploy --dry-run`,
		Args: cobra.MaximumNArgs(1),
		RunE: runDeploy,
	}
	wkAddUploadFlags(c)
	c.Flags().StringArray("route", nil, "Route pattern to attach, e.g. example.com/api/* (repeatable)")
	c.Flags().StringArray("domain", nil, "Custom domain to attach, e.g. api.example.com (repeatable)")
	c.Flags().StringArray("cron", nil, "Cron trigger, e.g. '*/15 * * * *' (repeatable; replaces the config's crons)")
	c.Flags().Bool("workers-dev", true, "Serve on <name>.<subdomain>.workers.dev (default: config's workers_dev)")
	c.Flags().Bool("no-triggers", false, "Don't touch crons, routes, domains, or workers.dev")
	c.Flags().String("zone", "", "Zone for --route patterns (default: inferred from the hostname)")
	return c
}

func runDeploy(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	p, err := wkPrepareUpload(cmd, args)
	if err != nil {
		return err
	}
	defer p.Close()
	trig, err := wkTriggersFrom(cmd, p.Config)
	if err != nil {
		return err
	}
	if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
		if !trig.empty() {
			return wkPrintDryRun(p, map[string]any{"triggers": trig})
		}
		return wkPrintDryRun(p)
	}
	s, err := newAPISession(cmd)
	if err != nil {
		return err
	}
	if err := wkApplyMigrations(ctx, s, p); err != nil {
		return err
	}
	res, err := wkUpload(ctx, s, "PUT", wkScriptPath(p.Name, ""), p)
	if err != nil {
		return err
	}
	out := map[string]any{"name": p.Name, "script": res}
	if !jsonOutput {
		fmt.Println(ui.Success(fmt.Sprintf("Uploaded %s (%s)", p.Name, wkTotalSize(p.Modules))))
	}
	if noTrig, _ := cmd.Flags().GetBool("no-triggers"); !noTrig {
		applied, err := wkApplyTriggers(ctx, s, p.Name, trig)
		out["triggers"] = applied
		if err != nil {
			return err
		}
	}
	var deps struct {
		Deployments []wkDeployment `json:"deployments"`
	}
	if _, err := wkCall(ctx, s, "GET", wkScriptPath(p.Name, "/deployments"), nil, nil, &deps); err == nil && len(deps.Deployments) > 0 {
		out["deployment"] = deps.Deployments[0]
		if !jsonOutput && len(deps.Deployments[0].Versions) > 0 {
			fmt.Println(ui.Info("Current Version ID: " + deps.Deployments[0].Versions[0].VersionID))
		}
	}
	if jsonOutput {
		return printJSONValue(out)
	}
	return nil
}

// ---- triggers -------------------------------------------------------------

// wkTriggers is the trigger set a deploy (or `triggers deploy`) applies.
type wkTriggers struct {
	Crons      []string        `json:"crons,omitempty"`
	SetCrons   bool            `json:"-"`
	Routes     []workers.Route `json:"routes,omitempty"`
	Domains    []workers.Route `json:"custom_domains,omitempty"`
	WorkersDev *bool           `json:"workers_dev,omitempty"`
	// PreviewURLs is the config's preview_urls (previews_enabled).
	PreviewURLs *bool  `json:"preview_urls,omitempty"`
	Zone        string `json:"-"`
}

func (t wkTriggers) empty() bool {
	return !t.SetCrons && len(t.Routes) == 0 && len(t.Domains) == 0 && t.WorkersDev == nil && t.PreviewURLs == nil
}

func wkTriggersFrom(cmd *cobra.Command, cfg *workers.Config) (wkTriggers, error) {
	var t wkTriggers
	f := cmd.Flags()
	if f.Lookup("cron") != nil && f.Changed("cron") {
		t.Crons, _ = f.GetStringArray("cron")
		t.SetCrons = true
	} else if cfg != nil {
		if _, ok := cfg.Raw["triggers"]; ok {
			t.Crons = cfg.Triggers.Crons
			t.SetCrons = true
		}
	}
	var routes []workers.Route
	if cfg != nil {
		routes = append(routes, cfg.Routes...)
	}
	if f.Lookup("route") != nil {
		rs, _ := f.GetStringArray("route")
		for _, r := range rs {
			routes = append(routes, workers.Route{Pattern: r})
		}
		ds, _ := f.GetStringArray("domain")
		for _, d := range ds {
			routes = append(routes, workers.Route{Pattern: d, CustomDomain: true})
		}
	}
	for _, r := range routes {
		if r.CustomDomain {
			t.Domains = append(t.Domains, r)
		} else {
			t.Routes = append(t.Routes, r)
		}
	}
	if f.Lookup("workers-dev") != nil && f.Changed("workers-dev") {
		v, _ := f.GetBool("workers-dev")
		t.WorkersDev = &v
	} else if cfg != nil && cfg.WorkersDev != nil {
		t.WorkersDev = cfg.WorkersDev
	} else if cfg != nil && len(routes) == 0 {
		// wrangler's default: no routes → serve on workers.dev.
		v := true
		t.WorkersDev = &v
	}
	if cfg != nil && cfg.PreviewURLs != nil {
		t.PreviewURLs = cfg.PreviewURLs
	}
	if f.Lookup("zone") != nil {
		t.Zone, _ = f.GetString("zone")
	}
	return t, nil
}

// wkListZones lists the account's zones (name → id).
func wkListZones(ctx context.Context, s *apiSession) (map[string]string, error) {
	q := url.Values{"per_page": {"50"}}
	if id := config.AccountID(); id != "" {
		q.Set("account.id", id)
	}
	res, err := s.c.All(ctx, api.Request{Method: "GET", Path: "/zones", Query: q}, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to list zones: %w", err)
	}
	var zs []struct{ ID, Name string }
	_ = json.Unmarshal(res.Result, &zs)
	out := map[string]string{}
	for _, z := range zs {
		out[strings.ToLower(z.Name)] = z.ID
	}
	return out, nil
}

// wkRouteHost returns the hostname part of a route pattern ("*.example.com/x*" → "example.com").
func wkRouteHost(pattern string) string {
	h := pattern
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	h = strings.TrimPrefix(h, "*.")
	h = strings.TrimPrefix(h, "*")
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(h)
}

// wkZoneFor picks the zone whose name is the longest suffix of host.
func wkZoneFor(zones map[string]string, host string) (string, string, bool) {
	best := ""
	for name := range zones {
		if (host == name || strings.HasSuffix(host, "."+name)) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, zones[best], true
}

type wkRoute struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

// wkApplyTriggers applies crons, routes, custom domains, and workers.dev.
// Routes and domains are added (never removed); crons are replaced.
func wkApplyTriggers(ctx context.Context, s *apiSession, name string, t wkTriggers) (map[string]any, error) {
	applied := map[string]any{}
	say := func(msg string) {
		if !jsonOutput {
			fmt.Println(ui.Success(msg))
		}
	}
	if t.SetCrons {
		body := make([]map[string]string, 0, len(t.Crons))
		for _, c := range t.Crons {
			body = append(body, map[string]string{"cron": c})
		}
		if _, err := wkCall(ctx, s, "PUT", wkScriptPath(name, "/schedules"), nil, body, nil); err != nil {
			return applied, fmt.Errorf("failed to set cron triggers: %w", err)
		}
		applied["crons"] = t.Crons
		if len(t.Crons) == 0 {
			say("Cleared cron triggers")
		} else {
			say("Cron triggers: " + strings.Join(t.Crons, ", "))
		}
	}
	var zones map[string]string
	zoneOf := func(r workers.Route) (string, string, error) {
		if r.ZoneID != "" {
			return r.ZoneName, r.ZoneID, nil
		}
		if t.Zone != "" && r.ZoneName == "" {
			s.zoneArg = t.Zone
			id, err := s.zone(ctx)
			return t.Zone, id, err
		}
		if zones == nil {
			var err error
			if zones, err = wkListZones(ctx, s); err != nil {
				return "", "", err
			}
		}
		if r.ZoneName != "" {
			id, ok := zones[strings.ToLower(r.ZoneName)]
			if !ok {
				return "", "", fmt.Errorf("zone %q not found in this account", r.ZoneName)
			}
			return r.ZoneName, id, nil
		}
		zn, id, ok := wkZoneFor(zones, wkRouteHost(r.Pattern))
		if !ok {
			return "", "", fmt.Errorf("no zone in this account matches route %q; pass --zone or set zone_name", r.Pattern)
		}
		return zn, id, nil
	}
	var routesOut []string
	existing := map[string][]wkRoute{}
	for _, r := range t.Routes {
		_, zid, err := zoneOf(r)
		if err != nil {
			return applied, err
		}
		if _, ok := existing[zid]; !ok {
			var rs []wkRoute
			if _, err := wkCall(ctx, s, "GET", "/zones/"+zid+"/workers/routes", nil, nil, &rs); err != nil {
				return applied, fmt.Errorf("failed to list routes: %w", err)
			}
			existing[zid] = rs
		}
		var found *wkRoute
		for i := range existing[zid] {
			if existing[zid][i].Pattern == r.Pattern {
				found = &existing[zid][i]
			}
		}
		switch {
		case found != nil && found.Script == name:
			// already there
		case found != nil:
			if _, err := wkCall(ctx, s, "PUT", "/zones/"+zid+"/workers/routes/"+found.ID, nil, map[string]string{"pattern": r.Pattern, "script": name}, nil); err != nil {
				return applied, fmt.Errorf("failed to update route %s: %w", r.Pattern, err)
			}
		default:
			if _, err := wkCall(ctx, s, "POST", "/zones/"+zid+"/workers/routes", nil, map[string]string{"pattern": r.Pattern, "script": name}, nil); err != nil {
				return applied, fmt.Errorf("failed to add route %s: %w", r.Pattern, err)
			}
		}
		routesOut = append(routesOut, r.Pattern)
	}
	if len(routesOut) > 0 {
		applied["routes"] = routesOut
		say("Routes: " + strings.Join(routesOut, ", "))
	}
	var domainsOut []string
	for _, d := range t.Domains {
		host := wkRouteHost(d.Pattern)
		zn, zid, err := zoneOf(workers.Route{Pattern: host, ZoneName: d.ZoneName, ZoneID: d.ZoneID})
		if err != nil {
			return applied, err
		}
		body := map[string]any{"hostname": host, "service": name, "environment": "production", "zone_id": zid}
		if zn != "" {
			body["zone_name"] = zn
		}
		if _, err := wkCall(ctx, s, "PUT", "/accounts/{account_id}/workers/domains", nil, body, nil); err != nil {
			return applied, fmt.Errorf("failed to attach custom domain %s: %w", host, err)
		}
		domainsOut = append(domainsOut, host)
	}
	if len(domainsOut) > 0 {
		applied["custom_domains"] = domainsOut
		say("Custom domains: " + strings.Join(domainsOut, ", "))
	}
	if t.WorkersDev != nil || t.PreviewURLs != nil {
		var cur struct {
			Enabled         bool `json:"enabled"`
			PreviewsEnabled bool `json:"previews_enabled"`
		}
		_, gerr := wkCall(ctx, s, "GET", wkScriptPath(name, "/subdomain"), nil, nil, &cur)
		if gerr != nil && t.WorkersDev == nil {
			return applied, fmt.Errorf("failed to read workers.dev settings: %w", gerr)
		}
		want := cur.Enabled
		if t.WorkersDev != nil {
			want = *t.WorkersDev
		}
		body := map[string]bool{"enabled": want}
		changed := gerr != nil || cur.Enabled != want
		if t.PreviewURLs != nil {
			body["previews_enabled"] = *t.PreviewURLs
			changed = changed || cur.PreviewsEnabled != *t.PreviewURLs
		}
		if changed {
			if _, err := wkCall(ctx, s, "POST", wkScriptPath(name, "/subdomain"), nil, body, nil); err != nil {
				return applied, fmt.Errorf("failed to update workers.dev: %w", err)
			}
		}
		if t.WorkersDev != nil {
			applied["workers_dev"] = *t.WorkersDev
			if *t.WorkersDev {
				say("workers.dev: enabled")
			} else {
				say("workers.dev: disabled")
			}
		}
		if t.PreviewURLs != nil {
			applied["preview_urls"] = *t.PreviewURLs
		}
	}
	return applied, nil
}

// sortedKeys returns m's keys sorted.
func wkSortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func init() {
	workersCmd.AddCommand(newDeployCmd())
	rootCmd.AddCommand(newDeployCmd())
}

// wkRouteSpec is a route or custom domain to apply.
type wkRouteSpec = workers.Route
