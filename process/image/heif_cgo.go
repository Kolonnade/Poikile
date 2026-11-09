//go:build heif && cgo

package image

// #cgo pkg-config: libheif
// #include <stdlib.h>
// #include <libheif/heif.h>
import "C"

import (
	"encoding/binary"
	"fmt"
	stdimage "image"
	"unsafe"
)

// HEIFSupported reports whether this build decodes HEIC/HEIF via libheif.
const HEIFSupported = true

func heifErr(e C.struct_heif_error) error {
	if e.code == C.heif_error_Ok {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrDecode, C.GoString(e.message))
}

func decodeHEIF(b []byte, maxPixels int64) (stdimage.Image, []byte, error) {
	ctx := C.heif_context_alloc()
	if ctx == nil {
		return nil, nil, ErrDecode
	}
	defer C.heif_context_free(ctx)
	C.heif_context_set_maximum_image_size_limit(ctx, 20000)

	cb := C.CBytes(b)
	defer C.free(cb)
	if err := heifErr(C.heif_context_read_from_memory_without_copy(ctx, cb, C.size_t(len(b)), nil)); err != nil {
		return nil, nil, err
	}

	var h *C.struct_heif_image_handle
	if err := heifErr(C.heif_context_get_primary_image_handle(ctx, &h)); err != nil {
		return nil, nil, err
	}
	defer C.heif_image_handle_release(h)

	w := int64(C.heif_image_handle_get_width(h))
	ht := int64(C.heif_image_handle_get_height(h))
	if w < 1 || ht < 1 {
		return nil, nil, ErrDecode
	}
	if w*ht > maxPixels {
		return nil, nil, ErrTooManyPixels
	}

	var tiff []byte
	exifType := C.CString("Exif")
	defer C.free(unsafe.Pointer(exifType))
	if C.heif_image_handle_get_number_of_metadata_blocks(h, exifType) > 0 {
		var id C.heif_item_id
		C.heif_image_handle_get_list_of_metadata_block_IDs(h, exifType, &id, 1)
		size := int(C.heif_image_handle_get_metadata_size(h, id))
		if size > 4 {
			buf := make([]byte, size)
			if heifErr(C.heif_image_handle_get_metadata(h, id, unsafe.Pointer(&buf[0]))) == nil {
				off := int(binary.BigEndian.Uint32(buf[0:4]))
				if off >= 0 && 4+off < len(buf) {
					tiff = buf[4+off:]
				}
			}
		}
	}

	var img *C.struct_heif_image
	if err := heifErr(C.heif_decode_image(h, &img, C.heif_colorspace_RGB, C.heif_chroma_interleaved_RGBA, nil)); err != nil {
		return nil, nil, err
	}
	defer C.heif_image_release(img)

	var stride C.int
	p := C.heif_image_get_plane_readonly(img, C.heif_channel_interleaved, &stride)
	if p == nil {
		return nil, nil, ErrDecode
	}
	iw := int(C.heif_image_get_width(img, C.heif_channel_interleaved))
	ih := int(C.heif_image_get_height(img, C.heif_channel_interleaved))
	if iw < 1 || ih < 1 {
		return nil, nil, ErrDecode
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(p)), int(stride)*ih)
	out := stdimage.NewNRGBA(stdimage.Rect(0, 0, iw, ih))
	for y := 0; y < ih; y++ {
		copy(out.Pix[y*out.Stride:y*out.Stride+iw*4], src[y*int(stride):y*int(stride)+iw*4])
	}
	return out, tiff, nil
}
