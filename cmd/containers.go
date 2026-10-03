package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/dorkitude/cfctl/internal/version"
	"github.com/spf13/cobra"
)

const ctBase = "/accounts/{account_id}/containers"

var ctUUID = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}$`)

// ctRegistryScheme is "https"; tests point it at a plain-HTTP fake.
var ctRegistryScheme = "https"

// ctRegistryHost is Cloudflare's managed registry (overridable like wrangler).
func ctRegistryHost() string {
	if h := strings.TrimSpace(os.Getenv("CLOUDFLARE_CONTAINER_REGISTRY")); h != "" {
		return h
	}
	return "registry.cloudflare.com"
}

// ctResolveApp turns an application name in args[0] into its ID.
func ctResolveApp(ctx context.Context, s *apiSession, _ *cobra.Command, args []string) ([]string, error) {
	if len(args) == 0 || ctUUID.MatchString(args[0]) {
		return args, nil
	}
	path, _, err := platFill(ctx, s, ctBase+"/applications", nil)
	if err != nil {
		return nil, err
	}
	raw, err := platDo(ctx, s, "GET", path, nil, nil)
	if err != nil {
		return nil, platErr("Containers", err)
	}
	var apps []struct {
		ID, Name string
	}
	if err := platDecode(raw, &apps); err != nil {
		return nil, err
	}
	for _, a := range apps {
		if a.Name == args[0] {
			out := append([]string(nil), args...)
			out[0] = a.ID
			return out, nil
		}
	}
	return nil, fmt.Errorf("no container application named %q (see 'cfctl containers list')", args[0])
}

func init() {
	appCols := []platCol{
		{H: "id", Path: "id"}, {H: "name", Path: "name"}, {H: "instances", Path: "instances"},
		{H: "image", Path: "configuration.image", W: 50}, {H: "instance type", Path: "configuration.instance_type"},
		{H: "version", Path: "version"}, {H: "created", Path: "created_at"},
	}
	registries := platGroup("registries", "Configure non-Cloudflare image registries", "", []string{"registry"},
		platSpecs(
			platSpec{Use: "list", Short: "List configured registries", Aliases: []string{"ls"}, Path: ctBase + "/registries",
				Cols: []platCol{{H: "domain", Path: "domain"}, {H: "public", Path: "is_public"}, {H: "created", Path: "created_at"}}, Title: "📦 %d registries", Product: "Containers"},
			platSpec{Use: "configure <domain>", Short: "Configure credentials for a registry (body via --data)", Method: "POST", Path: ctBase + "/registries", ExtraArgs: 1,
				Long: `Configure a non-Cloudflare registry (e.g. an Amazon ECR domain). Pass the
registry's auth configuration with --data, for example:

  cfctl containers registries configure 123.dkr.ecr.us-east-1.amazonaws.com \
    --data '{"auth":{"public_credential":"AKIA...","private_credential":{"store_id":"...","secret_name":"..."}}}'

See 'cfctl api describe containers ...' for the full schema.`,
				Flags: func(c *cobra.Command) {
					c.Flags().String("kind", "", "Registry provider: ECR, DockerHub, or GAR (default: inferred from the domain)")
				},
				Body: func(c *cobra.Command, args []string) (any, error) {
					kind, _ := c.Flags().GetString("kind")
					if kind == "" {
						kind = ctRegistryKind(args[0])
					}
					if kind == "" {
						return nil, fmt.Errorf("can't tell the registry kind of %q; pass --kind ECR, DockerHub, or GAR", args[0])
					}
					return map[string]any{"domain": args[0], "kind": kind, "is_public": false}, nil
				}, Data: true, Done: "Configured registry %s", Secrets: []string{"auth.private_credential"}, Product: "Containers"},
			platSpec{Use: "delete <domain>", Short: "Delete a configured registry", Aliases: []string{"rm"}, Method: "DELETE", Path: ctBase + "/registries/{domain}",
				Confirm: "delete registry %s", Done: "Deleted registry %s", Product: "Containers"},
			platSpec{Use: "credentials <domain>", Short: "Get a temporary registry password (secret; needs --reveal)", Method: "POST", Path: ctBase + "/registries/{domain}/credentials",
				Flags: func(c *cobra.Command) {
					c.Flags().Int("expiration-minutes", 15, "Credential lifetime")
					c.Flags().StringSlice("permissions", []string{"pull"}, "Permissions: pull, push")
				},
				Body: func(c *cobra.Command, _ []string) (any, error) {
					m, _ := c.Flags().GetInt("expiration-minutes")
					p, _ := c.Flags().GetStringSlice("permissions")
					return map[string]any{"expiration_minutes": m, "permissions": p}, nil
				},
				Secrets: []string{"password"}, Product: "Containers"},
		)...)

	images := platGroup("images", "Manage images in the Cloudflare managed registry", "", []string{"image"},
		ctImagesList(), ctImagesDelete())

	ctCmd := platGroup("containers", "Manage Containers: applications, instances, images, registries", `Manage Cloudflare Containers.

  cfctl containers list | info <app> | instances <app> | delete <app>
  cfctl containers images list | delete <image:tag>
  cfctl containers registries list|configure|delete|credentials
  cfctl containers build <path> -t <image:tag> [--push]   (needs docker)
  cfctl containers push <image:tag>                       (needs docker)

Applications accept a name or an ID. Generated: 'cfctl api container-instances ...'.`,
		[]string{"container"},
		append(platSpecs(
			platSpec{Use: "list", Short: "List container applications", Aliases: []string{"ls"}, Path: ctBase + "/applications", Cols: appCols, Title: "📦 %d container applications", Product: "Containers"},
			platSpec{Use: "info <app>", Short: "Show a container application", Aliases: []string{"get"}, Path: ctBase + "/applications/{application_id}", ArgsHook: ctResolveApp, Product: "Containers",
				Fields: []platCol{{H: "ID", Path: "id"}, {H: "Name", Path: "name"}, {H: "Version", Path: "version"}, {H: "Instances", Path: "instances"},
					{H: "Max", Path: "max_instances"}, {H: "Image", Path: "configuration.image"}, {H: "Instance type", Path: "configuration.instance_type"},
					{H: "vCPU", Path: "configuration.vcpu"}, {H: "Memory", Path: "configuration.memory|configuration.memory_mib"}, {H: "Scheduling", Path: "scheduling_policy"},
					{H: "Durable Obj", Path: "durable_objects.namespace_id"}, {H: "Health", Path: "health.instances"}, {H: "Created", Path: "created_at"}},
				Title: "📦 Container application"},
			platSpec{Use: "instances <app>", Short: "List an application's instances", Path: ctBase + "/applications/{application_id}/instances-v2", ArgsHook: ctResolveApp, ItemsKey: "instances",
				Cols:  []platCol{{H: "id", Path: "id"}, {H: "state", Path: "state|current_state"}, {H: "location", Path: "location"}, {H: "version", Path: "app_version|version"}, {H: "created", Path: "created_at"}},
				Title: "📦 Instances", Product: "Containers"},
			platSpec{Use: "versions <app>", Short: "List an application's versions", Path: ctBase + "/applications/{application_id}/versions", ArgsHook: ctResolveApp, Product: "Containers"},
			platSpec{Use: "delete <app>", Short: "Delete a container application", Aliases: []string{"rm"}, Method: "DELETE", Path: ctBase + "/applications/{application_id}", ArgsHook: ctResolveApp,
				Confirm: "delete container application %s", Done: "Deleted container application %s", Product: "Containers"},
		), images, registries, ctBuildCmd(), ctPushCmd(),
			platLocalOnly("ssh", "SSH into a container instance", "wrangler containers ssh", "it tunnels an SSH session over a WebSocket with your local ssh client"))...)
	rootCmd.AddCommand(ctCmd)
}

// ctRegistryKind infers the API's registry "kind" from its domain, using
// the same patterns as wrangler; "" when it can't tell.
func ctRegistryKind(domain string) string {
	switch {
	case ctECRDomain.MatchString(domain):
		return "ECR"
	case domain == "docker.io":
		return "DockerHub"
	case ctGARDomain.MatchString(domain):
		return "GAR"
	}
	return ""
}

var (
	ctECRDomain = regexp.MustCompile(`^[0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com$`)
	ctGARDomain = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?-docker\.pkg\.dev$`)
)

// ctRegistryCreds asks the API for short-lived managed-registry credentials.
func ctRegistryCreds(ctx context.Context, s *apiSession, perms []string) (user, pass string, err error) {
	path, _, err := platFill(ctx, s, ctBase+"/registries/{domain}/credentials", []string{ctRegistryHost()})
	if err != nil {
		return "", "", err
	}
	raw, err := platDo(ctx, s, "POST", path, nil, map[string]any{"expiration_minutes": 5, "permissions": perms})
	if err != nil {
		return "", "", platErr("Containers", err)
	}
	var cr struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := platDecode(raw, &cr); err != nil {
		return "", "", err
	}
	if cr.Password == "" {
		return "", "", fmt.Errorf("the API returned no registry credentials")
	}
	return cr.Username, cr.Password, nil
}

// ctRegistry calls the managed registry's Docker v2 API with basic auth
// (never the Cloudflare API token). It uses the shared HTTP client, so the
// read-only guard and debug logging apply.
func ctRegistry(ctx context.Context, method, path, password string, accept string) (*http.Response, []byte, error) {
	u := ctRegistryScheme + "://" + ctRegistryHost() + path
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("v1:"+password)))
	req.Header.Set("User-Agent", "cfctl/"+version.Version)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if method == "PUT" {
		req.Header.Set("Content-Type", "application/json") // as wrangler sends for /v2/gc/layers
	}
	resp, err := api.NewHTTPClient().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, nil, fmt.Errorf("registry request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 300 {
		return resp, body, fmt.Errorf("registry %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return resp, body, nil
}

func ctImagesList() *cobra.Command {
	return platCommand(platSpec{
		Use: "list", Short: "List images and tags in the managed registry", Aliases: []string{"ls"}, Path: ctBase + "/registries",
		Long: `List repositories and tags in registry.cloudflare.com for this account.

This asks the API for a short-lived pull credential (a POST), so it is refused
in --read-only mode.`,
		Flags: func(c *cobra.Command) { c.Flags().String("filter", "", "Only repositories matching this substring") },
		Run: func(c *cobra.Command, _ []string) error {
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			acct, err := s.account(ctx)
			if err != nil {
				return err
			}
			_, pass, err := ctRegistryCreds(ctx, s, []string{"pull"})
			if err != nil {
				return err
			}
			filter, _ := c.Flags().GetString("filter")
			repos := map[string][]string{}
			next := "/v2/_catalog?tags=true"
			for i := 0; i < 100 && next != ""; i++ {
				resp, body, err := ctRegistry(ctx, "GET", next, pass, "")
				if err != nil {
					return err
				}
				var cat struct {
					Repositories map[string][]string `json:"repositories"`
				}
				if err := json.Unmarshal(body, &cat); err != nil {
					return fmt.Errorf("unexpected registry catalog: %w", err)
				}
				for r, tags := range cat.Repositories {
					repos[r] = append(repos[r], tags...)
				}
				next = ctNextLink(resp.Header.Get("Link"))
			}
			type img struct {
				Name string   `json:"name"`
				Tags []string `json:"tags"`
			}
			var out []img
			for r, tags := range repos {
				name := strings.TrimPrefix(r, acct+"/")
				if filter != "" && !strings.Contains(name, filter) {
					continue
				}
				sort.Strings(tags)
				out = append(out, img{Name: name, Tags: tags})
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			if jsonOutput {
				return printJSONValue(out)
			}
			if len(out) == 0 {
				fmt.Println(ui.Warn("No images"))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("📦 %d repositories in %s", len(out), ctRegistryHost())))
			for _, i := range out {
				fmt.Printf("  %s  %s\n", ui.AccentStyle.Render(i.Name), strings.Join(i.Tags, ", "))
			}
			return nil
		},
	})
}

var ctLinkRE = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="?next"?`)

func ctNextLink(h string) string {
	m := ctLinkRE.FindStringSubmatch(h)
	if m == nil {
		return ""
	}
	u, err := url.Parse(m[1])
	if err != nil {
		return ""
	}
	if u.RawQuery != "" {
		return u.Path + "?" + u.RawQuery
	}
	return u.Path
}

func ctImagesDelete() *cobra.Command {
	return platCommand(platSpec{
		Use: "delete <image:tag>", Short: "Delete an image tag from the managed registry", Aliases: []string{"rm"}, Path: ctBase + "/registries", ExtraArgs: 1,
		Confirm: "delete image %s",
		Run: func(c *cobra.Command, args []string) error {
			name, tag, ok := strings.Cut(args[0], ":")
			if !ok || name == "" || tag == "" {
				return fmt.Errorf("give the image as name:tag")
			}
			if err := confirm(c, "delete image "+args[0]); err != nil {
				return err
			}
			ctx := context.Background()
			s, err := newAPISession(c)
			if err != nil {
				return err
			}
			acct, err := s.account(ctx)
			if err != nil {
				return err
			}
			_, pass, err := ctRegistryCreds(ctx, s, []string{"pull", "push"})
			if err != nil {
				return err
			}
			accept := "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
			mpath := fmt.Sprintf("/v2/%s/%s/manifests/%s", acct, name, tag)
			resp, _, err := ctRegistry(ctx, "HEAD", mpath, pass, accept)
			if err != nil {
				return err
			}
			digest := resp.Header.Get("Docker-Content-Digest")
			if digest == "" {
				return fmt.Errorf("no digest for %s", args[0])
			}
			// Delete by tag, like wrangler: deleting by digest would also
			// remove every other tag that points at the same manifest.
			if _, _, err := ctRegistry(ctx, "DELETE", mpath, pass, accept); err != nil {
				return err
			}
			if _, _, err := ctRegistry(ctx, "PUT", "/v2/gc/layers", pass, ""); err != nil {
				fmt.Fprintln(os.Stderr, ui.Warn("deleted, but garbage collection failed: "+err.Error()))
			}
			if jsonOutput {
				return printJSONValue(map[string]any{"image": args[0], "digest": digest, "deleted": true})
			}
			fmt.Println(ui.Success(fmt.Sprintf("Deleted %s (%s)", args[0], digest)))
			return nil
		},
	})
}

// docker wrappers -------------------------------------------------------------

func ctDocker() string {
	if d := os.Getenv("CFCTL_DOCKER"); d != "" {
		return d
	}
	if d := os.Getenv("WRANGLER_DOCKER_BIN"); d != "" {
		return d
	}
	return "docker"
}

func ctRunDocker(stdin string, args ...string) error {
	cmd := exec.Command(ctDocker(), args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("docker not found; install Docker (or set CFCTL_DOCKER) to build and push images")
		}
		return fmt.Errorf("docker %s failed: %w", args[0], err)
	}
	return nil
}

func ctBuildCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "build <path>",
		Short: "Build a container image with docker (linux/amd64)",
		Long: `Build an image with 'docker build --platform linux/amd64' (the platform
Containers run on). With --push, also push it to the managed registry.`,
		Example: "  cfctl containers build . -t my-app:v1 --push",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			tag, _ := c.Flags().GetString("tag")
			if tag == "" {
				return fmt.Errorf("--tag (-t) name:tag is required")
			}
			dargs := []string{"build", "--platform", "linux/amd64", "-t", tag}
			if f, _ := c.Flags().GetString("file"); f != "" {
				dargs = append(dargs, "-f", f)
			}
			dargs = append(dargs, args[0])
			if err := ctRunDocker("", dargs...); err != nil {
				return err
			}
			if push, _ := c.Flags().GetBool("push"); push {
				return ctPush(c, tag)
			}
			if !jsonOutput {
				fmt.Println(ui.Success("Built " + tag))
			}
			return nil
		},
	}
	c.Flags().StringP("tag", "t", "", "Image name and tag (name:tag)")
	c.Flags().StringP("file", "f", "", "Path to the Dockerfile")
	c.Flags().BoolP("push", "p", false, "Push to the managed registry after building")
	return c
}

func ctPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push <image:tag>",
		Short: "Push a local image to the managed registry (registry.cloudflare.com)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(c *cobra.Command, args []string) error { return ctPush(c, args[0]) },
	}
}

// ctPush logs docker in with short-lived push credentials (password via
// stdin, never argv), tags the image under the account namespace, pushes.
func ctPush(c *cobra.Command, image string) error {
	ctx := context.Background()
	s, err := newAPISession(c)
	if err != nil {
		return err
	}
	acct, err := s.account(ctx)
	if err != nil {
		return err
	}
	user, pass, err := ctRegistryCreds(ctx, s, []string{"push", "pull"})
	if err != nil {
		return err
	}
	host := ctRegistryHost()
	if err := ctRunDocker(pass, "login", "--password-stdin", "--username", user, host); err != nil {
		return err
	}
	local := image
	if strings.HasPrefix(image, host+"/") {
		local = strings.TrimPrefix(strings.TrimPrefix(image, host+"/"), acct+"/")
	}
	remote := host + "/" + acct + "/" + local
	if remote != image {
		if err := ctRunDocker("", "tag", image, remote); err != nil {
			return err
		}
	}
	if err := ctRunDocker("", "push", remote); err != nil {
		return err
	}
	if jsonOutput {
		return printJSONValue(map[string]any{"image": remote, "pushed": true})
	}
	fmt.Println(ui.Success("Pushed " + remote))
	return nil
}
