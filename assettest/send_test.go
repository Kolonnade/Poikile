package assettest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Kolonnade/Poikile/upload"
)

func TestSendPostOrder(t *testing.T) {
	type part struct{ name, value string }
	var got []part
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			t.Errorf("MultipartReader: %v", err)
			return
		}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("NextPart: %v", err)
				return
			}
			b, _ := io.ReadAll(p)
			got = append(got, part{p.FormName(), string(b)})
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tg := upload.Target{Method: upload.MethodPost, URL: srv.URL, Fields: map[string]string{"key": "incoming/x", "a": "1"}}
	status, err := Send(context.Background(), tg, "image/png", []byte("bytes"))
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("Send = %d, %v", status, err)
	}
	want := []part{{"a", "1"}, {"key", "incoming/x"}, {"Content-Type", "image/png"}, {"file", "bytes"}}
	if len(got) != len(want) {
		t.Fatalf("parts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSendPut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "image/png" {
			t.Errorf("Content-Type = %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		if !bytes.Equal(b, []byte("bytes")) {
			t.Errorf("body = %q", b)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	status, err := Send(context.Background(), upload.Target{Method: upload.MethodPut, URL: srv.URL}, "image/png", []byte("bytes"))
	if err != nil || status != http.StatusCreated {
		t.Fatalf("Send = %d, %v", status, err)
	}
}
