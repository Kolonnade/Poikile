package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Kolonnade/Poikile/process/image"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/upload"
)

// Files are stored as uploaded, under <kind>/<assetID>/FileVariant, and
// only ever served as attachments (README rule 6).
const (
	FileVariant     = "file"
	DefaultFileKind = "files"
)

// Refusals from StoreFile. A refused upload's incoming object is deleted.
var (
	ErrFileSize = errors.New("file is empty, over MaxBytes, or not the expected size")
	ErrFileType = errors.New("file's declared type is malformed, or names an image its bytes are not")
)

// sniffLen is how many leading bytes StoreFile sniffs, as
// http.DetectContentType reads.
const sniffLen = 512

// FileOptions configure StoreFile.
type FileOptions struct {
	// Kind is the key's first segment; empty means DefaultFileKind.
	Kind string
	// MaxBytes is the largest file accepted, normally the MaxBytes the
	// upload was signed with. Required.
	MaxBytes int64
	// Size, when positive, is the exact size the app expects (the size the
	// client declared).
	Size int64
	// KeepIncoming leaves incoming/<uploadID> in place after storing the
	// file, so a Pipeline.Run can then make an image's variants from the
	// same upload. A refusal still deletes it.
	KeepIncoming bool
}

// File is what the app records for a stored file.
type File struct {
	// Key is where the file is stored, <kind>/<assetID>/file.
	Key string
	// DeclaredType is the Content-Type the upload carried and the file is
	// stored and served with.
	DeclaredType string
	// SniffedType is what the leading bytes look like, e.g.
	// "application/pdf", "text/plain; charset=utf-8" or
	// "application/octet-stream" when nothing matched. The app compares it
	// with DeclaredType and refuses the mismatches it cares about.
	SniffedType string
	Size        int64
}

// StoreFile verifies incoming/<uploadID> and copies its bytes, unchanged,
// to <kind>/<assetID>/file, then deletes incoming unless KeepIncoming.
//
// It refuses (ErrFileSize, ErrFileType) and deletes incoming when the file
// is empty, over MaxBytes or not Size; when the declared type is missing
// or malformed; or when the declared type is a raster image (image/*
// other than SVG) and the bytes are not an image. Any other failure keeps
// incoming so the job can retry.
func StoreFile(ctx context.Context, p store.Provider, bucket, uploadID, assetID string, o FileOptions) (File, error) {
	if o.MaxBytes <= 0 {
		return File{}, errors.New("process: FileOptions.MaxBytes must be positive")
	}
	if o.Kind == "" {
		o.Kind = DefaultFileKind
	}
	in, err := store.IncomingKey(uploadID)
	if err != nil {
		return File{}, fmt.Errorf("process: %w", err)
	}
	out, err := store.ObjectKey(o.Kind, assetID, FileVariant)
	if err != nil {
		return File{}, fmt.Errorf("process: %w", err)
	}

	rc, info, err := p.Get(ctx, bucket, in)
	if err != nil {
		return File{}, fmt.Errorf("process: get %s: %w", in, err)
	}
	defer rc.Close()

	refuse := func(err error) (File, error) {
		rc.Close()
		if derr := p.Delete(ctx, bucket, in); derr != nil {
			return File{}, fmt.Errorf("process: %w (delete incoming: %v)", err, derr)
		}
		return File{}, fmt.Errorf("process: %w", err)
	}
	if info.Size <= 0 || info.Size > o.MaxBytes || (o.Size > 0 && info.Size != o.Size) {
		return refuse(fmt.Errorf("%w: %d bytes", ErrFileSize, info.Size))
	}
	if upload.ValidContentType(info.ContentType) != nil {
		return refuse(fmt.Errorf("%w: declared %q", ErrFileType, info.ContentType))
	}

	head := make([]byte, min(int64(sniffLen), info.Size))
	n, err := io.ReadFull(rc, head)
	if err != nil {
		return File{}, fmt.Errorf("process: read %s: %w", in, err)
	}
	head = head[:n]
	f := File{Key: out, DeclaredType: info.ContentType, SniffedType: sniff(head), Size: info.Size}
	if rasterType(f.DeclaredType) && !strings.HasPrefix(f.SniffedType, "image/") {
		return refuse(fmt.Errorf("%w: declared %s, sniffed %s", ErrFileType, f.DeclaredType, f.SniffedType))
	}

	body := &exactReader{r: io.MultiReader(bytes.NewReader(head), rc), left: info.Size}
	if err := p.Put(ctx, bucket, out, body, store.ObjectInfo{ContentType: f.DeclaredType, Size: f.Size}); err != nil {
		return File{}, fmt.Errorf("process: put %s: %w", out, err)
	}
	if !o.KeepIncoming {
		if err := p.Delete(ctx, bucket, in); err != nil {
			return File{}, fmt.Errorf("process: delete incoming: %w", err)
		}
	}
	return f, nil
}

// rasterType reports an image type a browser would decode: image/*
// except SVG, which is a document.
func rasterType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && strings.HasPrefix(mt, "image/") && mt != "image/svg+xml"
}

// sniff names the type of b from its leading bytes: the image formats
// process/image reads, then TIFF and AVIF, then http.DetectContentType.
func sniff(b []byte) string {
	switch {
	case len(b) >= 12 && string(b[4:8]) == "ftyp" && (string(b[8:12]) == "avif" || string(b[8:12]) == "avis"):
		return "image/avif"
	case bytes.HasPrefix(b, []byte("II*\x00")), bytes.HasPrefix(b, []byte("MM\x00*")):
		return "image/tiff"
	}
	if f, err := image.Sniff(b); err == nil {
		return "image/" + f
	}
	return http.DetectContentType(b)
}

// exactReader fails unless r yields exactly left bytes, so a short or long
// object is never stored as if whole.
type exactReader struct {
	r    io.Reader
	left int64
}

var errSizeChanged = errors.New("process: object size differs from its Content-Length")

func (e *exactReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	e.left -= int64(n)
	if e.left < 0 {
		return n, errSizeChanged
	}
	if err == io.EOF && e.left != 0 {
		return n, errSizeChanged
	}
	return n, err
}
