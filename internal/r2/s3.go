package r2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/dorkitude/cfctl/internal/api"
)

// TempCredentials are short-lived S3 credentials from
// POST /accounts/{id}/r2/temp-access-credentials.
type TempCredentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
}

// S3EndpointEnv overrides the S3 endpoint (tests, or a custom gateway).
const S3EndpointEnv = "CFCTL_R2_S3_ENDPOINT"

// S3Endpoint returns the account's R2 S3 endpoint for a jurisdiction.
func S3Endpoint(acct, jurisdiction string) string {
	if e := strings.TrimSpace(os.Getenv(S3EndpointEnv)); e != "" {
		return strings.TrimSuffix(e, "/")
	}
	if jurisdiction != "" && jurisdiction != "default" {
		return fmt.Sprintf("https://%s.%s.r2.cloudflarestorage.com", acct, jurisdiction)
	}
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", acct)
}

// NewS3 builds an S3 client for R2. It uses cfctl's shared HTTP client, so the
// read-only guard (only GET/HEAD/OPTIONS go out) and --debug logging apply.
func NewS3(acct, jurisdiction string, creds TempCredentials) *s3.Client {
	return s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(S3Endpoint(acct, jurisdiction)),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken),
		HTTPClient:   api.NewHTTPClient(),
		// R2 doesn't need the SDK's default trailing checksums.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		RetryMaxAttempts:           3,
	})
}

// PutOptions are the object metadata for an upload.
type PutOptions struct {
	ContentType        string
	CacheControl       string
	ContentDisposition string
	ContentEncoding    string
	ContentLanguage    string
	StorageClass       string
	Metadata           map[string]string
}

// MultipartOptions tune multipart uploads.
type MultipartOptions struct {
	PartSize    int64 // bytes; ≥5 MiB
	Concurrency int
	// Progress is called after each part with the bytes uploaded so far.
	Progress func(done int64)
}

// DefaultPartSize is 16 MiB.
const DefaultPartSize int64 = 16 << 20

// MinPartSize is S3's minimum for every part but the last.
const MinPartSize int64 = 5 << 20

// UploadFile uploads a local file to bucket/key: one PutObject for files
// up to the part size, a multipart upload above it. A failed multipart upload
// is aborted.
func UploadFile(ctx context.Context, c *s3.Client, bucket, key, path string, o PutOptions, mo MultipartOptions) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if mo.PartSize < MinPartSize {
		mo.PartSize = DefaultPartSize
	}
	if mo.Concurrency < 1 {
		mo.Concurrency = 4
	}
	if st.Size() <= mo.PartSize {
		in := &s3.PutObjectInput{Bucket: &bucket, Key: &key, Body: io.NewSectionReader(f, 0, st.Size()), ContentLength: aws.Int64(st.Size())}
		applyPut(in, o)
		_, err := c.PutObject(ctx, in)
		if err == nil && mo.Progress != nil {
			mo.Progress(st.Size())
		}
		return err
	}
	return uploadMultipart(ctx, c, bucket, key, f, st.Size(), o, mo)
}

func applyPut(in *s3.PutObjectInput, o PutOptions) {
	if o.ContentType != "" {
		in.ContentType = aws.String(o.ContentType)
	}
	if o.CacheControl != "" {
		in.CacheControl = aws.String(o.CacheControl)
	}
	if o.ContentDisposition != "" {
		in.ContentDisposition = aws.String(o.ContentDisposition)
	}
	if o.ContentEncoding != "" {
		in.ContentEncoding = aws.String(o.ContentEncoding)
	}
	if o.ContentLanguage != "" {
		in.ContentLanguage = aws.String(o.ContentLanguage)
	}
	if o.StorageClass != "" {
		in.StorageClass = types.StorageClass(o.StorageClass)
	}
	if len(o.Metadata) > 0 {
		in.Metadata = o.Metadata
	}
}

func uploadMultipart(ctx context.Context, c *s3.Client, bucket, key string, f io.ReaderAt, size int64, o PutOptions, mo MultipartOptions) error {
	create := &s3.CreateMultipartUploadInput{Bucket: &bucket, Key: &key}
	if o.ContentType != "" {
		create.ContentType = aws.String(o.ContentType)
	}
	if o.CacheControl != "" {
		create.CacheControl = aws.String(o.CacheControl)
	}
	if o.ContentDisposition != "" {
		create.ContentDisposition = aws.String(o.ContentDisposition)
	}
	if o.ContentEncoding != "" {
		create.ContentEncoding = aws.String(o.ContentEncoding)
	}
	if o.ContentLanguage != "" {
		create.ContentLanguage = aws.String(o.ContentLanguage)
	}
	if o.StorageClass != "" {
		create.StorageClass = types.StorageClass(o.StorageClass)
	}
	if len(o.Metadata) > 0 {
		create.Metadata = o.Metadata
	}
	up, err := c.CreateMultipartUpload(ctx, create)
	if err != nil {
		return fmt.Errorf("starting multipart upload: %w", err)
	}
	uploadID := aws.ToString(up.UploadId)
	abort := func(cause error) error {
		_, _ = c.AbortMultipartUpload(context.WithoutCancel(ctx), &s3.AbortMultipartUploadInput{Bucket: &bucket, Key: &key, UploadId: &uploadID})
		return cause
	}

	nParts := int((size + mo.PartSize - 1) / mo.PartSize)
	parts := make([]types.CompletedPart, nParts)
	jobs := make(chan int)
	var (
		mu       sync.Mutex
		firstErr error
		done     int64
		wg       sync.WaitGroup
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for w := 0; w < mo.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				off := int64(i) * mo.PartSize
				n := min(mo.PartSize, size-off)
				num := int32(i + 1)
				out, err := c.UploadPart(ctx, &s3.UploadPartInput{
					Bucket: &bucket, Key: &key, UploadId: &uploadID, PartNumber: &num,
					Body: io.NewSectionReader(f, off, n), ContentLength: aws.Int64(n),
				})
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("part %d: %w", num, err)
						cancel()
					}
				} else {
					parts[i] = types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(num)}
					done += n
					if mo.Progress != nil {
						mo.Progress(done)
					}
				}
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < nParts; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return abort(firstErr)
	}
	if err := ctx.Err(); err != nil {
		return abort(err)
	}
	sort.Slice(parts, func(i, j int) bool { return *parts[i].PartNumber < *parts[j].PartNumber })
	_, err = c.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &bucket, Key: &key, UploadId: &uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return abort(fmt.Errorf("completing multipart upload: %w", err))
	}
	return nil
}

// DownloadFile writes bucket/key to path (via a temp file, renamed into place).
func DownloadFile(ctx context.Context, c *s3.Client, bucket, key, path string) (int64, error) {
	out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return 0, err
	}
	defer out.Body.Close()
	return WriteFileAtomic(path, out.Body)
}

// WriteFileAtomic streams r into path via a temp file in the same directory.
func WriteFileAtomic(path string, r io.Reader) (int64, error) {
	dir := "."
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		dir = path[:i+1]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, ".cfctl-download-*")
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return n, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return n, err
	}
	return n, nil
}

// DeleteKeys deletes keys with S3 DeleteObjects (1000 per request) and
// returns the keys that failed.
func DeleteKeys(ctx context.Context, c *s3.Client, bucket string, keys []string) ([]string, error) {
	var failed []string
	for start := 0; start < len(keys); start += 1000 {
		end := min(start+1000, len(keys))
		ids := make([]types.ObjectIdentifier, 0, end-start)
		for _, k := range keys[start:end] {
			ids = append(ids, types.ObjectIdentifier{Key: aws.String(k)})
		}
		out, err := c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &bucket, Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)}})
		if err != nil {
			return failed, err
		}
		for _, e := range out.Errors {
			failed = append(failed, aws.ToString(e.Key))
		}
	}
	return failed, nil
}

// IsReadOnly reports whether err (possibly wrapped by the AWS SDK) is the
// read-only guard's refusal.
func IsReadOnly(err error) bool { return errors.Is(err, api.ErrReadOnly) }

// statusOf extracts an HTTP status from an AWS SDK error (0 if none).
func statusOf(err error) int {
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

// IsNotFound reports whether an S3 error is a 404.
func IsNotFound(err error) bool { return statusOf(err) == http.StatusNotFound }
