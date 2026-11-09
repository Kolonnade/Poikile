package cache

import (
	"context"
	"fmt"
)

// call is one in-flight fetch shared by every Fill on its key.
type call struct {
	done      chan struct{}
	e         Entry
	tier      string
	err       error
	forgotten bool
}

// Fill returns the cached entry for key, or calls fetch to load and store it.
// Concurrent misses on one key share a single fetch. An error is returned to
// every waiter and never stored. The fetch runs detached from ctx, so one
// caller giving up does not fail the others; a caller whose ctx ends returns
// ctx.Err(). Fill's lookup is not counted; call Get first to count
// lookups, as serve does.
func (c *Cache) Fill(ctx context.Context, key string, fetch func(context.Context) (Entry, error)) (Entry, string, error) {
	// One critical section for the lookup and the join, so a caller can't
	// miss the cache and then also miss a call that just stored its result.
	c.mu.Lock()
	if e, tier, ok := c.getLocked(key, false); ok {
		c.mu.Unlock()
		return e, tier, nil
	}
	cl, ok := c.inflight[key]
	if ok {
		c.coalesced++
	} else {
		c.fills++
		cl = &call{done: make(chan struct{})}
		c.inflight[key] = cl
		go c.run(context.WithoutCancel(ctx), key, cl, fetch)
	}
	c.mu.Unlock()

	select {
	case <-cl.done:
		if cl.err != nil {
			return Entry{}, "", cl.err
		}
		return cl.e, cl.tier, nil
	case <-ctx.Done():
		return Entry{}, "", ctx.Err()
	}
}

func (c *Cache) run(ctx context.Context, key string, cl *call, fetch func(context.Context) (Entry, error)) {
	e, err := safeFetch(ctx, fetch)
	if err == nil {
		e.Size = int64(len(e.Data))
	}
	c.mu.Lock()
	if err == nil {
		cl.e = e
		if !cl.forgotten {
			cl.tier = c.putLocked(key, e)
			if t := c.tierByName(cl.tier); t != nil {
				t.misses++
				t.bump(c.variant(key), false)
			}
		}
	} else {
		cl.err = err
		c.fillErrors++
	}
	if c.inflight[key] == cl {
		delete(c.inflight, key)
	}
	c.mu.Unlock()
	close(cl.done)
}

func safeFetch(ctx context.Context, fetch func(context.Context) (Entry, error)) (e Entry, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("cache: fetch panicked: %v", r)
		}
	}()
	return fetch(ctx)
}
