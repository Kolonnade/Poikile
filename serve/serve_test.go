package serve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/cache"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/store/memory"
)

const testBucket = "test-grp-0f8fad5b-d9cb-469f-a165-70867728950e"

type fakeResolver struct {
	mu     sync.Mutex
	assets map[string]Asset
	allow  atomic.Bool
	calls  atomic.Int64
}

func (f *fakeResolver) Resolve(r *http.Request, id, variant string) (Asset, error) {
	f.calls.Add(1)
	if !f.allow.Load() {
		return Asset{}, errors.New("forbidden")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.assets[id+"/"+variant]
	if !ok {
		return Asset{}, ErrNotFound
	}
	return a, nil
}

type countingProvider struct {
	store.Provider
	gets     atomic.Int64
	inflight atomic.Int64
	max      atomic.Int64
	delay    time.Duration
	failNext atomic.Bool
}

func (p *countingProvider) Get(ctx context.Context, b, key string) (io.ReadCloser, store.ObjectInfo, error) {
	p.gets.Add(1)
	n := p.inflight.Add(1)
	defer p.inflight.Add(-1)
	for {
		m := p.max.Load()
		if n <= m || p.max.CompareAndSwap(m, n) {
			break
		}
	}
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	if p.failNext.CompareAndSwap(true, false) {
		return nil, store.ObjectInfo{}, errors.New("boom")
	}
	return p.Provider.Get(ctx, b, key)
}

type fixture struct {
	res  *fakeResolver
	prov *countingProvider
	c    *cache.Cache
	h    http.Handler
	cfg  Config
}

func newFixture(t *testing.T, mod func(*Config)) *fixture {
	t.Helper()
	mem := memory.New()
	if err := mem.EnsureBucket(context.Background(), testBucket, bucket.Baseline("https://example.com")); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		res:  &fakeResolver{assets: map[string]Asset{}},
		prov: &countingProvider{Provider: mem},
		c:    cache.New(cache.Config{Tiers: []cache.Tier{{Name: "hot", Bytes: 1 << 24}}}),
	}
	f.res.allow.Store(true)
	f.cfg = Config{Resolver: f.res, Provider: f.prov, Cache: f.c, Variants: []string{"thumb", "web"}, DebugHeader: "X-Asset-Cache"}
	if mod != nil {
		mod(&f.cfg)
	}
	f.h = New(f.cfg)
	return f
}

// add stores an asset and returns its id.
func (f *fixture) add(t *testing.T, n int, variant string, data []byte) (string, Asset) {
	t.Helper()
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	key, err := store.ObjectKey("photos", id, variant)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.prov.Provider.Put(context.Background(), testBucket, key, bytes.NewReader(data), store.ObjectInfo{ContentType: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	a := Asset{ID: id, Owner: "owner-1", Bucket: testBucket, Key: key, Variant: variant, ContentType: "image/jpeg", Size: int64(len(data)), CreatedAt: time.Now(), ETag: "etag-" + id}
	f.res.mu.Lock()
	f.res.assets[id+"/"+variant] = a
	f.res.mu.Unlock()
	return id, a
}

func (f *fixture) do(path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func entries(c *cache.Cache) int {
	n := 0
	for _, t := range c.Stats().Tiers {
		n += t.Entries
	}
	return n
}

func TestServeHit(t *testing.T) {
	f := newFixture(t, nil)
	data := []byte("jpeg-bytes")
	id, a := f.add(t, 1, "thumb", data)
	rec := f.do("/photos/"+id+"?size=thumb", nil)
	if rec.Code != 200 || rec.Body.String() != string(data) {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	if h.Get("Content-Type") != "image/jpeg" || h.Get("Content-Length") != fmt.Sprint(len(data)) ||
		h.Get("ETag") != `"`+a.ETag+`"` || h.Get("Cache-Control") != defaultCacheControl {
		t.Fatalf("headers %v", h)
	}
	if got := h.Get("X-Asset-Cache"); got != "miss" {
		t.Fatalf("debug header %q", got)
	}
	rec = f.do("/photos/"+id+"?size=thumb", nil)
	if got := rec.Header().Get("X-Asset-Cache"); got != "hot" {
		t.Fatalf("debug header %q", got)
	}
	if n := f.prov.gets.Load(); n != 1 {
		t.Fatalf("gets %d", n)
	}
}

func TestAuthorizeOnCacheHit(t *testing.T) {
	f := newFixture(t, nil)
	id, _ := f.add(t, 1, "thumb", []byte("x"))
	f.do("/photos/"+id+"?size=thumb", nil)
	f.do("/photos/"+id+"?size=thumb", nil)
	if n := f.res.calls.Load(); n != 2 {
		t.Fatalf("resolver calls %d", n)
	}
	f.res.allow.Store(false)
	if rec := f.do("/photos/"+id+"?size=thumb", nil); rec.Code != 404 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestRefusalsIdentical(t *testing.T) {
	f := newFixture(t, nil)
	id, a := f.add(t, 1, "thumb", []byte("x"))
	// An asset the resolver knows but whose object is gone from storage.
	gone := "00000000-0000-4000-8000-000000000099"
	ga := a
	ga.ID, ga.Key = gone, "photos/"+gone+"/thumb"
	f.res.assets[gone+"/thumb"] = ga

	missing := "00000000-0000-4000-8000-000000000042"
	type rq struct {
		path   string
		forbid bool
	}
	cases := []rq{
		{"/photos/" + missing + "?size=thumb", false},
		{"/photos/" + id + "?size=thumb", true},
		{"/photos/" + id + "?size=huge", false},
		{"/photos/" + id + "?size=original", false},
		{"/photos/" + id, false},
		{"/photos/" + gone + "?size=thumb", false},
	}
	var first *httptest.ResponseRecorder
	for i, c := range cases {
		f.res.allow.Store(!c.forbid)
		rec := f.do(c.path, nil)
		if rec.Code != 404 {
			t.Fatalf("case %d: code %d", i, rec.Code)
		}
		if rec.Header().Get("X-Asset-Cache") != "" {
			t.Fatalf("case %d: debug header set", i)
		}
		if first == nil {
			first = rec
			continue
		}
		if !reflect.DeepEqual(rec.Header(), first.Header()) || rec.Body.String() != first.Body.String() {
			t.Fatalf("case %d differs: %v %q vs %v %q", i, rec.Header(), rec.Body.String(), first.Header(), first.Body.String())
		}
	}
	f.res.allow.Store(true)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/photos/"+id+"?size=thumb", nil))
	if rec.Code != 404 || !reflect.DeepEqual(rec.Header(), first.Header()) || rec.Body.String() != first.Body.String() {
		t.Fatalf("POST differs: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
}

func Test304AfterResolve(t *testing.T) {
	f := newFixture(t, nil)
	id, a := f.add(t, 1, "thumb", []byte("x"))
	etag := `"` + a.ETag + `"`
	p := "/photos/" + id + "?size=thumb"

	f.res.allow.Store(false)
	if rec := f.do(p, map[string]string{"If-None-Match": etag}); rec.Code != 404 {
		t.Fatalf("forbidden got %d", rec.Code)
	}
	f.res.allow.Store(true)
	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag} {
		before := f.res.calls.Load()
		rec := f.do(p, map[string]string{"If-None-Match": inm})
		if rec.Code != 304 || rec.Body.Len() != 0 {
			t.Fatalf("%q: got %d %q", inm, rec.Code, rec.Body.String())
		}
		if f.res.calls.Load() != before+1 {
			t.Fatalf("%q: resolver not called", inm)
		}
	}
	if n := f.prov.gets.Load(); n != 0 {
		t.Fatalf("gets %d", n)
	}
	if rec := f.do(p, map[string]string{"If-None-Match": `"nope"`}); rec.Code != 200 {
		t.Fatalf("non-matching got %d", rec.Code)
	}
}

func TestRateLimit(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Limit = func(*http.Request) bool { return true } })
	id, _ := f.add(t, 1, "thumb", []byte("x"))
	if rec := f.do("/photos/"+id+"?size=thumb", nil); rec.Code != 429 {
		t.Fatalf("got %d", rec.Code)
	}
	if n := f.res.calls.Load(); n != 0 {
		t.Fatalf("resolver calls %d", n)
	}
}

func TestBoundedConcurrency(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Concurrency = 4 })
	f.prov.delay = 20 * time.Millisecond
	ids := make([]string, 60)
	for i := range ids {
		ids[i], _ = f.add(t, i+1, "thumb", []byte(fmt.Sprintf("data-%d", i)))
	}
	var wg sync.WaitGroup
	codes := make([]int, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = f.do("/photos/"+id+"?size=thumb", nil).Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("request %d: %d", i, c)
		}
	}
	if m := f.prov.max.Load(); m > 4 || m < 2 {
		t.Fatalf("max in-flight %d", m)
	}
}

func TestErrorNotCached(t *testing.T) {
	f := newFixture(t, nil)
	id, _ := f.add(t, 1, "thumb", []byte("x"))
	f.prov.failNext.Store(true)
	if rec := f.do("/photos/"+id+"?size=thumb", nil); rec.Code != 500 {
		t.Fatalf("got %d", rec.Code)
	}
	if n := entries(f.c); n != 0 {
		t.Fatalf("entries %d", n)
	}
	if rec := f.do("/photos/"+id+"?size=thumb", nil); rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if n := f.prov.gets.Load(); n != 2 {
		t.Fatalf("gets %d", n)
	}
}

func TestSizeMismatchNotCached(t *testing.T) {
	f := newFixture(t, nil)
	id, a := f.add(t, 1, "thumb", []byte("abc"))
	a.Size = 10
	f.res.assets[id+"/thumb"] = a
	if rec := f.do("/photos/"+id+"?size=thumb", nil); rec.Code != 500 {
		t.Fatalf("got %d", rec.Code)
	}
	if n := entries(f.c); n != 0 {
		t.Fatalf("entries %d", n)
	}
}

func TestEvent(t *testing.T) {
	var mu sync.Mutex
	var evs []Event
	f := newFixture(t, func(c *Config) {
		c.OnServe = func(e Event) { mu.Lock(); evs = append(evs, e); mu.Unlock() }
	})
	id, _ := f.add(t, 1, "thumb", []byte("hello"))
	f.do("/photos/"+id+"?size=thumb", nil)
	f.do("/photos/"+id+"?size=thumb", nil)
	f.res.allow.Store(false)
	f.do("/photos/"+id+"?size=thumb", nil)
	if len(evs) != 3 {
		t.Fatalf("events %d", len(evs))
	}
	for i, want := range []string{"miss", "hot"} {
		e := evs[i]
		if e.Owner != "owner-1" || e.AssetID != id || e.Variant != "thumb" || e.Tier != want || e.Bytes != 5 || e.Status != 200 {
			t.Fatalf("event %d: %+v", i, e)
		}
	}
	if e := evs[2]; e.Status != 404 || e.Owner != "" || e.AssetID != "" {
		t.Fatalf("refusal event %+v", e)
	}
}

func TestNewPanics(t *testing.T) {
	base := func() Config {
		return Config{Resolver: &fakeResolver{}, Provider: memory.New(), Cache: cache.New(cache.Config{Tiers: []cache.Tier{{Name: "hot", Bytes: 10}}}), Variants: []string{"thumb"}}
	}
	cases := map[string]func(*Config){
		"original":  func(c *Config) { c.Variants = []string{"thumb", "original"} },
		"nil cache": func(c *Config) { c.Cache = nil },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			c := base()
			mod(&c)
			New(c)
		})
	}
}

func TestMissCountedOnce(t *testing.T) {
	f := newFixture(t, nil)
	id, _ := f.add(t, 1, "thumb", []byte("x"))
	f.do("/photos/"+id+"?size=thumb", nil)
	f.do("/photos/"+id+"?size=thumb", nil)
	st := f.c.Stats()
	if st.Misses != 1 || st.Fills != 1 || st.Tiers[0].Hits != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestPool(t *testing.T) {
	p := NewPool(3)
	if p.Size() != 3 || p.InUse() != 0 {
		t.Fatalf("size %d in use %d", p.Size(), p.InUse())
	}
	if NewPool(0).Size() != defaultConcurrency {
		t.Fatal("default size")
	}
	shared := NewPool(2)
	f := newFixture(t, func(c *Config) { c.Pool = shared })
	f.prov.delay = 200 * time.Millisecond
	id, _ := f.add(t, 1, "thumb", []byte("x"))
	done := make(chan struct{})
	go func() {
		f.do("/photos/"+id+"?size=thumb", nil)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for shared.InUse() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("pool never in use")
		}
		time.Sleep(time.Millisecond)
	}
	<-done
	if shared.InUse() != 0 {
		t.Fatalf("in use after = %d", shared.InUse())
	}
}
