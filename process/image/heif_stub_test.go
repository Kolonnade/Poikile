//go:build !heif || !cgo

package image_test

import (
	"errors"
	"testing"

	pimage "github.com/Kolonnade/Poikile/process/image"
	pt "github.com/Kolonnade/Poikile/process/image/imagetest"
)

func TestProcess_HEICUnsupportedWithoutTag(t *testing.T) {
	if _, err := process(pt.HEICHeader()); !errors.Is(err, pimage.ErrHEICUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
