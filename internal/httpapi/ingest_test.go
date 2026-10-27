package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"google.golang.org/protobuf/proto"

	"github.com/LLawli/wamux-http/internal/config"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/testenv"
)

const (
	peerJID = "5511888888888@s.whatsapp.net"
	peerID  = "5511888888888@c.us"
	groupID = "120363000000000000@g.us"
	otherID = "5511777777777@c.us"
)

// ingestEnv is a WORKING session "default" whose webhook takes every event,
// engine.event included.
type ingestEnv struct {
	*testenv.Env
	t    *testing.T
	uuid string
	hook *testenv.Hook
	seq  atomic.Int64
}

func startIngest(t *testing.T, opts testenv.Options, config map[string]any) *ingestEnv {
	t.Helper()
	e := testenv.Start(t, opts)
	hook := e.NewHook(nil)
	if config == nil {
		config = map[string]any{}
	}
	config["webhooks"] = []map[string]any{{"url": hook.URL, "events": []string{"*", "engine.event"}}}
	uuid := e.WorkingSession("default", meJID, config)
	return &ingestEnv{Env: e, t: t, uuid: uuid, hook: hook}
}

func jid(v string) *pb.Jid { return &pb.Jid{Value: v} }

func rawMsg(t *testing.T, m *waE2E.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (ie *ingestEnv) emit(ev *pb.EventEnvelope) {
	ie.Core.Emit(ie.uuid, ev)
}

// emitMsg publishes a message event.
func (ie *ingestEnv) emitMsg(m *pb.InboundMessage) {
	ie.Core.Emit(ie.uuid, &pb.EventEnvelope{Event: &pb.EventEnvelope_Message{Message: m}})
}

// text is a text message in chat (a JID) from sender.
func text(t *testing.T, chat, sender, id, body string, fromMe bool) *pb.InboundMessage {
	m := &pb.InboundMessage{
		Key:  &pb.MessageKey{Chat: jid(chat), Id: id, FromMe: fromMe},
		Chat: jid(chat), Sender: jid(sender), Timestamp: time.Now().UnixMilli(), Text: body,
		RawMessage: rawMsg(t, &waE2E.Message{Conversation: proto.String(body)}),
	}
	if chat == groupID {
		m.Key.Participant = jid(sender)
	}
	if !fromMe {
		m.PushName = "Bia"
	}
	return m
}

// wait returns the payload of the first webhook named name that match
// accepts, checked against WAHA's schema for the event.
func (ie *ingestEnv) wait(name string, match func(p map[string]any) bool) map[string]any {
	ie.t.Helper()
	var got map[string]any
	ie.Eventually(name, 5*time.Second, func() bool {
		for _, ev := range ie.hook.Events(name) {
			p, _ := ev["payload"].(map[string]any)
			if match == nil || match(p) {
				got = ev
				return true
			}
		}
		return false
	})
	testenv.CheckWebhook(ie.t, got)
	return got["payload"].(map[string]any)
}

// barrier emits a marker event and waits for it, so every event emitted
// before it has been processed (a session's events are handled in order).
func (ie *ingestEnv) barrier() {
	ie.t.Helper()
	n := ie.seq.Add(1)
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_OfflineSyncCompleted{OfflineSyncCompleted: &pb.OfflineSyncCompleted{Count: int32(n)}}})
	ie.wait("engine.event", func(p map[string]any) bool {
		d, _ := p["data"].(map[string]any)
		return p["event"] == "offline_sync_completed" && d["count"] == float64(n)
	})
}

// count is how many webhooks named name match.
func (ie *ingestEnv) count(name string, match func(p map[string]any) bool) int {
	n := 0
	for _, ev := range ie.hook.Events(name) {
		p, _ := ev["payload"].(map[string]any)
		if match == nil || match(p) {
			n++
		}
	}
	return n
}

func byID(id string) func(map[string]any) bool {
	return func(p map[string]any) bool { return p["id"] == id }
}

func (ie *ingestEnv) stored(waID string) store.Message {
	ie.t.Helper()
	m, err := ie.App.Store.GetMessageByWAID(context.Background(), "default", waID)
	if err != nil {
		ie.t.Fatalf("message %s not stored: %v", waID, err)
	}
	return m
}

func TestIngestMessages(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)

	in1 := text(t, peerJID, peerJID, "IN1", "oi", false)
	ie.emitMsg(in1)
	msg := ie.wait("message", byID("false_"+peerID+"_IN1"))
	if msg["from"] != peerID || msg["fromMe"] != false || msg["source"] != "app" || msg["body"] != "oi" ||
		msg["to"] != nil || msg["participant"] != nil || msg["ack"] != float64(2) || msg["ackName"] != "DEVICE" {
		t.Fatalf("message payload: %v", msg)
	}
	if ts := msg["timestamp"].(float64); ts < float64(time.Now().Unix()-60) || ts > float64(time.Now().Unix()+1) {
		t.Fatalf("timestamp is not in seconds: %v", ts)
	}
	ie.wait("message.any", byID("false_"+peerID+"_IN1"))

	// The edge's own send comes back as the core's echo: message.any only.
	echo := text(t, peerJID, meJID, "OUT1", "tchau", true)
	ie.emitMsg(echo)
	echoed := ie.wait("message.any", byID("true_"+peerID+"_OUT1"))
	if echoed["source"] != "api" || echoed["ack"] != float64(1) || echoed["ackName"] != "SERVER" || echoed["fromMe"] != true {
		t.Fatalf("echo: %v", echoed)
	}
	// Mine from the phone (device 0) is "app".
	ie.emitMsg(text(t, peerJID, "5511999999999:0@s.whatsapp.net", "OUT2", "do celular", true))
	if p := ie.wait("message.any", byID("true_"+peerID+"_OUT2")); p["source"] != "app" {
		t.Fatalf("phone message source: %v", p["source"])
	}

	// A group message carries the sender.
	ie.emitMsg(text(t, groupID, "5511777777777:2@s.whatsapp.net", "G1", "grupo", false))
	g := ie.wait("message", byID("false_"+groupID+"_G1_"+otherID))
	if g["to"] != otherID || g["participant"] != otherID || g["from"] != groupID {
		t.Fatalf("group message: %v", g)
	}

	// A replay of the same message does not go out twice, nor count as
	// unread again.
	ie.emitMsg(in1)
	ie.barrier()
	if n := ie.count("message", byID("false_"+peerID+"_IN1")); n != 1 {
		t.Fatalf("replayed message went out %d times", n)
	}
	if n := ie.count("message", byID("true_"+peerID+"_OUT1")); n != 0 {
		t.Fatal("my own message went out as message")
	}

	// Stored with the core's message as raw, and the chat's overview moved.
	st := ie.stored("IN1")
	var in pb.InboundMessage
	if err := proto.Unmarshal(st.Raw, &in); err != nil || in.GetText() != "oi" || st.ChatID != peerID || st.Ack != 2 {
		t.Fatalf("stored: %+v %v", st, err)
	}
	ch, err := ie.App.Store.GetChat(context.Background(), "default", peerID)
	if err != nil || ch.LastMessageID != "true_"+peerID+"_OUT2" || ch.UnreadCount != 0 {
		t.Fatalf("chat after my reply: %+v %v", ch, err)
	}
	if c, err := ie.App.Store.GetContact(context.Background(), "default", peerID); err != nil || c.PushName != "Bia" {
		t.Fatalf("push name: %+v %v", c, err)
	}

	// A key share alone is no chat message: engine.event, nothing stored.
	ie.emitMsg(&pb.InboundMessage{Key: &pb.MessageKey{Chat: jid(peerJID), Id: "SKD"}, Sender: jid(peerJID),
		RawMessage: rawMsg(t, &waE2E.Message{SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{GroupID: proto.String("g")}})})
	ie.wait("engine.event", func(p map[string]any) bool {
		d, _ := p["data"].(map[string]any)
		k, _ := d["key"].(map[string]any)
		return p["event"] == "message" && k["id"] == "SKD"
	})
	if _, err := ie.App.Store.GetMessageByWAID(context.Background(), "default", "SKD"); err == nil {
		t.Fatal("a key share was stored")
	}
}

// An event past gRPC's default 4 MiB receive limit still arrives: a history
// sync blob measured 4.67 MB live, and the stream broke on it.
func TestIngestLargeEvent(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	big := text(t, peerJID, peerJID, "BIG1", "big", false)
	big.RawMessage = rawMsg(t, &waE2E.Message{
		Conversation:       proto.String("big"),
		MessageContextInfo: &waE2E.MessageContextInfo{PaddingBytes: bytes.Repeat([]byte{1}, 5<<20)},
	})
	ie.emitMsg(big)
	ie.emitMsg(text(t, peerJID, peerJID, "AFTER1", "after", false))
	ie.wait("message", byID("false_"+peerID+"_BIG1"))
	ie.wait("message", byID("false_"+peerID+"_AFTER1"))
}

func TestIngestMedia(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	ie.emitMsg(&pb.InboundMessage{
		Key: &pb.MessageKey{Chat: jid(peerJID), Id: "IMG1"}, Chat: jid(peerJID), Sender: jid(peerJID),
		Timestamp: time.Now().UnixMilli(), Caption: "foto",
		Media:      &pb.MediaDescriptor{MimeType: "image/jpeg", DirectPath: "/v/t62/x", MediaType: pb.MediaType_MEDIA_TYPE_IMAGE},
		RawMessage: rawMsg(t, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("foto"), Mimetype: proto.String("image/jpeg")}}),
	})
	p := ie.wait("message", byID("false_"+peerID+"_IMG1"))
	media := p["media"].(map[string]any)
	if p["hasMedia"] != true || p["body"] != "foto" || media["url"] != ie.URL+"/api/files/default/IMG1.jpeg" || media["mimetype"] != "image/jpeg" {
		t.Fatalf("media message: %v", p)
	}
	// What the files route needs: the descriptor, in the stored raw.
	var in pb.InboundMessage
	if err := proto.Unmarshal(ie.stored("IMG1").Raw, &in); err != nil || in.GetMedia().GetDirectPath() != "/v/t62/x" {
		t.Fatalf("stored raw: %v %v", in.GetMedia(), err)
	}
}

func TestIngestMediaFiltered(t *testing.T) {
	ie := startIngest(t, testenv.Options{Config: func(c *config.Config) { c.MediaMimetypes = []string{"audio"} }}, nil)
	ie.emitMsg(&pb.InboundMessage{
		Key: &pb.MessageKey{Chat: jid(peerJID), Id: "IMG2"}, Chat: jid(peerJID), Sender: jid(peerJID),
		Media: &pb.MediaDescriptor{MimeType: "image/png"},
	})
	p := ie.wait("message", byID("false_"+peerID+"_IMG2"))
	if m := p["media"].(map[string]any); m["url"] != nil || m["mimetype"] != "image/png" {
		t.Fatalf("filtered media: %v", m)
	}
}

func TestIngestIgnore(t *testing.T) {
	ie := startIngest(t, testenv.Options{Config: func(c *config.Config) { c.IgnoreStatus = true }},
		map[string]any{"ignore": map[string]bool{"groups": true}})
	ie.emitMsg(text(t, groupID, "5511777777777@s.whatsapp.net", "IG1", "grupo", false))
	ie.emitMsg(text(t, "status@broadcast", peerJID, "IS1", "status", false))
	ie.emitMsg(text(t, peerJID, peerJID, "OK1", "dm", false))
	ie.wait("message", byID("false_"+peerID+"_OK1"))
	ie.barrier()
	if n := ie.count("message.any", nil); n != 1 {
		t.Fatalf("ignored chats were published: %d message.any", n)
	}
	// Ignored means not published, not forgotten.
	ie.stored("IG1")
	ie.stored("IS1")
}

func TestIngestReactionEditRevoke(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	ie.emitMsg(text(t, peerJID, peerJID, "ORIG", "antes", false))
	ie.wait("message", byID("false_"+peerID+"_ORIG"))

	ie.emitMsg(&pb.InboundMessage{
		Key: &pb.MessageKey{Chat: jid(peerJID), Id: "R1"}, Chat: jid(peerJID), Sender: jid(peerJID),
		Timestamp: time.Now().UnixMilli(), Reaction: "👍", ReactionTarget: &pb.MessageKey{Chat: jid(peerJID), Id: "MINE", FromMe: true},
	})
	r := ie.wait("message.reaction", byID("false_"+peerID+"_R1"))
	if re := r["reaction"].(map[string]any); re["text"] != "👍" || re["messageId"] != "true_"+peerID+"_MINE" || r["fromMe"] != false {
		t.Fatalf("reaction: %v", r)
	}

	ie.emitMsg(&pb.InboundMessage{
		Key: &pb.MessageKey{Chat: jid(peerJID), Id: "E1"}, Chat: jid(peerJID), Sender: jid(peerJID),
		IsEdit: true, Text: "depois", ProtocolTarget: &pb.MessageKey{Chat: jid(peerJID), Id: "ORIG"},
	})
	ed := ie.wait("message.edited", byID("false_"+peerID+"_E1"))
	if ed["body"] != "depois" || ed["editedMessageId"] != "ORIG" {
		t.Fatalf("edited: %v", ed)
	}
	var orig map[string]any
	_ = json.Unmarshal(ie.stored("ORIG").Body, &orig)
	if orig["body"] != "depois" {
		t.Fatalf("stored original not edited: %v", orig["body"])
	}

	ie.emitMsg(&pb.InboundMessage{
		Key: &pb.MessageKey{Chat: jid(peerJID), Id: "D1"}, Chat: jid(peerJID), Sender: jid(peerJID),
		IsDelete: true, ProtocolTarget: &pb.MessageKey{Chat: jid(peerJID), Id: "ORIG"},
	})
	rv := ie.wait("message.revoked", func(p map[string]any) bool { return p["revokedMessageId"] == "ORIG" })
	before, _ := rv["before"].(map[string]any)
	after, _ := rv["after"].(map[string]any)
	if before["id"] != "false_"+peerID+"_ORIG" || before["body"] != "depois" || after["id"] != "false_"+peerID+"_D1" {
		t.Fatalf("revoked: %v", rv)
	}
	if _, err := ie.App.Store.GetMessageByWAID(context.Background(), "default", "ORIG"); err == nil {
		t.Fatal("revoked message still stored")
	}
	ie.barrier()
	for _, ev := range []string{"message", "message.any"} {
		for _, id := range []string{"R1", "E1", "D1"} {
			if ie.count(ev, byID("false_"+peerID+"_"+id)) != 0 {
				t.Fatalf("%s went out as %s", id, ev)
			}
		}
	}
}

func TestIngestAcks(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	ie.emitMsg(text(t, peerJID, meJID, "A1", "x", true))
	ie.emitMsg(text(t, groupID, meJID, "A2", "y", true))
	ie.wait("message.any", byID("true_"+groupID+"_A2_"+meID))

	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Receipt{Receipt: &pb.ReceiptEvent{Chat: jid(peerJID), Sender: jid(peerJID), MessageIds: []string{"A1"}, Type: pb.ReceiptType_RECEIPT_TYPE_READ}}})
	a := ie.wait("message.ack", byID("true_"+peerID+"_A1"))
	if a["ack"] != float64(3) || a["ackName"] != "READ" || a["fromMe"] != true || a["from"] != peerID || a["to"] != nil || a["participant"] != nil {
		t.Fatalf("dm ack: %v", a)
	}
	if st := ie.stored("A1"); st.Ack != 3 {
		t.Fatalf("stored ack: %d", st.Ack)
	}

	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Receipt{Receipt: &pb.ReceiptEvent{Chat: jid(groupID), Sender: jid("5511777777777@s.whatsapp.net"), MessageIds: []string{"A2"}, Type: pb.ReceiptType_RECEIPT_TYPE_DELIVERED}}})
	g := ie.wait("message.ack.group", byID("true_"+groupID+"_A2_"+meID))
	if g["participant"] != otherID || g["ack"] != float64(2) {
		t.Fatalf("group ack: %v", g)
	}

	// read-self is dropped; an ack never goes back down.
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Receipt{Receipt: &pb.ReceiptEvent{Chat: jid(peerJID), MessageIds: []string{"A1"}, Type: pb.ReceiptType_RECEIPT_TYPE_READ_SELF}}})
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Receipt{Receipt: &pb.ReceiptEvent{Chat: jid(peerJID), Sender: jid(peerJID), MessageIds: []string{"A1"}, Type: pb.ReceiptType_RECEIPT_TYPE_DELIVERED}}})
	ie.barrier()
	if st := ie.stored("A1"); st.Ack != 3 {
		t.Fatalf("a late DEVICE lowered READ: %d", st.Ack)
	}

	// The server refusing a send is an ERROR ack.
	ie.emitMsg(text(t, peerJID, meJID, "A3", "z", true))
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_ServerAck{ServerAck: &pb.ServerAckEvent{Id: "A3", Class: "message", From: jid(peerJID), Error: "479"}}})
	e := ie.wait("message.ack", func(p map[string]any) bool { return p["id"] == "true_"+peerID+"_A3" && p["ack"] == float64(-1) })
	if e["ackName"] != "ERROR" {
		t.Fatalf("error ack: %v", e)
	}
	if st := ie.stored("A3"); st.Ack != -1 {
		t.Fatalf("stored error ack: %d", st.Ack)
	}
	// A plain server ack says nothing: A1 got its READ and the late DEVICE
	// (published, as GOWS does, though the store keeps READ), nothing more.
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_ServerAck{ServerAck: &pb.ServerAckEvent{Id: "A1", Class: "message", From: jid(peerJID)}}})
	ie.barrier()
	if n := ie.count("message.ack", byID("true_"+peerID+"_A1")); n != 2 {
		t.Fatalf("A1 acks: %d", n)
	}
}

func pollCreation(t *testing.T, chat, sender, id string, fromMe bool, secret []byte) *pb.InboundMessage {
	raw := &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{
		Name:    proto.String("Almoço?"),
		Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Sim")}, {OptionName: proto.String("Não")}},
	}}
	if secret != nil {
		raw.MessageContextInfo = &waE2E.MessageContextInfo{MessageSecret: secret}
	}
	return &pb.InboundMessage{Key: &pb.MessageKey{Chat: jid(chat), Id: id, FromMe: fromMe}, Chat: jid(chat), Sender: jid(sender),
		Timestamp: time.Now().UnixMilli(), RawMessage: rawMsg(t, raw)}
}

func vote(t *testing.T, id, pollID, pollRemote string, pollFromMe bool, choice, iv string) *pb.InboundMessage {
	return &pb.InboundMessage{
		Key: &pb.MessageKey{Chat: jid(peerJID), Id: id}, Chat: jid(peerJID), Sender: jid(peerJID), Timestamp: time.Now().UnixMilli(),
		RawMessage: rawMsg(t, &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
			PollCreationMessageKey: &waCommon.MessageKey{RemoteJID: proto.String(pollRemote), FromMe: proto.Bool(pollFromMe), ID: proto.String(pollID)},
			Vote:                   &waE2E.PollEncValue{EncPayload: []byte(choice), EncIV: []byte(iv)},
			SenderTimestampMS:      proto.Int64(1760000000123),
		}}),
	}
}

func TestIngestPollVotes(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	// Their poll, with its secret in the message.
	ie.emitMsg(pollCreation(t, peerJID, peerJID, "POLL1", false, make([]byte, 32)))
	ie.wait("message", byID("false_"+peerID+"_POLL1"))
	ie.emitMsg(vote(t, "V1", "POLL1", peerJID, false, "Não", "iv"))
	p := ie.wait("poll.vote", func(p map[string]any) bool { return p["vote"].(map[string]any)["id"] == "false_"+peerID+"_V1" })
	v, poll := p["vote"].(map[string]any), p["poll"].(map[string]any)
	if sel := v["selectedOptions"].([]any); len(sel) != 1 || sel[0] != "Não" || v["timestamp"] != float64(1760000000123) {
		t.Fatalf("vote: %v", v)
	}
	if poll["id"] != "false_"+peerID+"_POLL1" || poll["from"] != peerID || poll["to"] != meID {
		t.Fatalf("poll: %v", poll)
	}
	req := fakecore.Requests[*pb.AggregatePollVotesRequest](ie.Core)
	if len(req) != 1 || req[0].GetPollCreator().GetValue() != peerJID || len(req[0].GetOptions()) != 2 || req[0].GetVotes()[0].GetVoter().GetValue() != peerJID {
		t.Fatalf("tally request: %v", req)
	}

	// My poll: the secret is the one the send kept; the voter's key names me.
	ie.emitMsg(pollCreation(t, peerJID, meJID, "POLL2", true, nil))
	ie.wait("message.any", byID("true_"+peerID+"_POLL2"))
	if err := ie.App.Store.PutPollSecret(context.Background(), "default", "POLL2", make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	ie.emitMsg(vote(t, "V2", "POLL2", meJID, false, "Sim", "iv"))
	p = ie.wait("poll.vote", func(p map[string]any) bool { return p["vote"].(map[string]any)["id"] == "false_"+peerID+"_V2" })
	if poll := p["poll"].(map[string]any); poll["id"] != "true_"+peerID+"_POLL2" || poll["fromMe"] != true {
		t.Fatalf("my poll: %v", poll)
	}

	// An unknown poll and a vote that does not open both fail.
	ie.emitMsg(vote(t, "V3", "NOPE", peerJID, false, "Sim", "iv"))
	ie.wait("poll.vote.failed", func(p map[string]any) bool { return p["vote"].(map[string]any)["id"] == "false_"+peerID+"_V3" })
	ie.emitMsg(vote(t, "V4", "POLL1", peerJID, false, "Sim", fakecore.BadVoteIV))
	f := ie.wait("poll.vote.failed", func(p map[string]any) bool { return p["vote"].(map[string]any)["id"] == "false_"+peerID+"_V4" })
	if sel := f["vote"].(map[string]any)["selectedOptions"].([]any); len(sel) != 0 {
		t.Fatalf("a failed vote selects nothing: %v", sel)
	}
	ie.barrier()
	if n := ie.count("message.any", byID("false_"+peerID+"_V1")); n != 0 {
		t.Fatal("a vote went out as a message")
	}
}

func TestIngestPresence(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Presence{Presence: &pb.PresenceUpdate{Jid: jid(peerJID), Online: proto.Bool(true)}}})
	p := ie.wait("presence.update", byID(peerID))
	ps := p["presences"].([]any)[0].(map[string]any)
	if ps["participant"] != peerID || ps["lastKnownPresence"] != "online" || ps["lastSeen"] != nil {
		t.Fatalf("presence: %v", p)
	}
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Presence{Presence: &pb.PresenceUpdate{Jid: jid("5511777777777@s.whatsapp.net"), Chat: jid(groupID), ChatState: pb.ChatState_CHAT_STATE_COMPOSING}}})
	p = ie.wait("presence.update", byID(groupID))
	if ps := p["presences"].([]any)[0].(map[string]any); ps["participant"] != otherID || ps["lastKnownPresence"] != "typing" {
		t.Fatalf("chat state: %v", p)
	}
	ps2, err := ie.App.Store.ListPresences(context.Background(), "default", "")
	if err != nil || len(ps2) != 2 {
		t.Fatalf("stored presences: %+v %v", ps2, err)
	}
}

// groups is a fake GroupService answering metadata for one group.
type groups struct {
	pb.UnimplementedGroupServiceServer
}

func (groups) GetGroupMetadata(_ context.Context, r *pb.GroupRef) (*pb.GroupMetadataResponse, error) {
	return &pb.GroupMetadataResponse{Metadata: &pb.GroupMetadata{
		Id: r.GetGroup(), Subject: proto.String("Família"), Description: proto.String("desc"),
		Participants: []*pb.GroupParticipant{{Jid: jid(meJID), Type: pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_MEMBER}},
	}}, nil
}

func groupEvent(u *pb.GroupUpdate) *pb.EventEnvelope {
	u.Group, u.Timestamp = jid(groupID), 1760000000000
	return &pb.EventEnvelope{Event: &pb.EventEnvelope_Group{Group: u}}
}

func members(jids ...string) *pb.GroupParticipantsChange {
	c := &pb.GroupParticipantsChange{}
	for _, j := range jids {
		c.Participants = append(c.Participants, &pb.GroupNotificationParticipant{Jid: jid(j)})
	}
	return c
}

func TestIngestGroups(t *testing.T) {
	ie := startIngest(t, testenv.Options{Core: fakecore.Options{Groups: groups{}}}, nil)
	ie.emit(groupEvent(&pb.GroupUpdate{Action: &pb.GroupUpdate_Add{Add: members("5511999999999@s.whatsapp.net")}}))
	j := ie.wait("group.v2.join", nil)
	g := j["group"].(map[string]any)
	if g["id"] != groupID || g["subject"] != "Família" || g["description"] != "desc" || j["timestamp"] != float64(1760000000000) {
		t.Fatalf("join: %v", j)
	}
	ie.wait("group.join", nil)
	part := ie.wait("group.v2.participants", func(p map[string]any) bool { return p["type"] == "join" })
	if ps := part["participants"].([]any)[0].(map[string]any); ps["id"] != meID || ps["role"] != "participant" || ps["pn"] != meID {
		t.Fatalf("participants: %v", part)
	}
	if ch, err := ie.App.Store.GetChat(context.Background(), "default", groupID); err != nil || ch.Name != "Família" {
		t.Fatalf("group chat name: %+v %v", ch, err)
	}

	ie.emit(groupEvent(&pb.GroupUpdate{Action: &pb.GroupUpdate_Promote{Promote: members("5511777777777@s.whatsapp.net")}}))
	pr := ie.wait("group.v2.participants", func(p map[string]any) bool { return p["type"] == "promote" })
	if ps := pr["participants"].([]any)[0].(map[string]any); ps["role"] != "admin" {
		t.Fatalf("promote: %v", pr)
	}
	ie.emit(groupEvent(&pb.GroupUpdate{Action: &pb.GroupUpdate_Subject{Subject: &pb.GroupSubjectChange{Subject: "Família 2"}}}))
	up := ie.wait("group.v2.update", nil)
	if up["group"].(map[string]any)["subject"] != "Família 2" {
		t.Fatalf("update: %v", up)
	}
	ie.emit(groupEvent(&pb.GroupUpdate{Action: &pb.GroupUpdate_Remove{Remove: members("5511777777777@s.whatsapp.net")}}))
	ie.wait("group.v2.participants", func(p map[string]any) bool { return p["type"] == "leave" })
	ie.barrier()
	if ie.count("group.v2.leave", nil) != 0 {
		t.Fatal("someone else leaving is not my leave")
	}
	ie.emit(groupEvent(&pb.GroupUpdate{Action: &pb.GroupUpdate_Remove{Remove: members(meJID)}}))
	l := ie.wait("group.v2.leave", nil)
	if l["group"].(map[string]any)["id"] != groupID {
		t.Fatalf("leave: %v", l)
	}
	ie.wait("group.leave", nil)
	ie.emit(groupEvent(&pb.GroupUpdate{Action: &pb.GroupUpdate_Invite{Invite: &pb.GroupInviteJoin{Code: "abc"}}}))
	ie.wait("engine.event", func(p map[string]any) bool { return p["event"] == "group" })
}

func TestIngestCalls(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	call := func(c *pb.CallEvent) {
		c.From, c.Timestamp = jid(peerJID), 1760000000000
		ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Call{Call: c}})
	}
	call(&pb.CallEvent{CallId: "C1", Action: &pb.CallEvent_Offer{Offer: &pb.CallOffer{IsVideo: true}}})
	c := ie.wait("call.received", byID("C1"))
	if c["from"] != peerID || c["isVideo"] != true || c["isGroup"] != false || c["timestamp"] != float64(1760000000) {
		t.Fatalf("call.received: %v", c)
	}
	call(&pb.CallEvent{CallId: "C1", Action: &pb.CallEvent_Accept{Accept: &pb.CallAcceptance{}}})
	ie.wait("call.accepted", byID("C1"))
	call(&pb.CallEvent{CallId: "C2", Action: &pb.CallEvent_Reject{Reject: &pb.CallRejection{}}})
	ie.wait("call.rejected", byID("C2"))
	call(&pb.CallEvent{CallId: "C3", Action: &pb.CallEvent_Terminate{Terminate: &pb.CallTermination{Reason: proto.String("rejected_elsewhere")}}})
	ie.barrier()
	if ie.count("call.rejected", byID("C3")) != 0 {
		t.Fatal("a call handled elsewhere was published")
	}
}

func TestIngestLabels(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	label := func(l *pb.LabelUpdate) {
		ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Label{Label: l}})
	}
	label(&pb.LabelUpdate{LabelId: "5", FromFullSync: false, Action: &pb.LabelUpdate_Edit{Edit: &pb.LabelEdit{Name: proto.String("Leads"), Color: proto.Int32(1)}}})
	l := ie.wait("label.upsert", byID("5"))
	if l["name"] != "Leads" || l["color"] != float64(1) || l["colorHex"] != "#64c4ff" {
		t.Fatalf("label.upsert: %v", l)
	}
	label(&pb.LabelUpdate{LabelId: "5", FromFullSync: false, Action: &pb.LabelUpdate_Chat{Chat: &pb.LabelChatAssociation{Chat: jid(peerJID), Labeled: proto.Bool(true)}}})
	a := ie.wait("label.chat.added", nil)
	if a["labelId"] != "5" || a["chatId"] != peerID || a["label"].(map[string]any)["name"] != "Leads" {
		t.Fatalf("label.chat.added: %v", a)
	}
	// A full sync is stored without a webhook.
	label(&pb.LabelUpdate{LabelId: "6", FromFullSync: true, Action: &pb.LabelUpdate_Edit{Edit: &pb.LabelEdit{Name: proto.String("Sync"), Color: proto.Int32(2)}}})
	ie.barrier()
	if ie.count("label.upsert", byID("6")) != 0 {
		t.Fatal("a full-sync label was published")
	}
	if l, err := ie.App.Store.GetLabel(context.Background(), "default", "6"); err != nil || l.Name != "Sync" {
		t.Fatalf("full-sync label not stored: %+v %v", l, err)
	}
	label(&pb.LabelUpdate{LabelId: "5", FromFullSync: false, Action: &pb.LabelUpdate_Chat{Chat: &pb.LabelChatAssociation{Chat: jid(peerJID), Labeled: proto.Bool(false)}}})
	ie.wait("label.chat.deleted", func(p map[string]any) bool { return p["chatId"] == peerID })
	label(&pb.LabelUpdate{LabelId: "5", FromFullSync: false, Action: &pb.LabelUpdate_Edit{Edit: &pb.LabelEdit{Deleted: proto.Bool(true)}}})
	d := ie.wait("label.deleted", byID("5"))
	if d["name"] != "Leads" {
		t.Fatalf("label.deleted keeps the name it had: %v", d)
	}
}

func TestIngestAppState(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	app := func(u *pb.AppStateUpdate) {
		u.Chat, u.Timestamp = jid(peerJID), 1760000000000
		ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_AppState{AppState: u}})
	}
	app(&pb.AppStateUpdate{FromFullSync: false, Action: &pb.AppStateUpdate_Archive{Archive: &pb.ArchiveChange{Archived: proto.Bool(true)}}})
	a := ie.wait("chat.archive", byID(peerID))
	if a["archived"] != true || a["timestamp"] != float64(1760000000) {
		t.Fatalf("chat.archive: %v", a)
	}
	app(&pb.AppStateUpdate{FromFullSync: false, Action: &pb.AppStateUpdate_Pin{Pin: &pb.PinChange{Pinned: proto.Bool(true)}}})
	app(&pb.AppStateUpdate{FromFullSync: false, Action: &pb.AppStateUpdate_MarkRead{MarkRead: &pb.MarkReadChange{Read: proto.Bool(false)}}})
	app(&pb.AppStateUpdate{FromFullSync: true, Action: &pb.AppStateUpdate_Archive{Archive: &pb.ArchiveChange{Archived: proto.Bool(false)}}})
	ie.barrier()
	ch, err := ie.App.Store.GetChat(context.Background(), "default", peerID)
	if err != nil || ch.Archived || !ch.Pinned || ch.UnreadCount != 1 {
		t.Fatalf("chat flags: %+v %v", ch, err)
	}
	if n := ie.count("chat.archive", nil); n != 1 {
		t.Fatalf("a full-sync archive was published: %d", n)
	}
}

func TestIngestHistorySync(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	hs := &waHistorySync.HistorySync{
		SyncType:  waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum(),
		Pushnames: []*waHistorySync.Pushname{{ID: proto.String(peerJID), Pushname: proto.String("Bia")}},
		Conversations: []*waHistorySync.Conversation{{
			ID: proto.String(peerJID), Name: proto.String("Beatriz"), ConversationTimestamp: proto.Uint64(1700000100),
			Archived: proto.Bool(true), UnreadCount: proto.Uint32(2),
			Messages: []*waHistorySync.HistorySyncMsg{
				{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String(peerJID), ID: proto.String("H1")},
					MessageTimestamp: proto.Uint64(1700000000), Status: waWeb.WebMessageInfo_DELIVERY_ACK.Enum(),
					Message: &waE2E.Message{Conversation: proto.String("velha")}}},
				{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String(peerJID), ID: proto.String("H2"), FromMe: proto.Bool(true)},
					MessageTimestamp: proto.Uint64(1700000100), Status: waWeb.WebMessageInfo_READ.Enum(),
					Message: &waE2E.Message{Conversation: proto.String("minha")}}},
				{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String(peerJID), ID: proto.String("H3")},
					Message: &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("x")}}}},
			},
		}},
	}
	raw, err := proto.Marshal(hs)
	if err != nil {
		t.Fatal(err)
	}
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_HistorySync{HistorySync: &pb.HistorySyncEvent{Raw: raw}}})
	ie.barrier()
	for _, ev := range []string{"message", "message.any"} {
		if n := ie.count(ev, nil); n != 0 {
			t.Fatalf("history published %d %s", n, ev)
		}
	}
	h2 := ie.stored("H2")
	if h2.ID != "true_"+peerID+"_H2" || h2.Ack != 3 || h2.Timestamp != 1700000100000 {
		t.Fatalf("history message: %+v", h2)
	}
	if h1 := ie.stored("H1"); h1.Ack != 2 {
		t.Fatalf("history ack: %d", h1.Ack)
	}
	if _, err := ie.App.Store.GetMessageByWAID(context.Background(), "default", "H3"); err == nil {
		t.Fatal("a reaction was stored as a history message")
	}
	ch, err := ie.App.Store.GetChat(context.Background(), "default", peerID)
	if err != nil || ch.Name != "Beatriz" || !ch.Archived || ch.UnreadCount != 2 || ch.LastMessageID != h2.ID || ch.ConversationTS != 1700000100000 {
		t.Fatalf("history chat: %+v %v", ch, err)
	}
	if c, _ := ie.App.Store.GetContact(context.Background(), "default", peerID); c.PushName != "Bia" || c.Name != "Beatriz" {
		t.Fatalf("history contact: %+v", c)
	}
}

func TestIngestContactsAndEngineEvents(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	star := ie.NewHook(nil)
	if r := ie.Do("PUT", "/api/sessions/default", map[string]any{"config": map[string]any{"webhooks": []map[string]any{
		{"url": ie.hook.URL, "events": []string{"*", "engine.event"}}, {"url": star.URL, "events": []string{"*"}},
	}}}); r.Status != 200 {
		t.Fatalf("update session: %d %s", r.Status, r.Body)
	}
	ie.Eventually("WORKING again", 5*time.Second, func() bool { return ie.Status("default") == "WORKING" })

	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_PushName{PushName: &pb.PushNameUpdate{Jid: jid(peerJID), PushName: "Bia"}}})
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Contact{Contact: &pb.ContactUpdate{Jid: jid(peerJID), Action: &pb.ContactAction{FullName: proto.String("Beatriz Souza")}}}})
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_SelfPushName{SelfPushName: &pb.SelfPushNameUpdate{NewName: "Eu"}}})
	ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_CallLog{CallLog: &pb.CallLogUpdate{CallId: "L1", Result: pb.CallLogResult_CALL_LOG_RESULT_MISSED}}})
	p := ie.wait("engine.event", func(p map[string]any) bool { return p["event"] == "call_log" })
	if d := p["data"].(map[string]any); d["callId"] != "L1" || d["result"] != "CALL_LOG_RESULT_MISSED" {
		t.Fatalf("engine.event: %v", p)
	}
	c, err := ie.App.Store.GetContact(context.Background(), "default", peerID)
	if err != nil || c.PushName != "Bia" || c.Name != "Beatriz Souza" {
		t.Fatalf("contact: %+v %v", c, err)
	}
	if me, err := ie.App.Store.GetContact(context.Background(), "default", meID); err != nil || me.PushName != "Eu" {
		t.Fatalf("self push name: %+v %v", me, err)
	}
	// "*" does not include engine.event.
	ie.emitMsg(text(t, peerJID, peerJID, "LAST", "fim", false))
	ie.Eventually("star hook message", 5*time.Second, func() bool { return len(star.Events("message")) == 1 })
	if n := len(star.Events("engine.event")); n != 0 {
		t.Fatalf("a '*' webhook got %d engine.event", n)
	}
}
