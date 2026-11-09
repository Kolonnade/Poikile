package image

import (
	"encoding/binary"
	"testing"
)

func TestParseTIFF_Malformed(t *testing.T) {
	looping := []byte("MM\x00\x2a\x00\x00\x00\x08")
	// One entry: ExifIFD pointer back to IFD0 itself.
	looping = append(looping, 0, 1, 0x87, 0x69, 0, 4, 0, 0, 0, 1, 0, 0, 0, 8, 0, 0, 0, 0)
	huge := []byte("II\x2a\x00\x08\x00\x00\x00\xff\xff")
	outOfRange := []byte("MM\x00\x2a\x7f\xff\xff\xff")
	badString := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01\x87\x69\x00\x04\x00\x00\x00\x01\x00\x00\x00\x1a\x00\x00\x00\x00")
	badString = append(badString, 0, 1, 0x90, 0x03, 0, 2, 0, 0, 0, 20, 0x7f, 0xff, 0xff, 0xff)
	neg := make([]byte, 8)
	copy(neg, "II\x2a\x00")
	binary.LittleEndian.PutUint32(neg[4:], 0xFFFFFFF0)
	for name, b := range map[string][]byte{
		"empty": nil, "short": []byte("MM\x00"), "badmagic": []byte("XX\x00\x2a\x00\x00\x00\x08"),
		"looping": looping, "huge-count": huge, "out-of-range": outOfRange, "bad-string": badString, "neg": neg,
	} {
		o, c := parseTIFF(b)
		if o != 1 || c != nil {
			t.Errorf("%s: %d %v", name, o, c)
		}
	}
}
