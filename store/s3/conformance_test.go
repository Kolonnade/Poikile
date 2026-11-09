package s3_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Kolonnade/Poikile/assettest"
	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store/s3"
)

// TestConformance runs the suite against real S3. It creates a
// bucket named TEST_S3_BUCKET_PREFIX (default poikile-test-) plus a random
// suffix and removes it, and every object version
// in it, when done.
func TestConformance(t *testing.T) {
	profile := os.Getenv("TEST_S3_PROFILE")
	if profile == "" {
		t.Skip("TEST_S3_PROFILE not set")
	}
	region := os.Getenv("TEST_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	ctx := context.Background()

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	prefix := os.Getenv("TEST_S3_BUCKET_PREFIX")
	if prefix == "" {
		prefix = "poikile-test-"
	}
	name := prefix + hex.EncodeToString(suffix)
	if err := bucket.ValidateName(name); err != nil {
		t.Fatal(err)
	}
	t.Logf("bucket %s", name)

	p, err := s3.FromProfile(ctx, profile, region, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(profile), config.WithRegion(region))
	if err != nil {
		t.Fatal(err)
	}
	admin := awss3.NewFromConfig(cfg)

	// Registered before Provider: cleanups run LIFO, so the suite's own
	// object deletes run first, then this removes every remaining version
	// (versioning is on) by key and deletes the bucket.
	var keys []string
	t.Cleanup(func() {
		for _, k := range keys {
			purgeKey(t, ctx, admin, name, k)
		}
		if _, err := admin.DeleteBucket(ctx, &awss3.DeleteBucketInput{Bucket: aws.String(name)}); err != nil {
			var re *awshttp.ResponseError
			if errors.As(err, &re) && re.HTTPStatusCode() == 404 {
				return
			}
			t.Errorf("delete bucket %s failed, remove it by hand: %v", name, err)
			return
		}
		t.Logf("deleted bucket %s", name)
	})

	assettest.Provider(t, p, assettest.Options{
		Bucket:   name,
		Template: bucket.Baseline("https://app.example"),
		Uploads:  true,
		OnKey:    func(k string) { keys = append(keys, k) },
		CheckTemplate: func(t *testing.T, name string, tmpl bucket.Template) {
			checkTemplate(t, ctx, admin, name, tmpl)
		},
		// ListDenied is nil: the suite runs as admin, which may list.
		ListDenied: nil,
	})
}

// purgeKey removes every version and delete marker of key, by key, without
// listing: HeadObject names the current version (or the delete marker on a
// 404), which is deleted by id until the key is plainly absent.
func purgeKey(t *testing.T, ctx context.Context, c *awss3.Client, b, key string) {
	t.Helper()
	for i := 0; i < 10; i++ {
		out, err := c.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(b), Key: aws.String(key)})
		var vid *string
		if err == nil {
			vid = out.VersionId
		} else {
			var re *awshttp.ResponseError
			if !errors.As(err, &re) || re.HTTPStatusCode() != 404 {
				t.Errorf("purge %s/%s: head: %v (remove by hand)", b, key, err)
				return
			}
			h := re.Response.Header
			if h.Get("x-amz-delete-marker") != "true" {
				return
			}
			vid = aws.String(h.Get("x-amz-version-id"))
		}
		if _, err := c.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(b), Key: aws.String(key), VersionId: vid}); err != nil {
			t.Errorf("purge %s/%s: delete version %s: %v (remove by hand)", b, key, aws.ToString(vid), err)
			return
		}
		t.Logf("deleted %s version %s", key, aws.ToString(vid))
	}
	t.Errorf("purge %s/%s: versions remain after 10 deletes (remove by hand)", b, key)
}

func checkTemplate(t *testing.T, ctx context.Context, c *awss3.Client, name string, tmpl bucket.Template) {
	t.Helper()
	b := aws.String(name)

	pab, err := c.GetPublicAccessBlock(ctx, &awss3.GetPublicAccessBlockInput{Bucket: b})
	if err != nil {
		t.Errorf("public access block: %v", err)
	} else {
		pc := pab.PublicAccessBlockConfiguration
		if pc == nil || !aws.ToBool(pc.BlockPublicAcls) || !aws.ToBool(pc.IgnorePublicAcls) ||
			!aws.ToBool(pc.BlockPublicPolicy) || !aws.ToBool(pc.RestrictPublicBuckets) {
			t.Errorf("public access block not fully on: %+v", pc)
		}
		t.Logf("checked public access block")
	}

	enc, err := c.GetBucketEncryption(ctx, &awss3.GetBucketEncryptionInput{Bucket: b})
	if err != nil {
		t.Errorf("encryption: %v", err)
	} else {
		ok := false
		if sc := enc.ServerSideEncryptionConfiguration; sc != nil {
			for _, r := range sc.Rules {
				if d := r.ApplyServerSideEncryptionByDefault; d != nil && d.SSEAlgorithm == types.ServerSideEncryptionAes256 {
					ok = true
				}
			}
		}
		if !ok {
			t.Errorf("encryption: no AES256 default rule")
		}
		t.Logf("checked encryption")
	}

	ver, err := c.GetBucketVersioning(ctx, &awss3.GetBucketVersioningInput{Bucket: b})
	if err != nil {
		t.Errorf("versioning: %v", err)
	} else {
		if ver.Status != types.BucketVersioningStatusEnabled {
			t.Errorf("versioning = %q, want Enabled", ver.Status)
		}
		t.Logf("checked versioning")
	}

	lc, err := c.GetBucketLifecycleConfiguration(ctx, &awss3.GetBucketLifecycleConfigurationInput{Bucket: b})
	if err != nil {
		t.Errorf("lifecycle: %v", err)
	} else {
		var nonc, inc bool
		for _, r := range lc.Rules {
			switch aws.ToString(r.ID) {
			case "expire-noncurrent-versions":
				nonc = r.NoncurrentVersionExpiration != nil &&
					aws.ToInt32(r.NoncurrentVersionExpiration.NoncurrentDays) == int32(tmpl.NoncurrentDays)
			case "expire-incoming":
				inc = r.Filter != nil && aws.ToString(r.Filter.Prefix) == bucket.IncomingPrefix &&
					r.Expiration != nil && aws.ToInt32(r.Expiration.Days) == int32(tmpl.IncomingDays)
			}
		}
		if !nonc {
			t.Errorf("lifecycle: expire-noncurrent-versions missing or not %d days", tmpl.NoncurrentDays)
		}
		if !inc {
			t.Errorf("lifecycle: expire-incoming missing or not %s after %d days", bucket.IncomingPrefix, tmpl.IncomingDays)
		}
		t.Logf("checked lifecycle")
	}

	cors, err := c.GetBucketCors(ctx, &awss3.GetBucketCorsInput{Bucket: b})
	if err != nil {
		t.Errorf("cors: %v", err)
	} else {
		if len(cors.CORSRules) != 1 {
			t.Errorf("cors: %d rules, want 1", len(cors.CORSRules))
		} else {
			r := cors.CORSRules[0]
			if !slices.Equal(r.AllowedOrigins, []string{tmpl.Origin}) {
				t.Errorf("cors origins = %v, want [%s]", r.AllowedOrigins, tmpl.Origin)
			}
			if !slices.Equal(r.AllowedMethods, bucket.CORSMethods) {
				t.Errorf("cors methods = %v, want %v", r.AllowedMethods, bucket.CORSMethods)
			}
		}
		t.Logf("checked cors")
	}
}
