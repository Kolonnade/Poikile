package image_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"

	pimage "github.com/Kolonnade/Poikile/process/image"
	pt "github.com/Kolonnade/Poikile/process/image/imagetest"
)

var (
	red  = color.NRGBA{R: 255, A: 255}
	blue = color.NRGBA{B: 255, A: 255}
)

func leftRedRightBlue(w int) func(x, y int) color.Color {
	return func(x, y int) color.Color {
		if x < w/2 {
			return red
		}
		return blue
	}
}

func variant(t *testing.T, r pimage.Result, name string) pimage.Rendition {
	t.Helper()
	for _, v := range r.Variants {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no %s variant in %+v", name, r.Variants)
	return pimage.Rendition{}
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("variant is not a JPEG: %v", err)
	}
	return img
}

func near(c color.Color, want color.NRGBA) bool {
	r, g, b, _ := c.RGBA()
	d := func(a uint32, w uint8) bool {
		x := int(a>>8) - int(w)
		return x > -40 && x < 40
	}
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}

func TestProcess_StripsEXIFAndGPS(t *testing.T) {
	data := pt.JPEG(64, 48, nil, &pt.EXIF{Orientation: 1, GPS: true, Make: "Pixel", DateTimeOriginal: "2026:10:27 18:30:00"})
	if !pt.HasEXIF(data) {
		t.Fatal("fixture has no EXIF")
	}
	r, err := process(data)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, v := range r.Variants {
		names = append(names, v.Name)
		if _, err := jpeg.DecodeConfig(bytes.NewReader(v.Data)); err != nil {
			t.Errorf("%s: %v", v.Name, err)
		}
		if pt.HasEXIF(v.Data) || pt.Contains(v.Data, "Pixel") {
			t.Errorf("%s still carries metadata", v.Name)
		}
	}
	if len(names) != 3 || names[0] != "thumb" || names[1] != "display" || names[2] != "original" {
		t.Errorf("variants = %v", names)
	}
	want := time.Date(2026, 10, 27, 18, 30, 0, 0, time.UTC)
	if r.CapturedAt == nil || !r.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v", r.CapturedAt, want)
	}
}

func TestProcess_CaptureTimeOffset(t *testing.T) {
	r, err := process(pt.JPEG(8, 8, nil, &pt.EXIF{Orientation: 1, DateTimeOriginal: "2026:10:27 18:30:00", OffsetTimeOriginal: "-07:00"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 28, 1, 30, 0, 0, time.UTC); r.CapturedAt == nil || !r.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v", r.CapturedAt, want)
	}
	r, err = process(pt.JPEG(8, 8, nil, &pt.EXIF{Orientation: 1, DateTimeOriginal: "2026:10:27 18:30:00"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 27, 18, 30, 0, 0, time.UTC); r.CapturedAt == nil || !r.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v", r.CapturedAt, want)
	}
}

func TestProcess_Orientation(t *testing.T) {
	for _, tc := range []struct {
		o      int
		w, h   int
		p1, p2 image.Point
		c1, c2 color.NRGBA
	}{
		{6, 20, 40, image.Pt(10, 5), image.Pt(10, 35), red, blue},
		{3, 40, 20, image.Pt(5, 10), image.Pt(35, 10), blue, red},
		{8, 20, 40, image.Pt(10, 5), image.Pt(10, 35), blue, red},
	} {
		r, err := process(pt.JPEG(40, 20, leftRedRightBlue(40), &pt.EXIF{Orientation: tc.o}))
		if err != nil {
			t.Fatal(err)
		}
		if r.Width != tc.w || r.Height != tc.h {
			t.Errorf("o=%d: %d×%d, want %d×%d", tc.o, r.Width, r.Height, tc.w, tc.h)
			continue
		}
		img := decode(t, variant(t, r, "original").Data)
		if b := img.Bounds(); b.Dx() != tc.w || b.Dy() != tc.h {
			t.Errorf("o=%d: original %v", tc.o, b)
			continue
		}
		if !near(img.At(tc.p1.X, tc.p1.Y), tc.c1) || !near(img.At(tc.p2.X, tc.p2.Y), tc.c2) {
			t.Errorf("o=%d: colors %v %v", tc.o, img.At(tc.p1.X, tc.p1.Y), img.At(tc.p2.X, tc.p2.Y))
		}
	}
}

func dims(v pimage.Rendition) (int, int) { return v.Width, v.Height }

func TestProcess_DisplayLongEdge(t *testing.T) {
	r, err := process(pt.JPEG(6000, 400, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][2]int{"display": {2048, 137}, "thumb": {480, 32}, "original": {6000, 400}} {
		v := variant(t, r, name)
		if w, h := dims(v); w != want[0] || h != want[1] {
			t.Errorf("%s = %d×%d, want %v", name, w, h, want)
		}
		if b := decode(t, v.Data).Bounds(); b.Dx() != want[0] || b.Dy() != want[1] {
			t.Errorf("%s decodes to %v", name, b)
		}
	}
}

func TestProcess_NoUpscale(t *testing.T) {
	r, err := process(pt.JPEG(300, 200, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r.Variants {
		if w, h := dims(v); w != 300 || h != 200 {
			t.Errorf("%s = %d×%d", v.Name, w, h)
		}
	}
}

func TestProcess_Formats(t *testing.T) {
	alpha := func(x, y int) color.Color { return color.NRGBA{R: 200, A: uint8(x * 20)} }
	for name, tc := range map[string]struct {
		data   []byte
		format string
		w, h   int
	}{
		"png-alpha": {pt.PNG(10, 6, alpha, nil), "png", 10, 6},
		"png-exif6": {pt.PNG(10, 6, nil, &pt.EXIF{Orientation: 6}), "png", 6, 10},
		"gif":       {pt.GIF(12, 8), "gif", 12, 8},
		"webp":      {pt.WebP(), "webp", 1, 1},
	} {
		r, err := process(tc.data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if r.Format != tc.format || r.Width != tc.w || r.Height != tc.h || len(r.Variants) != 3 {
			t.Errorf("%s: %s %d×%d %d variants", name, r.Format, r.Width, r.Height, len(r.Variants))
		}
		for _, v := range r.Variants {
			decode(t, v.Data)
		}
	}
}

func TestProcess_Rejects(t *testing.T) {
	j := pt.JPEG(32, 32, nil, nil)
	html := append([]byte("<html><script>alert(1)</script>"), j...)
	mp4 := append([]byte{0, 0, 0, 24}, "ftypisom\x00\x00\x02\x00isomiso2"...)
	for name, data := range map[string][]byte{
		"svg":       pt.SVG(),
		"html":      html,
		"zip":       []byte("PK\x03\x04rest-of-zip"),
		"truncated": j[:len(j)/2],
		"empty":     nil,
		"mp4":       mp4,
	} {
		_, err := process(data)
		if !errors.Is(err, pimage.ErrUnsupportedFormat) && !errors.Is(err, pimage.ErrDecode) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestProcess_PixelLimit(t *testing.T) {
	if _, err := process(pt.PNGHeader(20000, 20000)); !errors.Is(err, pimage.ErrTooManyPixels) {
		t.Fatalf("err = %v", err)
	}
}

func TestProcess_TrailingPayloadNeutralized(t *testing.T) {
	data := append(pt.JPEG(32, 32, nil, nil), "PK\x03\x04payload<script>alert(1)</script>"...)
	r, err := process(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r.Variants {
		if pt.Contains(v.Data, "PK\x03\x04") || pt.Contains(v.Data, "<script") {
			t.Errorf("%s carries the payload", v.Name)
		}
	}
}

func process(b []byte) (pimage.Result, error) {
	return pimage.Process(bytes.NewReader(b), pimage.DefaultConfig())
}

func noisy(x, y int) color.Color {
	return color.NRGBA{R: uint8(x * 7), G: uint8(y * 13), B: uint8(x*y + x ^ y), A: 255}
}

func TestProcess_TooLarge(t *testing.T) {
	data := pt.JPEG(200, 200, noisy, nil)
	if len(data) <= 1024 {
		t.Fatalf("fixture is only %d bytes", len(data))
	}
	cfg := pimage.DefaultConfig()
	cfg.MaxBytes = 1024
	if _, err := pimage.Process(bytes.NewReader(data), cfg); !errors.Is(err, pimage.ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestProcess_PixelLimitConfigurable(t *testing.T) {
	cfg := pimage.DefaultConfig()
	cfg.MaxPixels = 100
	if _, err := pimage.Process(bytes.NewReader(pt.JPEG(20, 20, nil, nil)), cfg); !errors.Is(err, pimage.ErrTooManyPixels) {
		t.Fatalf("err = %v", err)
	}
}

func TestProcess_CustomVariants(t *testing.T) {
	cfg := pimage.Config{Variants: []pimage.Variant{{Name: "avatar", LongEdge: 96, Quality: 80, Served: true}}}
	r, err := pimage.Process(bytes.NewReader(pt.JPEG(400, 200, nil, nil)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Variants) != 1 {
		t.Fatalf("variants = %d", len(r.Variants))
	}
	v := r.Variants[0]
	if v.Name != "avatar" || !v.Served || v.Width != 96 || v.Height != 48 {
		t.Errorf("rendition = %s served=%v %d×%d", v.Name, v.Served, v.Width, v.Height)
	}
	if b := decode(t, v.Data).Bounds(); b.Dx() != 96 || b.Dy() != 48 {
		t.Errorf("decodes to %v", b)
	}
}

func TestProcess_NoXMP(t *testing.T) {
	r, err := process(pt.JPEG(64, 48, nil, &pt.EXIF{Orientation: 1, GPS: true, Make: "Pixel"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r.Variants {
		if pt.Contains(v.Data, "http://ns.adobe.com/xap") || pt.Contains(v.Data, "GPS") || pt.HasEXIF(v.Data) {
			t.Errorf("%s carries metadata", v.Name)
		}
	}
}

func TestConfig_Validate(t *testing.T) {
	ok := pimage.Variant{Name: "thumb", LongEdge: 10, Quality: 80}
	for name, vs := range map[string][]pimage.Variant{
		"duplicate": {ok, ok},
		"bad-name":  {{Name: "Thumb!", LongEdge: 10, Quality: 80}},
		"q0":        {{Name: "thumb", LongEdge: 10, Quality: 0}},
		"edge-neg":  {{Name: "thumb", LongEdge: -1, Quality: 80}},
		"none":      nil,
	} {
		cfg := pimage.Config{Variants: vs}
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	cfg := pimage.Config{Variants: []pimage.Variant{ok}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.MaxBytes != pimage.DefaultMaxBytes || cfg.MaxPixels != pimage.DefaultMaxPixels {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	neg := pimage.Config{Variants: []pimage.Variant{ok}, MaxBytes: -1}
	if err := neg.Validate(); err == nil {
		t.Error("negative MaxBytes: no error")
	}
}

func TestProcess_ContentType(t *testing.T) {
	r, err := process(pt.JPEG(32, 32, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r.Variants {
		if v.ContentType != "image/jpeg" {
			t.Errorf("%s: %q", v.Name, v.ContentType)
		}
	}
}
