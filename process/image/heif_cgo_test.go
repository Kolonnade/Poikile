//go:build heif && cgo

package image_test

import (
	"bytes"
	"image/jpeg"
	"os"
	"testing"

	pimage "github.com/Kolonnade/Poikile/process/image"
	pt "github.com/Kolonnade/Poikile/process/image/imagetest"
)

func TestProcess_HEIC(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.heic")
	if err != nil {
		t.Skip("generate with heif-enc; see README")
	}
	r, err := process(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != "heif" || len(r.Variants) != 3 {
		t.Fatalf("result = %s, %d variants", r.Format, len(r.Variants))
	}
	for _, v := range r.Variants {
		if _, err := jpeg.DecodeConfig(bytes.NewReader(v.Data)); err != nil {
			t.Errorf("%s: %v", v.Name, err)
		}
		if pt.HasEXIF(v.Data) {
			t.Errorf("%s carries EXIF", v.Name)
		}
	}
}
