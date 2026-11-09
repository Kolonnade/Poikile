// Package cache is a tiered, byte-budgeted LRU cache. Each tier is its own
// LRU with its own budget; an entry lives in the first tier that admits it,
// and moves to the next admitting tier as it ages (lazily on Get, or eagerly
// on Sweep). Fill collapses concurrent misses on a key into one fetch.
//
// One mutex guards the whole cache. Admit functions run under it, so they
// must be cheap and pure. No I/O happens under the lock.
package cache

import (
	"fmt"
	"sync"
	"time"
)

// Entry is one cached value. Size is always set from len(Data) by the cache.
// Owner attributes bytes for metering.
type Entry struct {
	Data        []byte
	ContentType string
	ETag        string
	Owner       string
	CreatedAt   time.Time
	Size        int64
}

// Tier configures one LRU. A nil Admit accepts every entry. Admit receives
// the cache's current time so an injected clock and Sweep(now) reach it.
type Tier struct {
	Name  string
	Bytes int64
	Admit func(e Entry, now time.Time) bool
}

// NewerThan admits an entry whose CreatedAt is within d of now.
func NewerThan(d time.Duration) func(Entry, time.Time) bool {
	return func(e Entry, now time.Time) bool {
		return now.Sub(e.CreatedAt) < d
	}
}

// Config configures a Cache. Now defaults to time.Now.
type Config struct {
	Tiers []Tier
	Now   func() time.Time
	// Variant names the variant of a key for per-variant counts; nil
	// disables them.
	Variant func(key string) string
}

// VariantStats counts lookups for one variant.
type VariantStats struct {
	Hits   int64
	Misses int64
}

// TierStats is a snapshot of one tier.
type TierStats struct {
	Name         string
	Hits         int64
	Misses       int64
	Evictions    int64
	Inserts      int64
	BytesEvicted int64
	TooBig       int64
	Variants     map[string]VariantStats
	Bytes        int64
	Budget       int64
	Entries      int
	OwnerBytes   map[string]int64
}

// Stats is a snapshot of the cache.
type Stats struct {
	Misses     int64
	Fills      int64
	Coalesced  int64
	FillErrors int64
	InFlight   int
	Variants   map[string]VariantStats
	Tiers      []TierStats
}

// Cache is a tiered LRU cache, safe for concurrent use.
type Cache struct {
	mu         sync.Mutex
	tiers      []*tier
	inflight   map[string]*call
	misses     int64
	fills      int64
	coalesced  int64
	fillErrors int64
	variants   map[string]*VariantStats
	variantOf  func(string) string
	now        func() time.Time
}

// New builds a Cache. It panics on no tiers, a duplicate tier name, or a
// non-positive budget.
func New(cfg Config) *Cache {
	if len(cfg.Tiers) == 0 {
		panic("cache: no tiers")
	}
	c := &Cache{inflight: make(map[string]*call), now: cfg.Now,
		variants: make(map[string]*VariantStats), variantOf: cfg.Variant}
	if c.now == nil {
		c.now = time.Now
	}
	seen := make(map[string]bool)
	for _, t := range cfg.Tiers {
		if seen[t.Name] {
			panic(fmt.Sprintf("cache: duplicate tier %q", t.Name))
		}
		if t.Bytes <= 0 {
			panic(fmt.Sprintf("cache: tier %q has no budget", t.Name))
		}
		seen[t.Name] = true
		c.tiers = append(c.tiers, newTier(t))
	}
	return c
}

func (c *Cache) admitIndex(e Entry, now time.Time, from int) int {
	for i := from; i < len(c.tiers); i++ {
		t := c.tiers[i]
		if !t.admits(e, now) {
			continue
		}
		if t.budget >= e.Size {
			return i
		}
		t.tooBig++
	}
	return -1
}

func (c *Cache) tierByName(name string) *tier {
	for _, t := range c.tiers {
		if t.name == name {
			return t
		}
	}
	return nil
}

// Get returns the entry for key and the name of the tier holding it.
func (c *Cache) Get(key string) (Entry, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getLocked(key, true)
}

// variant returns key's variant, or "" when per-variant counts are off.
func (c *Cache) variant(key string) string {
	if c.variantOf == nil {
		return ""
	}
	return c.variantOf(key)
}

// bump counts a hit or miss for variant v across the cache.
func (c *Cache) bump(v string, hit bool) {
	if v == "" {
		return
	}
	vs := c.variants[v]
	if vs == nil {
		vs = &VariantStats{}
		c.variants[v] = vs
	}
	if hit {
		vs.Hits++
	} else {
		vs.Misses++
	}
}

// getLocked looks key up; count records the hit or miss.
func (c *Cache) getLocked(key string, count bool) (Entry, string, bool) {
	now := c.now()
	for i, t := range c.tiers {
		el, ok := t.items[key]
		if !ok {
			continue
		}
		e := el.Value.(*item).e
		if t.admits(e, now) {
			t.ll.MoveToFront(el)
			if count {
				v := c.variant(key)
				t.hits++
				t.bump(v, true)
				c.bump(v, true)
			}
			return e, t.name, true
		}
		t.removeElement(el)
		j := c.admitIndex(e, now, i+1)
		if j < 0 {
			break
		}
		nt := c.tiers[j]
		nt.insert(key, e)
		if _, ok := nt.items[key]; !ok {
			break
		}
		if count {
			v := c.variant(key)
			nt.hits++
			nt.bump(v, true)
			c.bump(v, true)
		}
		return e, nt.name, true
	}
	if count {
		c.misses++
		c.bump(c.variant(key), false)
	}
	return Entry{}, "", false
}

// Put stores e in the first tier that admits it and returns that tier's
// name, or "" when no tier stores it.
func (c *Cache) Put(key string, e Entry) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.putLocked(key, e)
}

func (c *Cache) putLocked(key string, e Entry) string {
	e.Size = int64(len(e.Data))
	for _, t := range c.tiers {
		t.remove(key)
	}
	i := c.admitIndex(e, c.now(), 0)
	if i < 0 {
		return ""
	}
	c.tiers[i].insert(key, e)
	return c.tiers[i].name
}

// Remove deletes key from every tier. An in-flight Fill for key still
// returns its result but does not store it.
func (c *Cache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.tiers {
		t.remove(key)
	}
	if cl, ok := c.inflight[key]; ok {
		cl.forgotten = true
		delete(c.inflight, key)
	}
}

// Sweep moves every entry its tier no longer admits at now to the next tier
// that does, or drops it. Moves are not counted as evictions.
func (c *Cache) Sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, t := range c.tiers {
		for el := t.ll.Back(); el != nil; {
			prev := el.Prev()
			it := el.Value.(*item)
			if !t.admits(it.e, now) {
				key, e := it.key, it.e
				t.removeElement(el)
				if j := c.admitIndex(e, now, i+1); j >= 0 {
					c.tiers[j].insert(key, e)
				}
			}
			el = prev
		}
	}
}

// Stats returns a snapshot of every tier.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Stats{
		Misses: c.misses, Fills: c.fills, Coalesced: c.coalesced, FillErrors: c.fillErrors,
		InFlight: len(c.inflight), Variants: copyVariants(c.variants),
	}
	for _, t := range c.tiers {
		owners := make(map[string]int64, len(t.owners))
		for k, v := range t.owners {
			owners[k] = v
		}
		s.Tiers = append(s.Tiers, TierStats{
			Name: t.name, Hits: t.hits, Misses: t.misses, Evictions: t.evictions,
			Inserts: t.inserts, BytesEvicted: t.bytesEvicted, TooBig: t.tooBig,
			Variants: copyVariants(t.variants),
			Bytes:    t.used, Budget: t.budget, Entries: t.ll.Len(), OwnerBytes: owners,
		})
	}
	return s
}

func copyVariants(m map[string]*VariantStats) map[string]VariantStats {
	out := make(map[string]VariantStats, len(m))
	for k, v := range m {
		out[k] = *v
	}
	return out
}
