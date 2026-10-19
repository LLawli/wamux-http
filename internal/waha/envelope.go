package waha

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Environment is WAHA's WAHAEnvironment, sent with every event.
type Environment struct {
	Version  string  `json:"version"`
	Engine   string  `json:"engine"`
	Tier     string  `json:"tier"`
	Browser  *string `json:"browser"`
	Platform string  `json:"platform"`
	Worker   Worker  `json:"worker"`
}

// Worker names the process that produced an event.
type Worker struct {
	ID *string `json:"id"`
}

// Envelope is a webhook or WebSocket event. The field order is WAHA's.
type Envelope struct {
	ID          string            `json:"id"`
	Timestamp   int64             `json:"timestamp"`
	Event       string            `json:"event"`
	Session     string            `json:"session"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Me          json.RawMessage   `json:"me"`
	Payload     any               `json:"payload"`
	Engine      string            `json:"engine"`
	Environment Environment       `json:"environment"`
}

// NewEventID is "evt_" + a lowercase ULID.
func NewEventID() string {
	return "evt_" + strings.ToLower(ulid.Make().String())
}

// NewPrefixedID is prefix + "_" + a lowercase ULID (session_..., key_id_...).
func NewPrefixedID(prefix string) string {
	return prefix + strings.ToLower(ulid.Make().String())
}

// NowMs is the current time in ms.
func NowMs() int64 { return time.Now().UnixMilli() }
