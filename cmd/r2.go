package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/api"
	"github.com/dorkitude/cfctl/internal/r2"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
)

var r2Cmd = &cobra.Command{
	Use:   "r2",
	Short: "Manage R2 buckets, objects, bucket settings, usage, and sync",
	Long: `Manage R2: buckets and their settings, objects (ls/get/put/rm/stat/du),
usage and cost estimates, temporary S3 credentials, and directory sync.

Objects are addressed as <bucket>/<key>. Object commands use the Cloudflare
REST API; sync and large uploads (over --multipart-threshold) use R2's S3 API
with short-lived credentials minted for the job.

Examples:
  cfctl r2 buckets list
  cfctl r2 ls my-bucket                      # folder view of the bucket root
  cfctl r2 ls my-bucket/assets/ -r           # everything under assets/
  cfctl r2 get my-bucket/notes.txt           # to stdout
  cfctl r2 put my-bucket/img/logo.png --file logo.png
  cfctl r2 rm my-bucket/tmp/ -r
  cfctl r2 du my-bucket --by ext --top 10
  cfctl r2 usage                             # sizes, operations, estimated cost
  cfctl r2 sync ./public my-bucket/site/ --dry-run

Generated equivalents: cfctl api r2-bucket|r2-object|r2-account <op>.`,
}

// r2Jurisdiction returns --jurisdiction (persistent on r2).
func r2Jurisdiction(cmd *cobra.Command) string {
	j, _ := cmd.Flags().GetString("jurisdiction")
	return j
}

// r2Header returns the jurisdiction header (nil for the default).
func r2Header(cmd *cobra.Command) http.Header {
	return r2.JurisdictionHeader(r2Jurisdiction(cmd))
}

// r2Split splits "bucket/key" (also accepts "r2://bucket/key").
func r2Split(arg string) (bucket, key string) {
	arg = strings.TrimPrefix(arg, "r2://")
	bucket, key, _ = strings.Cut(arg, "/")
	return bucket, key
}

// r2SplitKey requires a key.
func r2SplitKey(arg string) (string, string, error) {
	b, k := r2Split(arg)
	if b == "" || k == "" {
		return "", "", fmt.Errorf("want <bucket>/<key>, got %q", arg)
	}
	return b, k, nil
}

// --- buckets ----------------------------------------------------------------

var r2BucketsCmd = &cobra.Command{
	Use:     "buckets",
	Aliases: []string{"bucket", "b"},
	Short:   "List, inspect, create, update, and delete buckets; bucket settings",
}

type r2Bucket struct {
	Name         string `json:"name"`
	CreationDate string `json:"creation_date,omitempty"`
	Location     string `json:"location,omitempty"`
	StorageClass string `json:"storage_class,omitempty"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
}

func r2ListBuckets(c *stClient, cmd *cobra.Command) ([]r2Bucket, json.RawMessage, error) {
	q := url.Values{"per_page": {"1000"}}
	if v, _ := cmd.Flags().GetString("name-contains"); v != "" {
		q.Set("name_contains", v)
	}
	res, err := c.c.All(c.ctx, api.Request{Method: "GET", Path: c.p("r2/buckets"), Query: q, Header: r2Header(cmd)}, 0)
	if err != nil {
		return nil, nil, err
	}
	var wrap struct {
		Buckets []r2Bucket `json:"buckets"`
	}
	if err := json.Unmarshal(res.Result, &wrap); err != nil {
		return nil, nil, fmt.Errorf("decoding bucket list: %w", err)
	}
	return wrap.Buckets, res.Result, nil
}

var r2BucketsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List buckets",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		buckets, raw, err := r2ListBuckets(c, cmd)
		if err != nil {
			return err
		}
		return stEmit(raw, func() error {
			if len(buckets) == 0 {
				fmt.Println(ui.Warn("No R2 buckets found"))
				return nil
			}
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("🪣 %d R2 buckets", len(buckets))))
			// The list endpoint usually omits location and storage class; only
			// show those columns when at least one bucket has them.
			showLoc, showClass := false, false
			for _, b := range buckets {
				showLoc = showLoc || b.Location != ""
				showClass = showClass || b.StorageClass != ""
			}
			header := []string{"NAME", "CREATED"}
			if showLoc {
				header = append(header, "LOCATION")
			}
			if showClass {
				header = append(header, "CLASS")
			}
			rows := [][]string{}
			for _, b := range buckets {
				row := []string{b.Name, stShortTime(b.CreationDate)}
				if showLoc {
					row = append(row, b.Location)
				}
				if showClass {
					row = append(row, b.StorageClass)
				}
				rows = append(rows, row)
			}
			stTable(header, rows)
			return nil
		})
	},
}

// r2Storage is one bucket's latest storage numbers from GraphQL.
type r2Storage struct {
	Bucket       string  `json:"bucket"`
	StorageClass string  `json:"storage_class"`
	Objects      float64 `json:"objects"`
	PayloadBytes float64 `json:"payload_bytes"`
	MetaBytes    float64 `json:"metadata_bytes"`
	Uploads      float64 `json:"multipart_uploads"`
	AsOf         string  `json:"as_of"`
}

// r2StorageNow returns the latest storage datapoint per bucket and storage
// class from r2StorageAdaptiveGroups (last 3 days). bucket "" means all.
func r2StorageNow(c *stClient, bucket string) ([]r2Storage, error) {
	filter := `{datetime_geq: $since}`
	vars := map[string]any{"acct": c.acct, "since": time.Now().UTC().Add(-72 * time.Hour).Format(time.RFC3339)}
	if bucket != "" {
		filter = `{datetime_geq: $since, bucketName: $bucket}`
		vars["bucket"] = bucket
	}
	q := `query($acct: String!, $since: Time!` + map[bool]string{true: `, $bucket: String!`, false: ``}[bucket != ""] + `) {
  viewer { accounts(filter: {accountTag: $acct}) {
    r2StorageAdaptiveGroups(limit: 10000, filter: ` + filter + `, orderBy: [datetime_DESC]) {
      max { objectCount payloadSize metadataSize uploadCount }
      dimensions { bucketName storageClass datetime }
    } } } }`
	var data struct {
		Viewer struct {
			Accounts []struct {
				Groups []struct {
					Max struct {
						ObjectCount  float64 `json:"objectCount"`
						PayloadSize  float64 `json:"payloadSize"`
						MetadataSize float64 `json:"metadataSize"`
						UploadCount  float64 `json:"uploadCount"`
					} `json:"max"`
					Dim struct {
						Bucket   string `json:"bucketName"`
						Class    string `json:"storageClass"`
						Datetime string `json:"datetime"`
					} `json:"dimensions"`
				} `json:"r2StorageAdaptiveGroups"`
			} `json:"accounts"`
		} `json:"viewer"`
	}
	if err := c.graphql(q, vars, &data); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []r2Storage
	for _, a := range data.Viewer.Accounts {
		for _, g := range a.Groups { // newest first
			k := g.Dim.Bucket + "\x00" + g.Dim.Class
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, r2Storage{Bucket: g.Dim.Bucket, StorageClass: g.Dim.Class, Objects: g.Max.ObjectCount,
				PayloadBytes: g.Max.PayloadSize, MetaBytes: g.Max.MetadataSize, Uploads: g.Max.UploadCount, AsOf: g.Dim.Datetime})
		}
	}
	return out, nil
}

var r2BucketsGetCmd = &cobra.Command{
	Use:     "get <bucket>",
	Aliases: []string{"info"},
	Short:   "Show a bucket's properties, object count, and size",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "GET", Path: r2.BucketPath(c.acct, args[0]), Header: r2Header(cmd)})
		if err != nil {
			return err
		}
		stats, serr := r2StorageNow(c, args[0])
		var objects, bytes float64
		for _, s := range stats {
			objects += s.Objects
			bytes += s.PayloadBytes
		}
		if jsonOutput {
			var obj map[string]any
			_ = json.Unmarshal(raw, &obj)
			if obj == nil {
				obj = map[string]any{}
			}
			if serr == nil {
				obj["object_count"] = objects
				obj["payload_bytes"] = bytes
				obj["storage"] = stats
			}
			return printJSONValue(obj)
		}
		if err := stDetail("🪣 "+args[0], raw); err != nil {
			return err
		}
		if serr != nil {
			fmt.Println(ui.Warn("size unavailable: " + serr.Error()))
			return nil
		}
		fmt.Printf("  %-14s %s\n", "objects:", r2.HumanCount(objects))
		fmt.Printf("  %-14s %s\n", "size:", r2.HumanBytes(bytes))
		return nil
	},
}

var r2BucketsCreateCmd = &cobra.Command{
	Use:   "create <bucket>",
	Short: "Create a bucket",
	Long: `Create a bucket.

Examples:
  cfctl r2 buckets create my-bucket
  cfctl r2 buckets create my-bucket --location weur --storage-class InfrequentAccess
  cfctl r2 buckets create eu-bucket --jurisdiction eu`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		body := map[string]any{"name": args[0]}
		stFlagStr(cmd, body, "location", "locationHint")
		stFlagStr(cmd, body, "storage-class", "storageClass")
		raw, err := c.result(stReq{Method: "POST", Path: c.p("r2/buckets"), Header: r2Header(cmd), Body: body})
		if err != nil {
			return err
		}
		if err := stOK(raw, "Created bucket "+args[0]); err != nil || jsonOutput {
			return err
		}
		fmt.Println(ui.SubtleStyle.Render("  Binding config (wrangler.jsonc):"))
		fmt.Printf("    \"r2_buckets\": [{ \"binding\": \"%s\", \"bucket_name\": \"%s\" }]\n", kvBindingName(args[0]), args[0])
		return nil
	},
}

var r2BucketsUpdateCmd = &cobra.Command{
	Use:   "update <bucket> --storage-class <Standard|InfrequentAccess>",
	Short: "Change a bucket's default storage class",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		class, _ := cmd.Flags().GetString("storage-class")
		if class == "" {
			return fmt.Errorf("--storage-class is required (Standard or InfrequentAccess)")
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		h := r2Header(cmd)
		if h == nil {
			h = http.Header{}
		}
		h.Set("cf-r2-storage-class", class)
		raw, err := c.result(stReq{Method: "PATCH", Path: r2.BucketPath(c.acct, args[0]), Header: h})
		if err != nil {
			return err
		}
		return stOK(raw, fmt.Sprintf("Default storage class of %s is now %s", args[0], class))
	},
}

var r2BucketsDeleteCmd = &cobra.Command{
	Use:   "delete <bucket>",
	Short: "Delete a bucket (--force empties it first)",
	Long: `Delete a bucket. The bucket must be empty unless --force is given, in which
case every object is deleted first (listed, then removed 1,000 at a time).
Incomplete multipart uploads and event notification rules also block
deletion; remove those first.

Examples:
  cfctl r2 buckets delete old-bucket
  cfctl r2 buckets delete scratch --force --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		bucket := args[0]
		force, _ := cmd.Flags().GetBool("force")
		if force {
			res, err := r2.List(c.ctx, c.c, c.acct, bucket, r2.ListOptions{Jurisdiction: r2Jurisdiction(cmd)}, nil)
			if err != nil {
				return err
			}
			var size int64
			keys := make([]string, 0, len(res.Objects))
			for _, o := range res.Objects {
				keys = append(keys, o.Key)
				size += o.Size
			}
			what := fmt.Sprintf("delete bucket %s", bucket)
			if len(keys) > 0 {
				what = fmt.Sprintf("delete %s objects (%s) and then bucket %s", r2.HumanCount(float64(len(keys))), r2.HumanBytes(float64(size)), bucket)
			}
			if err := confirm(cmd, what); err != nil {
				return err
			}
			if len(keys) > 0 {
				if err := r2DeleteKeys(c, cmd, bucket, keys); err != nil {
					return err
				}
			}
		} else if err := confirm(cmd, "delete bucket "+bucket); err != nil {
			return err
		}
		raw, err := c.result(stReq{Method: "DELETE", Path: r2.BucketPath(c.acct, bucket), Header: r2Header(cmd)})
		if err != nil {
			if !force && strings.Contains(err.Error(), "not empty") {
				return fmt.Errorf("%w (use --force to delete its objects first)", err)
			}
			return err
		}
		return stOK(raw, "Deleted bucket "+bucket)
	},
}

// r2DeleteKeys deletes keys through the REST bulk endpoint, 1,000 per call,
// printing progress to stderr.
func r2DeleteKeys(c *stClient, cmd *cobra.Command, bucket string, keys []string) error {
	failed := 0
	for start := 0; start < len(keys); start += 1000 {
		end := min(start+1000, len(keys))
		resp, err := c.do(stReq{Method: "DELETE", Path: r2.BucketPath(c.acct, bucket) + "/objects", Header: r2Header(cmd), Body: keys[start:end]})
		if err != nil {
			// success:false with per-key errors: the rest were deleted.
			if resp != nil && resp.Envelope != nil && resp.Status == 200 {
				failed += len(resp.Envelope.Errors)
			} else {
				return fmt.Errorf("deleting objects %d-%d: %w", start, end-1, err)
			}
		}
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "\r  deleted %d/%d", end, len(keys))
		}
	}
	if !jsonOutput {
		fmt.Fprintln(os.Stderr)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d objects could not be deleted (locked or missing)", failed, len(keys))
	}
	return nil
}

func init() {
	r2Cmd.PersistentFlags().String("jurisdiction", "", "Bucket jurisdiction: eu, us, fedramp, fedramp-high (default: none)")

	r2BucketsListCmd.Flags().String("name-contains", "", "Only buckets whose name contains this")
	r2BucketsCreateCmd.Flags().String("location", "", "Location hint: apac, eeur, enam, weur, wnam, oc")
	r2BucketsCreateCmd.Flags().String("storage-class", "", "Default storage class: Standard or InfrequentAccess")
	r2BucketsUpdateCmd.Flags().String("storage-class", "", "New default storage class: Standard or InfrequentAccess")
	r2BucketsDeleteCmd.Flags().Bool("force", false, "Delete every object in the bucket first")
	stYes(r2BucketsDeleteCmd)
	r2BucketsCmd.AddCommand(r2BucketsListCmd, r2BucketsGetCmd, r2BucketsCreateCmd, r2BucketsUpdateCmd, r2BucketsDeleteCmd)

	r2Cmd.AddCommand(r2BucketsCmd)
	rootCmd.AddCommand(r2Cmd)
}
