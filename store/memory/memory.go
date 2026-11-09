// Package memory is an in-memory store.Provider, safe for concurrent use.
// Tests use it.
package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/upload"
)

type object struct {
	data []byte
	ct   string
}

// Provider keeps objects in maps.
type Provider struct {
	mu      sync.RWMutex
	buckets map[string]map[string]object
}

var _ store.Provider = (*Provider)(nil)

// New returns an empty Provider.
func New() *Provider {
	return &Provider{buckets: map[string]map[string]object{}}
}

// EnsureBucket creates the bucket if absent.
func (p *Provider) EnsureBucket(ctx context.Context, name string, t bucket.Template) error {
	if err := bucket.ValidateName(name); err != nil {
		return err
	}
	if err := t.Validate(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.buckets[name]; !ok {
		p.buckets[name] = map[string]object{}
	}
	return nil
}

// Put stores body; info.Size is ignored in favor of the bytes read.
func (p *Provider) Put(ctx context.Context, b, key string, body io.Reader, info store.ObjectInfo) error {
	if err := store.ValidKey(key); err != nil {
		return err
	}
	p.mu.RLock()
	_, ok := p.buckets[b]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("memory: put %s/%s: %w", b, key, store.ErrNoBucket)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("memory: put %s/%s: %w", b, key, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	objs, ok := p.buckets[b]
	if !ok {
		return fmt.Errorf("memory: put %s/%s: %w", b, key, store.ErrNoBucket)
	}
	objs[key] = object{data: data, ct: info.ContentType}
	return nil
}

// Get returns a copy of the object.
func (p *Provider) Get(ctx context.Context, b, key string) (io.ReadCloser, store.ObjectInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	o, ok := p.buckets[b][key]
	if !ok {
		return nil, store.ObjectInfo{}, fmt.Errorf("memory: get %s/%s: %w", b, key, store.ErrNotFound)
	}
	data := bytes.Clone(o.data)
	return io.NopCloser(bytes.NewReader(data)), store.ObjectInfo{ContentType: o.ct, Size: int64(len(data))}, nil
}

// Delete removes the object; a missing one is not an error.
func (p *Provider) Delete(ctx context.Context, b, key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if objs, ok := p.buckets[b]; ok {
		delete(objs, key)
	}
	return nil
}

// SignUpload returns a memory:// target; nothing serves it.
func (p *Provider) SignUpload(ctx context.Context, b, key string, lim upload.Limits) (upload.Target, error) {
	lim, err := store.CheckUpload(key, lim)
	if err != nil {
		return upload.Target{}, err
	}
	return upload.Target{
		Method:  "PUT",
		URL:     "memory://" + b + "/" + key,
		Expires: time.Now().Add(lim.TTL),
	}, nil
}
