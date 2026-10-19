// Package waha holds WAHA's data model: ids, message and event payloads. It
// converts from the core's generated types and knows nothing about HTTP.
package waha

import (
	"errors"
	"strings"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

const (
	serverUser  = "s.whatsapp.net"
	serverCUs   = "c.us"
	serverGroup = "g.us"
)

// ChatIDFromJID turns a core JID into the id WAHA uses for a chat or contact:
// a person's `@s.whatsapp.net` becomes `@c.us` and any device suffix is
// dropped. Groups, `@lid`, `@newsletter` and `status@broadcast` pass through.
func ChatIDFromJID(jid string) string {
	user, server, ok := strings.Cut(jid, "@")
	if !ok {
		return jid
	}
	if server == serverUser {
		server = serverCUs
	}
	if server == serverCUs || server == "lid" {
		user, _, _ = strings.Cut(user, ":") // device
	}
	return user + "@" + server
}

// FromPB is ChatIDFromJID for a generated Jid; nil gives "".
func FromPB(j *pb.Jid) string {
	if j == nil {
		return ""
	}
	return ChatIDFromJID(j.GetValue())
}

// ErrBadChatID is returned for a chat id that names nobody.
var ErrBadChatID = errors.New("invalid chatId")

// JIDFromChatID turns a WAHA chat id into the JID the core expects. A bare
// phone number (digits, optionally with + and separators) is a person.
func JIDFromChatID(chatID string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "", ErrBadChatID
	}
	user, server, ok := strings.Cut(chatID, "@")
	if !ok {
		digits := onlyDigits(chatID)
		if digits == "" {
			return "", ErrBadChatID
		}
		return digits + "@" + serverUser, nil
	}
	if user == "" || server == "" {
		return "", ErrBadChatID
	}
	if server == serverCUs {
		server = serverUser
	}
	return user + "@" + server, nil
}

// ToPB is JIDFromChatID wrapped in the generated Jid.
func ToPB(chatID string) (*pb.Jid, error) {
	j, err := JIDFromChatID(chatID)
	if err != nil {
		return nil, err
	}
	return &pb.Jid{Value: j}, nil
}

// IsGroup reports whether a chat id (either form) is a group.
func IsGroup(id string) bool { return strings.HasSuffix(id, "@"+serverGroup) }

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return ""
		}
	}
	return b.String()
}

// MessageID is WAHA's serialized message id:
// `<fromMe>_<chatId>_<id>`, plus `_<participant>` for a message in a group.
type MessageID struct {
	FromMe      bool
	Chat        string // WAHA form
	ID          string
	Participant string // WAHA form; empty outside groups
}

func (m MessageID) String() string {
	s := boolStr(m.FromMe) + "_" + m.Chat + "_" + m.ID
	if m.Participant != "" {
		s += "_" + m.Participant
	}
	return s
}

// ErrBadMessageID is returned for an id that is not WAHA's serialized form.
var ErrBadMessageID = errors.New("invalid message id")

// ParseMessageID reads a serialized id. WhatsApp message ids never contain an
// underscore, and neither do JIDs, so splitting on it is unambiguous.
func ParseMessageID(s string) (MessageID, error) {
	parts := strings.Split(s, "_")
	if len(parts) != 3 && len(parts) != 4 {
		return MessageID{}, ErrBadMessageID
	}
	var m MessageID
	switch parts[0] {
	case "true":
		m.FromMe = true
	case "false":
	default:
		return MessageID{}, ErrBadMessageID
	}
	m.Chat, m.ID = parts[1], parts[2]
	if m.Chat == "" || m.ID == "" || !strings.Contains(m.Chat, "@") {
		return MessageID{}, ErrBadMessageID
	}
	if len(parts) == 4 {
		m.Participant = parts[3]
	}
	return m, nil
}

// Key converts back to a core message key.
func (m MessageID) Key() (*pb.MessageKey, error) {
	chat, err := ToPB(m.Chat)
	if err != nil {
		return nil, err
	}
	k := &pb.MessageKey{Chat: chat, Id: m.ID, FromMe: m.FromMe}
	if m.Participant != "" {
		if k.Participant, err = ToPB(m.Participant); err != nil {
			return nil, err
		}
	}
	return k, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
