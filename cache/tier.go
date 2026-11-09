package cache

import (
	"container/list"
	"time"
)

type item struct {
	key string
	e   Entry
}

// tier is one LRU with its own byte budget. The Cache's mutex guards it.
type tier struct {
	name         string
	budget       int64
	admit        func(Entry, time.Time) bool
	ll           *list.List
	items        map[string]*list.Element
	used         int64
	owners       map[string]int64
	hits         int64
	misses       int64
	evictions    int64
	inserts      int64
	bytesEvicted int64
	tooBig       int64
	variants     map[string]*VariantStats
}

func newTier(t Tier) *tier {
	return &tier{
		name:     t.Name,
		budget:   t.Bytes,
		admit:    t.Admit,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		owners:   make(map[string]int64),
		variants: make(map[string]*VariantStats),
	}
}

func (t *tier) admits(e Entry, now time.Time) bool {
	return t.admit == nil || t.admit(e, now)
}

// bump counts a hit or miss for variant v; "" is not counted.
func (t *tier) bump(v string, hit bool) {
	if v == "" {
		return
	}
	vs := t.variants[v]
	if vs == nil {
		vs = &VariantStats{}
		t.variants[v] = vs
	}
	if hit {
		vs.Hits++
	} else {
		vs.Misses++
	}
}

func (t *tier) addOwner(owner string, n int64) {
	v := t.owners[owner] + n
	if v == 0 {
		delete(t.owners, owner)
		return
	}
	t.owners[owner] = v
}

// insert pushes the entry to the front and evicts from the back until the
// tier is within budget.
func (t *tier) insert(key string, e Entry) {
	t.items[key] = t.ll.PushFront(&item{key: key, e: e})
	t.used += e.Size
	t.inserts++
	t.addOwner(e.Owner, e.Size)
	for t.used > t.budget {
		back := t.ll.Back()
		if back == nil {
			break
		}
		ev := t.removeElement(back)
		t.evictions++
		t.bytesEvicted += ev.Size
	}
}

func (t *tier) removeElement(el *list.Element) Entry {
	it := el.Value.(*item)
	t.ll.Remove(el)
	delete(t.items, it.key)
	t.used -= it.e.Size
	t.addOwner(it.e.Owner, -it.e.Size)
	return it.e
}

func (t *tier) remove(key string) {
	if el, ok := t.items[key]; ok {
		t.removeElement(el)
	}
}
