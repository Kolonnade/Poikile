package bucket

import (
	"strings"
	"testing"
)

const testUUID = "0f8fad5b-d9cb-469f-a165-70867728950e"

func TestName(t *testing.T) {
	p23 := strings.Repeat("a", 23)
	tests := []struct {
		name, prefix, kind, owner string
		ok                        bool
	}{
		{"valid", "acme-prod-", "grp", testUUID, true},
		{"63 chars", p23, "grp", testUUID, true},
		{"prefix 24", p23 + "a", "grp", testUUID, false},
		{"uppercase prefix", "Acme-", "grp", testUUID, false},
		{"underscore prefix", "acme_", "grp", testUUID, false},
		{"empty kind", "acme-", "", testUUID, false},
		{"long kind", "acme-", "grps", testUUID, false},
		{"upper kind", "acme-", "Grp", testUUID, false},
		{"bad owner", "acme-", "grp", "not-a-uuid", false},
		{"upper owner", "acme-", "grp", strings.ToUpper(testUUID), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := Name(tt.prefix, tt.kind, tt.owner)
			if (err == nil) != tt.ok {
				t.Fatalf("Name = %q, %v; want ok=%v", n, err, tt.ok)
			}
			if tt.ok {
				if n != tt.prefix+tt.kind+"-"+tt.owner || len(n) > 63 {
					t.Fatalf("Name = %q", n)
				}
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	for _, n := range []string{"ab", strings.Repeat("a", 64), "-abc", "abc-"} {
		if ValidateName(n) == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
	if err := ValidateName("abc"); err != nil {
		t.Errorf("ValidateName(abc) = %v", err)
	}
}
