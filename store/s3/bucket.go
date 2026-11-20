package s3

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store"
)

// EnsureBucket applies the whole template every time, so applying and
// repairing are the same call. It stops at the first failing step.
//
// With Options.AllowUnsupported, the public access block, encryption, CORS
// and logging steps are skipped when the server says it does not implement
// them (see unsupported). Creating the bucket, versioning and lifecycle are
// never skipped: deletion and expiry depend on them.
func (p *Provider) EnsureBucket(ctx context.Context, name string, t bucket.Template) error {
	if err := bucket.ValidateName(name); err != nil {
		return err
	}
	if err := t.Validate(); err != nil {
		return err
	}
	wrap := func(step string, err error) error {
		if err == nil {
			return nil
		}
		return fmt.Errorf("s3: ensure %s: %s: %w", name, step, err)
	}
	optional := func(step string, err error) error {
		if err != nil && p.opts.AllowUnsupported && unsupported(err) {
			return nil
		}
		return wrap(step, err)
	}
	b := aws.String(name)

	if err := wrap("create", p.createBucket(ctx, name)); err != nil {
		return err
	}
	_, err := p.api.PutPublicAccessBlock(ctx, &awss3.PutPublicAccessBlockInput{
		Bucket: b,
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	if err := optional("public access block", err); err != nil {
		return err
	}
	_, err = p.api.PutBucketEncryption(ctx, &awss3.PutBucketEncryptionInput{
		Bucket: b,
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{{
				ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
					SSEAlgorithm: types.ServerSideEncryptionAes256,
				},
			}},
		},
	})
	if err := optional("encryption", err); err != nil {
		return err
	}
	_, err = p.api.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{
		Bucket:                  b,
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	})
	if err := wrap("versioning", err); err != nil {
		return err
	}
	_, err = p.api.PutBucketLifecycleConfiguration(ctx, &awss3.PutBucketLifecycleConfigurationInput{
		Bucket: b,
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{
			Rules: []types.LifecycleRule{
				{
					ID:     aws.String("expire-noncurrent-versions"),
					Status: types.ExpirationStatusEnabled,
					Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")},
					NoncurrentVersionExpiration: &types.NoncurrentVersionExpiration{
						NoncurrentDays: aws.Int32(int32(t.NoncurrentDays)),
					},
				},
				{
					ID:         aws.String("expire-incoming"),
					Status:     types.ExpirationStatusEnabled,
					Filter:     &types.LifecycleRuleFilter{Prefix: aws.String(bucket.IncomingPrefix)},
					Expiration: &types.LifecycleExpiration{Days: aws.Int32(int32(t.IncomingDays))},
				},
			},
		},
	})
	if err := wrap("lifecycle", err); err != nil {
		return err
	}
	_, err = p.api.PutBucketCors(ctx, &awss3.PutBucketCorsInput{
		Bucket: b,
		CORSConfiguration: &types.CORSConfiguration{
			CORSRules: []types.CORSRule{{
				AllowedOrigins: []string{t.Origin},
				AllowedMethods: append([]string(nil), bucket.CORSMethods...),
				AllowedHeaders: []string{"*"},
				MaxAgeSeconds:  aws.Int32(3000),
			}},
		},
	})
	if err := optional("cors", err); err != nil {
		return err
	}
	if t.LogBucket != "" {
		_, err = p.api.PutBucketLogging(ctx, &awss3.PutBucketLoggingInput{
			Bucket: b,
			BucketLoggingStatus: &types.BucketLoggingStatus{
				LoggingEnabled: &types.LoggingEnabled{
					TargetBucket: aws.String(t.LogBucket),
					TargetPrefix: aws.String(name + "/"),
				},
			},
		})
		if err := optional("logging", err); err != nil {
			return err
		}
	}
	return nil
}

// createBucket creates the bucket; BucketAlreadyOwnedByYou is success.
func (p *Provider) createBucket(ctx context.Context, name string) error {
	in := &awss3.CreateBucketInput{Bucket: aws.String(name)}
	if r := p.opts.Region; r != "" && r != "us-east-1" {
		in.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(r),
		}
	}
	_, err := p.api.CreateBucket(ctx, in)
	if err == nil {
		return nil
	}
	var owned *types.BucketAlreadyOwnedByYou
	if errors.As(err, &owned) {
		return nil
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) && coded.ErrorCode() == "BucketAlreadyOwnedByYou" {
		return nil
	}
	return err
}

// unsupported reports how S3-compatible servers refuse a bucket
// configuration they do not implement. MinIO answers CORS, and encryption
// without a KMS, with 501 NotImplemented, and the public access block and
// logging with 400 MalformedXML.
func unsupported(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NotImplemented", "MalformedXML":
			return true
		}
	}
	var re *awshttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == 501
}

// DeleteBucket removes an empty bucket. A bucket that still holds objects
// (or, versioned, any version or delete marker) returns
// store.ErrBucketNotEmpty; a missing one is not an error.
func (p *Provider) DeleteBucket(ctx context.Context, name string) error {
	if err := bucket.ValidateName(name); err != nil {
		return err
	}
	_, err := p.api.DeleteBucket(ctx, &awss3.DeleteBucketInput{Bucket: aws.String(name)})
	if err == nil || noBucket(err) {
		return nil
	}
	var ae smithy.APIError
	if errors.As(err, &ae) && ae.ErrorCode() == "BucketNotEmpty" {
		return fmt.Errorf("s3: delete bucket %s: %w", name, store.ErrBucketNotEmpty)
	}
	var re *awshttp.ResponseError
	if errors.As(err, &re) && re.HTTPStatusCode() == 404 {
		return nil
	}
	return fmt.Errorf("s3: delete bucket %s: %w", name, err)
}
