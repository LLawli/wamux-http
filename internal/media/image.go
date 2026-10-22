package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	_ "image/gif" // decoders for goThumbnail
	"image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp" // pure Go: no ffmpeg needed for WebP
)

// maxPixels bounds the images the edge decodes (in-process or through
// ffmpeg). A file's header can declare any size: a 74-byte PNG claiming
// 200000x200000 makes a full decode allocate hundreds of GB, and Go's
// out-of-memory is fatal to the whole process, not a recoverable panic.
const maxPixels = 40_000_000

// ErrImageTooLarge is an image whose declared size exceeds maxPixels.
var ErrImageTooLarge = errors.New("image too large")

// goThumbnail scales an image Go can decode (JPEG, PNG, GIF, WebP) so its longest
// side is at most size, and encodes it as JPEG. Each output pixel averages a
// 2x2 grid of source samples, which is plenty for a preview. The header is
// read first, so an oversized image is refused before anything is allocated.
func goThumbnail(in []byte, size int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(in))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, ErrImageTooLarge
	}
	src, _, err := image.Decode(bytes.NewReader(in))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, errors.New("empty image")
	}
	tw, th := w, h
	if w > size || h > size {
		if w >= h {
			tw, th = size, max(1, h*size/w)
		} else {
			tw, th = max(1, w*size/h), size
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := range th {
		for x := range tw {
			var r, g, bl, a uint32
			for _, o := range [4][2]int{{1, 1}, {3, 1}, {1, 3}, {3, 3}} {
				sx := b.Min.X + (4*x+o[0])*w/(4*tw)
				sy := b.Min.Y + (4*y+o[1])*h/(4*th)
				cr, cg, cb, ca := src.At(sx, sy).RGBA()
				r, g, bl, a = r+cr, g+cg, bl+cb, a+ca
			}
			dst.Set(x, y, color.RGBA64{R: uint16(r / 4), G: uint16(g / 4), B: uint16(bl / 4), A: uint16(a / 4)})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 75}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
