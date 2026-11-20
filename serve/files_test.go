package serve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kolonnade/Poikile/store"
)

// addFile stores data as a file asset with type ct and returns its id.
func (f *fixture) addFile(t *testing.T, n int, ct, filename string, attach bool, data []byte) (string, Asset) {
	t.Helper()
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	key, err := store.ObjectKey("files", id, "file")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.prov.Provider.Put(context.Background(), testBucket, key, bytes.NewReader(data), store.ObjectInfo{ContentType: ct}); err != nil {
		t.Fatal(err)
	}
	a := Asset{ID: id, Owner: "owner-1", Bucket: testBucket, Key: key, Variant: "file", ContentType: ct, Size: int64(len(data)),
		CreatedAt: time.Now(), ETag: "etag-" + id, Attachment: attach, Filename: filename}
	f.res.mu.Lock()
	f.res.assets[id+"/file"] = a
	f.res.mu.Unlock()
	return id, a
}

func fileFixture(t *testing.T, mod func(*Config)) *fixture {
	return newFixture(t, func(c *Config) {
		c.Variants = []string{"thumb", "file"}
		if mod != nil {
			mod(c)
		}
	})
}

func TestServeFileHeaders(t *testing.T) {
	tests := []struct {
		name, ct, filename string
		attach             bool
		disposition        string
	}{
		{"pdf", "application/pdf", "report.pdf", true, `attachment; filename="report.pdf"`},
		{"log no name", "text/plain; charset=utf-8", "", true, "attachment"},
		{"name implies attachment", "image/png", "shot.png", false, `attachment; filename="shot.png"`},
		{"raw jpeg file", "image/jpeg", "", true, "attachment"},
		{"svg never inline", "image/svg+xml", "", false, "attachment"},
		{"html never inline", "text/html", "", false, "attachment"},
		{"xhtml never inline", "application/xhtml+xml", "", false, "attachment"},
		{"jpeg variant inline", "image/jpeg", "", false, "inline"},
		{"webp inline", "image/webp", "", false, "inline"},
		{"unicode name", "application/pdf", "Grüße €.pdf", true, `attachment; filename="Gr__e _.pdf"; filename*=UTF-8''Gr%C3%BC%C3%9Fe%20%E2%82%AC.pdf`},
		{"path and quotes", "text/plain", `../../etc/"pass".txt`, true, `attachment; filename="_pass_.txt"`},
		{"windows path", "text/plain", `C:\Users\me\x.txt`, true, `attachment; filename="x.txt"`},
		{"control chars", "text/plain", "a\r\nX-Evil: 1.txt", true, `attachment; filename="a__X-Evil: 1.txt"`},
		{"dot dot", "text/plain", "..", true, "attachment"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fileFixture(t, nil)
			data := []byte("<html><script>alert(1)</script>")
			id, a := f.addFile(t, i+1, tt.ct, tt.filename, tt.attach, data)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := httptest.NewRecorder()
				f.h.ServeHTTP(rec, httptest.NewRequest(method, "/files/"+id+"?size=file", nil))
				if rec.Code != 200 {
					t.Fatalf("%s: code %d", method, rec.Code)
				}
				h := rec.Header()
				if got := h.Get("Content-Disposition"); got != tt.disposition {
					t.Errorf("%s: Content-Disposition = %q, want %q", method, got, tt.disposition)
				}
				if h.Get("Content-Type") != tt.ct || h.Get("X-Content-Type-Options") != "nosniff" ||
					h.Get("Content-Security-Policy") != "sandbox" || h.Get("ETag") != `"`+a.ETag+`"` ||
					h.Get("Content-Length") != fmt.Sprint(len(data)) {
					t.Errorf("%s: headers %v", method, h)
				}
				if method == http.MethodGet && rec.Body.String() != string(data) {
					t.Errorf("body %q", rec.Body.String())
				}
			}
		})
	}
}

func TestServeStreamsLargeFiles(t *testing.T) {
	pool := NewPool(2)
	f := fileFixture(t, func(c *Config) { c.StreamAbove = 100; c.Pool = pool })
	small := bytes.Repeat([]byte("s"), 100)
	large := bytes.Repeat([]byte("L"), 5000)
	sid, _ := f.addFile(t, 1, "application/octet-stream", "small.bin", true, small)
	lid, la := f.addFile(t, 2, "application/zip", "large.zip", true, large)

	rec := f.do("/files/"+lid+"?size=file", nil)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), large) {
		t.Fatalf("large: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	h := rec.Header()
	if h.Get("X-Asset-Cache") != "stream" || h.Get("Content-Length") != "5000" ||
		h.Get("Content-Disposition") != `attachment; filename="large.zip"` || h.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("large headers %v", h)
	}
	if n := entries(f.c); n != 0 {
		t.Fatalf("large file cached: %d entries", n)
	}
	if rec := f.do("/files/"+sid+"?size=file", nil); rec.Code != 200 || rec.Header().Get("X-Asset-Cache") != "miss" || entries(f.c) != 1 {
		t.Fatalf("small: %d %v, %d entries", rec.Code, rec.Header(), entries(f.c))
	}
	if pool.InUse() != 0 {
		t.Fatalf("pool slot held after the stream")
	}

	// 304 still answers before storage.
	before := f.prov.gets.Load()
	if rec := f.do("/files/"+lid+"?size=file", map[string]string{"If-None-Match": `"` + la.ETag + `"`}); rec.Code != 304 {
		t.Fatalf("304: got %d", rec.Code)
	}
	if f.prov.gets.Load() != before {
		t.Fatal("304 read storage")
	}

	// A size the resolver doesn't agree with is an error, not a short body.
	la.Size = 6000
	f.res.assets[lid+"/file"] = la
	if rec := f.do("/files/"+lid+"?size=file", nil); rec.Code != 500 {
		t.Fatalf("size mismatch: %d", rec.Code)
	}
	// A deleted object is the usual NotFound.
	if err := f.prov.Provider.Delete(context.Background(), testBucket, la.Key); err != nil {
		t.Fatal(err)
	}
	if rec := f.do("/files/"+lid+"?size=file", nil); rec.Code != 404 {
		t.Fatalf("deleted: %d", rec.Code)
	}
}

func TestDeleteEvictsThenDeletes(t *testing.T) {
	f := fileFixture(t, nil)
	id, a := f.addFile(t, 1, "application/pdf", "a.pdf", true, []byte("%PDF-"))
	if rec := f.do("/files/"+id+"?size=file", nil); rec.Code != 200 || entries(f.c) != 1 {
		t.Fatalf("not cached: %d", rec.Code)
	}
	if err := Delete(context.Background(), f.c, f.prov, a.Bucket, a.Key); err != nil {
		t.Fatal(err)
	}
	if n := entries(f.c); n != 0 {
		t.Fatalf("still cached: %d entries", n)
	}
	if _, _, err := f.prov.Get(context.Background(), a.Bucket, a.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("still stored: %v", err)
	}
	if rec := f.do("/files/"+id+"?size=file", nil); rec.Code != 404 {
		t.Fatalf("served after delete: %d", rec.Code)
	}
	if err := Delete(context.Background(), f.c, f.prov, a.Bucket, a.Key); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}
