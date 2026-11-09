package store

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/upload"
)

var partRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// IncomingKey returns "incoming/<uploadID>".
func IncomingKey(uploadID string) (string, error) {
	if !bucket.IsUUID(uploadID) {
		return "", fmt.Errorf("store: upload id %q is not a lowercase UUID", uploadID)
	}
	return bucket.IncomingPrefix + uploadID, nil
}

// ObjectKey returns "<kind>/<assetID>/<variant>", e.g. "photos/<uuid>/thumb".
func ObjectKey(kind, assetID, variant string) (string, error) {
	if !partRE.MatchString(kind) {
		return "", fmt.Errorf("store: kind %q is invalid", kind)
	}
	if !bucket.IsUUID(assetID) {
		return "", fmt.Errorf("store: asset id %q is not a lowercase UUID", assetID)
	}
	if !partRE.MatchString(variant) {
		return "", fmt.Errorf("store: variant %q is invalid", variant)
	}
	return kind + "/" + assetID + "/" + variant, nil
}

// ValidKey rejects keys that are empty, absolute, have empty or
// dot-leading segments, or are not already clean. Providers call it so a
// key can never escape its bucket.
func ValidKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || path.Clean(key) != key {
		return fmt.Errorf("store: invalid key %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return fmt.Errorf("store: invalid key %q", key)
		}
	}
	return nil
}

// CheckUpload is the rule every SignUpload applies: the key is valid and
// under incoming/, and the limits normalize.
func CheckUpload(key string, lim upload.Limits) (upload.Limits, error) {
	if err := ValidKey(key); err != nil {
		return lim, err
	}
	if !strings.HasPrefix(key, bucket.IncomingPrefix) {
		return lim, fmt.Errorf("store: uploads must go under %s, not %q", bucket.IncomingPrefix, key)
	}
	return lim.Normalize()
}
