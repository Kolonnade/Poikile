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
		"both types":   {MaxBytes: 10, ContentTypePrefix: "image/", ContentType: "image/png"},
		"bad type":     {MaxBytes: 10, ContentType: "pdf"},
		"newline type": {MaxBytes: 10, ContentType: "text/plain\r\nX: y"},
		"newline pfx":  {MaxBytes: 10, ContentTypePrefix: "image/\n"},
	} {
		if _, err := bad.Normalize(); err == nil {
			t.Errorf("%s: Normalize accepted %+v", name, bad)
		}
	}
}

func TestNormalizeExactType(t *testing.T) {
	for _, ct := range []string{"application/pdf", "text/plain; charset=utf-8", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"} {
		if _, err := (Limits{MaxBytes: 10, ContentType: ct}).Normalize(); err != nil {
			t.Errorf("%s: %v", ct, err)
		}
	}
}
