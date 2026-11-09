package bucket

import (
	"fmt"
	"net/url"
)

// Defaults for Template.
const (
	DefaultNoncurrentDays = 30
	IncomingDays          = 1
	IncomingPrefix        = "incoming/"
)

// CORSMethods are the only methods the one allowed origin may use.
var CORSMethods = []string{"PUT", "POST"}

// Template is the configurable part of the bucket baseline.
//
// Blocking all public access, default encryption (SSE-S3) and versioning
// are always applied by every provider and are deliberately not fields, so
// they cannot be switched off. Objects under IncomingPrefix expire after
// IncomingDays; CORS allows CORSMethods from Origin only.
type Template struct {
	// Origin is the one web origin CORS allows, scheme://host[:port].
	Origin string
	// NoncurrentDays is when noncurrent versions expire.
	NoncurrentDays int
	// IncomingDays is when objects under incoming/ expire.
	IncomingDays int
	// LogBucket receives access logs; empty disables access logging.
	LogBucket string
}

// Baseline returns the template with defaults for origin.
func Baseline(origin string) Template {
	return Template{Origin: origin, NoncurrentDays: DefaultNoncurrentDays, IncomingDays: IncomingDays}
}

// Validate checks the template.
func (t Template) Validate() error {
	u, err := url.Parse(t.Origin)
	if t.Origin == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("bucket: origin %q must be scheme://host[:port]", t.Origin)
	}
	if t.NoncurrentDays < 1 {
		return fmt.Errorf("bucket: NoncurrentDays must be at least 1")
	}
	if t.IncomingDays < 1 {
		return fmt.Errorf("bucket: IncomingDays must be at least 1")
	}
	if t.LogBucket != "" {
		if err := ValidateName(t.LogBucket); err != nil {
			return fmt.Errorf("bucket: log bucket: %w", err)
		}
	}
	return nil
}
