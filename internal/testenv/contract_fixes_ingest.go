package testenv

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// specFix is one entry of specFixes.
type specFix = struct {
	schema string
	path   []string
	why    string
	apply  func(map[string]any)
}

// The messages, acks and chat reads: GOWS sends null where WAHA's document
// says string or object (docs/waha-behavior.md, section 6), and leaves
// mediaUrl out.
func init() {
	null := func(schema, why string, props ...string) []specFix {
		var out []specFix
		for _, p := range props {
			out = append(out, specFix{schema, []string{"properties", p}, why, nullable})
		}
		return out
	}
	noMediaURL := func(schema string) specFix {
		return specFix{schema, nil, "GOWS never sets mediaUrl", func(m map[string]any) { m["required"] = without(m["required"], "mediaUrl") }}
	}
	fixes := [][]specFix{
		null("WAMessage", "null outside groups, or when the message has none", "to", "participant", "body", "media", "location", "vCards", "replyTo"),
		{noMediaURL("WAMessage")},
		null("WAMessageEditedBody", "null outside groups, or when the message has none", "to", "participant", "media", "location", "vCards", "replyTo"),
		{noMediaURL("WAMessageEditedBody")},
		null("WAMedia", "null when the file is not served, or has no name", "url", "filename"),
		null("ReplyToMessage", "null when the quote does not say", "participant", "body", "media"),
		null("WAMessageAckBody", "null outside groups", "to", "participant"),
		null("WAMessageReaction", "null outside groups", "to", "participant"),
		null("WAMessageRevokedBody", "null when the revoked message is unknown", "before"),
		null("PollVote", "null outside groups", "to", "participant"),
		null("WAHAPresenceData", "null for chat states and unknown last seen", "lastSeen"),
		null("ChatSummary", "null for a chat with no stored message", "lastMessage"),
		null("ChatPictureResponse", "null when the chat has no picture", "url"),
		null("GroupParticipant", "null when the phone number is unknown", "pn"),
	}
	for _, f := range fixes {
		specFixes = append(specFixes, f...)
	}
}

func without(list any, name string) []any {
	var out []any
	items, _ := list.([]any)
	for _, v := range items {
		if v != name {
			out = append(out, v)
		}
	}
	return out
}

// CheckWebhook validates a received envelope's payload against the schema
// WAHA documents for its event: the payload of the webhook schema whose
// event defaults to that name (WAHAWebhookMessage, WebhookGroupV2Join, ...).
func CheckWebhook(t testing.TB, ev map[string]any) {
	t.Helper()
	specOnce.Do(loadSpec)
	if specErr != nil {
		t.Fatalf("contract: %v", specErr)
	}
	name, _ := ev["event"].(string)
	var payload *openapi3.Schema
	for _, ref := range spec.Components.Schemas {
		if ref.Value == nil {
			continue
		}
		evProp := ref.Value.Properties["event"]
		if evProp == nil || evProp.Value == nil || evProp.Value.Default != name {
			continue
		}
		if p := ref.Value.Properties["payload"]; p != nil {
			payload = p.Value
		}
	}
	if payload == nil {
		t.Errorf("contract: no webhook schema documents event %q", name)
		return
	}
	if err := payload.VisitJSON(ev["payload"], openapi3.MultiErrors()); err != nil {
		t.Errorf("contract: %s payload breaks WAHA's schema:\n%v\npayload: %v", name, err, ev["payload"])
	}
}
