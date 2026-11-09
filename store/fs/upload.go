package fs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/upload"
)

func (p *Provider) sign(b, key string, exp, max int64, ct string) string {
	m := hmac.New(sha256.New, p.opts.Secret)
	m.Write([]byte(b + "\n" + key + "\n" + strconv.FormatInt(exp, 10) + "\n" + strconv.FormatInt(max, 10) + "\n" + ct))
	return hex.EncodeToString(m.Sum(nil))
}

// SignUpload returns a PUT target on UploadHandler, signed with Secret.
func (p *Provider) SignUpload(ctx context.Context, b, key string, lim upload.Limits) (upload.Target, error) {
	lim, err := store.CheckUpload(key, lim)
	if err != nil {
		return upload.Target{}, err
	}
	if _, _, err := p.path(b, key); err != nil {
		return upload.Target{}, err
	}
	expires := p.opts.Now().Add(lim.TTL)
	exp := expires.Unix()
	q := url.Values{}
	q.Set("exp", strconv.FormatInt(exp, 10))
	q.Set("max", strconv.FormatInt(lim.MaxBytes, 10))
	q.Set("ct", lim.ContentTypePrefix)
	q.Set("sig", p.sign(b, key, exp, lim.MaxBytes, lim.ContentTypePrefix))
	return upload.Target{
		Method:  "PUT",
		URL:     p.opts.BaseURL + "/" + b + "/" + key + "?" + q.Encode(),
		Expires: time.Unix(exp, 0),
	}, nil
}

// UploadHandler accepts the PUTs SignUpload signs. The request path must be
// "<bucket>/<key>" (a leading "/" is allowed); mount it with
// http.StripPrefix.
func (p *Provider) UploadHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.Header().Set("Allow", http.MethodPut)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b, key, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		exp, err1 := strconv.ParseInt(q.Get("exp"), 10, 64)
		max, err2 := strconv.ParseInt(q.Get("max"), 10, 64)
		ct := q.Get("ct")
		if err1 != nil || err2 != nil || max <= 0 || ct == "" ||
			!strings.HasPrefix(key, "incoming/") ||
			!hmac.Equal([]byte(q.Get("sig")), []byte(p.sign(b, key, exp, max, ct))) ||
			p.opts.Now().Unix() > exp {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		contentType := r.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, ct) {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			return
		}
		if r.ContentLength > max {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		body := http.MaxBytesReader(w, r.Body, max)
		if err := p.Put(r.Context(), b, key, body, store.ObjectInfo{ContentType: contentType}); err != nil {
			var mbe *http.MaxBytesError
			switch {
			case errors.As(err, &mbe):
				http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			case errors.Is(err, store.ErrNoBucket):
				http.Error(w, "not found", http.StatusNotFound)
			default:
				http.Error(w, "upload failed", http.StatusBadRequest)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
