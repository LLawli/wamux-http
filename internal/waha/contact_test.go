package waha

import (
	"encoding/json"
	"testing"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

func TestNewContact(t *testing.T) {
	raw, _ := json.Marshal(NewContact("5511999999999@c.us", "", "Ana"))
	if string(raw) != `{"id":"5511999999999@c.us","name":null,"pushname":"Ana"}` {
		t.Fatalf("contact: %s", raw)
	}
}

func TestLidPairFromPB(t *testing.T) {
	raw, _ := json.Marshal(LidPairFromPB(&pb.LidPnMapping{Lid: jid("1@lid"), Pn: jid("5511999999999@s.whatsapp.net")}))
	if string(raw) != `{"lid":"1@lid","pn":"5511999999999@c.us"}` {
		t.Fatalf("pair: %s", raw)
	}
	raw, _ = json.Marshal(LidPairFromPB(&pb.LidPnMapping{Lid: jid("1@lid")}))
	if string(raw) != `{"lid":"1@lid","pn":null}` {
		t.Fatalf("lid only: %s", raw)
	}
}

func TestNumberExistFromPB(t *testing.T) {
	got := NumberExistFromPB(&pb.CheckResult{Query: jid("5511999999999@s.whatsapp.net"), IsOnWhatsapp: true, Jid: jid("5511999999999@s.whatsapp.net")})
	if got != (NumberExistResult{NumberExists: true, ChatID: "5511999999999@c.us", PN: "5511999999999@c.us"}) {
		t.Fatalf("registered: %+v", got)
	}
	got = NumberExistFromPB(&pb.CheckResult{Query: jid("5511999999999@s.whatsapp.net"), IsOnWhatsapp: true, Jid: jid("123@lid")})
	if got.ChatID != "123@lid" || got.PN != "5511999999999@c.us" {
		t.Fatalf("lid answer keeps the phone in pn: %+v", got)
	}
	raw, _ := json.Marshal(NumberExistFromPB(&pb.CheckResult{Query: jid("1@s.whatsapp.net")}))
	if string(raw) != `{"numberExists":false}` {
		t.Fatalf("not registered: %s", raw)
	}
}
