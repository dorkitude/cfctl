package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/dorkitude/cfctl/internal/r2"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// r2ParentKeyID is the parent access key ID for temporary credentials:
// --parent-access-key-id, else the ID of cfctl's own API token (R2 S3
// credentials derived from an API token use the token ID as the key ID).
func r2ParentKeyID(cmd *cobra.Command, c *stClient) (string, error) {
	if id, _ := cmd.Flags().GetString("parent-access-key-id"); id != "" {
		return id, nil
	}
	app, err := getApp(c.ctx)
	if err != nil {
		return "", err
	}
	info, err := app.VerifyToken(c.ctx)
	if err != nil {
		return "", fmt.Errorf("finding this token's ID for --parent-access-key-id: %w", err)
	}
	return info.ID, nil
}

// r2MintCredentials calls POST /accounts/{id}/r2/temp-access-credentials.
func r2MintCredentials(cmd *cobra.Command, c *stClient, bucket, permission string, ttl time.Duration, prefixes, objects []string) (r2.TempCredentials, json.RawMessage, error) {
	parent, err := r2ParentKeyID(cmd, c)
	if err != nil {
		return r2.TempCredentials{}, nil, err
	}
	body := map[string]any{
		"bucket":            bucket,
		"parentAccessKeyId": parent,
		"permission":        permission,
		"ttlSeconds":        int(ttl.Seconds()),
	}
	if len(prefixes) > 0 {
		body["prefixes"] = prefixes
	}
	if len(objects) > 0 {
		body["objects"] = objects
	}
	raw, err := c.result(stReq{Method: "POST", Path: c.p("r2/temp-access-credentials"), Body: body})
	if err != nil {
		return r2.TempCredentials{}, nil, fmt.Errorf("minting temporary R2 credentials: %w", err)
	}
	var creds r2.TempCredentials
	if err := json.Unmarshal(raw, &creds); err != nil || creds.AccessKeyID == "" {
		return r2.TempCredentials{}, nil, fmt.Errorf("unexpected temporary credentials response")
	}
	return creds, raw, nil
}

var r2TempCredsCmd = &cobra.Command{
	Use:   "temp-credentials <bucket>",
	Short: "Mint short-lived S3 credentials for a bucket (optionally scoped)",
	Long: `Mint temporary S3 credentials (access key, secret, session token) for one
bucket, optionally limited to prefixes or objects. They can't exceed the
permissions of the parent R2 token, which defaults to this API token's ID
(an API token with R2 permissions doubles as an S3 key pair).

The credentials are printed, since that's the point; treat them as secrets.

Examples:
  cfctl r2 temp-credentials my-bucket --permission object-read-only --ttl 1h
  cfctl r2 temp-credentials my-bucket --prefix uploads/ --permission object-read-write --ttl 15m --json
  eval "$(cfctl r2 temp-credentials my-bucket --env)"   # AWS_* variables for aws-cli / rclone`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		perm, _ := cmd.Flags().GetString("permission")
		ttl, _ := cmd.Flags().GetDuration("ttl")
		if ttl <= 0 || ttl > 7*24*time.Hour {
			return fmt.Errorf("--ttl must be between 1s and 168h (7 days)")
		}
		prefixes, _ := cmd.Flags().GetStringArray("prefix")
		objects, _ := cmd.Flags().GetStringArray("object")
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		creds, raw, err := r2MintCredentials(cmd, c, args[0], perm, ttl, prefixes, objects)
		if err != nil {
			return err
		}
		endpoint := r2.S3Endpoint(c.acct, r2Jurisdiction(cmd))
		if env, _ := cmd.Flags().GetBool("env"); env {
			fmt.Printf("export AWS_ACCESS_KEY_ID=%s\nexport AWS_SECRET_ACCESS_KEY=%s\nexport AWS_SESSION_TOKEN=%s\nexport AWS_ENDPOINT_URL=%s\nexport AWS_REGION=auto\n",
				creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken, endpoint)
			return nil
		}
		if jsonOutput {
			var obj map[string]any
			_ = json.Unmarshal(raw, &obj)
			obj["endpoint"] = endpoint
			obj["bucket"] = args[0]
			obj["expires_at"] = time.Now().Add(ttl).UTC().Format(time.RFC3339)
			return printJSONValue(obj)
		}
		fmt.Println(ui.TitleStyle.Render("🔐 Temporary R2 credentials for " + args[0]))
		fmt.Printf("  %-18s %s\n", "access key id:", creds.AccessKeyID)
		fmt.Printf("  %-18s %s\n", "secret access key:", creds.SecretAccessKey)
		fmt.Printf("  %-18s %s\n", "session token:", creds.SessionToken)
		fmt.Printf("  %-18s %s\n", "endpoint:", endpoint)
		fmt.Printf("  %-18s %s (%s)\n", "expires:", time.Now().Add(ttl).UTC().Format(time.RFC3339), ttl)
		fmt.Printf("  %-18s %s\n", "permission:", perm)
		return nil
	},
}

// r2S3Session mints credentials and returns an S3 client for one job.
func r2S3Session(cmd *cobra.Command, c *stClient, bucket, permission string, prefixes []string) (*s3.Client, error) {
	ttl := time.Hour
	if v, err := cmd.Flags().GetDuration("credentials-ttl"); err == nil && v > 0 {
		ttl = v
	}
	creds, _, err := r2MintCredentials(cmd, c, bucket, permission, ttl, prefixes, nil)
	if err != nil {
		return nil, err
	}
	return r2.NewS3(c.acct, r2Jurisdiction(cmd), creds), nil
}

// r2PutS3 uploads one file through the S3 API (multipart above the part size).
func r2PutS3(cmd *cobra.Command, c *stClient, bucket, key, file string, size int64, opts r2.PutOptions) error {
	client, err := r2S3Session(cmd, c, bucket, "object-read-write", []string{key})
	if err != nil {
		return err
	}
	partMiB, _ := cmd.Flags().GetInt64("part-size")
	conc, _ := cmd.Flags().GetInt("concurrency")
	partSize := partMiB << 20
	if partSize < r2.MinPartSize {
		return fmt.Errorf("--part-size must be at least 5 (MiB)")
	}
	if size/partSize >= 10000 {
		partSize = size/9999 + 1
	}
	progress := !jsonOutput && term.IsTerminal(int(os.Stderr.Fd()))
	start := time.Now()
	err = r2.UploadFile(c.ctx, client, bucket, key, file, opts, r2.MultipartOptions{PartSize: partSize, Concurrency: conc, Progress: func(done int64) {
		if progress {
			fmt.Fprintf(os.Stderr, "\r  %s / %s", r2.HumanBytes(float64(done)), r2.HumanBytes(float64(size)))
		}
	}})
	if progress {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	if err != nil {
		return fmt.Errorf("uploading %s/%s: %w", bucket, key, err)
	}
	return stEmitValue(map[string]any{"bucket": bucket, "key": key, "size": size, "content_type": opts.ContentType, "via": "s3"}, func() error {
		fmt.Println(ui.Success(fmt.Sprintf("Uploaded %s/%s (%s, %s) via S3 in %s", bucket, key, r2.HumanBytes(float64(size)), opts.ContentType, time.Since(start).Round(time.Millisecond))))
		return nil
	})
}

var r2SyncCmd = &cobra.Command{
	Use:   "sync <src> <dst>",
	Short: "Sync a local directory to a bucket prefix, or a prefix to a directory",
	Long: `Copy new and changed files between a local directory and a bucket prefix.
The direction follows the arguments: a local directory first uploads, a
<bucket>/<prefix> first downloads.

A file is unchanged when its size matches and (unless --size-only) its MD5
matches the object's ETag (multipart ETags can't be compared; size decides).
--delete removes what exists only on the destination (asks first).

The plan is computed from a REST listing, so --dry-run works in read-only
mode. Transfers use R2's S3 API with temporary credentials minted for this
sync (scoped to the bucket and prefix; see 'cfctl r2 temp-credentials').

Examples:
  cfctl r2 sync ./public my-bucket/site/ --dry-run
  cfctl r2 sync ./public my-bucket/site/ --delete --exclude '*.map' --exclude .DS_Store
  cfctl r2 sync my-bucket/assets/ ./assets --concurrency 16`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		src, dst := args[0], args[1]
		upload := false
		if st, err := os.Stat(src); err == nil && st.IsDir() {
			upload = true
		} else if strings.HasPrefix(src, ".") || strings.HasPrefix(src, "/") || strings.HasPrefix(src, "~") {
			return fmt.Errorf("%s is not a directory", src)
		}
		dir, remote := dst, src
		if upload {
			dir, remote = src, dst
		}
		bucket, prefix := r2Split(remote)
		if bucket == "" {
			return fmt.Errorf("want <bucket>/<prefix>, got %q", remote)
		}
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		excludes, _ := cmd.Flags().GetStringArray("exclude")
		sizeOnly, _ := cmd.Flags().GetBool("size-only")
		del, _ := cmd.Flags().GetBool("delete")
		dry, _ := cmd.Flags().GetBool("dry-run")
		conc, _ := cmd.Flags().GetInt("concurrency")
		if conc < 1 {
			conc = 1
		}
		stTransferTimeout(cmd, 30*time.Minute)

		c, err := newST(cmd)
		if err != nil {
			return err
		}
		remoteList, err := r2.List(c.ctx, c.c, c.acct, bucket, r2.ListOptions{Prefix: prefix, Jurisdiction: r2Jurisdiction(cmd)}, nil)
		if err != nil {
			return err
		}
		var local []r2.LocalFile
		if _, err := os.Stat(dir); err == nil {
			if local, err = r2.WalkLocal(dir, excludes); err != nil {
				return err
			}
		} else if upload {
			return err
		}
		var plan r2.SyncPlan
		if upload {
			plan = r2.PlanUpload(local, remoteList.Objects, prefix, sizeOnly, del, excludes)
		} else {
			plan = r2.PlanDownload(remoteList.Objects, local, prefix, dir, sizeOnly, del, excludes)
		}
		verb := map[bool]string{true: "upload", false: "download"}[upload]

		if dry {
			return stEmitValue(map[string]any{"direction": verb, "dry_run": true, "plan": plan}, func() error {
				for _, t := range plan.Transfers {
					fmt.Printf("would %s %s (%s, %s)\n", verb, r2SyncLabel(upload, bucket, t), t.Reason, r2.HumanBytes(float64(t.Size)))
				}
				for _, d := range plan.Deletes {
					fmt.Printf("would delete %s\n", r2DeleteLabel(upload, bucket, d))
				}
				fmt.Println(ui.SubtleStyle.Render(fmt.Sprintf("  %d to %s (%s), %d to delete, %d unchanged (dry run)", len(plan.Transfers), verb, r2.HumanBytes(float64(plan.Bytes)), len(plan.Deletes), plan.Unchanged)))
				return nil
			})
		}
		if len(plan.Transfers) == 0 && len(plan.Deletes) == 0 {
			return stEmitValue(map[string]any{"direction": verb, "transferred": 0, "deleted": 0, "unchanged": plan.Unchanged}, func() error {
				fmt.Println(ui.Success(fmt.Sprintf("Already in sync (%d files)", plan.Unchanged)))
				return nil
			})
		}
		if len(plan.Deletes) > 0 {
			if err := confirm(cmd, fmt.Sprintf("delete %d %s that exist only at the destination", len(plan.Deletes), map[bool]string{true: "objects", false: "local files"}[upload])); err != nil {
				return err
			}
		}
		perm := "object-read-only"
		if upload {
			perm = "object-read-write"
		}
		var scope []string
		if prefix != "" {
			scope = []string{prefix}
		}
		client, err := r2S3Session(cmd, c, bucket, perm, scope)
		if err != nil {
			return err
		}
		done, failed := r2RunTransfers(c.ctx, client, bucket, plan.Transfers, upload, conc)
		deleted := 0
		if len(plan.Deletes) > 0 {
			if upload {
				bad, err := r2.DeleteKeys(c.ctx, client, bucket, plan.Deletes)
				if err != nil {
					return fmt.Errorf("deleting objects: %w", err)
				}
				deleted = len(plan.Deletes) - len(bad)
			} else {
				for _, p := range plan.Deletes {
					if os.Remove(p) == nil {
						deleted++
					}
				}
			}
		}
		result := map[string]any{"direction": verb, "transferred": done, "failed": failed, "deleted": deleted, "unchanged": plan.Unchanged, "bytes": plan.Bytes}
		if err := stEmitValue(result, func() error {
			fmt.Println(ui.Success(fmt.Sprintf("%s %d files (%s), deleted %d, %d unchanged", map[bool]string{true: "Uploaded", false: "Downloaded"}[upload], done, r2.HumanBytes(float64(plan.Bytes)), deleted, plan.Unchanged)))
			return nil
		}); err != nil {
			return err
		}
		if len(failed) > 0 {
			return fmt.Errorf("%d transfers failed: %s", len(failed), strings.Join(failed, "; "))
		}
		return nil
	},
}

func r2SyncLabel(upload bool, bucket string, t r2.SyncItem) string {
	if upload {
		return t.Path + " → " + bucket + "/" + t.Key
	}
	return bucket + "/" + t.Key + " → " + t.Path
}

func r2DeleteLabel(upload bool, bucket, d string) string {
	if upload {
		return bucket + "/" + d
	}
	return d
}

// r2RunTransfers runs uploads or downloads with a worker pool and returns
// the number done and descriptions of failures.
func r2RunTransfers(ctx context.Context, client *s3.Client, bucket string, items []r2.SyncItem, upload bool, conc int) (int, []string) {
	var (
		done   atomic.Int64
		mu     sync.Mutex
		failed []string
		wg     sync.WaitGroup
	)
	progress := !jsonOutput && term.IsTerminal(int(os.Stderr.Fd()))
	jobs := make(chan r2.SyncItem)
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				var err error
				if upload {
					head := make([]byte, 512)
					if f, ferr := os.Open(it.Path); ferr == nil {
						n, _ := f.Read(head)
						head = head[:n]
						f.Close()
					}
					err = r2.UploadFile(ctx, client, bucket, it.Key, it.Path, r2.PutOptions{ContentType: r2DetectContentType(it.Path, head)}, r2.MultipartOptions{})
				} else {
					_, err = r2.DownloadFile(ctx, client, bucket, it.Key, it.Path)
				}
				if err != nil {
					mu.Lock()
					failed = append(failed, it.Key+": "+err.Error())
					mu.Unlock()
					continue
				}
				n := done.Add(1)
				if progress {
					fmt.Fprintf(os.Stderr, "\r  %d/%d", n, len(items))
				}
			}
		}()
	}
	for _, it := range items {
		jobs <- it
	}
	close(jobs)
	wg.Wait()
	if progress {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	return int(done.Load()), failed
}

func init() {
	r2TempCredsCmd.Flags().String("permission", "object-read-only", "admin-read-write, admin-read-only, object-read-write, or object-read-only")
	r2TempCredsCmd.Flags().Duration("ttl", time.Hour, "Lifetime of the credentials (max 168h)")
	r2TempCredsCmd.Flags().StringArray("prefix", nil, "Limit to keys under this prefix (repeatable)")
	r2TempCredsCmd.Flags().StringArray("object", nil, "Limit to this object key (repeatable)")
	r2TempCredsCmd.Flags().String("parent-access-key-id", "", "Access key ID of the parent R2 token (default: this API token's ID)")
	r2TempCredsCmd.Flags().Bool("env", false, "Print AWS_* environment variable exports")

	r2SyncCmd.Flags().StringArray("exclude", nil, "Skip paths matching this glob (matched against the relative path and the base name; repeatable)")
	r2SyncCmd.Flags().Bool("size-only", false, "Compare sizes only (skip MD5)")
	r2SyncCmd.Flags().Bool("delete", false, "Delete destination files/objects that don't exist at the source")
	r2SyncCmd.Flags().Bool("dry-run", false, "Show what would change")
	r2SyncCmd.Flags().Int("concurrency", 8, "Parallel transfers")
	r2SyncCmd.Flags().String("parent-access-key-id", "", "Access key ID of the parent R2 token (default: this API token's ID)")
	r2SyncCmd.Flags().Duration("credentials-ttl", time.Hour, "Lifetime of the temporary credentials minted for the sync")
	stYes(r2SyncCmd)

	r2Cmd.AddCommand(r2TempCredsCmd, r2SyncCmd)
}
