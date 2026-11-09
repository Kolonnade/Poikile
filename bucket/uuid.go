package bucket

import "regexp"

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func isUUID(s string) bool { return uuidRE.MatchString(s) }

// IsUUID reports whether s is a canonical lowercase UUID.
func IsUUID(s string) bool { return isUUID(s) }
