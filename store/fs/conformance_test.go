package fs_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Kolonnade/Poikile/assettest"
	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store/fs"
)

func TestConformance(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p, err := fs.New(t.TempDir(), fs.Options{BaseURL: srv.URL + "/upload", Secret: bytes.Repeat([]byte("s"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/upload/", http.StripPrefix("/upload", p.UploadHandler()))
	assettest.Provider(t, p, assettest.Options{
		Bucket:   "conformance-fs",
		Template: bucket.Baseline("https://app.example"),
		Uploads:  true,
	})
}
