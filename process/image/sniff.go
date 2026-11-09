package image

import "bytes"

var heifBrands = map[string]bool{
	"heic": true, "heix": true, "heim": true, "heis": true,
	"hevc": true, "hevx": true, "mif1": true, "msf1": true,
}

// Sniff detects the image format from magic bytes. It never trusts a
// declared content type. It returns "jpeg", "png", "gif", "webp" or "heif".
func Sniff(b []byte) (string, error) {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "jpeg", nil
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png", nil
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "gif", nil
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp", nil
	case len(b) >= 12 && string(b[4:8]) == "ftyp" && heifBrands[string(b[8:12])]:
		return "heif", nil
	}
	return "", ErrUnsupportedFormat
}
