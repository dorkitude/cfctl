package cmd

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dorkitude/cfctl/internal/r2"
	"github.com/dorkitude/cfctl/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// r2RESTPutLimit is the REST API's maximum upload size (300 MB).
const r2RESTPutLimit = 300 * 1000 * 1000

var r2LsCmd = &cobra.Command{
	Use:   "ls <bucket>[/prefix] [prefix]",
	Short: "List objects (folder view; -r for everything under a prefix)",
	Long: `List objects in a bucket. By default it shows one level, like a directory
listing: "folders" (common prefixes) and the objects directly under the
prefix. -r lists every object under the prefix.

Examples:
  cfctl r2 ls my-bucket
  cfctl r2 ls my-bucket/assets/
  cfctl r2 ls my-bucket assets/audio/
  cfctl r2 ls my-bucket/assets/ -r --limit 100
  cfctl r2 ls my-bucket -r --json`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		bucket, prefix := r2Split(args[0])
		if len(args) == 2 {
			prefix += args[1]
		}
		recursive, _ := cmd.Flags().GetBool("recursive")
		limit, _ := cmd.Flags().GetInt("limit")
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		o := r2.ListOptions{Prefix: prefix, Limit: limit, Jurisdiction: r2Jurisdiction(cmd)}
		if !recursive {
			o.Delimiter = "/"
		}
		res, err := r2.List(c.ctx, c.c, c.acct, bucket, o, nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return printJSONValue(res)
		}
		if len(res.Objects) == 0 && len(res.Prefixes) == 0 {
			fmt.Println(ui.Warn(fmt.Sprintf("No objects under %s/%s", bucket, prefix)))
			return nil
		}
		human, _ := cmd.Flags().GetBool("bytes")
		rows := [][]string{}
		for _, p := range res.Prefixes {
			rows = append(rows, []string{"DIR", "", "", p})
		}
		var total int64
		for _, ob := range res.Objects {
			total += ob.Size
			size := r2.HumanBytes(float64(ob.Size))
			if human {
				size = fmt.Sprint(ob.Size)
			}
			rows = append(rows, []string{size, stShortTime(ob.LastModified), r2ClassShort(ob.StorageClass), ob.Key})
		}
		stTable([]string{"SIZE", "MODIFIED", "CLASS", "KEY"}, rows)
		summary := fmt.Sprintf("%d objects, %s", len(res.Objects), r2.HumanBytes(float64(total)))
		if len(res.Prefixes) > 0 {
			summary = fmt.Sprintf("%d folders, ", len(res.Prefixes)) + summary
		}
		if res.Truncated {
			summary += " (more not shown)"
		}
		fmt.Println(ui.SubtleStyle.Render("  " + summary))
		return nil
	},
}

func r2ClassShort(c string) string {
	switch c {
	case r2.InfrequentAccess:
		return "IA"
	case r2.Standard, "":
		return "Std"
	}
	return c
}

var r2StatCmd = &cobra.Command{
	Use:   "stat <bucket>/<key>",
	Short: "Show an object's size, ETag, dates, and metadata",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bucket, key, err := r2SplitKey(args[0])
		if err != nil {
			return err
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		o, err := r2.Stat(c.ctx, c.c, c.acct, bucket, key, r2Jurisdiction(cmd))
		if err != nil {
			return err
		}
		if o == nil {
			return fmt.Errorf("no object %s/%s", bucket, key)
		}
		return stEmitValue(o, func() error {
			fmt.Println(ui.TitleStyle.Render("📄 " + bucket + "/" + key))
			fmt.Printf("  %-15s %s (%d bytes)\n", "size:", r2.HumanBytes(float64(o.Size)), o.Size)
			fmt.Printf("  %-15s %s\n", "etag:", o.ETag)
			fmt.Printf("  %-15s %s\n", "last modified:", o.LastModified)
			fmt.Printf("  %-15s %s\n", "storage class:", o.StorageClass)
			for _, k := range stSortedKeys(o.HTTPMetadata) {
				fmt.Printf("  %-15s %s\n", k+":", stString(o.HTTPMetadata[k]))
			}
			for _, k := range r2.SortedKeys(o.CustomMetadata) {
				fmt.Printf("  %-15s %s\n", "x-"+k+":", o.CustomMetadata[k])
			}
			return nil
		})
	},
}

var r2GetCmd = &cobra.Command{
	Use:   "get <bucket>/<key>",
	Short: "Download an object (to stdout, or --file)",
	Long: `Download an object. It streams to stdout unless --file is given (or stdout
is a terminal, in which case it is saved under the key's base name).

Examples:
  cfctl r2 get my-bucket/notes.txt
  cfctl r2 get my-bucket/backups/db.sql.gz --file db.sql.gz
  cfctl r2 get my-bucket/data.json | jq .`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bucket, key, err := r2SplitKey(args[0])
		if err != nil {
			return err
		}
		stTransferTimeout(cmd, 30*time.Minute)
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		dst, _ := cmd.Flags().GetString("file")
		if pipe, _ := cmd.Flags().GetBool("pipe"); pipe {
			dst = "-"
		}
		if dst == "" {
			if term.IsTerminal(int(os.Stdout.Fd())) {
				dst = path.Base(key)
			} else {
				dst = "-"
			}
		}
		resp, err := c.stream("GET", r2.ObjectPath(c.acct, bucket, key), nil, r2Header(cmd), nil, 0, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if dst == "-" {
			_, err = io.Copy(os.Stdout, resp.Body)
			return err
		}
		n, err := r2.WriteFileAtomic(dst, resp.Body)
		if err != nil {
			return err
		}
		if resp.ContentLength >= 0 && n != resp.ContentLength {
			return fmt.Errorf("short read: got %d of %d bytes", n, resp.ContentLength)
		}
		fmt.Fprintln(os.Stderr, ui.Success(fmt.Sprintf("Downloaded %s/%s to %s (%s)", bucket, key, dst, r2.HumanBytes(float64(n)))))
		return nil
	},
}

// r2DetectContentType picks a content type from the extension, then by
// sniffing the first 512 bytes.
func r2DetectContentType(name string, head []byte) string {
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); ct != "" {
		return ct
	}
	if len(head) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(head)
}

var r2PutCmd = &cobra.Command{
	Use:   "put <bucket>/<key> [--file path]",
	Short: "Upload an object (content type detected; multipart above the threshold)",
	Long: `Upload a file (or stdin) as an object. The content type comes from
--content-type, else the file extension, else by sniffing the content. If the
key ends in "/", the file's base name is appended.

Files up to --multipart-threshold (default 100 MB) go through the REST API.
Larger files, and uploads that set metadata the REST API can't carry
(--cache-control, --content-disposition, --content-encoding,
--content-language, --meta), go through R2's S3 API in parallel parts, with
temporary credentials minted for this upload (see 'cfctl r2 temp-credentials').

Examples:
  cfctl r2 put my-bucket/img/logo.png --file logo.png
  cfctl r2 put my-bucket/img/ --file logo.png             # key img/logo.png
  tar cz dir | cfctl r2 put my-bucket/backup.tgz
  cfctl r2 put my-bucket/video.mp4 --file video.mp4 --part-size 64 --concurrency 8
  cfctl r2 put my-bucket/a.html --file a.html --cache-control 'max-age=60' --meta owner=kyle`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bucket, key := r2Split(args[0])
		file, _ := cmd.Flags().GetString("file")
		if bucket == "" {
			return fmt.Errorf("want <bucket>/<key>, got %q", args[0])
		}
		if key == "" || strings.HasSuffix(key, "/") {
			if file == "" || file == "-" {
				return fmt.Errorf("want <bucket>/<key> (a key is needed when reading stdin)")
			}
			key += filepath.Base(file)
		}
		stTransferTimeout(cmd, 30*time.Minute)
		opts, needS3, err := r2PutOptions(cmd)
		if err != nil {
			return err
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}

		// stdin: buffer to a temp file so we know the size (and can retry).
		if file == "" || file == "-" {
			tmp, err := os.CreateTemp("", "cfctl-r2-put-*")
			if err != nil {
				return err
			}
			defer os.Remove(tmp.Name())
			if _, err := io.Copy(tmp, os.Stdin); err != nil {
				tmp.Close()
				return err
			}
			tmp.Close()
			file = tmp.Name()
		}
		st, err := os.Stat(file)
		if err != nil {
			return err
		}
		if st.IsDir() {
			return fmt.Errorf("%s is a directory; use 'cfctl r2 sync' for directories", file)
		}
		if opts.ContentType == "" {
			head := make([]byte, 512)
			if f, err := os.Open(file); err == nil {
				n, _ := io.ReadFull(f, head)
				head = head[:n]
				f.Close()
			}
			name := key
			if fl, _ := cmd.Flags().GetString("file"); fl != "" && fl != "-" {
				name = fl
			}
			opts.ContentType = r2DetectContentType(name, head)
		}
		threshold, _ := cmd.Flags().GetInt64("multipart-threshold")
		threshold *= 1000 * 1000
		if threshold <= 0 || threshold > r2RESTPutLimit {
			threshold = r2RESTPutLimit
		}
		if forceS3, _ := cmd.Flags().GetBool("s3"); forceS3 {
			needS3 = true
		}
		if needS3 || st.Size() > threshold {
			return r2PutS3(cmd, c, bucket, key, file, st.Size(), opts)
		}

		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		h := r2Header(cmd)
		if h == nil {
			h = http.Header{}
		}
		h.Set("Content-Type", opts.ContentType)
		if opts.StorageClass != "" {
			h.Set("cf-r2-storage-class", r2.RESTStorageClass(opts.StorageClass))
		}
		reopen := func() (io.ReadCloser, error) { return os.Open(file) }
		resp, err := c.stream("PUT", r2.ObjectPath(c.acct, bucket, key), nil, h, f, st.Size(), reopen)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if jsonOutput {
			return printBody(bytes.TrimSpace(body), nil)
		}
		fmt.Println(ui.Success(fmt.Sprintf("Uploaded %s/%s (%s, %s)", bucket, key, r2.HumanBytes(float64(st.Size())), opts.ContentType)))
		return nil
	},
}

// r2PutOptions reads metadata flags; needS3 is true when one can only be set
// through the S3 API.
func r2PutOptions(cmd *cobra.Command) (r2.PutOptions, bool, error) {
	var o r2.PutOptions
	o.ContentType, _ = cmd.Flags().GetString("content-type")
	o.CacheControl, _ = cmd.Flags().GetString("cache-control")
	o.ContentDisposition, _ = cmd.Flags().GetString("content-disposition")
	o.ContentEncoding, _ = cmd.Flags().GetString("content-encoding")
	o.ContentLanguage, _ = cmd.Flags().GetString("content-language")
	o.StorageClass, _ = cmd.Flags().GetString("storage-class")
	metas, _ := cmd.Flags().GetStringArray("meta")
	for _, m := range metas {
		k, v, ok := strings.Cut(m, "=")
		if !ok || k == "" {
			return o, false, fmt.Errorf("invalid --meta %q: want key=value", m)
		}
		if o.Metadata == nil {
			o.Metadata = map[string]string{}
		}
		o.Metadata[k] = v
	}
	needS3 := o.CacheControl != "" || o.ContentDisposition != "" || o.ContentEncoding != "" || o.ContentLanguage != "" || len(o.Metadata) > 0
	return o, needS3, nil
}

var r2RmCmd = &cobra.Command{
	Use:     "rm <bucket>/<key>... ",
	Aliases: []string{"delete"},
	Short:   "Delete objects (-r deletes everything under a prefix)",
	Long: `Delete one or more objects. With -r, each argument is a prefix and every
object under it is deleted (after listing them and asking for confirmation).

Examples:
  cfctl r2 rm my-bucket/tmp/a.txt
  cfctl r2 rm my-bucket/a.txt my-bucket/b.txt --yes
  cfctl r2 rm my-bucket/tmp/ -r
  cfctl r2 rm my-bucket/tmp/ -r --dry-run`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		recursive, _ := cmd.Flags().GetBool("recursive")
		dry, _ := cmd.Flags().GetBool("dry-run")
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		byBucket := map[string][]string{}
		var order []string
		var size int64
		for _, a := range args {
			bucket, key := r2Split(a)
			if bucket == "" {
				return fmt.Errorf("want <bucket>/<key>, got %q", a)
			}
			if _, seen := byBucket[bucket]; !seen {
				order = append(order, bucket)
			}
			if !recursive {
				if key == "" {
					return fmt.Errorf("%q has no key; use -r to delete everything under a prefix", a)
				}
				byBucket[bucket] = append(byBucket[bucket], key)
				continue
			}
			if key == "" && !dry {
				fmt.Fprintln(os.Stderr, ui.Warn("this deletes every object in bucket "+bucket))
			}
			res, err := r2.List(c.ctx, c.c, c.acct, bucket, r2.ListOptions{Prefix: key, Jurisdiction: r2Jurisdiction(cmd)}, nil)
			if err != nil {
				return err
			}
			for _, o := range res.Objects {
				byBucket[bucket] = append(byBucket[bucket], o.Key)
				size += o.Size
			}
		}
		total := 0
		for _, b := range order {
			total += len(byBucket[b])
		}
		if total == 0 {
			fmt.Println(ui.Warn("Nothing to delete"))
			return nil
		}
		if dry {
			for _, b := range order {
				for _, k := range byBucket[b] {
					fmt.Printf("would delete %s/%s\n", b, k)
				}
			}
			fmt.Println(ui.SubtleStyle.Render(fmt.Sprintf("  %d objects (dry run)", total)))
			return nil
		}
		what := fmt.Sprintf("delete %d objects", total)
		if recursive {
			what += fmt.Sprintf(" (%s)", r2.HumanBytes(float64(size)))
		}
		if total == 1 {
			what = fmt.Sprintf("delete %s/%s", order[0], byBucket[order[0]][0])
		}
		if err := confirm(cmd, what); err != nil {
			return err
		}
		for _, b := range order {
			keys := byBucket[b]
			if len(keys) == 1 {
				if _, err := c.result(stReq{Method: "DELETE", Path: r2.ObjectPath(c.acct, b, keys[0]), Header: r2Header(cmd)}); err != nil {
					return err
				}
				continue
			}
			if err := r2DeleteKeys(c, cmd, b, keys); err != nil {
				return err
			}
		}
		return stEmitValue(map[string]any{"deleted": total}, func() error {
			fmt.Println(ui.Success(fmt.Sprintf("Deleted %d objects", total)))
			return nil
		})
	},
}

// r2DUGroup is one row of `r2 du --by`.
type r2DUGroup struct {
	Group   string `json:"group"`
	Objects int    `json:"objects"`
	Bytes   int64  `json:"bytes"`
}

var r2DuCmd = &cobra.Command{
	Use:   "du <bucket>[/prefix]",
	Short: "Count objects and bytes (optionally grouped by extension or prefix)",
	Long: `Count objects and bytes under a bucket or prefix, by listing every object.
--by ext groups by file extension; --by prefix groups by the next path
segment(s) under the prefix (--depth). --top N keeps the N largest groups.

Examples:
  cfctl r2 du my-bucket
  cfctl r2 du my-bucket/assets/ --by prefix
  cfctl r2 du my-bucket --by ext --top 10
  cfctl r2 du my-bucket --by prefix --depth 2 --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bucket, prefix := r2Split(args[0])
		by, _ := cmd.Flags().GetString("by")
		depth, _ := cmd.Flags().GetInt("depth")
		top, _ := cmd.Flags().GetInt("top")
		if by != "" && by != "ext" && by != "prefix" && by != "class" {
			return fmt.Errorf("--by must be ext, prefix, or class")
		}
		c, err := newST(cmd)
		if err != nil {
			return err
		}
		showProgress := !jsonOutput && term.IsTerminal(int(os.Stderr.Fd()))
		n := 0
		res, err := r2.List(c.ctx, c.c, c.acct, bucket, r2.ListOptions{Prefix: prefix, Jurisdiction: r2Jurisdiction(cmd)}, func(page []r2.Object) {
			n += len(page)
			if showProgress {
				fmt.Fprintf(os.Stderr, "\r  listed %d objects…", n)
			}
		})
		if showProgress {
			fmt.Fprint(os.Stderr, "\r\033[K")
		}
		if err != nil {
			return err
		}
		var total int64
		groups := map[string]*r2DUGroup{}
		for _, o := range res.Objects {
			total += o.Size
			if by == "" {
				continue
			}
			var g string
			switch by {
			case "ext":
				g = r2.Ext(o.Key)
			case "prefix":
				g = r2.PrefixAt(o.Key, prefix, depth)
			case "class":
				g = o.StorageClass
			}
			if groups[g] == nil {
				groups[g] = &r2DUGroup{Group: g}
			}
			groups[g].Objects++
			groups[g].Bytes += o.Size
		}
		list := make([]r2DUGroup, 0, len(groups))
		for _, g := range groups {
			list = append(list, *g)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Bytes != list[j].Bytes {
				return list[i].Bytes > list[j].Bytes
			}
			return list[i].Group < list[j].Group
		})
		if top > 0 && len(list) > top {
			list = list[:top]
		}
		out := map[string]any{"bucket": bucket, "prefix": prefix, "objects": len(res.Objects), "bytes": total}
		if by != "" {
			out["by"] = by
			out["groups"] = list
		}
		if res.Truncated {
			out["truncated"] = true
		}
		return stEmitValue(out, func() error {
			fmt.Println(ui.TitleStyle.Render(fmt.Sprintf("📊 %s/%s", bucket, prefix)))
			fmt.Printf("  %s objects, %s (%d bytes)\n", r2.HumanCount(float64(len(res.Objects))), r2.HumanBytes(float64(total)), total)
			if res.Truncated {
				fmt.Println(ui.Warn("listing stopped early; totals are incomplete"))
			}
			if by == "" {
				return nil
			}
			fmt.Println()
			rows := [][]string{}
			for _, g := range list {
				pct := 0.0
				if total > 0 {
					pct = 100 * float64(g.Bytes) / float64(total)
				}
				rows = append(rows, []string{g.Group, r2.HumanCount(float64(g.Objects)), r2.HumanBytes(float64(g.Bytes)), fmt.Sprintf("%.1f%%", pct)})
			}
			stTable([]string{strings.ToUpper(by), "OBJECTS", "SIZE", "SHARE"}, rows)
			return nil
		})
	},
}

// r2ObjectCmd mirrors wrangler's `r2 object get|put|delete` names.
var r2ObjectCmd = &cobra.Command{
	Use:     "object",
	Aliases: []string{"objects"},
	Short:   "Wrangler-style aliases: object get|put|delete|ls|stat",
}

func r2AddPutFlags(c *cobra.Command) {
	c.Flags().StringP("file", "f", "", "Local file to upload (default: stdin)")
	c.Flags().String("content-type", "", "Content-Type (default: detected)")
	c.Flags().String("cache-control", "", "Cache-Control (uses the S3 API)")
	c.Flags().String("content-disposition", "", "Content-Disposition (uses the S3 API)")
	c.Flags().String("content-encoding", "", "Content-Encoding (uses the S3 API)")
	c.Flags().String("content-language", "", "Content-Language (uses the S3 API)")
	c.Flags().StringArray("meta", nil, "Custom metadata key=value (repeatable; uses the S3 API)")
	c.Flags().String("storage-class", "", "Storage class: Standard or InfrequentAccess")
	c.Flags().Int64("multipart-threshold", 100, "Use S3 multipart uploads above this size, in MB (REST maximum: 300)")
	c.Flags().Int64("part-size", 16, "Multipart part size in MiB (minimum 5)")
	c.Flags().Int("concurrency", 4, "Parallel part uploads")
	c.Flags().Bool("s3", false, "Upload through the S3 API even for small files")
	c.Flags().String("parent-access-key-id", "", "Access key ID of the parent R2 token for temporary credentials (default: this token's ID)")
}

func r2AddGetFlags(c *cobra.Command) {
	c.Flags().String("file", "", "Write to this file (default: stdout, or the key's base name on a terminal)")
	c.Flags().BoolP("pipe", "p", false, "Write to stdout even on a terminal")
}

func init() {
	r2LsCmd.Flags().BoolP("recursive", "r", false, "List everything under the prefix (no folder view)")
	r2LsCmd.Flags().Int("limit", 0, "Stop after this many objects (0 = all)")
	r2LsCmd.Flags().Bool("bytes", false, "Show sizes in bytes")
	r2AddGetFlags(r2GetCmd)
	r2AddPutFlags(r2PutCmd)
	r2RmCmd.Flags().BoolP("recursive", "r", false, "Treat each argument as a prefix and delete everything under it")
	r2RmCmd.Flags().Bool("dry-run", false, "Show what would be deleted")
	stYes(r2RmCmd)
	r2DuCmd.Flags().String("by", "", "Group by: ext, prefix, or class")
	r2DuCmd.Flags().Int("depth", 1, "With --by prefix: how many path segments to group by")
	r2DuCmd.Flags().Int("top", 0, "Show only the N largest groups")

	// Wrangler-style aliases under `r2 object`.
	objGet := *r2GetCmd
	objGet.ResetFlags()
	r2AddGetFlags(&objGet)
	objPut := *r2PutCmd
	objPut.ResetFlags()
	r2AddPutFlags(&objPut)
	objDel := *r2RmCmd
	objDel.Use, objDel.Aliases = "delete <bucket>/<key>...", nil
	objDel.ResetFlags()
	objDel.Flags().BoolP("recursive", "r", false, "Treat each argument as a prefix and delete everything under it")
	objDel.Flags().Bool("dry-run", false, "Show what would be deleted")
	stYes(&objDel)
	objStat := *r2StatCmd
	objStat.ResetFlags()
	r2ObjectCmd.AddCommand(&objGet, &objPut, &objDel, &objStat)

	r2Cmd.AddCommand(r2LsCmd, r2StatCmd, r2GetCmd, r2PutCmd, r2RmCmd, r2DuCmd, r2ObjectCmd)
}
