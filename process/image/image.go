package image

import (
	"bytes"
	"errors"
	"fmt"
	stdimage "image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"regexp"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Default limits.
const (
	DefaultMaxBytes  int64 = 25 << 20
	DefaultMaxPixels int64 = 100_000_000
)

// ContentTypeJPEG is the content type of every rendition.
const ContentTypeJPEG = "image/jpeg"

var (
	ErrUnsupportedFormat = errors.New("not a JPEG, PNG, WebP, GIF or HEIC/HEIF image")
	ErrTooManyPixels     = errors.New("image exceeds the pixel limit")
	ErrTooLarge          = errors.New("image exceeds the byte limit")
	ErrDecode            = errors.New("image could not be decoded")
	ErrHEICUnsupported   = errors.New("HEIC not supported in this build")
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Variant configures one rendition. LongEdge 0 means full size.
type Variant struct {
	Name     string
	LongEdge int
	Quality  int
	Served   bool
}

// Config lists the variants to produce and the limits to enforce. A zero
// MaxBytes or MaxPixels means the default.
type Config struct {
	Variants  []Variant
	MaxBytes  int64
	MaxPixels int64
}

// DefaultConfig returns thumb, display and original.
func DefaultConfig() Config {
	return Config{
		Variants: []Variant{
			{Name: "thumb", LongEdge: 480, Quality: 85, Served: true},
			{Name: "display", LongEdge: 2048, Quality: 85, Served: true},
			{Name: "original", LongEdge: 0, Quality: 92, Served: false},
		},
		MaxBytes:  DefaultMaxBytes,
		MaxPixels: DefaultMaxPixels,
	}
}

// Validate fills zero limits with the defaults and checks the config.
func (c *Config) Validate() error {
	if c.MaxBytes < 0 || c.MaxPixels < 0 {
		return errors.New("image: negative limit")
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = DefaultMaxBytes
	}
	if c.MaxPixels == 0 {
		c.MaxPixels = DefaultMaxPixels
	}
	if len(c.Variants) == 0 {
		return errors.New("image: no variants")
	}
	seen := map[string]bool{}
	for _, v := range c.Variants {
		if !nameRE.MatchString(v.Name) {
			return fmt.Errorf("image: variant name %q is invalid", v.Name)
		}
		if seen[v.Name] {
			return fmt.Errorf("image: duplicate variant %q", v.Name)
		}
		seen[v.Name] = true
		if v.LongEdge < 0 {
			return fmt.Errorf("image: variant %q has a negative long edge", v.Name)
		}
		if v.Quality < 1 || v.Quality > 100 {
			return fmt.Errorf("image: variant %q quality %d is outside 1..100", v.Name, v.Quality)
		}
	}
	return nil
}

// Rendition is one re-encoded JPEG.
type Rendition struct {
	Name          string
	Data          []byte
	Width, Height int
	ContentType   string
	Served        bool
}

// Result is the outcome of Process. Width/Height are of the upright
// original. Variants keep the Config order.
type Result struct {
	Format        string
	Width, Height int
	CapturedAt    *time.Time
	Variants      []Rendition
}

// Process reads r up to the byte limit, then sniffs, decodes, orients,
// flattens and re-encodes it as each configured variant.
func Process(r io.Reader, cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	data, err := io.ReadAll(io.LimitReader(r, cfg.MaxBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("image: read: %w", err)
	}
	if int64(len(data)) > cfg.MaxBytes {
		return Result{}, ErrTooLarge
	}

	format, err := Sniff(data)
	if err != nil {
		return Result{}, err
	}

	var (
		img  stdimage.Image
		tiff []byte
	)
	if format == "heif" {
		img, tiff, err = decodeHEIF(data, cfg.MaxPixels)
		if err != nil {
			return Result{}, err
		}
	} else {
		var ic stdimage.Config
		br := bytes.NewReader(data)
		switch format {
		case "jpeg":
			ic, err = jpeg.DecodeConfig(br)
		case "png":
			ic, err = png.DecodeConfig(br)
		case "gif":
			ic, err = gif.DecodeConfig(br)
		case "webp":
			ic, err = webp.DecodeConfig(br)
		}
		if err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrDecode, err)
		}
		if ic.Width < 1 || ic.Height < 1 {
			return Result{}, ErrDecode
		}
		if int64(ic.Width)*int64(ic.Height) > cfg.MaxPixels {
			return Result{}, ErrTooManyPixels
		}
		br = bytes.NewReader(data)
		switch format {
		case "jpeg":
			img, err = jpeg.Decode(br)
		case "png":
			img, err = png.Decode(br)
		case "gif":
			img, err = gif.Decode(br)
		case "webp":
			img, err = webp.Decode(br)
		}
		if err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrDecode, err)
		}
		tiff = extractEXIF(format, data)
	}

	orientation, captured := 1, (*time.Time)(nil)
	if tiff != nil {
		orientation, captured = parseTIFF(tiff)
	}
	if format == "heif" {
		orientation = 1 // libheif already applies irot/imir
	}

	upright := flatten(orient(img, orientation))
	b := upright.Bounds()

	res := Result{Format: format, Width: b.Dx(), Height: b.Dy(), CapturedAt: captured}
	for _, v := range cfg.Variants {
		out := upright
		if v.LongEdge > 0 {
			out = resize(upright, v.LongEdge)
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: v.Quality}); err != nil {
			return Result{}, fmt.Errorf("image: encode %s: %w", v.Name, err)
		}
		ob := out.Bounds()
		res.Variants = append(res.Variants, Rendition{
			Name: v.Name, Data: buf.Bytes(), Width: ob.Dx(), Height: ob.Dy(),
			ContentType: ContentTypeJPEG, Served: v.Served,
		})
	}
	return res, nil
}

// flatten draws a non-opaque image over white, since JPEG has no alpha.
func flatten(src stdimage.Image) stdimage.Image {
	if o, ok := src.(interface{ Opaque() bool }); ok && o.Opaque() {
		if _, paletted := src.(*stdimage.Paletted); !paletted {
			return src
		}
	}
	b := src.Bounds()
	dst := stdimage.NewRGBA(stdimage.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), stdimage.NewUniform(color.White), stdimage.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}

// resize scales src so its long edge is edge, never upscaling.
func resize(src stdimage.Image, edge int) stdimage.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long, short := w, h
	if h > w {
		long, short = h, w
	}
	if long <= edge {
		return src
	}
	s := int(math.Round(float64(short) * float64(edge) / float64(long)))
	if s < 1 {
		s = 1
	}
	dw, dh := edge, s
	if h > w {
		dw, dh = s, edge
	}
	dst := stdimage.NewRGBA(stdimage.Rect(0, 0, dw, dh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}
