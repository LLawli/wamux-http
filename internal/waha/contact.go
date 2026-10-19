package waha

import (
	"strings"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// Contact is a contact the way GOWS answers it (`toWAContact`): the id in
// WAHA form, the saved name and the push name. A name nobody knows is null.
type Contact struct {
	ID       string  `json:"id"`
	Name     *string `json:"name"`
	PushName *string `json:"pushname"`
}

// NewContact builds a contact; empty names become null.
func NewContact(id, name, pushName string) Contact {
	return Contact{ID: id, Name: nonEmpty(name), PushName: nonEmpty(pushName)}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// LidToPhoneNumber is WAHA's LidToPhoneNumber. WAHA answers null for the side
// it does not know, so both are pointers that marshal as null.
type LidToPhoneNumber struct {
	LID *string `json:"lid"`
	PN  *string `json:"pn"`
}

// LidPairFromPB maps one LID mapping; a side the core did not send is null.
func LidPairFromPB(m *pb.LidPnMapping) LidToPhoneNumber {
	return LidToPhoneNumber{LID: nonEmpty(FromPB(m.GetLid())), PN: nonEmpty(FromPB(m.GetPn()))}
}

// NumberExistResult is WAHA's WANumberExistResult. Both ids are left out for
// a number that is not on WhatsApp.
type NumberExistResult struct {
	NumberExists bool   `json:"numberExists"`
	ChatID       string `json:"chatId,omitempty"`
	PN           string `json:"pn,omitempty"`
}

// NumberExistFromPB maps one CheckOnWhatsApp result.
func NumberExistFromPB(r *pb.CheckResult) NumberExistResult {
	if !r.GetIsOnWhatsapp() {
		return NumberExistResult{}
	}
	out := NumberExistResult{NumberExists: true, ChatID: FromPB(r.GetJid())}
	if out.ChatID == "" {
		out.ChatID = FromPB(r.GetQuery())
	}
	if q := FromPB(r.GetQuery()); IsPhoneChatID(q) {
		out.PN = q
	} else if IsPhoneChatID(out.ChatID) {
		out.PN = out.ChatID
	}
	return out
}

// IsPhoneChatID reports whether a WAHA id names a person by phone (`@c.us`).
func IsPhoneChatID(id string) bool { return strings.HasSuffix(id, "@"+serverCUs) }

// IsLIDChatID reports whether a WAHA id is a hidden identity (`@lid`).
func IsLIDChatID(id string) bool { return strings.HasSuffix(id, "@lid") }
