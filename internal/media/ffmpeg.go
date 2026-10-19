package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrNoFFmpeg is a conversion asked of a host without ffmpeg.
var ErrNoFFmpeg = errors.New("ffmpeg is not installed")

// Converter runs ffmpeg the way WAHA does (src/core/utils/ffmpeg.ts), at most
// one process per CPU at a time.
type Converter struct {
	ffmpeg, ffprobe, opustags string
	sem                       chan struct{}
	timeout                   time.Duration
}

// convertTimeout bounds one ffmpeg run.
const convertTimeout = 5 * time.Minute

// NewConverter finds ffmpeg, ffprobe and opustags on the PATH. Missing tools
// are not an error until something needs them.
func NewConverter() *Converter {
	c := &Converter{sem: make(chan struct{}, runtime.NumCPU()), timeout: convertTimeout}
	c.ffmpeg, _ = exec.LookPath("ffmpeg")
	c.ffprobe, _ = exec.LookPath("ffprobe")
	c.opustags, _ = exec.LookPath("opustags")
	return c
}

// Available reports whether ffmpeg was found.
func (c *Converter) Available() bool { return c.ffmpeg != "" }

// workdir runs fn in a fresh temp dir holding the input as "input".
func (c *Converter) workdir(ctx context.Context, in []byte, fn func(ctx context.Context, dir, input string) error) error {
	if c.ffmpeg == "" {
		return ErrNoFFmpeg
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "wamux-media-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	input := filepath.Join(dir, "input")
	if err := os.WriteFile(input, in, 0o600); err != nil {
		return err
	}
	return fn(ctx, dir, input)
}

func run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return nil, fmt.Errorf("%s failed: %w: %s", filepath.Base(bin), err, msg)
	}
	return stdout.Bytes(), nil
}

var quiet = []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}

// Voice converts any audio to WhatsApp's voice note format: OGG/Opus,
// 48 kHz, mono, 32 kb/s, through a WAV step as WAHA does.
func (c *Converter) Voice(ctx context.Context, in []byte) ([]byte, error) {
	var out []byte
	err := c.workdir(ctx, in, func(ctx context.Context, dir, input string) error {
		wav, opus := filepath.Join(dir, "input.wav"), filepath.Join(dir, "output.opus")
		if _, err := run(ctx, c.ffmpeg, append(quiet, "-i", input, "-vn", "-sn", "-dn", "-map", "0:a:0",
			"-map_metadata", "-1", "-ac", "1", "-ar", "48000", "-c:a", "pcm_s16le", wav)...); err != nil {
			return err
		}
		if _, err := run(ctx, c.ffmpeg, append(quiet, "-i", wav, "-c:a", "libopus", "-b:a", "32k",
			"-ar", "48000", "-ac", "1", opus)...); err != nil {
			return err
		}
		if c.opustags != "" {
			_, _ = run(ctx, c.opustags, "--in-place", "--delete", "ENCODER", "--delete", "encoder", "--set-vendor", "Recorder", opus)
		}
		var err error
		out, err = os.ReadFile(opus)
		return err
	})
	return out, err
}

// Video converts any video to H.264 MP4 with even dimensions and the index
// up front, as WAHA does.
func (c *Converter) Video(ctx context.Context, in []byte) ([]byte, error) {
	var out []byte
	err := c.workdir(ctx, in, func(ctx context.Context, dir, input string) error {
		mp4 := filepath.Join(dir, "output.mp4")
		if _, err := run(ctx, c.ffmpeg, append(quiet, "-i", input, "-c:v", "libx264", "-map", "0",
			"-pix_fmt", "yuv420p", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", "-movflags", "+faststart", mp4)...); err != nil {
			return err
		}
		var err error
		out, err = os.ReadFile(mp4)
		return err
	})
	return out, err
}

// waveformRate is the sample rate audio is decoded at for its waveform; the
// strip has 64 bars, so a low rate loses nothing.
const waveformRate = 16000

// WaveformSamples is the number of bars WhatsApp draws in a voice bubble.
const WaveformSamples = 64

// VoiceInfo decodes audio and returns its duration in seconds and its
// waveform, for SendMediaHeader.seconds and .waveform.
func (c *Converter) VoiceInfo(ctx context.Context, in []byte) (uint32, []byte, error) {
	var pcm []byte
	err := c.workdir(ctx, in, func(ctx context.Context, _, input string) error {
		var err error
		pcm, err = run(ctx, c.ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", input,
			"-vn", "-ac", "1", "-ar", strconv.Itoa(waveformRate), "-f", "s16le", "-c:a", "pcm_s16le", "pipe:1")
		return err
	})
	if err != nil {
		return 0, nil, err
	}
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	return seconds(float64(len(samples)) / waveformRate), Waveform(samples), nil
}

// Waveform is Baileys' getAudioWaveform: the mean absolute amplitude of 64
// equal blocks, scaled so the loudest block is 100.
func Waveform(samples []int16) []byte {
	out := make([]byte, WaveformSamples)
	block := len(samples) / WaveformSamples
	if block == 0 {
		return out
	}
	var avg [WaveformSamples]float64
	peak := 0.0
	for i := range avg {
		sum := 0.0
		for _, v := range samples[i*block : (i+1)*block] {
			sum += math.Abs(float64(v))
		}
		avg[i] = sum / float64(block)
		peak = max(peak, avg[i])
	}
	if peak == 0 {
		return out
	}
	for i, v := range avg {
		out[i] = byte(math.Floor(100 * v / peak))
	}
	return out
}

func seconds(s float64) uint32 {
	if s <= 0 {
		return 0
	}
	return uint32(max(1, math.Round(s)))
}

// Duration is a media file's length in seconds, from ffprobe.
func (c *Converter) Duration(ctx context.Context, in []byte) (uint32, error) {
	if c.ffprobe == "" {
		return 0, errors.New("ffprobe is not installed")
	}
	var out []byte
	err := c.workdir(ctx, in, func(ctx context.Context, _, input string) error {
		var err error
		out, err = run(ctx, c.ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", input)
		return err
	})
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration %q: %w", out, err)
	}
	return seconds(f), nil
}

// thumbSize is the longest side of a generated thumbnail, in pixels.
const thumbSize = 320

// Thumbnail is a small JPEG of an image, or of a video's first frame.
// Images Go can decode are scaled in-process; anything else goes through
// ffmpeg.
func (c *Converter) Thumbnail(ctx context.Context, in []byte) ([]byte, error) {
	if out, err := goThumbnail(in, thumbSize); err == nil {
		return out, nil
	}
	var out []byte
	err := c.workdir(ctx, in, func(ctx context.Context, dir, input string) error {
		jpg := filepath.Join(dir, "thumb.jpg")
		scale := fmt.Sprintf("scale=w='min(%d,iw)':h='min(%d,ih)':force_original_aspect_ratio=decrease", thumbSize, thumbSize)
		if _, err := run(ctx, c.ffmpeg, append(quiet, "-i", input, "-frames:v", "1", "-vf", scale,
			"-q:v", "5", "-f", "image2", "-c:v", "mjpeg", jpg)...); err != nil {
			return err
		}
		var err error
		out, err = os.ReadFile(jpg)
		return err
	})
	return out, err
}
