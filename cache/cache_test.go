package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const day = 24 * time.Hour

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func agingCache(clk *clock) *Cache {
	return New(Config{Now: clk.Now, Tiers: []Tier{
		{Name: "recent", Bytes: 10_000, Admit: NewerThan(14 * day)},
		{Name: "older", Bytes: 10_000},
	}})
}

func tierStats(t *testing.T, c *Cache, name string) TierStats {
	t.Helper()
	for _, ts := range c.Stats().Tiers {
		if ts.Name == name {
			return ts
		}
	}
	t.Fatalf("no tier %q", name)
	return TierStats{}
}

func TestTierBudget(t *testing.T) {
	clk := newClock()
	c := New(Config{Now: clk.Now, Tiers: []Tier{
		{Name: "a", Bytes: 100, Admit: func(e Entry, _ time.Time) bool { return e.Owner == "a" }},
		{Name: "b", Bytes: 50},
	}})
	for i := 0; i < 20; i++ {
		owner := "a"
		if i%2 == 1 {
			owner = "b"
		}
		c.Put(fmt.Sprint(i), Entry{Data: make([]byte, 30), Owner: owner})
		for _, ts := range c.Stats().Tiers {
			if ts.Bytes > ts.Budget {
				t.Fatalf("tier %s over budget: %d > %d", ts.Name, ts.Bytes, ts.Budget)
			}
		}
	}
	for _, ts := range c.Stats().Tiers {
		if ts.Evictions == 0 {
			t.Errorf("tier %s had no evictions", ts.Name)
		}
	}
}

func TestOversizeNotStored(t *testing.T) {
	c := New(Config{Tiers: []Tier{{Name: "a", Bytes: 10}, {Name: "b", Bytes: 20}}})
	if got := c.Put("k", Entry{Data: make([]byte, 21)}); got != "" {
		t.Fatalf("Put = %q, want empty", got)
	}
	if _, _, ok := c.Get("k"); ok {
		t.Fatal("oversize entry was stored")
	}
}

func TestScanKeepsRecent(t *testing.T) {
	clk := newClock()
	c := agingCache(clk)
	now := clk.Now()
	for i := 0; i < 50; i++ {
		c.Put(fmt.Sprintf("r%d", i), Entry{Data: make([]byte, 100), CreatedAt: now})
	}
	for i := 0; i < 1000; i++ {
		c.Put(fmt.Sprintf("o%d", i), Entry{Data: make([]byte, 100), CreatedAt: now.Add(-30 * day)})
	}
	for i := 0; i < 50; i++ {
		_, tier, ok := c.Get(fmt.Sprintf("r%d", i))
		if !ok || tier != "recent" {
			t.Fatalf("r%d: ok=%v tier=%q", i, ok, tier)
		}
	}
	if ts := tierStats(t, c, "older"); ts.Bytes > ts.Budget {
		t.Fatalf("older over budget: %d", ts.Bytes)
	}
}

func TestAgingGetMoves(t *testing.T) {
	clk := newClock()
	c := agingCache(clk)
	if got := c.Put("k", Entry{Data: []byte("x"), CreatedAt: clk.Now()}); got != "recent" {
		t.Fatalf("Put = %q", got)
	}
	clk.Advance(15 * day)
	if _, tier, ok := c.Get("k"); !ok || tier != "older" {
		t.Fatalf("Get ok=%v tier=%q", ok, tier)
	}
	if n := tierStats(t, c, "recent").Entries; n != 0 {
		t.Fatalf("recent entries = %d", n)
	}
}

func TestSweepMoves(t *testing.T) {
	clk := newClock()
	c := agingCache(clk)
	c.Put("k", Entry{Data: []byte("x"), CreatedAt: clk.Now()})
	c.Sweep(clk.Now().Add(15 * day))
	if n := tierStats(t, c, "recent").Entries; n != 0 {
		t.Fatalf("recent entries = %d", n)
	}
	if n := tierStats(t, c, "older").Entries; n != 1 {
		t.Fatalf("older entries = %d", n)
	}
}

func TestAgingDropsWhenNoTierAdmits(t *testing.T) {
	clk := newClock()
	c := New(Config{Now: clk.Now, Tiers: []Tier{{Name: "recent", Bytes: 100, Admit: NewerThan(14 * day)}}})
	c.Put("k", Entry{Data: []byte("x"), CreatedAt: clk.Now()})
	clk.Advance(15 * day)
	if _, _, ok := c.Get("k"); ok {
		t.Fatal("aged entry still hit")
	}
	if b := tierStats(t, c, "recent").Bytes; b != 0 {
		t.Fatalf("bytes = %d", b)
	}
}

func TestRemoveEveryTier(t *testing.T) {
	clk := newClock()
	c := agingCache(clk)
	assertEmpty := func() {
		t.Helper()
		if _, _, ok := c.Get("k"); ok {
			t.Fatal("removed key hit")
		}
		for _, ts := range c.Stats().Tiers {
			if ts.Bytes != 0 {
				t.Fatalf("tier %s bytes = %d", ts.Name, ts.Bytes)
			}
		}
	}
	c.Put("k", Entry{Data: []byte("x"), CreatedAt: clk.Now()})
	c.Remove("k")
	assertEmpty()

	c.Put("k", Entry{Data: []byte("x"), CreatedAt: clk.Now()})
	clk.Advance(15 * day)
	c.Get("k")
	c.Remove("k")
	assertEmpty()

	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		e, _, err := c.Fill(context.Background(), "k", func(context.Context) (Entry, error) {
			close(started)
			<-release
			return Entry{Data: []byte("v"), CreatedAt: clk.Now()}, nil
		})
		if err == nil && string(e.Data) != "v" {
			err = fmt.Errorf("data = %q", e.Data)
		}
		done <- err
	}()
	<-started
	c.Remove("k")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertEmpty()
}

func TestFillSingleflight(t *testing.T) {
	c := New(Config{Tiers: []Tier{{Name: "a", Bytes: 1000}}})
	const n = 50
	var started, calls atomic.Int64
	release := make(chan struct{})
	fetch := func(context.Context) (Entry, error) {
		calls.Add(1)
		<-release
		return Entry{Data: []byte("payload")}, nil
	}
	results := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			started.Add(1)
			e, _, err := c.Fill(context.Background(), "k", fetch)
			results[i], errs[i] = e.Data, err
		}(i)
	}
	for started.Load() < n {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d", got)
	}
	for i := range results {
		if errs[i] != nil || !bytes.Equal(results[i], []byte("payload")) {
			t.Fatalf("result %d: %q %v", i, results[i], errs[i])
		}
	}
	st := c.Stats()
	if st.Fills != 1 || st.Coalesced != n-1 || st.FillErrors != 0 || st.InFlight != 0 || st.Misses != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if ts := tierStats(t, c, "a"); ts.Misses != 1 {
		t.Fatalf("tier misses = %d", ts.Misses)
	}
	if _, _, ok := c.Get("k"); !ok {
		t.Fatal("filled entry not cached")
	}
}

func TestFillErrorNotCached(t *testing.T) {
	c := New(Config{Tiers: []Tier{{Name: "a", Bytes: 1000}}})
	boom := errors.New("boom")
	var calls atomic.Int64
	release := make(chan struct{})
	fetch := func(context.Context) (Entry, error) {
		calls.Add(1)
		<-release
		return Entry{}, boom
	}
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = c.Fill(context.Background(), "k", fetch)
		}(i)
	}
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Fatalf("caller %d err = %v", i, err)
		}
	}
	if _, _, ok := c.Get("k"); ok {
		t.Fatal("error was cached")
	}
	c.Fill(context.Background(), "k", fetch)
	if got := calls.Load(); got < 2 {
		t.Fatalf("fetch calls = %d, want >= 2", got)
	}
	if got := c.Stats().FillErrors; got != calls.Load() {
		t.Fatalf("fill errors = %d, want %d", got, calls.Load())
	}
}

func TestFillCtxCancel(t *testing.T) {
	c := New(Config{Tiers: []Tier{{Name: "a", Bytes: 1000}}})
	started, release := make(chan struct{}), make(chan struct{})
	fetch := func(context.Context) (Entry, error) {
		close(started)
		<-release
		return Entry{Data: []byte("v")}, nil
	}
	leader := make(chan error, 1)
	go func() {
		_, _, err := c.Fill(context.Background(), "k", fetch)
		leader <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Fill(ctx, "k", fetch); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter err = %v", err)
	}
	close(release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Get("k"); !ok {
		t.Fatal("leader result not stored")
	}
}

func TestOwnerBytes(t *testing.T) {
	c := New(Config{Tiers: []Tier{{Name: "a", Bytes: 1000}}})
	c.Put("1", Entry{Data: make([]byte, 10), Owner: "x"})
	c.Put("2", Entry{Data: make([]byte, 20), Owner: "x"})
	c.Put("3", Entry{Data: make([]byte, 5), Owner: "y"})
	ob := tierStats(t, c, "a").OwnerBytes
	if ob["x"] != 30 || ob["y"] != 5 {
		t.Fatalf("owner bytes = %v", ob)
	}
	c.Remove("3")
	if _, ok := tierStats(t, c, "a").OwnerBytes["y"]; ok {
		t.Fatal("owner y still present")
	}
}

func TestStats_InsertsBytesEvictedTooBig(t *testing.T) {
	c := New(Config{Tiers: []Tier{{Name: "a", Bytes: 100}}})
	for i := 0; i < 3; i++ {
		c.Put(fmt.Sprint("k", i), Entry{Data: make([]byte, 40)})
	}
	ts := tierStats(t, c, "a")
	if ts.Inserts != 3 || ts.Evictions != 1 || ts.BytesEvicted != 40 || ts.TooBig != 0 {
		t.Fatalf("tier = %+v", ts)
	}
	if got := c.Put("big", Entry{Data: make([]byte, 200)}); got != "" {
		t.Fatalf("big stored in %q", got)
	}
	ts = tierStats(t, c, "a")
	if ts.TooBig != 1 || ts.Inserts != 3 {
		t.Fatalf("tier = %+v", ts)
	}
}

func TestStats_VariantHitsMisses(t *testing.T) {
	c := New(Config{Variant: path.Base, Tiers: []Tier{{Name: "a", Bytes: 1000}}})
	c.Put("b/photos/x/thumb", Entry{Data: []byte("t")})
	c.Get("b/photos/x/thumb")
	c.Get("b/photos/x/thumb")
	c.Get("b/photos/y/display")
	st := c.Stats()
	if st.Variants["thumb"] != (VariantStats{Hits: 2}) || st.Variants["display"] != (VariantStats{Misses: 1}) {
		t.Fatalf("variants = %+v", st.Variants)
	}
	if st.Misses != 1 {
		t.Fatalf("misses = %d", st.Misses)
	}
	if ts := tierStats(t, c, "a"); ts.Variants["thumb"].Hits != 2 {
		t.Fatalf("tier variants = %+v", ts.Variants)
	}
}

func TestStats_InFlight(t *testing.T) {
	c := New(Config{Tiers: []Tier{{Name: "a", Bytes: 1000}}})
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		c.Fill(context.Background(), "k", func(context.Context) (Entry, error) {
			close(started)
			<-release
			return Entry{Data: []byte("v")}, nil
		})
		close(done)
	}()
	<-started
	if got := c.Stats().InFlight; got != 1 {
		t.Fatalf("in flight = %d, want 1", got)
	}
	close(release)
	<-done
	if got := c.Stats().InFlight; got != 0 {
		t.Fatalf("in flight = %d, want 0", got)
	}
}
