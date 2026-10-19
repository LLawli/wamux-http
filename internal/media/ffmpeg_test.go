package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWaveform(t *testing.T) {
	// Silence, then a block of loud samples, then half as loud.
	samples := make([]int16, 64*100)
	for i := 32 * 100; i < 48*100; i++ {
		samples[i] = 20000
		if i%2 == 0 {
			samples[i] = -20000 // amplitude, not sign
		}
	}
	for i := 48 * 100; i < 64*100; i++ {
		samples[i] = 10000
	}
	w := Waveform(samples)
	if len(w) != WaveformSamples {
		t.Fatalf("len %d", len(w))
	}
	for i, v := range w {
		want := byte(0)
		switch {
		case i >= 48:
			want = 50
		case i >= 32:
			want = 100
		}
		if v != want {
			t.Fatalf("bar %d = %d, want %d (%v)", i, v, want, w)
		}
	}
	if w := Waveform(make([]int16, 10)); !bytes.Equal(w, make([]byte, 64)) {
		t.Fatalf("too short for 64 bars: %v", w)
	}
	if w := Waveform(make([]int16, 6400)); !bytes.Equal(w, make([]byte, 64)) {
		t.Fatalf("silence: %v", w)
	}
}

func needFFmpeg(t *testing.T) *Converter {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	return NewConverter()
}

// generate makes a test file with ffmpeg's synthetic sources.
func generate(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("ffmpeg", append(append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, args...), out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate %s: %v %s", name, err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func probe(t *testing.T, data []byte, entries string) string {
	t.Helper()
	in := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", entries, "-of", "default=nw=1", in).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	return string(out)
}

func TestVoiceConversionAndInfo(t *testing.T) {
	c := needFFmpeg(t)
	ctx := context.Background()
	// 2 s of a tone in stereo 44.1 kHz WAV: every property the voice
	// pipeline must change.
	wav := generate(t, "in.wav", "-f", "lavfi", "-i", "sine=frequency=440:duration=2:sample_rate=44100", "-ac", "2")
	out, err := c.Voice(ctx, wav)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte("OggS")) {
		t.Fatalf("not an Ogg file: % x", out[:min(8, len(out))])
	}
	info := probe(t, out, "stream=codec_name,sample_rate,channels")
	for _, want := range []string{"codec_name=opus", "sample_rate=48000", "channels=1"} {
		if !strings.Contains(info, want) {
			t.Fatalf("converted voice %q lacks %s", info, want)
		}
	}
	secs, wave, err := c.VoiceInfo(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if secs != 2 {
		t.Fatalf("duration %d s, want 2", secs)
	}
	peak := byte(0)
	for _, v := range wave {
		peak = max(peak, v)
	}
	if len(wave) != 64 || peak != 100 || wave[32] < 50 {
		t.Fatalf("a steady tone has a flat, full waveform: %v", wave)
	}
	if _, err := c.Voice(ctx, []byte("not audio")); err == nil {
		t.Fatal("garbage converted")
	}
}

func TestVideoConversionAndDuration(t *testing.T) {
	c := needFFmpeg(t)
	ctx := context.Background()
	// Odd dimensions and a codec WhatsApp does not play.
	in := generate(t, "in.avi", "-f", "lavfi", "-i", "testsrc=size=161x121:rate=10:duration=3", "-c:v", "mpeg4")
	out, err := c.Video(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	info := probe(t, out, "stream=codec_name,width,height,pix_fmt")
	for _, want := range []string{"codec_name=h264", "width=160", "height=120", "pix_fmt=yuv420p"} {
		if !strings.Contains(info, want) {
			t.Fatalf("converted video %q lacks %s", info, want)
		}
	}
	if secs, err := c.Duration(ctx, out); err != nil || secs != 3 {
		t.Fatalf("duration %d %v", secs, err)
	}
	thumb, err := c.Thumbnail(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(thumb))
	if err != nil || img.Bounds().Dx() != 160 {
		t.Fatalf("video thumbnail: %v %v", img.Bounds(), err)
	}
}

func TestThumbnailScalesImages(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	for y := range 500 {
		for x := range 1000 {
			src.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	thumb, err := NewConverter().Thumbnail(context.Background(), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(thumb))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 320 || b.Dy() != 160 {
		t.Fatalf("thumbnail %v, want 320x160", b)
	}
	if r, g, _, _ := img.At(100, 80).RGBA(); r>>8 < 200 || g>>8 > 60 {
		t.Fatalf("colors lost: %d %d", r>>8, g>>8)
	}
}
