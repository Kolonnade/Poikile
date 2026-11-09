// Package process runs the image pipeline against a store: read
// incoming/<uploadID>, make each variant, put it under
// <kind>/<assetID>/<variant>, then delete incoming. The original is
// kept as uploaded.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/Kolonnade/Poikile/process/image"
	"github.com/Kolonnade/Poikile/store"
)

// probeID is a fixed valid UUID used to validate the kind.
const probeID = "00000000-0000-4000-8000-000000000000"

// Pipeline bounds concurrent decodes with a semaphore.
type Pipeline struct {
	Kind   string
	Config image.Config
	sem    chan struct{}
}

// New returns a Pipeline. An empty kind means "photos"; workers <= 0 means
// runtime.NumCPU().
func New(kind string, cfg image.Config, workers int) (*Pipeline, error) {
	if kind == "" {
		kind = "photos"
	}
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if _, err := store.ObjectKey(kind, probeID, "x"); err != nil {
		return nil, fmt.Errorf("process: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("process: %w", err)
	}
	return &Pipeline{Kind: kind, Config: cfg, sem: make(chan struct{}, workers)}, nil
}

// Stored describes one variant written to the store.
type Stored struct {
	Name, Key, ContentType string
	Size                   int64
	Width, Height          int
	Served                 bool
}

// Result is what the app records for an asset.
type Result struct {
	Format        string
	Width, Height int
	CapturedAt    *time.Time
	Variants      []Stored
}

func refused(err error) bool {
	for _, e := range []error{image.ErrUnsupportedFormat, image.ErrDecode, image.ErrTooLarge, image.ErrTooManyPixels, image.ErrHEICUnsupported} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// Run processes incoming/<uploadID> into the asset's variants. A refused
// image deletes incoming; any other failure keeps it so the job can retry.
func (pl *Pipeline) Run(ctx context.Context, p store.Provider, bucket, uploadID, assetID string) (Result, error) {
	in, err := store.IncomingKey(uploadID)
	if err != nil {
		return Result{}, fmt.Errorf("process: %w", err)
	}
	keys := make([]string, len(pl.Config.Variants))
	for i, v := range pl.Config.Variants {
		if keys[i], err = store.ObjectKey(pl.Kind, assetID, v.Name); err != nil {
			return Result{}, fmt.Errorf("process: %w", err)
		}
	}

	res, err := pl.decode(ctx, p, bucket, in)
	if err != nil {
		if refused(err) {
			if derr := p.Delete(ctx, bucket, in); derr != nil {
				return Result{}, fmt.Errorf("process: %w (delete incoming: %v)", err, derr)
			}
		}
		return Result{}, fmt.Errorf("process: %w", err)
	}

	out := Result{Format: res.Format, Width: res.Width, Height: res.Height, CapturedAt: res.CapturedAt}
	for i, r := range res.Variants {
		info := store.ObjectInfo{ContentType: r.ContentType, Size: int64(len(r.Data))}
		if err := p.Put(ctx, bucket, keys[i], bytes.NewReader(r.Data), info); err != nil {
			return Result{}, fmt.Errorf("process: put %s: %w", keys[i], err)
		}
		out.Variants = append(out.Variants, Stored{
			Name: r.Name, Key: keys[i], ContentType: r.ContentType, Size: info.Size,
			Width: r.Width, Height: r.Height, Served: r.Served,
		})
	}
	if err := p.Delete(ctx, bucket, in); err != nil {
		return Result{}, fmt.Errorf("process: delete incoming: %w", err)
	}
	return out, nil
}

// decode holds a worker slot through Get and Process only.
func (pl *Pipeline) decode(ctx context.Context, p store.Provider, bucket, in string) (image.Result, error) {
	select {
	case pl.sem <- struct{}{}:
	case <-ctx.Done():
		return image.Result{}, ctx.Err()
	}
	defer func() { <-pl.sem }()

	rc, _, err := p.Get(ctx, bucket, in)
	if err != nil {
		return image.Result{}, fmt.Errorf("get %s: %w", in, err)
	}
	defer rc.Close()
	return image.Process(rc, pl.Config)
}

// Workers returns the bound on concurrent decodes.
func (pl *Pipeline) Workers() int { return cap(pl.sem) }
