// Package assettest is the provider conformance suite:
// one set of checks every store.Provider must pass. It imports testing, so
// only test code may import it.
package assettest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/upload"
)

// Options configure one run of the suite.
type Options struct {
	// Bucket is the bucket the suite ensures and writes to.
	Bucket string
	// Template is applied by EnsureBucket.
	Template bucket.Template
	// Uploads is true when the provider's upload targets are served and
	// can be sent to; false (memory) skips the upload checks.
	Uploads bool
	// OnKey, if set, is called with every key the suite creates or tries
	// to create, so a caller can clean up what the suite cannot.
	OnKey func(key string)
	// CheckTemplate reads the bucket back through an admin client and
	// asserts the whole template was applied. Nil skips the check
	// (providers with no admin view: memory, fs).
	CheckTemplate func(t *testing.T, name string, tmpl bucket.Template)
	// ListDenied returns nil when listing the bucket is refused for the
	// provider's credentials. Nil skips the check: the suite's own
	// credentials may be admin (real S3 runs as admin today), and admin
	// may list.
	ListDenied func() error
}

// Provider runs the conformance suite against p.
func Provider(t *testing.T, p store.Provider, o Options) {
	t.Helper()
	ctx := context.Background()
	var created []string
	t.Cleanup(func() {
		for _, k := range created {
			if err := p.Delete(ctx, o.Bucket, k); err != nil {
				t.Errorf("cleanup: delete %s: %v", k, err)
			}
		}
	})
	track := func(k string) {
		created = append(created, k)
		if o.OnKey != nil {
			o.OnKey(k)
		}
	}
	objectKey := func(t *testing.T) string {
		t.Helper()
		k, err := store.ObjectKey("conformance", newUUID(t), "orig")
		if err != nil {
			t.Fatalf("object key: %v", err)
		}
		track(k)
		return k
	}
	incomingKey := func(t *testing.T) string {
		t.Helper()
		k, err := store.IncomingKey(newUUID(t))
		if err != nil {
			t.Fatalf("incoming key: %v", err)
		}
		track(k)
		return k
	}

	if !t.Run("EnsureBucket", func(t *testing.T) {
		t.Logf("ensuring bucket %s twice", o.Bucket)
		for i := 0; i < 2; i++ {
			if err := p.EnsureBucket(ctx, o.Bucket, o.Template); err != nil {
				t.Fatalf("EnsureBucket #%d: %v", i+1, err)
			}
		}
		if o.CheckTemplate == nil {
			t.Skip("no admin client")
		}
		t.Logf("reading the template back")
		o.CheckTemplate(t, o.Bucket, o.Template)
	}) {
		t.Fatalf("EnsureBucket failed; later steps need the bucket")
	}

	t.Run("RoundTrip", func(t *testing.T) {
		t.Logf("put, then get, 1 KiB of image/jpeg")
		k := objectKey(t)
		data := randomBytes(t, 1024)
		if err := p.Put(ctx, o.Bucket, k, bytes.NewReader(data), store.ObjectInfo{ContentType: "image/jpeg", Size: int64(len(data))}); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
		got, info := mustGet(t, ctx, p, o.Bucket, k)
		if !bytes.Equal(got, data) {
			t.Errorf("Get %s: bytes differ from what was put", k)
		}
		if info.ContentType != "image/jpeg" {
			t.Errorf("Get %s: ContentType = %q, want image/jpeg", k, info.ContentType)
		}
		if info.Size != 1024 {
			t.Errorf("Get %s: Size = %d, want 1024", k, info.Size)
		}
	})

	t.Run("NotFoundAfterDelete", func(t *testing.T) {
		t.Logf("delete, then get, and delete again")
		k := objectKey(t)
		if err := p.Put(ctx, o.Bucket, k, bytes.NewReader([]byte("x")), store.ObjectInfo{ContentType: "image/jpeg", Size: 1}); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
		if err := p.Delete(ctx, o.Bucket, k); err != nil {
			t.Fatalf("Delete %s: %v", k, err)
		}
		wantNotFound(t, ctx, p, o.Bucket, k)
		if err := p.Delete(ctx, o.Bucket, k); err != nil {
			t.Errorf("second Delete %s: %v", k, err)
		}
		never := objectKey(t)
		wantNotFound(t, ctx, p, o.Bucket, never)
	})

	lim := upload.Limits{MaxBytes: 64, ContentTypePrefix: "image/"}
	send := func(t *testing.T, ct string, n int) (string, []byte, int) {
		t.Helper()
		k := incomingKey(t)
		tg, err := p.SignUpload(ctx, o.Bucket, k, lim)
		if err != nil {
			t.Fatalf("SignUpload %s: %v", k, err)
		}
		body := randomBytes(t, n)
		status, err := Send(ctx, tg, ct, body)
		if err != nil {
			t.Fatalf("Send %s: %v", k, err)
		}
		t.Logf("sent %d bytes of %s: status %d", n, ct, status)
		return k, body, status
	}

	t.Run("UploadAccepted", func(t *testing.T) {
		if !o.Uploads {
			t.Skip("provider's upload targets are not served")
		}
		t.Logf("upload 32 bytes of image/png within limits")
		k, body, status := send(t, "image/png", 32)
		if status < 200 || status > 299 {
			t.Fatalf("status %d, want 2xx", status)
		}
		got, _ := mustGet(t, ctx, p, o.Bucket, k)
		if !bytes.Equal(got, body) {
			t.Errorf("Get %s: bytes differ from what was uploaded", k)
		}
	})

	t.Run("UploadTooLarge", func(t *testing.T) {
		if !o.Uploads {
			t.Skip("provider's upload targets are not served")
		}
		t.Logf("upload 65 bytes against a 64-byte limit")
		k, _, status := send(t, "image/png", 65)
		if status >= 200 && status <= 299 {
			t.Errorf("status %d, want a refusal", status)
		}
		wantNotFound(t, ctx, p, o.Bucket, k)
	})

	t.Run("UploadWrongType", func(t *testing.T) {
		if !o.Uploads {
			t.Skip("provider's upload targets are not served")
		}
		t.Logf("upload text/plain against an image/ limit")
		k, _, status := send(t, "text/plain", 32)
		if status >= 200 && status <= 299 {
			t.Errorf("status %d, want a refusal", status)
		}
		wantNotFound(t, ctx, p, o.Bucket, k)
	})

	t.Run("ListDenied", func(t *testing.T) {
		if o.ListDenied == nil {
			t.Skip("no list check for these credentials")
		}
		t.Logf("listing the bucket must be refused")
		if err := o.ListDenied(); err != nil {
			t.Errorf("ListDenied: %v", err)
		}
	})
}

func mustGet(t *testing.T, ctx context.Context, p store.Provider, b, k string) ([]byte, store.ObjectInfo) {
	t.Helper()
	rc, info, err := p.Get(ctx, b, k)
	if err != nil {
		t.Fatalf("Get %s: %v", k, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", k, err)
	}
	return data, info
}

func wantNotFound(t *testing.T, ctx context.Context, p store.Provider, b, k string) {
	t.Helper()
	rc, _, err := p.Get(ctx, b, k)
	if err == nil {
		rc.Close()
		t.Errorf("Get %s: found, want ErrNotFound", k)
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get %s: %v, want ErrNotFound", k, err)
	}
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return b
}

// newUUID returns a random lowercase v4 UUID.
func newUUID(t *testing.T) string {
	b := randomBytes(t, 16)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
