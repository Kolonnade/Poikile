// Package serve is the delivery http.Handler: it authorizes every request,
// answers from the tiered cache, and fills misses from storage through a
// bounded pool.
package serve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Kolonnade/Poikile/cache"
	"github.com/Kolonnade/Poikile/store"
)

// Asset is one stored variant the Resolver has authorized for a request.
// The cache key is Bucket + "/" + Key; deletion must Cache.Remove that same
// key.
type Asset struct {
	ID          string
	Owner       string
	Bucket      string
	Key         string
	Variant     string
	ContentType string
	Size        int64
	CreatedAt   time.Time
	ETag        string
}

// CacheKey returns the cache key for a, which deletion must also use.
func (a Asset) CacheKey() string { return a.Bucket + "/" + a.Key }

// Resolver authorizes a request and locates the asset. Any non-nil error is
// a refusal and answers the identical 404.
type Resolver interface {
	Resolve(r *http.Request, assetID, variant string) (Asset, error)
}

// ErrNotFound may be returned by a Resolver; it is store.ErrNotFound.
var ErrNotFound = store.ErrNotFound

// Event records one served request.
type Event struct {
	Owner    string
	AssetID  string
	Variant  string
	Tier     string
	Bytes    int64
	Status   int
	Duration time.Duration
}

// Config configures the handler. Resolver, Provider, Cache and Variants are
// required; Variants must never include "original".
type Config struct {
	Resolver     Resolver
	Provider     store.Provider
	Cache        *cache.Cache
	Variants     []string
	Concurrency  int    // parallel misses to storage; default 16
	Pool         *Pool  // shared fetch pool; nil builds NewPool(Concurrency)
	CacheControl string // default "private, max-age=1800, immutable"
	DebugHeader  string // header carrying the tier or "miss"; empty disables
	OnServe      func(Event)
	Limit        func(*http.Request) bool // true refuses with 429
}

const (
	defaultConcurrency  = 16
	defaultCacheControl = "private, max-age=1800, immutable"
	fetchTimeout        = 30 * time.Second
)

// Pool bounds parallel fetches to storage and reports its use.
type Pool struct{ sem chan struct{} }

// NewPool returns a pool of n slots; n <= 0 means 16.
func NewPool(n int) *Pool {
	if n <= 0 {
		n = defaultConcurrency
	}
	return &Pool{sem: make(chan struct{}, n)}
}

// Size returns the number of slots.
func (p *Pool) Size() int { return cap(p.sem) }

// InUse returns the number of fetches holding a slot now.
func (p *Pool) InUse() int { return len(p.sem) }

var errSizeMismatch = errors.New("serve: stored size does not match asset")

type handler struct {
	cfg      Config
	variants map[string]bool
	pool     *Pool
}

// New returns the delivery handler for GET {prefix}/{id}?size=. It panics on
// an invalid Config.
func New(cfg Config) http.Handler {
	if cfg.Resolver == nil || cfg.Provider == nil || cfg.Cache == nil {
		panic("serve: Resolver, Provider and Cache are required")
	}
	if len(cfg.Variants) == 0 {
		panic("serve: Variants is empty")
	}
	vs := make(map[string]bool, len(cfg.Variants))
	for _, v := range cfg.Variants {
		if v == "original" {
			panic("serve: originals are never served")
		}
		vs[v] = true
	}
	if cfg.Pool == nil {
		cfg.Pool = NewPool(cfg.Concurrency)
	}
	if cfg.CacheControl == "" {
		cfg.CacheControl = defaultCacheControl
	}
	return &handler{cfg: cfg, variants: vs, pool: cfg.Pool}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var ev Event
	if h.cfg.OnServe != nil {
		defer func() {
			ev.Duration = time.Since(start)
			h.cfg.OnServe(ev)
		}()
	}

	if h.cfg.Limit != nil && h.cfg.Limit(r) {
		ev.Status = writePlain(w, http.StatusTooManyRequests, "too many requests\n")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		ev.Status = notFound(w)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = path.Base(r.URL.Path)
	}
	variant := r.URL.Query().Get("size")
	if id == "" || id == "." || id == "/" || !h.variants[variant] {
		ev.Status = notFound(w)
		return
	}
	a, err := h.cfg.Resolver.Resolve(r, id, variant)
	if err != nil || a.Size <= 0 {
		ev.Status = notFound(w)
		return
	}
	ev.Owner, ev.AssetID, ev.Variant = a.Owner, id, variant

	etag := `"` + a.ETag + `"`
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", h.cfg.CacheControl)
		w.WriteHeader(http.StatusNotModified)
		ev.Status = http.StatusNotModified
		return
	}

	key := a.CacheKey()
	e, tier, ok := h.cfg.Cache.Get(key)
	if !ok {
		e, _, err = h.cfg.Cache.Fill(r.Context(), key, func(ctx context.Context) (cache.Entry, error) {
			return h.fetch(ctx, a)
		})
		if err != nil {
			switch {
			case errors.Is(err, store.ErrNotFound):
				ev.Status = notFound(w)
			case r.Context().Err() != nil:
				// Client went away; nothing to write.
			default:
				ev.Status = writePlain(w, http.StatusInternalServerError, "internal error\n")
			}
			return
		}
		tier = "miss"
	}
	ev.Tier = tier

	ct := a.ContentType
	if ct == "" {
		ct = e.ContentType
	}
	hd := w.Header()
	hd.Set("Content-Type", ct)
	hd.Set("Content-Length", strconv.Itoa(len(e.Data)))
	hd.Set("ETag", etag)
	hd.Set("Cache-Control", h.cfg.CacheControl)
	hd.Set("X-Content-Type-Options", "nosniff")
	if h.cfg.DebugHeader != "" {
		hd.Set(h.cfg.DebugHeader, tier)
	}
	w.WriteHeader(http.StatusOK)
	ev.Status = http.StatusOK
	if r.Method == http.MethodHead {
		return
	}
	n, _ := w.Write(e.Data)
	ev.Bytes = int64(n)
}

// fetch loads a from storage through the bounded pool.
func (h *handler) fetch(ctx context.Context, a Asset) (cache.Entry, error) {
	select {
	case h.pool.sem <- struct{}{}:
	case <-ctx.Done():
		return cache.Entry{}, ctx.Err()
	}
	defer func() { <-h.pool.sem }()

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	rc, _, err := h.cfg.Provider.Get(ctx, a.Bucket, a.Key)
	if err != nil {
		return cache.Entry{}, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, a.Size+1))
	if err != nil {
		return cache.Entry{}, err
	}
	if int64(len(data)) != a.Size {
		return cache.Entry{}, errSizeMismatch
	}
	return cache.Entry{Data: data, ContentType: a.ContentType, ETag: a.ETag, Owner: a.Owner, CreatedAt: a.CreatedAt}, nil
}

// notFound writes the one refusal every cause shares.
func notFound(w http.ResponseWriter) int {
	return writePlain(w, http.StatusNotFound, "not found\n")
}

func writePlain(w http.ResponseWriter, status int, body string) int {
	hd := w.Header()
	hd.Set("Content-Type", "text/plain; charset=utf-8")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	io.WriteString(w, body)
	return status
}

// etagMatch reports whether an If-None-Match header matches etag, using
// weak comparison.
func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" {
			return true
		}
		t = strings.TrimPrefix(t, "W/")
		if t == etag {
			return true
		}
	}
	return false
}
