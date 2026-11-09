// Package fs is a store.Provider over files under a root directory, for
// local dev and home servers. Each object is one file, root/<bucket>/<key>,
// holding its content type on the first line and then its bytes; writes go
// to a temp file and are renamed into place, so a failed Put leaves the
// previous object (or nothing) behind.
package fs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store"
)

const tmpDir = ".tmp"

// Options configure a Provider.
type Options struct {
	// BaseURL is where UploadHandler is mounted, e.g. "http://localhost:8080/upload".
	BaseURL string
	// Secret signs upload URLs; at least 16 bytes.
	Secret []byte
	// Now defaults to time.Now.
	Now func() time.Time
}

// Provider stores objects as files.
type Provider struct {
	root string
	opts Options
}

var _ store.Provider = (*Provider)(nil)

// New returns a Provider rooted at root.
func New(root string, opts Options) (*Provider, error) {
	if root == "" {
		return nil, errors.New("fs: root is required")
	}
	if len(opts.Secret) < 16 {
		return nil, errors.New("fs: Secret must be at least 16 bytes")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("fs: %w", err)
	}
	return &Provider{root: abs, opts: opts}, nil
}

func (p *Provider) bucketDir(b string) (string, error) {
	if err := bucket.ValidateName(b); err != nil {
		return "", err
	}
	return filepath.Join(p.root, b), nil
}

func (p *Provider) path(b, key string) (string, string, error) {
	dir, err := p.bucketDir(b)
	if err != nil {
		return "", "", err
	}
	if err := store.ValidKey(key); err != nil {
		return "", "", err
	}
	return dir, filepath.Join(dir, filepath.FromSlash(key)), nil
}

// EnsureBucket creates root/<bucket> and its temp directory.
func (p *Provider) EnsureBucket(ctx context.Context, name string, t bucket.Template) error {
	dir, err := p.bucketDir(name)
	if err != nil {
		return err
	}
	if err := t.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, tmpDir), 0o700); err != nil {
		return fmt.Errorf("fs: ensure bucket %s: %w", name, err)
	}
	return nil
}

// Put writes body atomically.
func (p *Provider) Put(ctx context.Context, b, key string, body io.Reader, info store.ObjectInfo) (err error) {
	dir, dst, err := p.path(b, key)
	if err != nil {
		return err
	}
	if strings.ContainsAny(info.ContentType, "\r\n") {
		return fmt.Errorf("fs: put %s/%s: content type contains a newline", b, key)
	}
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("fs: put %s/%s: %w", b, key, store.ErrNoBucket)
		}
		return fmt.Errorf("fs: put %s/%s: %w", b, key, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("fs: put %s/%s: %w", b, key, err)
	}
	f, err := os.CreateTemp(filepath.Join(dir, tmpDir), "put-*")
	if err != nil {
		return fmt.Errorf("fs: put %s/%s: %w", b, key, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err = io.WriteString(f, info.ContentType+"\n"); err != nil {
		return fmt.Errorf("fs: put %s/%s: %w", b, key, err)
	}
	if _, err = io.Copy(f, body); err != nil {
		return fmt.Errorf("fs: put %s/%s: %w", b, key, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("fs: put %s/%s: %w", b, key, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("fs: put %s/%s: %w", b, key, err)
	}
	if err = os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("fs: put %s/%s: %w", b, key, err)
	}
	return nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// Get opens the object.
func (p *Provider) Get(ctx context.Context, b, key string) (io.ReadCloser, store.ObjectInfo, error) {
	_, src, err := p.path(b, key)
	if err != nil {
		return nil, store.ObjectInfo{}, err
	}
	f, err := os.Open(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, store.ObjectInfo{}, fmt.Errorf("fs: get %s/%s: %w", b, key, store.ErrNotFound)
		}
		return nil, store.ObjectInfo{}, fmt.Errorf("fs: get %s/%s: %w", b, key, err)
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		return nil, store.ObjectInfo{}, fmt.Errorf("fs: get %s/%s: %w", b, key, store.ErrNotFound)
	}
	r := bufio.NewReader(f)
	ct, err := r.ReadString('\n')
	if err != nil {
		f.Close()
		return nil, store.ObjectInfo{}, fmt.Errorf("fs: get %s/%s: bad header: %w", b, key, err)
	}
	info := store.ObjectInfo{
		ContentType: strings.TrimSuffix(ct, "\n"),
		Size:        st.Size() - int64(len(ct)),
	}
	return readCloser{r, f}, info, nil
}

// Delete removes the object; a missing one is not an error.
func (p *Provider) Delete(ctx context.Context, b, key string) error {
	_, dst, err := p.path(b, key)
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fs: delete %s/%s: %w", b, key, err)
	}
	return nil
}
