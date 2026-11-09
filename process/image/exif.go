package image

import (
	"bytes"
	"encoding/binary"
	"strings"
	"time"
)

var exifHeader = []byte("Exif\x00\x00")

// extractEXIF returns the raw TIFF bytes of the EXIF block, or nil.
func extractEXIF(format string, b []byte) []byte {
	switch format {
	case "jpeg":
		return jpegEXIF(b)
	case "png":
		return pngEXIF(b)
	case "webp":
		return webpEXIF(b)
	}
	return nil
}

func jpegEXIF(b []byte) []byte {
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return nil
		}
		m := b[i+1]
		if m == 0xFF { // fill byte
			i++
			continue
		}
		if m == 0xDA || m == 0xD9 {
			return nil
		}
		n := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if n < 2 || i+2+n > len(b) {
			return nil
		}
		p := b[i+4 : i+2+n]
		if m == 0xE1 && bytes.HasPrefix(p, exifHeader) {
			return p[6:]
		}
		i += 2 + n
	}
	return nil
}

func pngEXIF(b []byte) []byte {
	i := 8
	for i+8 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[i : i+4]))
		typ := string(b[i+4 : i+8])
		if n < 0 || i+12+n > len(b) || i+12+n < i {
			return nil
		}
		if typ == "eXIf" {
			return b[i+8 : i+8+n]
		}
		if typ == "IEND" {
			return nil
		}
		i += 12 + n
	}
	return nil
}

func webpEXIF(b []byte) []byte {
	i := 12
	for i+8 <= len(b) {
		typ := string(b[i : i+4])
		n := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		if n < 0 || i+8+n > len(b) || i+8+n < i {
			return nil
		}
		if typ == "EXIF" {
			d := b[i+8 : i+8+n]
			return bytes.TrimPrefix(d, exifHeader)
		}
		i += 8 + n + n%2
	}
	return nil
}

// parseTIFF reads orientation and capture time from a TIFF/EXIF block.
// Malformed input yields (1, nil); it never panics.
func parseTIFF(t []byte) (orientation int, captured *time.Time) {
	orientation = 1
	defer func() {
		if recover() != nil {
			orientation, captured = 1, nil
		}
	}()
	if len(t) < 8 {
		return 1, nil
	}
	var bo binary.ByteOrder
	switch string(t[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1, nil
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 1, nil
	}
	type entry struct {
		typ   uint16
		count uint32
		val   []byte // the 4-byte value/offset field
	}
	readIFD := func(off uint32) map[uint16]entry {
		o := int(off)
		if o < 8 || o+2 > len(t) {
			return nil
		}
		n := int(bo.Uint16(t[o : o+2]))
		if n > 512 {
			n = 512
		}
		m := map[uint16]entry{}
		for k := 0; k < n; k++ {
			e := o + 2 + 12*k
			if e+12 > len(t) {
				break
			}
			m[bo.Uint16(t[e:e+2])] = entry{bo.Uint16(t[e+2 : e+4]), bo.Uint32(t[e+4 : e+8]), t[e+8 : e+12]}
		}
		return m
	}
	ascii := func(e entry) string {
		if e.typ != 2 || e.count == 0 || e.count > 64 {
			return ""
		}
		var d []byte
		if e.count <= 4 {
			d = e.val[:e.count]
		} else {
			o := int(bo.Uint32(e.val))
			if o < 0 || o+int(e.count) > len(t) {
				return ""
			}
			d = t[o : o+int(e.count)]
		}
		return strings.TrimRight(string(d), "\x00 ")
	}

	ifd0 := readIFD(bo.Uint32(t[4:8]))
	if ifd0 == nil {
		return 1, nil
	}
	if e, ok := ifd0[0x0112]; ok && e.typ == 3 {
		if v := int(bo.Uint16(e.val[0:2])); v >= 1 && v <= 8 {
			orientation = v
		}
	}
	if e, ok := ifd0[0x8769]; ok && e.typ == 4 {
		if ex := readIFD(bo.Uint32(e.val)); ex != nil {
			ds := ascii(ex[0x9003])
			if ds == "" {
				ds = ascii(ex[0x9004])
			}
			if ds != "" {
				loc := time.UTC
				if z := ascii(ex[0x9011]); z != "" {
					if zt, err := time.Parse("-07:00", z); err == nil {
						loc = zt.Location()
					}
				}
				if ct, err := time.ParseInLocation("2006:01:02 15:04:05", ds, loc); err == nil {
					if ct.Year() >= 1900 && ct.Before(time.Now().Add(24*time.Hour)) {
						captured = &ct
					}
				}
			}
		}
	}
	return orientation, captured
}
