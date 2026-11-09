//go:build !heif || !cgo

package image

import stdimage "image"

// HEIFSupported reports whether this build decodes HEIC/HEIF via libheif.
const HEIFSupported = false

func decodeHEIF(b []byte, maxPixels int64) (stdimage.Image, []byte, error) {
	return nil, nil, ErrHEICUnsupported
}
