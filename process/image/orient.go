package image

import stdimage "image"

// orient applies EXIF orientation o (1–8) and returns an upright image.
func orient(src stdimage.Image, o int) stdimage.Image {
	if o < 2 || o > 8 {
		return src
	}
	b := src.Bounds()
	W, H := b.Dx(), b.Dy()
	dw, dh := W, H
	if o >= 5 {
		dw, dh = H, W
	}
	dst := stdimage.NewNRGBA(stdimage.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = W-1-x, y
			case 3:
				sx, sy = W-1-x, H-1-y
			case 4:
				sx, sy = x, H-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, H-1-x
			case 7:
				sx, sy = W-1-y, H-1-x
			case 8:
				sx, sy = W-1-y, x
			}
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}
