// Package media moves files in and out of WhatsApp for the edge: it fetches
// the URLs clients send (behind an SSRF guard), converts voice and video with
// ffmpeg, and serves received media under /api/files, downloading it from the
// core on first request.
package media

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/core"
	"github.com/LLawli/wamux-http/internal/store"
)

// Service is the media layer. Its fetcher and converter are built on first
// use from the environment (settings.go), so the wiring stays three fields.
type Service struct {
	Config *config.Config
	Store  store.Store
	Core   *core.Client

	once     sync.Once
	settings Settings
	fetcher  *Fetcher
	conv     *Converter

	downloads sync.Map     // cache path -> *sync.Mutex, one download per file
	lastSweep atomic.Int64 // ms of the last expired-files sweep
}

func (s *Service) init() {
	s.once.Do(func() {
		s.settings = SettingsFromEnv()
		s.fetcher = NewFetcher(FetchOptions{
			AllowPrivate: s.settings.AllowPrivate,
			MaxBytes:     s.settings.MaxBytes,
			Timeout:      s.settings.FetchTimeout,
		})
		s.conv = NewConverter()
	})
}

// Fetcher is the SSRF-guarded HTTP client every outbound fetch goes through.
func (s *Service) Fetcher() *Fetcher { s.init(); return s.fetcher }

// Converter runs ffmpeg.
func (s *Service) Converter() *Converter { s.init(); return s.conv }

// ErrBadInput is a request the caller got wrong: no file, bad base64, a URL
// that is not http(s).
var ErrBadInput = errors.New("bad media input")

func badInput(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadInput, fmt.Sprintf(format, a...))
}

// decodeBase64 is as lenient as Node's Buffer.from(data, "base64"): standard
// or URL alphabet, with or without padding, whitespace ignored.
func decodeBase64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		}
		return r
	}, s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, badInput("file.data is not base64")
}
