// Package imagetest builds in-memory image fixtures for process/image and
// process tests. No binary fixtures are checked in.
package imagetest

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// EXIF describes the metadata TIFF builds.
type EXIF struct {
	Orientation        int
	DateTimeOriginal   string // "2006:01:02 15:04:05"
	OffsetTimeOriginal string // "+09:00"
	GPS                bool
	Make               string
}

func fillImage(w, h int, fill func(x, y int) color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.Color(color.NRGBA{R: uint8(x * 7), G: uint8(y * 5), B: 128, A: 255})
			if fill != nil {
				c = fill(x, y)
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// JPEG encodes a w×h JPEG and, if e is set, splices an EXIF APP1 after SOI.
func JPEG(w, h int, fill func(x, y int) color.Color, e *EXIF) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, fillImage(w, h, fill), &jpeg.Options{Quality: 95}); err != nil {
		panic(err)
	}
	b := buf.Bytes()
	if e == nil {
		return b
	}
	payload := append([]byte("Exif\x00\x00"), TIFF(*e)...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	out := append([]byte{}, b[:2]...)
	out = append(out, seg...)
	return append(out, b[2:]...)
}

type tiffEntry struct {
	tag, typ uint16
	count    uint32
	data     []byte // value bytes; inline if ≤4
}

// TIFF builds a big-endian TIFF/EXIF block from e.
func TIFF(e EXIF) []byte {
	bo := binary.BigEndian
	ascii := func(s string) []byte { return append([]byte(s), 0) }
	short := func(v uint16) []byte { b := make([]byte, 2); bo.PutUint16(b, v); return b }
	long := func(v uint32) []byte { b := make([]byte, 4); bo.PutUint32(b, v); return b }

	var exifEntries []tiffEntry
	if e.DateTimeOriginal != "" {
		exifEntries = append(exifEntries, tiffEntry{0x9003, 2, uint32(len(e.DateTimeOriginal) + 1), ascii(e.DateTimeOriginal)})
	}
	if e.OffsetTimeOriginal != "" {
		exifEntries = append(exifEntries, tiffEntry{0x9011, 2, uint32(len(e.OffsetTimeOriginal) + 1), ascii(e.OffsetTimeOriginal)})
	}
	var rat []byte
	for _, v := range []uint32{47, 1, 36, 1, 22, 1} {
		rat = append(rat, long(v)...)
	}
	gpsEntries := []tiffEntry{{0x0001, 2, 2, ascii("N")}, {0x0002, 5, 3, rat}}

	// Layout: header(8) | IFD0 | ExifIFD | GPS IFD | data area.
	ifdSize := func(n int) int { return 2 + 12*n + 4 }
	n0 := 2
	if e.Make != "" {
		n0++
	}
	if e.GPS {
		n0++
	}
	ifd0Off := 8
	exifOff := ifd0Off + ifdSize(n0)
	gpsOff := exifOff + ifdSize(len(exifEntries))
	dataOff := gpsOff
	if e.GPS {
		dataOff += ifdSize(len(gpsEntries))
	}

	var ifd0 []tiffEntry
	if e.Make != "" {
		ifd0 = append(ifd0, tiffEntry{0x010F, 2, uint32(len(e.Make) + 1), ascii(e.Make)})
	}
	ifd0 = append(ifd0, tiffEntry{0x0112, 3, 1, short(uint16(e.Orientation))})
	ifd0 = append(ifd0, tiffEntry{0x8769, 4, 1, long(uint32(exifOff))})
	if e.GPS {
		ifd0 = append(ifd0, tiffEntry{0x8825, 4, 1, long(uint32(gpsOff))})
	}

	out := []byte("MM")
	out = append(out, short(42)...)
	out = append(out, long(uint32(ifd0Off))...)
	var data []byte
	writeIFD := func(entries []tiffEntry) {
		out = append(out, short(uint16(len(entries)))...)
		for _, en := range entries {
			out = append(out, short(en.tag)...)
			out = append(out, short(en.typ)...)
			out = append(out, long(en.count)...)
			if len(en.data) <= 4 {
				v := make([]byte, 4)
				copy(v, en.data)
				out = append(out, v...)
			} else {
				out = append(out, long(uint32(dataOff+len(data)))...)
				data = append(data, en.data...)
				if len(data)%2 == 1 {
					data = append(data, 0)
				}
			}
		}
		out = append(out, long(0)...)
	}
	writeIFD(ifd0)
	writeIFD(exifEntries)
	if e.GPS {
		writeIFD(gpsEntries)
	}
	return append(out, data...)
}

func pngChunk(typ string, data []byte) []byte {
	c := make([]byte, 4)
	binary.BigEndian.PutUint32(c, uint32(len(data)))
	c = append(c, typ...)
	c = append(c, data...)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	return append(c, crc...)
}

// PNG encodes a w×h PNG (NRGBA, so fill may use alpha) with an optional eXIf chunk before IDAT.
func PNG(w, h int, fill func(x, y int) color.Color, e *EXIF) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, fillImage(w, h, fill)); err != nil {
		panic(err)
	}
	b := buf.Bytes()
	if e == nil {
		return b
	}
	idat := bytes.Index(b, []byte("IDAT")) - 4
	out := append([]byte{}, b[:idat]...)
	out = append(out, pngChunk("eXIf", TIFF(*e))...)
	return append(out, b[idat:]...)
}

// PNGHeader is a 1×1 PNG whose IHDR claims w×h, so DecodeConfig sees w×h.
func PNGHeader(w, h int) []byte {
	b := PNG(1, 1, nil, nil)
	// IHDR data starts at 16 (8 sig + 4 len + 4 type), 13 bytes, CRC after.
	binary.BigEndian.PutUint32(b[16:], uint32(w))
	binary.BigEndian.PutUint32(b[20:], uint32(h))
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	return b
}

// GIF encodes a two-frame w×h GIF.
func GIF(w, h int) []byte {
	g := &gif.GIF{}
	for i := 0; i < 2; i++ {
		f := image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				f.SetColorIndex(x, y, uint8((x+y+i*40)%256))
			}
		}
		g.Image = append(g.Image, f)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// WebP is a 1×1 lossless WebP.
func WebP() []byte {
	b, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		panic(err)
	}
	return b
}

// HEICHeader is a 24-byte ftyp box with brand heic followed by garbage.
func HEICHeader() []byte {
	b := []byte{0, 0, 0, 24}
	b = append(b, "ftypheic"...)
	b = append(b, 0, 0, 0, 0)
	b = append(b, "mif1heic"...)
	return append(b, "garbage-not-a-real-heif"...)
}

// SVG is a small SVG document with a script.
func SVG() []byte {
	return []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><script>alert(1)</script></svg>`)
}

// HasEXIF reports whether b carries an APP1 marker before SOS, an Exif
// header, or GPS tag bytes.
func HasEXIF(b []byte) bool {
	if bytes.Contains(b, []byte("Exif\x00\x00")) {
		return true
	}
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xD8 {
		i := 2
		for i+4 <= len(b) && b[i] == 0xFF {
			m := b[i+1]
			if m == 0xDA {
				break
			}
			if m == 0xE1 {
				return true
			}
			i += 2 + int(binary.BigEndian.Uint16(b[i+2:i+4]))
		}
	}
	return bytes.Contains(b, []byte{0x88, 0x25, 0x00, 0x04}) // GPS IFD pointer tag
}

// Contains reports whether b contains s.
func Contains(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }
