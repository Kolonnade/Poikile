// Package upload holds the limits a browser upload is signed with and the
// target the browser sends its bytes to (see README).
package upload

import (
	"errors"
	"mime"
	"strings"
	"time"
)

// DefaultTTL is how long a signed upload stays valid when Limits.TTL is 0.
const DefaultTTL = 5 * time.Minute

// Limits bound one signed upload. Set exactly one of ContentTypePrefix
// (images: any "image/") or ContentType (files: the one type the app
// declared).
type Limits struct {
	// MaxBytes is the largest body accepted.
	MaxBytes int64
	// ContentTypePrefix is what the Content-Type must start with, e.g. "image/".
	ContentTypePrefix string
	// ContentType is the exact Content-Type the upload must carry, e.g.
	// "application/pdf" or "text/plain; charset=utf-8".
	ContentType string
	// TTL is how long the signature lasts; 0 means DefaultTTL.
	TTL time.Duration
}

// Normalize fills defaults and rejects limits that would allow anything.
func (l Limits) Normalize() (Limits, error) {
	if l.MaxBytes <= 0 {
		return Limits{}, errors.New("upload: MaxBytes must be positive")
	}
	switch {
	case l.ContentTypePrefix == "" && l.ContentType == "":
		return Limits{}, errors.New("upload: ContentTypePrefix or ContentType is required")
	case l.ContentTypePrefix != "" && l.ContentType != "":
		return Limits{}, errors.New("upload: set ContentTypePrefix or ContentType, not both")
	case l.ContentType != "":
		if err := ValidContentType(l.ContentType); err != nil {
			return Limits{}, err
		}
	case strings.ContainsAny(l.ContentTypePrefix, "\r\n"):
		return Limits{}, errors.New("upload: ContentTypePrefix contains a newline")
	}
	if l.TTL < 0 {
		return Limits{}, errors.New("upload: TTL must not be negative")
	}
	if l.TTL == 0 {
		l.TTL = DefaultTTL
	}
	return l, nil
}

// Methods a Target may use.
const (
	MethodPost = "POST"
	MethodPut  = "PUT"
)

// Target is where and how the browser sends an upload.
type Target struct {
	// Method is MethodPost or MethodPut.
	Method string
	URL    string
	// Fields are POST form fields, sent before the file field; the browser
	// adds its own Content-Type field.
	Fields  map[string]string
	Expires time.Time
}

// ValidContentType reports whether ct is a well-formed media type, such as
// "application/pdf" or "text/plain; charset=utf-8", that is safe to store
// and send back as a header.
func ValidContentType(ct string) error {
	if strings.ContainsAny(ct, "\r\n") {
		return errors.New("upload: content type contains a newline")
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || !strings.Contains(mt, "/") {
		return errors.New("upload: content type " + ct + " is not a media type")
	}
	return nil
}
