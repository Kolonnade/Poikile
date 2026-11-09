package fs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/upload"
)

const upKey = "incoming/0f8fad5b-d9cb-469f-a165-70867728950e"

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func uploadSetup(t *testing.T) (*Provider, *clock, *httptest.Server) {
	t.Helper()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p, _ := setup(t, Options{BaseURL: srv.URL + "/upload", Now: c.now})
	mux.Handle("/upload/", http.StripPrefix("/upload", p.UploadHandler()))
	return p, c, srv
}

func sign(t *testing.T, p *Provider) string {
	t.Helper()
	tg, err := p.SignUpload(context.Background(), bkt, upKey, upload.Limits{MaxBytes: 10, ContentTypePrefix: "image/"})
	if err != nil {
		t.Fatal(err)
	}
	if tg.Method != "PUT" {
		t.Fatalf("Method = %s", tg.Method)
	}
	return tg.URL
}

func do(t *testing.T, method, u, ct, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestUploadValid(t *testing.T) {
	p, _, _ := uploadSetup(t)
	if code := do(t, "PUT", sign(t, p), "image/png", "pngbytes"); code != http.StatusNoContent {
		t.Fatalf("status = %d", code)
	}
	s, info, err := get(t, p, upKey)
	if err != nil || s != "pngbytes" || info.ContentType != "image/png" {
		t.Fatalf("Get = %q, %+v, %v", s, info, err)
	}
}

func TestUploadTamperedSig(t *testing.T) {
	p, _, _ := uploadSetup(t)
	u, _ := url.Parse(sign(t, p))
	q := u.Query()
	q.Set("max", "1000000")
	u.RawQuery = q.Encode()
	if code := do(t, "PUT", u.String(), "image/png", "x"); code != http.StatusForbidden {
		t.Fatalf("status = %d", code)
	}
}

func TestUploadExpired(t *testing.T) {
	p, c, _ := uploadSetup(t)
	u := sign(t, p)
	c.t = c.t.Add(upload.DefaultTTL + time.Second)
	if code := do(t, "PUT", u, "image/png", "x"); code != http.StatusForbidden {
		t.Fatalf("status = %d", code)
	}
}

func TestUploadWrongType(t *testing.T) {
	p, _, _ := uploadSetup(t)
	if code := do(t, "PUT", sign(t, p), "text/html", "x"); code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d", code)
	}
}

func TestUploadTooLarge(t *testing.T) {
	p, _, _ := uploadSetup(t)
	if code := do(t, "PUT", sign(t, p), "image/png", strings.Repeat("x", 11)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", code)
	}
	if _, _, err := get(t, p, upKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get = %v", err)
	}
}

func TestUploadMethod(t *testing.T) {
	p, _, _ := uploadSetup(t)
	if code := do(t, "GET", sign(t, p), "", ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", code)
	}
}
