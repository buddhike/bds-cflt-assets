// Package site is the CloudFormation custom resource that copies the React
// build into the site bucket, and empties the bucket when the stack is deleted.
package site

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func init() {
	// The machine's own table of types may be missing, and Go's built-in one is short.
	for ext, kind := range map[string]string{
		".woff2": "font/woff2", ".woff": "font/woff", ".ico": "image/x-icon",
		".map": "application/json", ".txt": "text/plain; charset=utf-8", ".webmanifest": "application/manifest+json",
	} {
		_ = mime.AddExtensionType(ext, kind)
	}
}

const (
	// Built files carry a hash in their name. The page that names them must not be cached.
	indexCache  = "no-cache"
	assetsCache = "public, max-age=31536000, immutable"
	maxZipBytes = 100 * 1024 * 1024
)

// S3 is the part of the S3 client that the deployer uses.
type S3 interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opts ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

// CloudFront is the part of the CloudFront client that the deployer uses.
type CloudFront interface {
	CreateInvalidation(ctx context.Context, in *cloudfront.CreateInvalidationInput, opts ...func(*cloudfront.Options)) (*cloudfront.CreateInvalidationOutput, error)
}

// Deployer does the work of the custom resource.
type Deployer struct {
	S3         S3
	CloudFront CloudFront
	HTTP       *http.Client
	Log        *slog.Logger
	Now        func() time.Time
}

// Properties are the custom resource's properties.
type Properties struct {
	ArtifactBucket string
	ArtifactKey    string
	SiteBucket     string
	DistributionId string
}

// Event is the CloudFormation custom resource request.
type Event struct {
	RequestType        string
	ResponseURL        string
	StackId            string
	RequestId          string
	LogicalResourceId  string
	PhysicalResourceId string
	ResourceProperties Properties
}

type response struct {
	Status             string
	Reason             string `json:",omitempty"`
	PhysicalResourceId string
	StackId            string
	RequestId          string
	LogicalResourceId  string
	Data               map[string]string
}

// Handle does what CloudFormation asked and tells it how that went.
func (d *Deployer) Handle(ctx context.Context, event Event) error {
	// The physical id must not change between updates. If it did (the default is
	// the log stream's name), CloudFormation would delete the "old" resource after
	// an update, and that delete would empty the bucket that was just filled.
	physical := event.ResourceProperties.SiteBucket
	err := d.safeApply(ctx, event, physical)
	status, reason := "SUCCESS", ""
	if err != nil {
		d.Log.Error("Site deployment failed", "request", event.RequestType, "error", err)
		status, reason = "FAILED", truncate(err.Error(), 200)
	}
	return d.respond(ctx, event, response{
		Status: status, Reason: reason, PhysicalResourceId: physical,
		StackId: event.StackId, RequestId: event.RequestId, LogicalResourceId: event.LogicalResourceId,
		Data: map[string]string{},
	})
}

func (d *Deployer) safeApply(ctx context.Context, event Event, physical string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return d.apply(ctx, event, physical)
}

func (d *Deployer) apply(ctx context.Context, event Event, physical string) error {
	p := event.ResourceProperties
	if event.RequestType != "Delete" {
		return d.deploy(ctx, p)
	}
	// Only the real delete empties the bucket. A delete for an id from an
	// older version of this function is cleanup and must change nothing.
	if event.PhysicalResourceId == physical {
		return d.empty(ctx, p.SiteBucket, nil)
	}
	return nil
}

// empty deletes every object of the bucket that is not in keep.
func (d *Deployer) empty(ctx context.Context, bucket string, keep map[string]bool) error {
	var token *string
	for {
		page, err := d.S3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &bucket, ContinuationToken: token})
		if err != nil {
			return fmt.Errorf("list %s: %w", bucket, err)
		}
		var stale []s3types.ObjectIdentifier
		for _, o := range page.Contents {
			if !keep[aws.ToString(o.Key)] {
				stale = append(stale, s3types.ObjectIdentifier{Key: o.Key})
			}
		}
		if len(stale) > 0 {
			out, err := d.S3.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &bucket, Delete: &s3types.Delete{Objects: stale, Quiet: aws.Bool(true)}})
			if err != nil {
				return fmt.Errorf("delete from %s: %w", bucket, err)
			}
			if len(out.Errors) > 0 {
				return fmt.Errorf("delete %s from %s: %s", aws.ToString(out.Errors[0].Key), bucket, aws.ToString(out.Errors[0].Message))
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return nil
		}
		token = page.NextContinuationToken
	}
}

func (d *Deployer) deploy(ctx context.Context, p Properties) error {
	got, err := d.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: &p.ArtifactBucket, Key: &p.ArtifactKey})
	if err != nil {
		return fmt.Errorf("read the web build: %w", err)
	}
	defer got.Body.Close()
	data, err := io.ReadAll(io.LimitReader(got.Body, maxZipBytes))
	if err != nil {
		return fmt.Errorf("read the web build: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open the web build: %w", err)
	}

	wanted := map[string]bool{}
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") || strings.HasPrefix(name, "/") || hasDotDot(name) {
			continue
		}
		body, err := readZipFile(f)
		if err != nil {
			return fmt.Errorf("read %s from the web build: %w", name, err)
		}
		wanted[name] = true
		cache := assetsCache
		if name == "index.html" {
			cache = indexCache
		}
		_, err = d.S3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &p.SiteBucket, Key: aws.String(name), Body: bytes.NewReader(body),
			ContentType: aws.String(contentType(name)), CacheControl: aws.String(cache),
		})
		if err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
	}
	if err := d.empty(ctx, p.SiteBucket, wanted); err != nil {
		return err
	}
	_, err = d.CloudFront.CreateInvalidation(ctx, &cloudfront.CreateInvalidationInput{
		DistributionId: &p.DistributionId,
		InvalidationBatch: &cftypes.InvalidationBatch{
			Paths:           &cftypes.Paths{Quantity: aws.Int32(1), Items: []string{"/*"}},
			CallerReference: aws.String(strconv.FormatInt(d.Now().UnixNano(), 10)),
		},
	})
	if err != nil {
		return fmt.Errorf("invalidate the cache: %w", err)
	}
	return nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func hasDotDot(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func contentType(name string) string {
	if kind := mime.TypeByExtension(path.Ext(name)); kind != "" {
		return kind
	}
	return "application/octet-stream"
}

// respond sends the result to the address CloudFormation gave. It sends
// through a PUT of the JSON, with no content type, as CloudFormation requires.
func (d *Deployer) respond(ctx context.Context, event Event, r response) error {
	if event.ResponseURL == "" {
		return errors.New("the request has no response URL")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// The answer must go out even when the work ran into the Lambda deadline, so it
	// is not tied to the invocation's context.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, event.ResponseURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "")
	req.ContentLength = int64(len(body))
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("answer CloudFormation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("CloudFormation answered %d", resp.StatusCode)
	}
	return nil
}

func truncate(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}
