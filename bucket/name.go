// Package bucket names buckets and describes the baseline every bucket gets
// (README rules 4 and 5): one bucket per owner, named
// prefix + kind + "-" + UUID and validated to S3's rules.
package bucket

import (
	"fmt"
	"regexp"
)

// MaxPrefixLen keeps prefix + a 3-letter kind + "-" + a 36-character UUID
// within S3's 63-character limit.
const MaxPrefixLen = 23

var (
	nameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	prefixRE = regexp.MustCompile(`^[a-z0-9-]+$`)
	kindRE   = regexp.MustCompile(`^[a-z]{1,3}$`)
)

// ValidatePrefix checks a bucket prefix: non-empty, lowercase letters,
// digits and hyphens, at most MaxPrefixLen characters.
func ValidatePrefix(p string) error {
	if p == "" {
		return fmt.Errorf("bucket: prefix is required")
	}
	if len(p) > MaxPrefixLen || !prefixRE.MatchString(p) {
		return fmt.Errorf("bucket: prefix %q must be at most %d lowercase letters, digits or hyphens", p, MaxPrefixLen)
	}
	return nil
}

// ValidateName checks n against S3's bucket naming rules.
func ValidateName(n string) error {
	if !nameRE.MatchString(n) {
		return fmt.Errorf("bucket: %q is not a valid S3 bucket name", n)
	}
	return nil
}

// Name returns prefix + kind + "-" + ownerID, e.g. "acme-prod-grp-<uuid>".
// kind is one to three lowercase letters ("grp", "usr"); ownerID is a
// canonical lowercase UUID.
func Name(prefix, kind, ownerID string) (string, error) {
	if err := ValidatePrefix(prefix); err != nil {
		return "", err
	}
	if !kindRE.MatchString(kind) {
		return "", fmt.Errorf("bucket: kind %q must be 1 to 3 lowercase letters", kind)
	}
	if !isUUID(ownerID) {
		return "", fmt.Errorf("bucket: owner %q is not a lowercase UUID", ownerID)
	}
	n := prefix + kind + "-" + ownerID
	if err := ValidateName(n); err != nil {
		return "", err
	}
	return n, nil
}
