package process_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/process"
	pimage "github.com/Kolonnade/Poikile/process/image"
	pt "github.com/Kolonnade/Poikile/process/image/imagetest"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/store/memory"
)

const (
	bkt     = "test-grp-0f8fad5b-d9cb-469f-a165-70867728950e"
	upID    = "1b4e28ba-2fa1-4d3b-a3f5-ef19b5a7633b"
	assetID = "6ba7b810-9dad-41d1-80b4-00c04fd430c8"
)

func setup(t *testing.T) *memory.Provider {
	t.Helper()
	p := memory.New()
	if err := p.EnsureBucket(context.Background(), bkt, bucket.Baseline("https://app.example")); err != nil {
		t.Fatal(err)
	}
	return p
}

func put(t *testing.T, p store.Provider, key string, data []byte) {
	t.Helper()
	if err := p.Put(context.Background(), bkt, key, bytes.NewReader(data), store.ObjectInfo{ContentType: "application/octet-stream", Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
}

func pipeline(t *testing.T, workers int) *process.Pipeline {
	t.Helper()
	pl, err := process.New("", pimage.DefaultConfig(), workers)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

func TestRun_WritesVariantsDeletesIncoming(t *testing.T) {
	ctx := context.Background()
	p := setup(t)
	in, _ := store.IncomingKey(upID)
	put(t, p, in, pt.JPEG(3000, 2000, nil, nil))

	res, err := pipeline(t, 0).Run(ctx, p, bkt, upID, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Width != 3000 || res.Height != 2000 || len(res.Variants) != 3 {
		t.Fatalf("result = %d×%d, %d variants", res.Width, res.Height, len(res.Variants))
	}
	wantServed := map[string]bool{"thumb": true, "display": true, "original": false}
	for _, v := range res.Variants {
		if want := "photos/" + assetID + "/" + v.Name; v.Key != want {
			t.Errorf("key = %s, want %s", v.Key, want)
		}
		if v.Served != wantServed[v.Name] {
			t.Errorf("%s served = %v", v.Name, v.Served)
		}
		rc, info, err := p.Get(ctx, bkt, v.Key)
		if err != nil {
			t.Fatalf("%s: %v", v.Key, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if info.ContentType != "image/jpeg" || v.ContentType != "image/jpeg" {
			t.Errorf("%s content type %q / %q", v.Name, info.ContentType, v.ContentType)
		}
		if v.Size != int64(len(b)) {
			t.Errorf("%s size %d, stored %d bytes", v.Name, v.Size, len(b))
		}
	}
	if _, _, err := p.Get(ctx, bkt, in); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("incoming still there: %v", err)
	}
}

func TestRun_RefusedDeletesIncoming(t *testing.T) {
	ctx := context.Background()
	p := setup(t)
	in, _ := store.IncomingKey(upID)
	put(t, p, in, pt.SVG())

	if _, err := pipeline(t, 0).Run(ctx, p, bkt, upID, assetID); !errors.Is(err, pimage.ErrUnsupportedFormat) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := p.Get(ctx, bkt, in); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("incoming still there: %v", err)
	}
	for _, name := range []string{"thumb", "display", "original"} {
		k, _ := store.ObjectKey("photos", assetID, name)
		if _, _, err := p.Get(ctx, bkt, k); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s exists: %v", k, err)
		}
	}
}

func TestRun_MissingIncoming(t *testing.T) {
	if _, err := pipeline(t, 0).Run(context.Background(), setup(t), bkt, upID, assetID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// counting wraps a Provider, counting Gets and the most in flight at once.
type counting struct {
	store.Provider
	delay    time.Duration
	gets     atomic.Int64
	mu       sync.Mutex
	inFlight int
	max      int
}

func (c *counting) Get(ctx context.Context, b, key string) (io.ReadCloser, store.ObjectInfo, error) {
	c.gets.Add(1)
	c.mu.Lock()
	c.inFlight++
	if c.inFlight > c.max {
		c.max = c.inFlight
	}
	c.mu.Unlock()
	time.Sleep(c.delay)
	defer func() {
		c.mu.Lock()
		c.inFlight--
		c.mu.Unlock()
	}()
	return c.Provider.Get(ctx, b, key)
}

func TestRun_BadIDs(t *testing.T) {
	c := &counting{Provider: setup(t)}
	pl := pipeline(t, 0)
	for name, ids := range map[string][2]string{
		"upload": {"not-a-uuid", assetID},
		"asset":  {upID, "NOT-A-UUID"},
	} {
		if _, err := pl.Run(context.Background(), c, bkt, ids[0], ids[1]); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := c.gets.Load(); n != 0 {
		t.Errorf("Get called %d times", n)
	}
}

func TestRun_WorkerBound(t *testing.T) {
	c := &counting{Provider: setup(t), delay: 20 * time.Millisecond}
	ids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555",
		"66666666-6666-4666-8666-666666666666",
	}
	for _, id := range ids {
		k, _ := store.IncomingKey(id)
		put(t, c, k, pt.JPEG(16, 16, nil, nil))
	}
	pl := pipeline(t, 2)
	var wg sync.WaitGroup
	errs := make([]error, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = pl.Run(context.Background(), c, bkt, id, id)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d: %v", i, err)
		}
	}
	if c.max > 2 {
		t.Errorf("max in flight = %d, want <= 2", c.max)
	}
}

func TestNew_Defaults(t *testing.T) {
	pl := pipeline(t, 0)
	if pl.Kind != "photos" {
		t.Errorf("kind = %q", pl.Kind)
	}
	if got := pl.Workers(); got != runtime.NumCPU() {
		t.Errorf("workers = %d, want %d", got, runtime.NumCPU())
	}
}
