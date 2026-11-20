// Package s3 is the S3 store.Provider. It never pre-signs GETs:
// objects are served by the app after it authorizes the request.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/Kolonnade/Poikile/store"
)

// api is the subset of *awss3.Client the provider uses. It has no List or
// HeadBucket on purpose: keys come from the app's database. HeadObject
// names a key's current version so Delete can remove it by id.
type api interface {
	CreateBucket(ctx context.Context, params *awss3.CreateBucketInput, optFns ...func(*awss3.Options)) (*awss3.CreateBucketOutput, error)
	PutPublicAccessBlock(ctx context.Context, params *awss3.PutPublicAccessBlockInput, optFns ...func(*awss3.Options)) (*awss3.PutPublicAccessBlockOutput, error)
	PutBucketEncryption(ctx context.Context, params *awss3.PutBucketEncryptionInput, optFns ...func(*awss3.Options)) (*awss3.PutBucketEncryptionOutput, error)
	PutBucketVersioning(ctx context.Context, params *awss3.PutBucketVersioningInput, optFns ...func(*awss3.Options)) (*awss3.PutBucketVersioningOutput, error)
	PutBucketLifecycleConfiguration(ctx context.Context, params *awss3.PutBucketLifecycleConfigurationInput, optFns ...func(*awss3.Options)) (*awss3.PutBucketLifecycleConfigurationOutput, error)
	PutBucketCors(ctx context.Context, params *awss3.PutBucketCorsInput, optFns ...func(*awss3.Options)) (*awss3.PutBucketCorsOutput, error)
	PutBucketLogging(ctx context.Context, params *awss3.PutBucketLoggingInput, optFns ...func(*awss3.Options)) (*awss3.PutBucketLoggingOutput, error)
	PutObject(ctx context.Context, params *awss3.PutObjectInput, optFns ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *awss3.GetObjectInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *awss3.DeleteObjectInput, optFns ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
	HeadObject(ctx context.Context, params *awss3.HeadObjectInput, optFns ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	DeleteBucket(ctx context.Context, params *awss3.DeleteBucketInput, optFns ...func(*awss3.Options)) (*awss3.DeleteBucketOutput, error)
}

// presigner signs browser uploads. Only POST: no GET or PUT pre-signing.
type presigner interface {
	PresignPostObject(ctx context.Context, params *awss3.PutObjectInput, optFns ...func(*awss3.PresignPostOptions)) (*awss3.PresignedPostRequest, error)
}

// Options configure a Provider.
type Options struct {
	// Region is used for the bucket location constraint (none for
	// us-east-1 or empty).
	Region string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// AllowUnsupported is for local S3-compatible dev servers such as
	// MinIO: an EnsureBucket step the server answers with NotImplemented
	// is skipped instead of failing. Leave it false on AWS, where every
	// step must apply.
	AllowUnsupported bool
}

// Provider is a store.Provider on S3 or an S3-compatible endpoint.
type Provider struct {
	api     api
	presign presigner
	opts    Options
}

var _ store.Provider = (*Provider)(nil)

// New wraps client. Use one Provider per credential (IAM role), so each
// role's permissions bound what its Provider can do.
func New(client *awss3.Client, o Options) *Provider {
	return newWithAPI(client, awss3.NewPresignClient(client), o)
}

func newWithAPI(a api, p presigner, o Options) *Provider {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Provider{api: a, presign: p, opts: o}
}

// FromProfile builds a Provider from a shared-config profile, so each IAM
// role is a profile and gets its own Provider. Empty profile or region use
// the SDK's default chain; a non-empty endpoint selects an S3-compatible
// service with path-style addressing.
func FromProfile(ctx context.Context, profile, region, endpoint string) (*Provider, error) {
	var lo []func(*config.LoadOptions) error
	if profile != "" {
		lo = append(lo, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		lo = append(lo, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, lo...)
	if err != nil {
		return nil, fmt.Errorf("s3: load AWS config: %w", err)
	}
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})
	return New(client, Options{Region: cfg.Region}), nil
}

// FromStatic builds a Provider for an S3-compatible endpoint, such as a
// local MinIO, with static credentials and path-style addressing. An empty
// o.Region means us-east-1.
func FromStatic(endpoint, accessKey, secretKey string, o Options) (*Provider, error) {
	if endpoint == "" || accessKey == "" || secretKey == "" {
		return nil, errors.New("s3: endpoint, access key and secret key are required")
	}
	if o.Region == "" {
		o.Region = "us-east-1"
	}
	client := awss3.New(awss3.Options{
		Region:       o.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
	})
	return New(client, o), nil
}

// Get opens an object. A missing bucket or key returns store.ErrNotFound.
func (p *Provider) Get(ctx context.Context, b, key string) (io.ReadCloser, store.ObjectInfo, error) {
	if err := store.ValidKey(key); err != nil {
		return nil, store.ObjectInfo{}, err
	}
	out, err := p.api.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(b), Key: aws.String(key)})
	if err != nil {
		if notFound(err) || denied(err) {
			return nil, store.ObjectInfo{}, fmt.Errorf("s3: get %s/%s: %w", b, key, store.ErrNotFound)
		}
		return nil, store.ObjectInfo{}, fmt.Errorf("s3: get %s/%s: %w", b, key, err)
	}
	return out.Body, store.ObjectInfo{ContentType: aws.ToString(out.ContentType), Size: aws.ToInt64(out.ContentLength)}, nil
}

// Put writes an object. A missing bucket returns store.ErrNoBucket.
func (p *Provider) Put(ctx context.Context, b, key string, body io.Reader, info store.ObjectInfo) error {
	if err := store.ValidKey(key); err != nil {
		return err
	}
	in := &awss3.PutObjectInput{Bucket: aws.String(b), Key: aws.String(key), Body: body}
	if info.ContentType != "" {
		in.ContentType = aws.String(info.ContentType)
	}
	if info.Size > 0 {
		in.ContentLength = aws.Int64(info.Size)
	}
	var opts []func(*awss3.Options)
	if _, ok := body.(io.Seeker); !ok {
		// A stream can't be hashed before it is sent, so sign it as
		// UNSIGNED-PAYLOAD; without this the SDK refuses it over plain
		// HTTP (a local MinIO).
		opts = append(opts, awss3.WithAPIOptions(v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware))
	}
	if _, err := p.api.PutObject(ctx, in, opts...); err != nil {
		if noBucket(err) {
			return fmt.Errorf("s3: put %s/%s: %w", b, key, store.ErrNoBucket)
		}
		return fmt.Errorf("s3: put %s/%s: %w", b, key, err)
	}
	return nil
}

// maxVersions bounds how many versions of one key Delete removes.
const maxVersions = 100

// Delete removes an object permanently; a missing one is not an error. The
// bucket is versioned, so a plain DeleteObject would only add a delete
// marker and the bucket could never be emptied. Instead Delete asks
// HeadObject for the current version (or delete marker) and deletes it by
// id, until the key is plainly absent. It never lists. A deleted object
// can't be restored from S3; versioning still keeps what an overwrite
// replaces, until Delete or NoncurrentDays.
func (p *Provider) Delete(ctx context.Context, b, key string) error {
	if err := store.ValidKey(key); err != nil {
		return err
	}
	for range maxVersions {
		vid, ok, err := p.currentVersion(ctx, b, key)
		if err != nil {
			return fmt.Errorf("s3: delete %s/%s: %w", b, key, err)
		}
		if !ok {
			return nil
		}
		in := &awss3.DeleteObjectInput{Bucket: aws.String(b), Key: aws.String(key), VersionId: vid}
		if _, err := p.api.DeleteObject(ctx, in); err != nil {
			if notFound(err) {
				return nil
			}
			return fmt.Errorf("s3: delete %s/%s: %w", b, key, err)
		}
		if vid == nil {
			// Unversioned: the plain delete removed it.
			return nil
		}
	}
	return fmt.Errorf("s3: delete %s/%s: more than %d versions", b, key, maxVersions)
}

// currentVersion returns the id of key's current version or delete marker
// (nil when the bucket is unversioned), and false when nothing is there. A
// 403 without a delete marker counts as absent, as in Get (see denied).
func (p *Provider) currentVersion(ctx context.Context, b, key string) (*string, bool, error) {
	out, err := p.api.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(b), Key: aws.String(key)})
	if err == nil {
		vid := out.VersionId
		if aws.ToString(vid) == "" {
			vid = nil
		}
		return vid, true, nil
	}
	var re *awshttp.ResponseError
	if errors.As(err, &re) && re.Response != nil && re.Response.Header.Get("x-amz-delete-marker") == "true" {
		if vid := re.Response.Header.Get("x-amz-version-id"); vid != "" {
			return aws.String(vid), true, nil
		}
	}
	if notFound(err) || denied(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func notFound(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return true
		}
	}
	var re *awshttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == 404
}

func noBucket(err error) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == "NoSuchBucket"
}

// denied reports a 403 AccessDenied. Get treats it as not found: the
// runtime role has no s3:ListBucket (README rule 4), and without it S3
// cannot reveal that a key is absent, so it answers a missing key with 403
// instead of 404. A real permission problem still surfaces, as an error,
// on Put and EnsureBucket.
func denied(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) && ae.ErrorCode() == "AccessDenied" {
		return true
	}
	var re *awshttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == 403
}
