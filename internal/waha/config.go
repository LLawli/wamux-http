package waha

import (
	"encoding/json"
	"strings"
)

// SessionConfig is the part of WAHA's SessionConfig the edge acts on. The
// stored config is kept verbatim, so fields read here are not the only ones
// a client gets back.
type SessionConfig struct {
	Metadata map[string]string `json:"metadata,omitempty"`
	Webhooks []WebhookConfig   `json:"webhooks,omitempty"`
	Ignore   *IgnoreConfig     `json:"ignore,omitempty"`
}

// WebhookConfig is one entry of SessionConfig.webhooks.
type WebhookConfig struct {
	URL           string         `json:"url"`
	Events        []string       `json:"events"`
	HMAC          *HMACConfig    `json:"hmac,omitempty"`
	Retries       *RetriesConfig `json:"retries,omitempty"`
	CustomHeaders []CustomHeader `json:"customHeaders,omitempty"`
}

// HMACConfig signs webhook bodies.
type HMACConfig struct {
	Key string `json:"key,omitempty"`
}

// RetriesConfig is a webhook's retry policy; nil fields take WAHA's defaults.
type RetriesConfig struct {
	DelaySeconds *float64 `json:"delaySeconds,omitempty"`
	Attempts     *int     `json:"attempts,omitempty"`
	Policy       string   `json:"policy,omitempty"`
}

// CustomHeader is an extra header sent with every webhook request.
type CustomHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// IgnoreConfig drops message events from some kinds of chat.
type IgnoreConfig struct {
	Status    bool `json:"status,omitempty"`
	Groups    bool `json:"groups,omitempty"`
	Channels  bool `json:"channels,omitempty"`
	Broadcast bool `json:"broadcast,omitempty"`
}

// ParseSessionConfig reads a stored config; nil or invalid gives a zero value.
func ParseSessionConfig(raw json.RawMessage) SessionConfig {
	var c SessionConfig
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &c)
	}
	return c
}

// AllEvents is every event name WAHA defines, in WAHA's order.
var AllEvents = []string{
	"session.status", "message", "message.reaction", "message.any", "message.ack",
	"message.ack.group", "message.waiting", "message.revoked", "message.edited",
	"state.change", "group.join", "group.leave", "group.v2.join", "group.v2.leave",
	"group.v2.update", "group.v2.participants", "group.v2.participants.join-request",
	"presence.update", "poll.vote", "poll.vote.failed", "chat.archive",
	"call.received", "call.accepted", "call.rejected", "label.upsert",
	"label.deleted", "label.chat.added", "label.chat.deleted", "event.response",
	"event.response.failed", "engine.event",
}

// EventMatches applies WAHA's selection rules: "*" is every event except
// state.change and engine.event, and an entry with "*" in it is a prefix match
// ("message.*" matches "message.ack" but not "message").
func EventMatches(selected []string, event string) bool {
	for _, s := range selected {
		switch {
		case s == "*":
			if event != "state.change" && event != "engine.event" {
				return true
			}
		case strings.Contains(s, "*"):
			if strings.HasPrefix(event, strings.SplitN(s, "*", 2)[0]) {
				return true
			}
		case s == event:
			return true
		}
	}
	return false
}
