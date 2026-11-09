package memory_test

import (
	"testing"

	"github.com/Kolonnade/Poikile/assettest"
	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/store/memory"
)

func TestConformance(t *testing.T) {
	assettest.Provider(t, memory.New(), assettest.Options{
		Bucket:   "conformance-memory",
		Template: bucket.Baseline("https://app.example"),
	})
}
