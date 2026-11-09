package s3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/upload"
)

const (
	testBucket = "acme-grp-0f8fad5b-d9cb-469f-a165-70867728950e"
	testUUID   = "0f8fad5b-d9cb-469f-a165-70867728950e"
)

type stubAPI struct {
	calls []string
	errs  map[string]error

	create    *awss3.CreateBucketInput
	lifecycle *awss3.PutBucketLifecycleConfigurationInput
	cors      *awss3.PutBucketCorsInput
	logging   *awss3.PutBucketLoggingInput
	put       *awss3.PutObjectInput
	getOut    *awss3.GetObjectOutput
}

func (s *stubAPI) call(name string) error {
	s.calls = append(s.calls, name)
	return s.errs[name]
}

func (s *stubAPI) CreateBucket(_ context.Context, in *awss3.CreateBucketInput, _ ...func(*awss3.Options)) (*awss3.CreateBucketOutput, error) {
	s.create = in
	return &awss3.CreateBucketOutput{}, s.call("CreateBucket")
}
func (s *stubAPI) PutPublicAccessBlock(_ context.Context, _ *awss3.PutPublicAccessBlockInput, _ ...func(*awss3.Options)) (*awss3.PutPublicAccessBlockOutput, error) {
	return &awss3.PutPublicAccessBlockOutput{}, s.call("PutPublicAccessBlock")
}
func (s *stubAPI) PutBucketEncryption(_ context.Context, _ *awss3.PutBucketEncryptionInput, _ ...func(*awss3.Options)) (*awss3.PutBucketEncryptionOutput, error) {
	return &awss3.PutBucketEncryptionOutput{}, s.call("PutBucketEncryption")
}
func (s *stubAPI) PutBucketVersioning(_ context.Context, _ *awss3.PutBucketVersioningInput, _ ...func(*awss3.Options)) (*awss3.PutBucketVersioningOutput, error) {
	return &awss3.PutBucketVersioningOutput{}, s.call("PutBucketVersioning")
}
func (s *stubAPI) PutBucketLifecycleConfiguration(_ context.Context, in *awss3.PutBucketLifecycleConfigurationInput, _ ...func(*awss3.Options)) (*awss3.PutBucketLifecycleConfigurationOutput, error) {
	s.lifecycle = in
	return &awss3.PutBucketLifecycleConfigurationOutput{}, s.call("PutBucketLifecycleConfiguration")
}
func (s *stubAPI) PutBucketCors(_ context.Context, in *awss3.PutBucketCorsInput, _ ...func(*awss3.Options)) (*awss3.PutBucketCorsOutput, error) {
	s.cors = in
	return &awss3.PutBucketCorsOutput{}, s.call("PutBucketCors")
}
func (s *stubAPI) PutBucketLogging(_ context.Context, in *awss3.PutBucketLoggingInput, _ ...func(*awss3.Options)) (*awss3.PutBucketLoggingOutput, error) {
	s.logging = in
	return &awss3.PutBucketLoggingOutput{}, s.call("PutBucketLogging")
}
func (s *stubAPI) PutObject(_ context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	s.put = in
	return &awss3.PutObjectOutput{}, s.call("PutObject")
}
func (s *stubAPI) GetObject(_ context.Context, _ *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	if err := s.call("GetObject"); err != nil {
		return nil, err
	}
	return s.getOut, nil
}
func (s *stubAPI) DeleteObject(_ context.Context, _ *awss3.DeleteObjectInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	return &awss3.DeleteObjectOutput{}, s.call("DeleteObject")
}

type stubPresigner struct {
	calls int
	in    *awss3.PutObjectInput
	opts  awss3.PresignPostOptions
}

func (s *stubPresigner) PresignPostObject(_ context.Context, in *awss3.PutObjectInput, fns ...func(*awss3.PresignPostOptions)) (*awss3.PresignedPostRequest, error) {
	s.calls++
	s.in = in
	for _, f := range fns {
		f(&s.opts)
	}
	return &awss3.PresignedPostRequest{URL: "https://s3.example/" + testBucket, Values: map[string]string{"key": aws.ToString(in.Key), "policy": "p"}}, nil
}

var fixedNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func newStub(region string) (*Provider, *stubAPI, *stubPresigner) {
	a := &stubAPI{errs: map[string]error{}}
	ps := &stubPresigner{}
	return newWithAPI(a, ps, Options{Region: region, Now: func() time.Time { return fixedNow }}), a, ps
}

var allSteps = []string{"CreateBucket", "PutPublicAccessBlock", "PutBucketEncryption", "PutBucketVersioning", "PutBucketLifecycleConfiguration", "PutBucketCors", "PutBucketLogging"}

func TestEnsureBucketOrder(t *testing.T) {
	p, a, _ := newStub("eu-west-1")
	tpl := bucket.Baseline("https://app.example")
	tpl.LogBucket = "acme-logs"
	if err := p.EnsureBucket(context.Background(), testBucket, tpl); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.calls, allSteps) {
		t.Fatalf("calls = %v", a.calls)
	}
	if a.create.CreateBucketConfiguration == nil || a.create.CreateBucketConfiguration.LocationConstraint != "eu-west-1" {
		t.Fatalf("location = %+v", a.create.CreateBucketConfiguration)
	}
	rules := a.lifecycle.LifecycleConfiguration.Rules
	if len(rules) != 2 || aws.ToInt32(rules[0].NoncurrentVersionExpiration.NoncurrentDays) != int32(tpl.NoncurrentDays) ||
		aws.ToString(rules[1].Filter.Prefix) != bucket.IncomingPrefix || aws.ToInt32(rules[1].Expiration.Days) != int32(tpl.IncomingDays) {
		t.Fatalf("lifecycle = %+v", rules)
	}
	r := a.cors.CORSConfiguration.CORSRules[0]
	if !reflect.DeepEqual(r.AllowedOrigins, []string{tpl.Origin}) || !reflect.DeepEqual(r.AllowedMethods, bucket.CORSMethods) {
		t.Fatalf("cors = %+v", r)
	}
	le := a.logging.BucketLoggingStatus.LoggingEnabled
	if aws.ToString(le.TargetBucket) != "acme-logs" || aws.ToString(le.TargetPrefix) != testBucket+"/" {
		t.Fatalf("logging = %+v", le)
	}
}

func TestEnsureBucketNoLogging(t *testing.T) {
	p, a, _ := newStub("us-east-1")
	if err := p.EnsureBucket(context.Background(), testBucket, bucket.Baseline("https://app.example")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.calls, allSteps[:6]) {
		t.Fatalf("calls = %v", a.calls)
	}
	if a.create.CreateBucketConfiguration != nil {
		t.Fatalf("location = %+v", a.create.CreateBucketConfiguration)
	}
}

func TestEnsureBucketAlreadyOwned(t *testing.T) {
	p, a, _ := newStub("")
	a.errs["CreateBucket"] = &types.BucketAlreadyOwnedByYou{}
	tpl := bucket.Baseline("https://app.example")
	for i := 0; i < 2; i++ {
		if err := p.EnsureBucket(context.Background(), testBucket, tpl); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(a.calls, append(append([]string(nil), allSteps[:6]...), allSteps[:6]...)) {
		t.Fatalf("calls = %v", a.calls)
	}
}

func TestEnsureBucketAlreadyExistsIsError(t *testing.T) {
	p, a, _ := newStub("")
	a.errs["CreateBucket"] = &types.BucketAlreadyExists{}
	err := p.EnsureBucket(context.Background(), testBucket, bucket.Baseline("https://app.example"))
	var taken *types.BucketAlreadyExists
	if !errors.As(err, &taken) {
		t.Fatalf("err = %v; want BucketAlreadyExists", err)
	}
	if !reflect.DeepEqual(a.calls, allSteps[:1]) {
		t.Fatalf("calls = %v", a.calls)
	}
}

func TestEnsureBucketStopsOnError(t *testing.T) {
	p, a, _ := newStub("")
	boom := errors.New("boom")
	a.errs["PutBucketVersioning"] = boom
	tpl := bucket.Baseline("https://app.example")
	tpl.LogBucket = "acme-logs"
	err := p.EnsureBucket(context.Background(), testBucket, tpl)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(a.calls, allSteps[:4]) {
		t.Fatalf("calls = %v", a.calls)
	}
}

func TestEnsureBucketRejectsInvalid(t *testing.T) {
	p, a, _ := newStub("")
	if p.EnsureBucket(context.Background(), "Bad_Bucket", bucket.Baseline("https://app.example")) == nil {
		t.Error("bad name accepted")
	}
	if p.EnsureBucket(context.Background(), testBucket, bucket.Baseline("")) == nil {
		t.Error("empty origin accepted")
	}
	if len(a.calls) != 0 {
		t.Fatalf("calls = %v", a.calls)
	}
}

func TestSignUploadPolicy(t *testing.T) {
	p, _, ps := newStub("")
	key := "incoming/" + testUUID
	lim := upload.Limits{MaxBytes: 25 << 20, ContentTypePrefix: "image/"}
	tg, err := p.SignUpload(context.Background(), testBucket, key, lim)
	if err != nil {
		t.Fatal(err)
	}
	if aws.ToString(ps.in.Bucket) != testBucket || aws.ToString(ps.in.Key) != key {
		t.Fatalf("input = %+v", ps.in)
	}
	if ps.opts.Expires != 5*time.Minute {
		t.Fatalf("Expires = %v", ps.opts.Expires)
	}
	want := []any{
		[]any{"content-length-range", 1, int64(25 << 20)},
		[]any{"starts-with", "$Content-Type", "image/"},
	}
	if !reflect.DeepEqual(ps.opts.Conditions, want) {
		t.Fatalf("Conditions = %#v", ps.opts.Conditions)
	}
	if tg.Method != upload.MethodPost || tg.URL != "https://s3.example/"+testBucket || tg.Fields["key"] != key || tg.Fields["policy"] != "p" {
		t.Fatalf("target = %+v", tg)
	}
	if !tg.Expires.Equal(fixedNow.Add(5 * time.Minute)) {
		t.Fatalf("Expires = %v", tg.Expires)
	}
}

func TestSignUploadRejects(t *testing.T) {
	p, _, ps := newStub("")
	if _, err := p.SignUpload(context.Background(), testBucket, "photos/"+testUUID+"/thumb", upload.Limits{MaxBytes: 10, ContentTypePrefix: "image/"}); err == nil {
		t.Error("non-incoming key accepted")
	}
	if _, err := p.SignUpload(context.Background(), testBucket, "incoming/"+testUUID, upload.Limits{ContentTypePrefix: "image/"}); err == nil {
		t.Error("MaxBytes 0 accepted")
	}
	if ps.calls != 0 {
		t.Fatalf("presigner called %d times", ps.calls)
	}
}

func TestSignUploadRealPresigner(t *testing.T) {
	client := awss3.New(awss3.Options{
		Region: "us-east-1",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}, nil
		}),
	})
	p := New(client, Options{Region: "us-east-1"})
	max := int64(25 << 20)
	tg, err := p.SignUpload(context.Background(), testBucket, "incoming/"+testUUID, upload.Limits{MaxBytes: max, ContentTypePrefix: "image/"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tg.URL, "?") {
		t.Fatalf("URL has a query: %s", tg.URL)
	}
	raw, err := base64.StdEncoding.DecodeString(tg.Fields["policy"])
	if err != nil {
		t.Fatal(err)
	}
	var pol struct {
		Conditions []json.RawMessage `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &pol); err != nil {
		t.Fatal(err)
	}
	var sawLen, sawCT, sawKey bool
	for _, c := range pol.Conditions {
		var m map[string]string
		if json.Unmarshal(c, &m) == nil && m["key"] == "incoming/"+testUUID {
			sawKey = true
		}
		var arr []any
		if json.Unmarshal(c, &arr) != nil || len(arr) != 3 {
			continue
		}
		if arr[0] == "content-length-range" && arr[1] == float64(1) && arr[2] == float64(max) {
			sawLen = true
		}
		if arr[0] == "starts-with" && arr[1] == "$Content-Type" && arr[2] == "image/" {
			sawCT = true
		}
	}
	if !sawLen || !sawCT || !sawKey {
		t.Fatalf("policy = %s", raw)
	}
}

func notFoundHTTP(code int) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: code}},
		Err:      errors.New("http"),
	}}
}

func TestGetNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		nf   bool
	}{
		{"NoSuchKey", &smithy.GenericAPIError{Code: "NoSuchKey"}, true},
		{"NotFound", &smithy.GenericAPIError{Code: "NotFound"}, true},
		{"NoSuchBucket", &smithy.GenericAPIError{Code: "NoSuchBucket"}, true},
		{"404", notFoundHTTP(404), true},
		{"AccessDenied", &smithy.GenericAPIError{Code: "AccessDenied"}, true},
		{"403", notFoundHTTP(403), true},
		{"500", notFoundHTTP(500), false},
		{"other", errors.New("other"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, a, _ := newStub("")
			a.errs["GetObject"] = tt.err
			_, _, err := p.Get(context.Background(), testBucket, "photos/"+testUUID+"/thumb")
			if err == nil || errors.Is(err, store.ErrNotFound) != tt.nf {
				t.Fatalf("err = %v, want notFound=%v", err, tt.nf)
			}
		})
	}
}

func TestGetReturnsInfo(t *testing.T) {
	p, a, _ := newStub("")
	a.getOut = &awss3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("x")), ContentType: aws.String("image/jpeg"), ContentLength: aws.Int64(1)}
	rc, info, err := p.Get(context.Background(), testBucket, "photos/"+testUUID+"/thumb")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if info != (store.ObjectInfo{ContentType: "image/jpeg", Size: 1}) {
		t.Fatalf("info = %+v", info)
	}
}

func TestPutNoBucket(t *testing.T) {
	p, a, _ := newStub("")
	a.errs["PutObject"] = &smithy.GenericAPIError{Code: "NoSuchBucket"}
	err := p.Put(context.Background(), testBucket, "photos/"+testUUID+"/thumb", strings.NewReader("x"), store.ObjectInfo{ContentType: "image/jpeg", Size: 1})
	if !errors.Is(err, store.ErrNoBucket) {
		t.Fatalf("err = %v", err)
	}
	if aws.ToString(a.put.ContentType) != "image/jpeg" || aws.ToInt64(a.put.ContentLength) != 1 {
		t.Fatalf("put = %+v", a.put)
	}
}

func TestDeleteMissingIsNil(t *testing.T) {
	p, a, _ := newStub("")
	a.errs["DeleteObject"] = &smithy.GenericAPIError{Code: "NoSuchKey"}
	if err := p.Delete(context.Background(), testBucket, "photos/"+testUUID+"/thumb"); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidKeyNoCall(t *testing.T) {
	p, a, _ := newStub("")
	ctx := context.Background()
	if _, _, err := p.Get(ctx, testBucket, "../x"); err == nil {
		t.Error("Get accepted ../x")
	}
	if err := p.Put(ctx, testBucket, "../x", strings.NewReader(""), store.ObjectInfo{}); err == nil {
		t.Error("Put accepted ../x")
	}
	if err := p.Delete(ctx, testBucket, "../x"); err == nil {
		t.Error("Delete accepted ../x")
	}
	if len(a.calls) != 0 {
		t.Fatalf("calls = %v", a.calls)
	}
}
