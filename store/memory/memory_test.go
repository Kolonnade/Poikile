package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/upload"
)

const bkt = "test-grp-0f8fad5b-d9cb-469f-a165-70867728950e"

func setup(t *testing.T) *Provider {
	t.Helper()
	p := New()
	if err := p.EnsureBucket(context.Background(), bkt, bucket.Baseline("https://app.example")); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := setup(t)
	if err := p.Put(ctx, bkt, "photos/a/thumb", strings.NewReader("hello"), store.ObjectInfo{ContentType: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	rc, info, err := p.Get(ctx, bkt, "photos/a/thumb")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello" || info.ContentType != "image/jpeg" || info.Size != 5 {
		t.Fatalf("Get = %q, %+v", b, info)
	}
	if err := p.Delete(ctx, bkt, "photos/a/thumb"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Get(ctx, bkt, "photos/a/thumb"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
	if err := p.Delete(ctx, bkt, "photos/a/thumb"); err != nil {
		t.Fatalf("second Delete = %v", err)
	}
}

func TestPutNoBucket(t *testing.T) {
	err := New().Put(context.Background(), bkt, "a", strings.NewReader("x"), store.ObjectInfo{})
	if !errors.Is(err, store.ErrNoBucket) {
		t.Fatalf("Put = %v", err)
	}
}

func TestGetIsolated(t *testing.T) {
	ctx := context.Background()
	p := setup(t)
	p.Put(ctx, bkt, "k", strings.NewReader("abc"), store.ObjectInfo{})
	rc, _, _ := p.Get(ctx, bkt, "k")
	b, _ := io.ReadAll(rc)
	b[0] = 'z'
	rc, _, _ = p.Get(ctx, bkt, "k")
	b2, _ := io.ReadAll(rc)
	if !bytes.Equal(b2, []byte("abc")) {
		t.Fatalf("store mutated: %q", b2)
	}
}

func TestConcurrent(t *testing.T) {
	ctx := context.Background()
	p := setup(t)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := fmt.Sprintf("k%d", i)
			if err := p.Put(ctx, bkt, k, strings.NewReader(k), store.ObjectInfo{}); err != nil {
				t.Error(err)
				return
			}
			rc, _, err := p.Get(ctx, bkt, k)
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := io.ReadAll(rc)
			if string(b) != k {
				t.Errorf("got %q want %q", b, k)
			}
		}()
	}
	wg.Wait()
}

func TestSignUploadIncomingOnly(t *testing.T) {
	ctx := context.Background()
	p := setup(t)
	lim := upload.Limits{MaxBytes: 1 << 20, ContentTypePrefix: "image/"}
	if _, err := p.SignUpload(ctx, bkt, "photos/a/thumb", lim); err == nil {
		t.Fatal("non-incoming key signed")
	}
	tg, err := p.SignUpload(ctx, bkt, "incoming/0f8fad5b-d9cb-469f-a165-70867728950e", lim)
	if err != nil || tg.Method != "PUT" || tg.URL == "" {
		t.Fatalf("SignUpload = %+v, %v", tg, err)
	}
	if _, err := p.SignUpload(ctx, bkt, "incoming/x", upload.Limits{}); err == nil {
		t.Fatal("zero limits accepted")
	}
}
