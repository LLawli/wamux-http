// Package config reads the edge's configuration from the environment. Where
// WAHA has a variable for the same thing, its name is the one read, so a WAHA
// deployment's env file carries over. The edge's own settings are WAMUX_HTTP_*.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Engine is the engine name WAHA clients see. The webhook schema allows only
// WAHA's engine names; GOWS (whatsmeow) is the closest in shape to the
// whatsapp-rust library under wamux.
const Engine = "GOWS"

// Version is the WAHA version this edge mirrors (contract/waha-openapi.json).
const Version = "2026.8.2"

// Config is the whole configuration.
type Config struct {
	Listen  string // host:port the HTTP server binds
	BaseURL string // public URL, used in media links

	APIKey           string // admin key: plain, or "sha512:<hex>"
	APIKeyGenerated  bool   // true when no key was configured and one was made up
	NoAPIKey         bool   // WAHA_NO_API_KEY: every request is admin
	APIKeyExcludeDir []string

	CoreSocket  string // the wamux daemon's Unix socket
	DatabaseURL string // sqlite:///path/to/file.db
	WorkerID    string // this process's identity in leases

	GlobalWebhook *GlobalWebhook

	FilesFolder    string
	FilesLifetime  time.Duration // 0 keeps files forever
	DownloadMedia  bool
	MediaMimetypes []string // empty means all

	IgnoreStatus, IgnoreGroups, IgnoreChannels, IgnoreBroadcast bool

	EventRetention  time.Duration // how long the event log keeps an event
	WebhookTimeout  time.Duration // one delivery attempt
	LeaseTTL        time.Duration
	LogLevel        string
	LogFormat       string // PRETTY or JSON
	StartSessions   []string
	HTTPStrictMode  bool
	MaxRequestBytes int64
}

// GlobalWebhook is WHATSAPP_HOOK_*: a webhook attached to every session.
type GlobalWebhook struct {
	URL            string
	Events         []string
	HMACKey        string
	RetriesPolicy  string
	RetriesDelay   *float64
	RetriesAttempt *int
	CustomHeaders  [][2]string
}

// Load reads the environment.
func Load() (*Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (*Config, error) {
	env := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(getenv(k)); v != "" {
				return v
			}
		}
		return ""
	}
	c := &Config{}
	var err error

	port := env("PORT", "WHATSAPP_API_PORT")
	if port == "" {
		port = "3000"
	}
	c.Listen = env("WAMUX_HTTP_LISTEN")
	if c.Listen == "" {
		c.Listen = ":" + port
	}
	c.BaseURL = strings.TrimRight(env("WAHA_BASE_URL"), "/")
	if c.BaseURL == "" {
		schema, host := env("WHATSAPP_API_SCHEMA"), env("WHATSAPP_API_HOSTNAME")
		if schema == "" {
			schema = "http"
		}
		if host == "" {
			host = "localhost"
		}
		c.BaseURL = fmt.Sprintf("%s://%s:%s", schema, host, port)
	}
	if _, err := url.Parse(c.BaseURL); err != nil {
		return nil, fmt.Errorf("WAHA_BASE_URL: %w", err)
	}

	// WHATSAPP_API_KEY overrides WAHA_API_KEY, as in WAHA.
	c.APIKey = env("WHATSAPP_API_KEY", "WAHA_API_KEY")
	c.NoAPIKey = truthy(env("WAHA_NO_API_KEY"))
	if weakKey(c.APIKey) && !c.NoAPIKey {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		c.APIKey, c.APIKeyGenerated = hex.EncodeToString(b), true
	}
	c.APIKeyExcludeDir = list(env("WHATSAPP_API_KEY_EXCLUDE_PATH"), ",")

	c.CoreSocket = env("WAMUX_HTTP_CORE_SOCKET", "WAMUX_SOCKET_PATH")
	if c.CoreSocket == "" {
		c.CoreSocket = "/run/wamux/wamux.sock"
	}
	c.DatabaseURL = env("WAMUX_HTTP_DATABASE_URL")
	if c.DatabaseURL == "" {
		c.DatabaseURL = "sqlite://./wamux-http.db"
	}
	c.WorkerID = env("WAHA_WORKER_ID")
	if c.WorkerID == "" {
		host, _ := os.Hostname()
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		c.WorkerID = host + "-" + hex.EncodeToString(b)
	}

	if c.GlobalWebhook, err = globalWebhook(env); err != nil {
		return nil, fmt.Errorf("invalid global webhook config: %w", err)
	}

	c.FilesFolder = env("WHATSAPP_FILES_FOLDER")
	if c.FilesFolder == "" {
		c.FilesFolder = "./files"
	}
	lifetime := 180
	if v := env("WHATSAPP_FILES_LIFETIME"); v != "" {
		if lifetime, err = strconv.Atoi(v); err != nil {
			return nil, fmt.Errorf("WHATSAPP_FILES_LIFETIME: %w", err)
		}
	}
	c.FilesLifetime = time.Duration(lifetime) * time.Second
	c.DownloadMedia = env("WHATSAPP_DOWNLOAD_MEDIA") == "" || truthy(env("WHATSAPP_DOWNLOAD_MEDIA"))
	c.MediaMimetypes = list(env("WHATSAPP_FILES_MIMETYPES"), ",")

	c.IgnoreStatus = truthy(env("WAHA_SESSION_CONFIG_IGNORE_STATUS"))
	c.IgnoreGroups = truthy(env("WAHA_SESSION_CONFIG_IGNORE_GROUPS"))
	c.IgnoreChannels = truthy(env("WAHA_SESSION_CONFIG_IGNORE_CHANNELS"))
	c.IgnoreBroadcast = truthy(env("WAHA_SESSION_CONFIG_IGNORE_BROADCAST"))

	if c.EventRetention, err = duration(env("WAMUX_HTTP_EVENT_RETENTION"), 24*time.Hour); err != nil {
		return nil, fmt.Errorf("WAMUX_HTTP_EVENT_RETENTION: %w", err)
	}
	if c.WebhookTimeout, err = duration(env("WAMUX_HTTP_WEBHOOK_TIMEOUT"), 30*time.Second); err != nil {
		return nil, fmt.Errorf("WAMUX_HTTP_WEBHOOK_TIMEOUT: %w", err)
	}
	if c.LeaseTTL, err = duration(env("WAMUX_HTTP_LEASE_TTL"), 15*time.Second); err != nil {
		return nil, fmt.Errorf("WAMUX_HTTP_LEASE_TTL: %w", err)
	}
	c.LogLevel = strings.ToLower(env("WAHA_LOG_LEVEL"))
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	c.LogFormat = strings.ToUpper(env("WAHA_LOG_FORMAT"))
	c.StartSessions = list(env("WHATSAPP_START_SESSION"), ",")
	c.HTTPStrictMode = truthy(env("WAHA_HTTP_STRICT_MODE"))
	c.MaxRequestBytes = 100 << 20
	if v := env("WAMUX_HTTP_MAX_REQUEST_BYTES"); v != "" {
		if c.MaxRequestBytes, err = strconv.ParseInt(v, 10, 64); err != nil {
			return nil, fmt.Errorf("WAMUX_HTTP_MAX_REQUEST_BYTES: %w", err)
		}
	}
	return c, nil
}

// weakKey reports the values WAHA refuses as a key.
func weakKey(k string) bool {
	switch k {
	case "", "123", "321", "waha", "admin", strings.Repeat("0", 32), strings.Repeat("1", 32):
		return true
	}
	return false
}

func globalWebhook(env func(...string) string) (*GlobalWebhook, error) {
	u, events := env("WHATSAPP_HOOK_URL"), list(env("WHATSAPP_HOOK_EVENTS"), ",")
	if u == "" || len(events) == 0 {
		return nil, nil
	}
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return nil, fmt.Errorf("WHATSAPP_HOOK_URL must be an http(s) URL")
	}
	g := &GlobalWebhook{URL: u, Events: events, HMACKey: env("WHATSAPP_HOOK_HMAC_KEY")}
	switch p := env("WHATSAPP_HOOK_RETRIES_POLICY"); p {
	case "", "linear", "exponential", "constant":
		g.RetriesPolicy = p
	default:
		return nil, fmt.Errorf("WHATSAPP_HOOK_RETRIES_POLICY %q", p)
	}
	if v := env("WHATSAPP_HOOK_RETRIES_DELAY_SECONDS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("WHATSAPP_HOOK_RETRIES_DELAY_SECONDS: %w", err)
		}
		g.RetriesDelay = &f
	}
	if v := env("WHATSAPP_HOOK_RETRIES_ATTEMPTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("WHATSAPP_HOOK_RETRIES_ATTEMPTS: %w", err)
		}
		g.RetriesAttempt = &n
	}
	for _, h := range list(env("WHATSAPP_HOOK_CUSTOM_HEADERS"), ";") {
		parts := strings.Split(h, ":")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("WHATSAPP_HOOK_CUSTOM_HEADERS: %q is not Name:value", h)
		}
		g.CustomHeaders = append(g.CustomHeaders, [2]string{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])})
	}
	return g, nil
}

func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func list(v, sep string) []string {
	var out []string
	for _, p := range strings.Split(v, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func duration(v string, def time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	return time.ParseDuration(v)
}
