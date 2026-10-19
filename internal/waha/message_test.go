package waha

import (
	"encoding/json"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

var testMe = Me{ID: "5511999999999@c.us", JID: "5511999999999:7@s.whatsapp.net"}

func rawOf(t *testing.T, m *waE2E.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ctxFor(download bool, mimetypes ...string) MessageContext {
	return MessageContext{Me: testMe, Media: MediaOptions{BaseURL: "http://edge:3000/", Session: "default", Download: download, Mimetypes: mimetypes}}
}

func TestNewMessageDM(t *testing.T) {
	m := &pb.InboundMessage{
		Key:  &pb.MessageKey{Chat: jid("5511888888888@s.whatsapp.net"), Id: "3EB0A"},
		Chat: jid("5511888888888@s.whatsapp.net"), Sender: jid("5511888888888:3@s.whatsapp.net"),
		Timestamp: 1760000000123, Text: "oi",
	}
	got := NewMessage(m, ctxFor(true))
	if got.ID != "false_5511888888888@c.us_3EB0A" || got.From != "5511888888888@c.us" || got.FromMe {
		t.Fatalf("ids: %+v", got)
	}
	if got.Timestamp != 1760000000 {
		t.Fatalf("timestamp is seconds: %d", got.Timestamp)
	}
	if got.To != nil || got.Participant != nil || got.Source != "app" {
		t.Fatalf("a DM has no to/participant and comes from the app: %+v", got)
	}
	if got.Body == nil || *got.Body != "oi" || got.HasMedia || got.Media != nil {
		t.Fatalf("body/media: %+v", got)
	}
	if got.Ack != AckDevice || got.AckName != "DEVICE" {
		t.Fatalf("others' live ack is DEVICE: %d %s", got.Ack, got.AckName)
	}
	var data map[string]any
	if err := json.Unmarshal(got.Data, &data); err != nil || data["text"] != "oi" {
		t.Fatalf("_data is the core message as JSON: %s", got.Data)
	}
	// Absent mediaUrl, null media: GOWS's shape.
	b, _ := json.Marshal(got)
	var asMap map[string]any
	_ = json.Unmarshal(b, &asMap)
	if _, ok := asMap["mediaUrl"]; ok {
		t.Fatal("mediaUrl must be absent")
	}
	if v, ok := asMap["media"]; !ok || v != nil {
		t.Fatalf("media must be null: %v", v)
	}
}

func TestNewMessageSource(t *testing.T) {
	mine := func(sender string) *pb.InboundMessage {
		m := &pb.InboundMessage{Key: &pb.MessageKey{Chat: jid("1@s.whatsapp.net"), Id: "X", FromMe: true}, Chat: jid("1@s.whatsapp.net")}
		if sender != "" {
			m.Sender = jid(sender)
		}
		return m
	}
	cases := map[string]string{
		"5511999999999:7@s.whatsapp.net": "api", // this edge's device
		"5511999999999:0@s.whatsapp.net": "app", // the phone
		"5511999999999@s.whatsapp.net":   "app",
		"":                               "api", // the core's echo before the account has a jid
	}
	for sender, want := range cases {
		got := NewMessage(mine(sender), ctxFor(true))
		if got.Source != want {
			t.Errorf("sender %q: source %q, want %q", sender, got.Source, want)
		}
		if got.Ack != AckServer || got.AckName != "SERVER" {
			t.Errorf("my live ack is SERVER: %d", got.Ack)
		}
	}
}

func TestNewMessageGroupAndStatus(t *testing.T) {
	g := &pb.InboundMessage{
		Key:    &pb.MessageKey{Chat: jid("120363000000000000@g.us"), Id: "G1", Participant: jid("5511777777777:2@s.whatsapp.net")},
		Chat:   jid("120363000000000000@g.us"),
		Sender: jid("5511777777777:2@s.whatsapp.net"),
	}
	got := NewMessage(g, ctxFor(true))
	if got.ID != "false_120363000000000000@g.us_G1_5511777777777@c.us" {
		t.Fatalf("group id: %s", got.ID)
	}
	if got.To == nil || *got.To != "5511777777777@c.us" || got.Participant == nil || *got.Participant != "5511777777777@c.us" {
		t.Fatalf("group to/participant: %+v", got)
	}
	// My own message in a group, echoed without a sender: participant is me.
	mine := &pb.InboundMessage{Key: &pb.MessageKey{Chat: jid("120363000000000000@g.us"), Id: "G2", FromMe: true}, Chat: jid("120363000000000000@g.us")}
	if got := NewMessage(mine, ctxFor(true)); got.ID != "true_120363000000000000@g.us_G2_5511999999999@c.us" {
		t.Fatalf("my group id: %s", got.ID)
	}
	st := &pb.InboundMessage{Key: &pb.MessageKey{Chat: jid("status@broadcast"), Id: "S1"}, Sender: jid("5511666666666@s.whatsapp.net")}
	if got := NewMessage(st, ctxFor(true)); got.ID != "false_status@broadcast_S1_5511666666666@c.us" {
		t.Fatalf("status id: %s", got.ID)
	}
}

func TestNewMessageMedia(t *testing.T) {
	doc := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("nota.pdf"), Caption: proto.String("segue")}}
	m := &pb.InboundMessage{
		Key:  &pb.MessageKey{Chat: jid("1@s.whatsapp.net"), Id: "M1"},
		Chat: jid("1@s.whatsapp.net"), Caption: "segue",
		Media:      &pb.MediaDescriptor{MimeType: "application/pdf", DirectPath: "/x"},
		RawMessage: rawOf(t, doc),
	}
	got := NewMessage(m, ctxFor(true))
	if !got.HasMedia || got.Media == nil || got.Media.URL == nil || *got.Media.URL != "http://edge:3000/api/files/default/M1.pdf" {
		t.Fatalf("media URL: %+v", got.Media)
	}
	if got.Media.Mimetype != "application/pdf" || got.Media.Filename == nil || *got.Media.Filename != "nota.pdf" {
		t.Fatalf("media fields: %+v", got.Media)
	}
	if *got.Body != "segue" {
		t.Fatalf("caption is the body: %v", *got.Body)
	}
	if off := NewMessage(m, ctxFor(false)); off.Media == nil || off.Media.URL != nil || !off.HasMedia {
		t.Fatalf("download off keeps media with a null url: %+v", off.Media)
	}
	if filtered := NewMessage(m, ctxFor(true, "image", "audio")); filtered.Media.URL != nil {
		t.Fatalf("a filtered mimetype gets no url: %v", *filtered.Media.URL)
	}
	if allowed := NewMessage(m, ctxFor(true, "application/")); allowed.Media.URL == nil {
		t.Fatal("a listed prefix gets a url")
	}
}

func TestNewMessageLocationVCardsReply(t *testing.T) {
	loc := &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
		DegreesLatitude: proto.Float64(-23.55), DegreesLongitude: proto.Float64(-46.633), Name: proto.String("Sé"),
		Comment: proto.String("aqui"), JPEGThumbnail: []byte{1, 2},
	}}
	m := &pb.InboundMessage{Key: &pb.MessageKey{Chat: jid("1@s.whatsapp.net"), Id: "L"}, RawMessage: rawOf(t, loc)}
	got := NewMessage(m, ctxFor(true))
	if got.Location == nil || got.Location.Latitude != "-23.55" || got.Location.Longitude != "-46.633" || got.Location.Live ||
		got.Location.Name != "Sé" || got.Location.Description != "aqui" || got.Location.Thumbnail != "AQI=" {
		t.Fatalf("location: %+v", got.Location)
	}
	if got.Body != nil {
		t.Fatalf("a location has no body: %v", *got.Body)
	}
	live := &waE2E.Message{LiveLocationMessage: &waE2E.LiveLocationMessage{DegreesLatitude: proto.Float64(1), DegreesLongitude: proto.Float64(2.5), Caption: proto.String("indo")}}
	m.RawMessage = rawOf(t, live)
	if got := NewMessage(m, ctxFor(true)); got.Location == nil || !got.Location.Live || got.Location.Latitude != "1" || got.Location.Description != "indo" {
		t.Fatalf("live location: %+v", got.Location)
	}

	cards := &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{Contacts: []*waE2E.ContactMessage{
		{Vcard: proto.String("BEGIN:VCARD\nFN:A\nEND:VCARD")}, {Vcard: proto.String("BEGIN:VCARD\nFN:B\nEND:VCARD")},
	}}}
	m.RawMessage = rawOf(t, cards)
	if got := NewMessage(m, ctxFor(true)); len(got.VCards) != 2 || got.VCards[1] != "BEGIN:VCARD\nFN:B\nEND:VCARD" {
		t.Fatalf("vCards: %v", got.VCards)
	}
	one := &waE2E.Message{ContactMessage: &waE2E.ContactMessage{Vcard: proto.String("V")}}
	m.RawMessage = rawOf(t, one)
	if got := NewMessage(m, ctxFor(true)); len(got.VCards) != 1 || got.VCards[0] != "V" {
		t.Fatalf("one vCard: %v", got.VCards)
	}

	reply := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("sim"),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID: proto.String("Q1"), Participant: proto.String("5511777777777@s.whatsapp.net"),
			QuotedMessage: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("foto")}},
		},
	}}
	m.RawMessage, m.Text = rawOf(t, reply), "sim"
	got = NewMessage(m, ctxFor(true))
	r := got.ReplyTo
	if r == nil || r.ID != "Q1" || r.Participant == nil || *r.Participant != "5511777777777@c.us" || r.Body == nil || *r.Body != "foto" || !r.HasMedia || r.Media != nil {
		t.Fatalf("replyTo: %+v", r)
	}
	if got.VCards != nil || got.Location != nil {
		t.Fatal("a text has no vCards or location")
	}
	m.RawMessage, m.Quote = nil, &pb.QuoteContext{Quoted: &pb.MessageKey{Id: "Q2"}, Participant: jid("5511555555555@s.whatsapp.net")}
	if got := NewMessage(m, ctxFor(true)); got.ReplyTo == nil || got.ReplyTo.ID != "Q2" || *got.ReplyTo.Participant != "5511555555555@c.us" || got.ReplyTo.Body != nil {
		t.Fatalf("replyTo from the core's quote: %+v", got.ReplyTo)
	}
	m.Quote = nil
	if got := NewMessage(m, ctxFor(true)); got.ReplyTo != nil {
		t.Fatalf("no quote, no replyTo: %+v", got.ReplyTo)
	}
}

func TestBodyOf(t *testing.T) {
	cases := []struct {
		m    *waE2E.Message
		want string
	}{
		{&waE2E.Message{Conversation: proto.String("a")}, "a"},
		{&waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("b")}}}}, "b"},
		{&waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{Response: &waE2E.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: "c"}}}, "c"},
		{&waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{Title: proto.String("t"), Description: proto.String("d")}}, "t\nd"},
		{&waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("v")}}, "v"},
	}
	for i, c := range cases {
		got := BodyOf(c.m)
		if got == nil || *got != c.want {
			t.Errorf("case %d: %v, want %q", i, got, c.want)
		}
	}
	if got := BodyOf(&waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}); got != nil {
		t.Errorf("sticker has no body: %q", *got)
	}
}

func TestRefreshMedia(t *testing.T) {
	m := &pb.InboundMessage{Key: &pb.MessageKey{Chat: jid("1@s.whatsapp.net"), Id: "M"}, Media: &pb.MediaDescriptor{MimeType: "image/jpeg"}}
	body, _ := json.Marshal(NewMessage(m, ctxFor(false)))
	out, err := RefreshMedia(body, "M", MediaOptions{BaseURL: "http://h", Session: "s", Download: true})
	if err != nil {
		t.Fatal(err)
	}
	var got Message
	_ = json.Unmarshal(out, &got)
	if got.Media.URL == nil || *got.Media.URL != "http://h/api/files/s/M.jpeg" || got.ID != "false_1@c.us_M" {
		t.Fatalf("refreshed: %s", out)
	}
	text, _ := json.Marshal(NewMessage(&pb.InboundMessage{Key: &pb.MessageKey{Chat: jid("1@s.whatsapp.net"), Id: "T"}, Text: "x"}, ctxFor(true)))
	if same, _ := RefreshMedia(text, "T", MediaOptions{Download: true}); string(same) != string(text) {
		t.Fatalf("a message without media is untouched: %s", same)
	}
}

func TestFromWebMessage(t *testing.T) {
	w := &waWeb.WebMessageInfo{
		Key:              &waCommon.MessageKey{RemoteJID: proto.String("120363000000000000@g.us"), ID: proto.String("H1")},
		Participant:      proto.String("5511777777777@s.whatsapp.net"),
		MessageTimestamp: proto.Uint64(1700000000),
		Status:           waWeb.WebMessageInfo_READ.Enum(),
		Message:          &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg"), DirectPath: proto.String("/d"), Caption: proto.String("c")}},
	}
	in, ok := FromWebMessage(w, testMe)
	if !ok {
		t.Fatal("history message refused")
	}
	if in.GetTimestamp() != 1700000000000 || in.GetSender().GetValue() != "5511777777777@s.whatsapp.net" || in.GetCaption() != "c" {
		t.Fatalf("inbound: %v", in)
	}
	if in.GetMedia().GetMediaType() != pb.MediaType_MEDIA_TYPE_IMAGE || in.GetMedia().GetDirectPath() != "/d" {
		t.Fatalf("media descriptor: %v", in.GetMedia())
	}
	got := NewMessage(in, ctxFor(true))
	if got.ID != "false_120363000000000000@g.us_H1_5511777777777@c.us" {
		t.Fatalf("id: %s", got.ID)
	}
	if HistoryAck(w.GetStatus()) != AckRead || HistoryAck(waWeb.WebMessageInfo_ERROR) != AckError {
		t.Fatal("history ack mapping")
	}
	w.Message = &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("x")}}
	if _, ok := FromWebMessage(w, testMe); ok {
		t.Fatal("a reaction is not a history message")
	}
	w.Message = nil
	if _, ok := FromWebMessage(w, testMe); ok {
		t.Fatal("a stub is not a history message")
	}
	raw, _ := proto.Marshal(&waHistorySync.HistorySync{SyncType: waHistorySync.HistorySync_RECENT.Enum()})
	if h, err := DecodeHistory(raw); err != nil || h.GetSyncType() != waHistorySync.HistorySync_RECENT {
		t.Fatalf("decode history: %v %v", h, err)
	}
}

func TestAckNames(t *testing.T) {
	for v := AckError; v <= AckPlayed; v++ {
		back, ok := AckFromName(AckName(v))
		if !ok || back != v {
			t.Errorf("ack %d round trip: %d %v", v, back, ok)
		}
	}
	if AckName(9) != "UNKNOWN" {
		t.Error("unknown ack")
	}
	if _, ok := AckFromName("NOPE"); ok {
		t.Error("unknown name accepted")
	}
}
