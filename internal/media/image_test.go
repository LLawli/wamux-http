package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"testing"
)

// pngHeaderOnly is a PNG that declares w x h but carries almost no data:
// decoding it whole would allocate for every declared pixel.
func pngHeaderOnly(w, h uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte("\x89PNG\r\n\x1a\n"))
	chunk := func(typ string, data []byte) {
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 16, 6 // 16-bit RGBA: 8 bytes per pixel
	chunk("IHDR", ihdr)
	chunk("IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})
	chunk("IEND", nil)
	return b.Bytes()
}

func TestThumbnailRefusesHugeDeclaredImage(t *testing.T) {
	bomb := pngHeaderOnly(200_000, 200_000)
	if len(bomb) > 100 {
		t.Fatalf("the bomb should be tiny, got %d bytes", len(bomb))
	}
	if _, err := goThumbnail(bomb, thumbSize); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("goThumbnail: %v", err)
	}
	c := &Converter{}
	if _, err := c.Thumbnail(t.Context(), bomb); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("Thumbnail must refuse, not hand it to ffmpeg: %v", err)
	}
}

func TestThumbnailOfAnOrdinaryImage(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1000, 500))); err != nil {
		t.Fatal(err)
	}
	out, err := goThumbnail(b.Bytes(), thumbSize)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != thumbSize || cfg.Height != thumbSize/2 {
		t.Fatalf("thumbnail %dx%d, %v", cfg.Width, cfg.Height, err)
	}
}

// WebP, common in link previews, is decoded in process too: an image with
// no ffmpeg in it (the scratch container) still gets its thumbnail.
func TestThumbnailOfAWebPWithoutFFmpeg(t *testing.T) {
	in, err := os.ReadFile("testdata/sample.webp")
	if err != nil {
		t.Fatal(err)
	}
	out, err := goThumbnail(in, thumbSize)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || format != "jpeg" || cfg.Width != thumbSize || cfg.Height != thumbSize*360/640 {
		t.Fatalf("thumbnail %s %dx%d, %v", format, cfg.Width, cfg.Height, err)
	}
}
