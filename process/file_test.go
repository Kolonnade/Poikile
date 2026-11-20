package process_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Kolonnade/Poikile/process"
	pt "github.com/Kolonnade/Poikile/process/image/imagetest"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/store/memory"
)

func putTyped(t *testing.T, p store.Provider, key, ct string, data []byte) {
	t.Helper()
	if err := p.Put(context.Background(), bkt, key, bytes.NewReader(data), store.ObjectInfo{ContentType: ct, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
}

func getBytes(t *testing.T, p store.Provider, key string) ([]byte, store.ObjectInfo, error) {
	t.Helper()
	rc, info, err := p.Get(context.Background(), bkt, key)
	if err != nil {
		return nil, info, err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return b, info, err
}

func TestStoreFile(t *testing.T) {
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0xA0}, 2000)...)
	tests := []struct {
		name, declared string
		data           []byte
		sniffed        string
	}{
		{"pdf", "application/pdf", pdf, "application/pdf"},
		{"log", "text/plain; charset=utf-8", []byte("line one\nline two\n"), "text/plain; charset=utf-8"},
		{"json", "application/json", []byte(`{"ok":true}`), "text/plain; charset=utf-8"},
		{"zip", "application/zip", []byte("PK\x03\x04rest-of-archive"), "application/zip"},
		{"tiny", "application/octet-stream", []byte{1}, "application/octet-stream"},
		{"html declared html", "text/html", []byte("<html><script>alert(1)</script>"), "text/html; charset=utf-8"},
		{"html declared text", "text/plain", []byte("<html><script>alert(1)</script>"), "text/html; charset=utf-8"},
		{"svg", "image/svg+xml", pt.SVG(), "text/xml; charset=utf-8"},
		{"jpeg", "image/jpeg", pt.JPEG(8, 8, nil, nil), "image/jpeg"},
		{"png as webp", "image/webp", pt.PNG(4, 4, nil, nil), "image/png"},
		{"tiff", "image/tiff", []byte("II*\x00\x08\x00\x00\x00"), "image/tiff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			p := setup(t)
			in, _ := store.IncomingKey(upID)
			putTyped(t, p, in, tt.declared, tt.data)
			f, err := process.StoreFile(ctx, p, bkt, upID, assetID, process.FileOptions{MaxBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			want := process.File{Key: "files/" + assetID + "/file", DeclaredType: tt.declared, SniffedType: tt.sniffed, Size: int64(len(tt.data))}
			if f != want {
				t.Fatalf("File = %+v, want %+v", f, want)
			}
			got, info, err := getBytes(t, p, f.Key)
			if err != nil || !bytes.Equal(got, tt.data) || info.ContentType != tt.declared {
				t.Fatalf("stored %d bytes of %q, %v; want the upload unchanged", len(got), info.ContentType, err)
			}
			if _, _, err := p.Get(ctx, bkt, in); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("incoming kept: %v", err)
			}
		})
	}
}

func TestStoreFileRefused(t *testing.T) {
	tests := []struct {
		name, declared string
		data           []byte
		o              process.FileOptions
		want           error
	}{
		{"not an image", "image/png", []byte("%PDF-1.7 not a png"), process.FileOptions{MaxBytes: 100}, process.ErrFileType},
		{"html as jpeg", "image/jpeg", []byte("<html><script>alert(1)</script>"), process.FileOptions{MaxBytes: 100}, process.ErrFileType},
		{"no type", "", []byte("x"), process.FileOptions{MaxBytes: 100}, process.ErrFileType},
		{"bad type", "pdf", []byte("x"), process.FileOptions{MaxBytes: 100}, process.ErrFileType},
		{"too large", "text/plain", []byte("12345"), process.FileOptions{MaxBytes: 4}, process.ErrFileSize},
		{"empty", "text/plain", nil, process.FileOptions{MaxBytes: 4}, process.ErrFileSize},
		{"wrong size", "text/plain", []byte("123"), process.FileOptions{MaxBytes: 4, Size: 4}, process.ErrFileSize},
		{"refused even when kept", "image/gif", []byte("nope"), process.FileOptions{MaxBytes: 100, KeepIncoming: true}, process.ErrFileType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			p := setup(t)
			in, _ := store.IncomingKey(upID)
			putTyped(t, p, in, tt.declared, tt.data)
			_, err := process.StoreFile(ctx, p, bkt, upID, assetID, tt.o)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if _, _, err := p.Get(ctx, bkt, in); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("refused incoming kept: %v", err)
			}
			if _, _, err := p.Get(ctx, bkt, "files/"+assetID+"/file"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("refused file stored: %v", err)
			}
		})
	}
}

func TestStoreFileMissingKeepsNothing(t *testing.T) {
	p := setup(t)
	_, err := process.StoreFile(context.Background(), p, bkt, upID, assetID, process.FileOptions{MaxBytes: 10})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := process.StoreFile(context.Background(), p, bkt, upID, assetID, process.FileOptions{}); err == nil {
		t.Fatal("MaxBytes 0 accepted")
	}
	if _, err := process.StoreFile(context.Background(), p, bkt, "nope", assetID, process.FileOptions{MaxBytes: 10}); err == nil {
		t.Fatal("bad upload id accepted")
	}
	if _, err := process.StoreFile(context.Background(), p, bkt, upID, assetID, process.FileOptions{MaxBytes: 10, Kind: "Bad"}); err == nil {
		t.Fatal("bad kind accepted")
	}
}

// failPut fails every Put, as a storage outage would.
type failPut struct{ *memory.Provider }

func (failPut) Put(context.Context, string, string, io.Reader, store.ObjectInfo) error {
	return errors.New("boom")
}

func TestStoreFilePutFailureKeepsIncoming(t *testing.T) {
	ctx := context.Background()
	p := setup(t)
	in, _ := store.IncomingKey(upID)
	putTyped(t, p, in, "text/plain", []byte("hello"))
	if _, err := process.StoreFile(ctx, failPut{p}, bkt, upID, assetID, process.FileOptions{MaxBytes: 10}); err == nil {
		t.Fatal("no error")
	}
	if _, _, err := p.Get(ctx, bkt, in); err != nil {
		t.Fatalf("incoming gone after a failed put: %v", err)
	}
}

// lyingSize reports a Size that its body doesn't have.
type lyingSize struct{ *memory.Provider }

func (l lyingSize) Get(ctx context.Context, b, key string) (io.ReadCloser, store.ObjectInfo, error) {
	rc, info, err := l.Provider.Get(ctx, b, key)
	info.Size++
	return rc, info, err
}

func TestStoreFileShortBodyNotStored(t *testing.T) {
	ctx := context.Background()
	p := setup(t)
	in, _ := store.IncomingKey(upID)
	putTyped(t, p, in, "text/plain", []byte(strings.Repeat("x", 600)))
	if _, err := process.StoreFile(ctx, lyingSize{p}, bkt, upID, assetID, process.FileOptions{MaxBytes: 1000}); err == nil {
		t.Fatal("short body stored")
	}
	if _, _, err := p.Get(ctx, bkt, "files/"+assetID+"/file"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("short body stored: %v", err)
	}
}

// An image can be kept as uploaded and also processed into variants.
func TestStoreFileThenRun(t *testing.T) {
	ctx := context.Background()
	p := setup(t)
	in, _ := store.IncomingKey(upID)
	jpeg := pt.JPEG(64, 48, nil, &pt.EXIF{})
	putTyped(t, p, in, "image/jpeg", jpeg)
	f, err := process.StoreFile(ctx, p, bkt, upID, assetID, process.FileOptions{MaxBytes: 1 << 20, KeepIncoming: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := pipeline(t, 1).Run(ctx, p, bkt, upID, assetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Variants) == 0 {
		t.Fatal("no variants")
	}
	got, _, err := getBytes(t, p, f.Key)
	if err != nil || !bytes.Equal(got, jpeg) {
		t.Fatalf("original not kept as uploaded: %v", err)
	}
	if _, _, err := p.Get(ctx, bkt, in); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("incoming kept after Run: %v", err)
	}
}
