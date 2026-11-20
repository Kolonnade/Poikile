package s3

import (
	"context"
	"maps"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/upload"
)

// SignUpload returns a pre-signed POST whose policy limits the body to
// 1..MaxBytes and the Content-Type to ContentTypePrefix, or exactly to
// ContentType. It is a POST, not a PUT, because only a POST policy can
// bound the size: a pre-signed PUT can't. No Content-Type field is added:
// the browser sends the Content-Type field itself and the policy checks it.
func (p *Provider) SignUpload(ctx context.Context, b, key string, lim upload.Limits) (upload.Target, error) {
	lim, err := store.CheckUpload(key, lim)
	if err != nil {
		return upload.Target{}, err
	}
	if err := bucket.ValidateName(b); err != nil {
		return upload.Target{}, err
	}
	ctCond := []any{"starts-with", "$Content-Type", lim.ContentTypePrefix}
	if lim.ContentType != "" {
		ctCond = []any{"eq", "$Content-Type", lim.ContentType}
	}
	expires := p.opts.Now().Add(lim.TTL)
	out, err := p.presign.PresignPostObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(b), Key: aws.String(key)},
		func(o *awss3.PresignPostOptions) {
			o.Expires = lim.TTL
			o.Conditions = []any{
				[]any{"content-length-range", 1, lim.MaxBytes},
				ctCond,
			}
		})
	if err != nil {
		return upload.Target{}, err
	}
	return upload.Target{Method: upload.MethodPost, URL: out.URL, Fields: maps.Clone(out.Values), Expires: expires}, nil
}
