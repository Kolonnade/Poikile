// Package serve is the delivery http.Handler: it authorizes every request,
// answers from the tiered cache, and fills misses from storage through a
// bounded pool.
package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Kolonnade/Poikile/cache"
	"github.com/Kolonnade/Poikile/store"
)

// Asset is one stored variant the Resolver has authorized for a request.
// The cache key is Bucket + "/" + Key; deletion must Cache.Remove that same
// key (Delete does).
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
	// Attachment serves the asset as a download. Files must set it (rule
	// 6). Any type but a raster image (JPEG, PNG, GIF, WebP, AVIF) is an
	// attachment regardless, so SVG and HTML are never inline.
	Attachment bool
	// Filename is the name a download is saved as. Filenames never reach
	// storage (rule 5), so the app supplies it here; setting it implies
	// Attachment.
	Filename string
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
	// StreamAbove is the size in bytes above which an asset skips the
	// cache and streams from storage, so large files don't sit in memory.
	// The pool bounds opening the stream, not sending it. Default 16 MiB.
	StreamAbove int64
}

const (
	defaultConcurrency  = 16
	defaultCacheControl = "private, max-age=1800, immutable"
	defaultStreamAbove  = 16 << 20
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
	if cfg.StreamAbove <= 0 {
		cfg.StreamAbove = defaultStreamAbove
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

	if a.Size > h.cfg.StreamAbove {
		h.stream(w, r, a, &ev)
		return
	}

	key := a.CacheKey()
	e, tier, ok := h.cfg.Cache.Get(key)
	if !ok {
		e, _, err = h.cfg.Cache.Fill(r.Context(), key, func(ctx context.Context) (cache.Entry, error) {
			return h.fetch(ctx, a)
		})
		if err != nil {
			h.fail(w, r, err, &ev)
			return
		}
		tier = "miss"
	}
	ev.Tier = tier

	ct := a.ContentType
	if ct == "" {
		ct = e.ContentType
	}
	h.writeHeader(w, a, ct, int64(len(e.Data)), tier)
	ev.Status = http.StatusOK
	if r.Method == http.MethodHead {
		return
	}
	n, _ := w.Write(e.Data)
	ev.Bytes = int64(n)
}

// fail answers a failed fetch: NotFound for a missing object, nothing for
// a client that went away, and 500 otherwise.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error, ev *Event) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		ev.Status = notFound(w)
	case r.Context().Err() != nil:
		// Client went away; nothing to write.
	default:
		ev.Status = writePlain(w, http.StatusInternalServerError, "internal error\n")
	}
}

// writeHeader writes a 200's headers. Every response is sandboxed and
// nosniff; disposition says whether it may display inline.
func (h *handler) writeHeader(w http.ResponseWriter, a Asset, ct string, size int64, tier string) {
	hd := w.Header()
	hd.Set("Content-Type", ct)
	hd.Set("Content-Length", strconv.FormatInt(size, 10))
	hd.Set("Content-Disposition", disposition(a, ct))
	hd.Set("ETag", `"`+a.ETag+`"`)
	hd.Set("Cache-Control", h.cfg.CacheControl)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "sandbox")
	if h.cfg.DebugHeader != "" {
		hd.Set(h.cfg.DebugHeader, tier)
	}
	w.WriteHeader(http.StatusOK)
}

// stream sends a large asset straight from storage, bypassing the cache.
// A pool slot is held only while the object is opened.
func (h *handler) stream(w http.ResponseWriter, r *http.Request, a Asset, ev *Event) {
	ev.Tier = "stream"
	rc, info, err := h.open(r.Context(), a)
	if err != nil {
		h.fail(w, r, err, ev)
		return
	}
	defer rc.Close()
	ct := a.ContentType
	if ct == "" {
		ct = info.ContentType
	}
	h.writeHeader(w, a, ct, a.Size, "stream")
	ev.Status = http.StatusOK
	if r.Method == http.MethodHead {
		return
	}
	n, _ := io.CopyN(w, rc, a.Size)
	ev.Bytes = n
}

func (h *handler) open(ctx context.Context, a Asset) (io.ReadCloser, store.ObjectInfo, error) {
	select {
	case h.pool.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, store.ObjectInfo{}, ctx.Err()
	}
	defer func() { <-h.pool.sem }()
	rc, info, err := h.cfg.Provider.Get(ctx, a.Bucket, a.Key)
	if err != nil {
		return nil, info, err
	}
	if info.Size != a.Size {
		rc.Close()
		return nil, info, errSizeMismatch
	}
	return rc, info, nil
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

// Delete removes a stored object the way rule 9 requires: it evicts key
// from every cache tier, deletes it from storage, and evicts again, so a
// fill that started in between can't keep it cached.
func Delete(ctx context.Context, c *cache.Cache, p store.Provider, bucket, key string) error {
	ck := Asset{Bucket: bucket, Key: key}.CacheKey()
	c.Remove(ck)
	err := p.Delete(ctx, bucket, key)
	c.Remove(ck)
	return err
}

// inlineTypes are the raster image types a browser may display inline.
var inlineTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true, "image/avif": true,
}

// disposition returns the Content-Disposition for a served as ct.
func disposition(a Asset, ct string) string {
	mt, _, err := mime.ParseMediaType(ct)
	if !a.Attachment && a.Filename == "" && err == nil && inlineTypes[mt] {
		return "inline"
	}
	name := cleanFilename(a.Filename)
	if name == "" {
		return "attachment"
	}
	v := `attachment; filename="` + asciiFilename(name) + `"`
	if asciiFilename(name) != name {
		v += "; filename*=UTF-8''" + encodeRFC5987(name)
	}
	return v
}

// cleanFilename drops path components and replaces control characters,
// quotes and backslashes, so the name is safe in a header and on disk.
func cleanFilename(s string) string {
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == utf8.RuneError {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// asciiFilename is the fallback filename= for clients without filename*.
func asciiFilename(s string) string {
	return strings.Map(func(r rune) rune {
		if r > 0x7e {
			return '_'
		}
		return r
	}, s)
}

// encodeRFC5987 percent-encodes s as an RFC 5987 ext-value.
func encodeRFC5987(s string) string {
	const attr = "!#$&+-.^_`|~"
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(attr, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
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
