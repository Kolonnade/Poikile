// Package store is the storage seam every other package sits on
// (see README). There is deliberately no List: keys come from the
// app's database, never from listing a bucket.
package store

import (
	"context"
	"errors"
	"io"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/upload"
)

// ErrNotFound is returned by Get for a missing bucket or key.
var ErrNotFound = errors.New("store: not found")

// ErrNoBucket is returned by Put when the bucket was never ensured.
var ErrNoBucket = errors.New("store: no such bucket")

// ErrBucketNotEmpty is returned by DeleteBucket when objects remain.
var ErrBucketNotEmpty = errors.New("store: bucket not empty")

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	ContentType string
	Size        int64
}

// Provider stores objects. Delete of a missing key is not an error, and
// Delete is permanent: on a versioned bucket it removes every version of
// the key, so the bucket can be emptied.
//
// DeleteBucket removes an empty bucket; deleting a missing bucket is not an
// error, so a deletion job can resume. It never lists or deletes objects
// itself (rule 4): the app deletes each key it knows first, and a bucket
// that still holds any returns ErrBucketNotEmpty.
type Provider interface {
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, ObjectInfo, error)
	Put(ctx context.Context, bucket, key string, body io.Reader, info ObjectInfo) error
	Delete(ctx context.Context, bucket, key string) error
	SignUpload(ctx context.Context, bucket, key string, lim upload.Limits) (upload.Target, error)
	EnsureBucket(ctx context.Context, name string, t bucket.Template) error
	DeleteBucket(ctx context.Context, name string) error
}
