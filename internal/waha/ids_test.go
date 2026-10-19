package waha

import (
	"testing"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

func TestChatIDFromJID(t *testing.T) {
	cases := map[string]string{
		"5511999999999@s.whatsapp.net":    "5511999999999@c.us",
		"5511999999999:12@s.whatsapp.net": "5511999999999@c.us",
		"120363000000000000@g.us":         "120363000000000000@g.us",
		"123456789@lid":                   "123456789@lid",
		"123456789:3@lid":                 "123456789@lid",
		"status@broadcast":                "status@broadcast",
		"120363000000000001@newsletter":   "120363000000000001@newsletter",
	}
	for in, want := range cases {
		if got := ChatIDFromJID(in); got != want {
			t.Errorf("ChatIDFromJID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJIDFromChatID(t *testing.T) {
	cases := map[string]string{
		"5511999999999@c.us":      "5511999999999@s.whatsapp.net",
		"5511999999999":           "5511999999999@s.whatsapp.net",
		"+55 (11) 99999-9999":     "5511999999999@s.whatsapp.net",
		"120363000000000000@g.us": "120363000000000000@g.us",
		"123@lid":                 "123@lid",
	}
	for in, want := range cases {
		got, err := JIDFromChatID(in)
		if err != nil || got != want {
			t.Errorf("JIDFromChatID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "@c.us", "123@"} {
		if _, err := JIDFromChatID(bad); err == nil {
			t.Errorf("JIDFromChatID(%q) accepted", bad)
		}
	}
}

func TestMessageIDRoundTrip(t *testing.T) {
	for _, s := range []string{
		"false_11111111111@c.us_AAAAAAAAAAAAAAAAAAAA",
		"true_11111111111@c.us_3EB0C0FFEE",
		"false_120363000000000000@g.us_ABCDEF_22222222222@c.us",
		"true_123@lid_XYZ",
	} {
		m, err := ParseMessageID(s)
		if err != nil {
			t.Fatalf("ParseMessageID(%q): %v", s, err)
		}
		if m.String() != s {
			t.Errorf("round trip %q -> %q", s, m.String())
		}
	}
	for _, bad := range []string{"", "ABC", "maybe_1@c.us_X", "true__X", "true_1@c.us_", "true_nochat_X"} {
		if _, err := ParseMessageID(bad); err == nil {
			t.Errorf("ParseMessageID(%q) accepted", bad)
		}
	}
}

func TestInboundIDFromKey(t *testing.T) {
	k := &pb.MessageKey{
		Chat:        &pb.Jid{Value: "120363000000000000@g.us"},
		Id:          "ABC",
		Participant: &pb.Jid{Value: "5511999999999:4@s.whatsapp.net"},
	}
	got := InboundID(&pb.InboundMessage{Key: k}, Me{}).String()
	if want := "false_120363000000000000@g.us_ABC_5511999999999@c.us"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	back, err := ParseMessageID(got)
	if err != nil {
		t.Fatal(err)
	}
	key, err := back.Key()
	if err != nil {
		t.Fatal(err)
	}
	if key.GetChat().GetValue() != "120363000000000000@g.us" || key.GetParticipant().GetValue() != "5511999999999@s.whatsapp.net" {
		t.Fatalf("Key() = %v", key)
	}
	dm := InboundID(&pb.InboundMessage{Key: &pb.MessageKey{Chat: &pb.Jid{Value: "1@s.whatsapp.net"}, Id: "X", FromMe: true, Participant: &pb.Jid{Value: "2@s.whatsapp.net"}}}, Me{})
	if dm.String() != "true_1@c.us_X" {
		t.Fatalf("a DM carries no participant suffix: %q", dm)
	}
}
