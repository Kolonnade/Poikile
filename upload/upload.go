// Package upload holds the limits a browser upload is signed with and the
// target the browser sends its bytes to (see README).
package upload

import (
	"errors"
	"time"
)

// DefaultTTL is how long a signed upload stays valid when Limits.TTL is 0.
const DefaultTTL = 5 * time.Minute

// Limits bound one signed upload.
type Limits struct {
	// MaxBytes is the largest body accepted.
	MaxBytes int64
	// ContentTypePrefix is what the Content-Type must start with, e.g. "image/".
	ContentTypePrefix string
	// TTL is how long the signature lasts; 0 means DefaultTTL.
	TTL time.Duration
}

// Normalize fills defaults and rejects limits that would allow anything.
func (l Limits) Normalize() (Limits, error) {
	if l.MaxBytes <= 0 {
		return Limits{}, errors.New("upload: MaxBytes must be positive")
	}
	if l.ContentTypePrefix == "" {
		return Limits{}, errors.New("upload: ContentTypePrefix is required")
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
