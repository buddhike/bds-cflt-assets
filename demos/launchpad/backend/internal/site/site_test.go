package site

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var ctx = context.Background()

type object struct {
	body        []byte
	contentType string
	cache       string
}

// fakeS3 keeps buckets in memory and pages its listings two keys at a time.
type fakeS3 struct {
	buckets map[string]map[string]object
	getErr  error
}

func newS3() *fakeS3 { return &fakeS3{buckets: map[string]map[string]object{}} }

func (f *fakeS3) bucket(name string) map[string]object {
	if f.buckets[name] == nil {
		f.buckets[name] = map[string]object{}
	}
	return f.buckets[name]
}

func (f *fakeS3) keys(name string) []string {
	var keys []string
	for k := range f.bucket(name) {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	o, ok := f.bucket(*in.Bucket)[*in.Key]
	if !ok {
		return nil, errors.New("NoSuchKey")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(o.body))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, _ := io.ReadAll(in.Body)
	f.bucket(*in.Bucket)[*in.Key] = object{body, aws.ToString(in.ContentType), aws.ToString(in.CacheControl)}
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	keys := f.keys(*in.Bucket)
	start := 0
	if in.ContinuationToken != nil {
		start = sort.SearchStrings(keys, *in.ContinuationToken)
	}
	end := min(start+2, len(keys))
	out := &s3.ListObjectsV2Output{}
	for _, k := range keys[start:end] {
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k)})
	}
	if end < len(keys) {
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(keys[end])
	}
	return out, nil
}

func (f *fakeS3) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	for _, o := range in.Delete.Objects {
		delete(f.bucket(*in.Bucket), *o.Key)
	}
	return &s3.DeleteObjectsOutput{}, nil
}

type fakeCF struct {
	invalidations []*cloudfront.CreateInvalidationInput
	err           error
}

func (f *fakeCF) CreateInvalidation(_ context.Context, in *cloudfront.CreateInvalidationInput, _ ...func(*cloudfront.Options)) (*cloudfront.CreateInvalidationOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.invalidations = append(f.invalidations, in)
	return &cloudfront.CreateInvalidationOutput{}, nil
}

type answers struct {
	bodies  []map[string]any
	headers []http.Header
	status  int
}

func (a *answers) RoundTrip(req *http.Request) (*http.Response, error) {
	data, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(data, &body)
	a.bodies = append(a.bodies, body)
	a.headers = append(a.headers, req.Header.Clone())
	status := a.status
	if status == 0 {
		status = 200
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

type rig struct {
	s3      *fakeS3
	cf      *fakeCF
	answers *answers
	d       *Deployer
}

func newRig() *rig {
	r := &rig{s3: newS3(), cf: &fakeCF{}, answers: &answers{}}
	r.d = &Deployer{
		S3: r.s3, CloudFront: r.cf, HTTP: &http.Client{Transport: r.answers},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return time.Unix(1_700_000_000, 5) },
	}
	return r
}

func zipOf(files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte(files[n]))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func (r *rig) event(kind string) Event {
	return Event{
		RequestType: kind, ResponseURL: "https://cfn.example/answer", StackId: "stack", RequestId: "req", LogicalResourceId: "SiteContent",
		PhysicalResourceId: "site-bucket",
		ResourceProperties: Properties{ArtifactBucket: "artifacts", ArtifactKey: "launchpad/frontend.zip", SiteBucket: "site-bucket", DistributionId: "DIST"},
	}
}

func (r *rig) lastAnswer(t *testing.T) map[string]any {
	t.Helper()
	if len(r.answers.bodies) == 0 {
		t.Fatal("CloudFormation was not answered")
	}
	return r.answers.bodies[len(r.answers.bodies)-1]
}

func TestADeployCopiesTheBuildAndRemovesWhatIsStale(t *testing.T) {
	r := newRig()
	r.s3.bucket("artifacts")["launchpad/frontend.zip"] = object{body: zipOf(map[string]string{
		"index.html": "<html>", "assets/app-1.js": "js", "assets/app-1.css": "css", "assets/inter.woff2": "font", "favicon.ico": "ico",
		"assets/": "", "../evil.txt": "evil", "/abs.txt": "abs", "a/../b.txt": "b",
	})}
	for _, old := range []string{"assets/app-0.js", "old.html", "assets/app-1.js"} {
		r.s3.bucket("site-bucket")[old] = object{body: []byte("old")}
	}
	if err := r.d.Handle(ctx, r.event("Update")); err != nil {
		t.Fatal(err)
	}

	want := []string{"assets/app-1.css", "assets/app-1.js", "assets/inter.woff2", "favicon.ico", "index.html"}
	if got := r.s3.keys("site-bucket"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("site holds %v, want %v", got, want)
	}
	site := r.s3.bucket("site-bucket")
	if string(site["assets/app-1.js"].body) != "js" {
		t.Error("a file that existed was not replaced")
	}
	if got := site["index.html"]; got.cache != "no-cache" || !strings.HasPrefix(got.contentType, "text/html") {
		t.Errorf("index.html: %+v", got)
	}
	if got := site["assets/app-1.js"]; got.cache != "public, max-age=31536000, immutable" || !strings.Contains(got.contentType, "javascript") {
		t.Errorf("app-1.js: %+v", got)
	}
	if got := site["assets/app-1.css"].contentType; !strings.HasPrefix(got, "text/css") {
		t.Errorf("css type %q", got)
	}
	if got := site["assets/inter.woff2"].contentType; got != "font/woff2" {
		t.Errorf("font type %q", got)
	}
	if len(r.cf.invalidations) != 1 || *r.cf.invalidations[0].DistributionId != "DIST" ||
		r.cf.invalidations[0].InvalidationBatch.Paths.Items[0] != "/*" || *r.cf.invalidations[0].InvalidationBatch.CallerReference == "" {
		t.Errorf("invalidations %+v", r.cf.invalidations)
	}

	answer := r.lastAnswer(t)
	if answer["Status"] != "SUCCESS" || answer["PhysicalResourceId"] != "site-bucket" || answer["StackId"] != "stack" || answer["RequestId"] != "req" || answer["LogicalResourceId"] != "SiteContent" {
		t.Errorf("answer %v", answer)
	}
	if got := r.answers.headers[0].Get("Content-Type"); got != "" {
		t.Errorf("the answer has a content type %q, which the presigned URL does not allow", got)
	}
}

func TestADeployWithMoreFilesThanAPageIsComplete(t *testing.T) {
	r := newRig()
	for i := range 7 {
		r.s3.bucket("site-bucket")["stale-"+string(rune('a'+i))] = object{}
	}
	r.s3.bucket("artifacts")["launchpad/frontend.zip"] = object{body: zipOf(map[string]string{"index.html": "x"})}
	if err := r.d.Handle(ctx, r.event("Create")); err != nil {
		t.Fatal(err)
	}
	if got := r.s3.keys("site-bucket"); len(got) != 1 || got[0] != "index.html" {
		t.Fatalf("site holds %v", got)
	}
}

func TestTheRealDeleteEmptiesTheBucket(t *testing.T) {
	r := newRig()
	for _, k := range []string{"index.html", "a", "b", "c", "d"} {
		r.s3.bucket("site-bucket")[k] = object{}
	}
	if err := r.d.Handle(ctx, r.event("Delete")); err != nil {
		t.Fatal(err)
	}
	if got := r.s3.keys("site-bucket"); len(got) != 0 {
		t.Fatalf("site holds %v", got)
	}
	if r.lastAnswer(t)["Status"] != "SUCCESS" {
		t.Fatalf("answer %v", r.lastAnswer(t))
	}
}

// An update used to empty the bucket it had just filled: CloudFormation sent a
// cleanup Delete for the physical id of an older version of this function.
func TestADeleteForAnOlderPhysicalIdChangesNothing(t *testing.T) {
	r := newRig()
	r.s3.bucket("site-bucket")["index.html"] = object{}
	ev := r.event("Delete")
	ev.PhysicalResourceId = "2024/01/01/[$LATEST]abcdef"
	if err := r.d.Handle(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := r.s3.keys("site-bucket"); len(got) != 1 {
		t.Fatalf("site holds %v", got)
	}
	answer := r.lastAnswer(t)
	if answer["Status"] != "SUCCESS" || answer["PhysicalResourceId"] != "site-bucket" {
		t.Fatalf("answer %v", answer)
	}
}

func TestAFailureIsReportedWithTheStablePhysicalId(t *testing.T) {
	r := newRig()
	r.s3.getErr = errors.New("AccessDenied: " + strings.Repeat("x", 500))
	ev := r.event("Create")
	ev.PhysicalResourceId = ""
	if err := r.d.Handle(ctx, ev); err != nil {
		t.Fatal(err)
	}
	answer := r.lastAnswer(t)
	reason, _ := answer["Reason"].(string)
	if answer["Status"] != "FAILED" || answer["PhysicalResourceId"] != "site-bucket" || !strings.Contains(reason, "AccessDenied") || len(reason) > 200 {
		t.Fatalf("answer %v", answer)
	}
	if len(r.cf.invalidations) != 0 {
		t.Error("the cache was invalidated after a failure")
	}
}

func TestABuildThatIsNotAZipFails(t *testing.T) {
	r := newRig()
	r.s3.bucket("artifacts")["launchpad/frontend.zip"] = object{body: []byte("not a zip")}
	_ = r.d.Handle(ctx, r.event("Create"))
	if r.lastAnswer(t)["Status"] != "FAILED" {
		t.Fatalf("answer %v", r.lastAnswer(t))
	}
}

func TestAnAnswerThatIsRefusedIsAnError(t *testing.T) {
	r := newRig()
	r.answers.status = 403
	if err := r.d.Handle(ctx, r.event("Delete")); err == nil {
		t.Fatal("want an error when CloudFormation does not accept the answer")
	}
}

func TestTheEventDecodesFromCloudFormationsJSON(t *testing.T) {
	raw := `{"RequestType":"Update","ResponseURL":"https://x","StackId":"s","RequestId":"r","LogicalResourceId":"SiteContent",
	"PhysicalResourceId":"b","ServiceToken":"arn","ResourceProperties":{"ServiceToken":"arn","ArtifactBucket":"a","ArtifactKey":"k",
	"DeployerVersion":"3","SiteBucket":"b","DistributionId":"D"}}`
	var ev Event
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	want := Properties{ArtifactBucket: "a", ArtifactKey: "k", SiteBucket: "b", DistributionId: "D"}
	if ev.ResponseURL != "https://x" || ev.ResourceProperties != want || ev.RequestType != "Update" || ev.PhysicalResourceId != "b" {
		t.Fatalf("%+v", ev)
	}
}

func TestCloudFormationIsAnsweredEvenAfterTheInvocationIsCancelled(t *testing.T) {
	r := newRig()
	r.s3.getErr = context.DeadlineExceeded
	expired, cancel := context.WithCancel(ctx)
	cancel()
	if err := r.d.Handle(expired, r.event("Create")); err != nil {
		t.Fatal(err)
	}
	if got := r.lastAnswer(t)["Status"]; got != "FAILED" {
		t.Fatalf("answer %v", r.lastAnswer(t))
	}
}

type panickingS3 struct{ *fakeS3 }

func (panickingS3) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	panic("boom")
}

func TestAPanicIsReportedToCloudFormation(t *testing.T) {
	r := newRig()
	r.d.S3 = panickingS3{r.s3}
	if err := r.d.Handle(ctx, r.event("Create")); err != nil {
		t.Fatal(err)
	}
	answer := r.lastAnswer(t)
	if answer["Status"] != "FAILED" || answer["PhysicalResourceId"] != "site-bucket" {
		t.Fatalf("answer %v", answer)
	}
}
