package fs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store"
)

const bkt = "test-grp-0f8fad5b-d9cb-469f-a165-70867728950e"

var secret = []byte("0123456789abcdef0123456789abcdef")

func setup(t *testing.T, opts Options) (*Provider, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	if opts.Secret == nil {
		opts.Secret = secret
	}
	p, err := New(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureBucket(context.Background(), bkt, bucket.Baseline("https://app.example")); err != nil {
		t.Fatal(err)
	}
	return p, root
}

func get(t *testing.T, p *Provider, key string) (string, store.ObjectInfo, error) {
	t.Helper()
	rc, info, err := p.Get(context.Background(), bkt, key)
	if err != nil {
		return "", info, err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return string(b), info, err
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	p, _ := setup(t, Options{})
	if err := p.Put(ctx, bkt, "photos/a/thumb", strings.NewReader("hello"), store.ObjectInfo{ContentType: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	s, info, err := get(t, p, "photos/a/thumb")
	if err != nil || s != "hello" || info.ContentType != "image/jpeg" || info.Size != 5 {
		t.Fatalf("Get = %q, %+v, %v", s, info, err)
	}
	if err := p.Delete(ctx, bkt, "photos/a/thumb"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := get(t, p, "photos/a/thumb"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
	if err := p.Delete(ctx, bkt, "photos/a/thumb"); err != nil {
		t.Fatalf("second Delete = %v", err)
	}
}

type failReader struct{ n int }

func (f *failReader) Read(b []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("boom")
	}
	n := min(len(b), f.n)
	for i := range n {
		b[i] = 'x'
	}
	f.n -= n
	return n, nil
}

func TestPutAtomicNew(t *testing.T) {
	p, root := setup(t, Options{})
	if err := p.Put(context.Background(), bkt, "k", &failReader{n: 10}, store.ObjectInfo{}); err == nil {
		t.Fatal("Put succeeded")
	}
	if _, _, err := get(t, p, "k"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get = %v", err)
	}
	ents, err := os.ReadDir(filepath.Join(root, bkt, ".tmp"))
	if err != nil || len(ents) != 0 {
		t.Fatalf(".tmp = %v, %v", ents, err)
	}
}

func TestPutAtomicOverwrite(t *testing.T) {
	ctx := context.Background()
	p, _ := setup(t, Options{})
	if err := p.Put(ctx, bkt, "k", strings.NewReader("original"), store.ObjectInfo{ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Put(ctx, bkt, "k", &failReader{n: 10}, store.ObjectInfo{}); err == nil {
		t.Fatal("Put succeeded")
	}
	s, info, err := get(t, p, "k")
	if err != nil || s != "original" || info.ContentType != "text/plain" {
		t.Fatalf("Get = %q, %+v, %v", s, info, err)
	}
}

func TestTraversalRejected(t *testing.T) {
	p, root := setup(t, Options{})
	err := p.Put(context.Background(), bkt, "incoming/../../x", strings.NewReader("x"), store.ObjectInfo{})
	if err == nil {
		t.Fatal("traversal accepted")
	}
	for _, f := range []string{filepath.Join(root, "x"), filepath.Join(filepath.Dir(root), "x")} {
		if _, err := os.Stat(f); err == nil {
			t.Fatalf("%s written", f)
		}
	}
}
