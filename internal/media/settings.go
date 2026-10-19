package media

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Settings are the media layer's own environment variables. They are read
// here rather than in config so the layer stays self-contained.
type Settings struct {
	// AllowPrivate (WAMUX_HTTP_MEDIA_ALLOW_PRIVATE) lets fetches reach
	// loopback, private and link-local addresses, for setups where the media
	// server is on the same network and every API caller is trusted.
	AllowPrivate bool
	// MaxBytes (WAMUX_HTTP_MEDIA_MAX_BYTES) caps one fetched file.
	MaxBytes int64
	// FetchTimeout (WAMUX_HTTP_MEDIA_FETCH_TIMEOUT) bounds one fetch,
	// redirects and body included.
	FetchTimeout time.Duration
}

// Defaults for Settings.
const (
	DefaultMaxBytes     = 100 << 20
	DefaultFetchTimeout = 60 * time.Second
)

// SettingsFromEnv reads Settings; an unreadable value keeps the default and
// is logged.
func SettingsFromEnv() Settings {
	s := Settings{MaxBytes: DefaultMaxBytes, FetchTimeout: DefaultFetchTimeout}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WAMUX_HTTP_MEDIA_ALLOW_PRIVATE"))) {
	case "1", "true", "yes", "on":
		s.AllowPrivate = true
	}
	if v := strings.TrimSpace(os.Getenv("WAMUX_HTTP_MEDIA_MAX_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			s.MaxBytes = n
		} else {
			slog.Warn("ignoring WAMUX_HTTP_MEDIA_MAX_BYTES", "value", v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("WAMUX_HTTP_MEDIA_FETCH_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			s.FetchTimeout = d
		} else {
			slog.Warn("ignoring WAMUX_HTTP_MEDIA_FETCH_TIMEOUT", "value", v)
		}
	}
	return s
}
