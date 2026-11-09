package upload

import "testing"

func TestNormalize(t *testing.T) {
	l, err := Limits{MaxBytes: 10, ContentTypePrefix: "image/"}.Normalize()
	if err != nil || l.TTL != DefaultTTL {
		t.Fatalf("Normalize = %+v, %v; want TTL %v", l, err, DefaultTTL)
	}
	for name, bad := range map[string]Limits{
		"zero max":     {MaxBytes: 0, ContentTypePrefix: "image/"},
		"negative max": {MaxBytes: -1, ContentTypePrefix: "image/"},
		"empty prefix": {MaxBytes: 10},
		"negative ttl": {MaxBytes: 10, ContentTypePrefix: "image/", TTL: -1},
	} {
		if _, err := bad.Normalize(); err == nil {
			t.Errorf("%s: Normalize accepted %+v", name, bad)
		}
	}
}
