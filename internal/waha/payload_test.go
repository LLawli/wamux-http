package waha

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

func TestReceiptAck(t *testing.T) {
	cases := map[pb.ReceiptType]int{
		pb.ReceiptType_RECEIPT_TYPE_UNSPECIFIED:  AckDevice,
		pb.ReceiptType_RECEIPT_TYPE_DELIVERED:    AckDevice,
		pb.ReceiptType_RECEIPT_TYPE_INACTIVE:     AckDevice,
		pb.ReceiptType_RECEIPT_TYPE_READ:         AckRead,
		pb.ReceiptType_RECEIPT_TYPE_PLAYED:       AckPlayed,
		pb.ReceiptType_RECEIPT_TYPE_SERVER_ERROR: AckError,
	}
	for typ, want := range cases {
		if got, ok := ReceiptAck(typ); !ok || got != want {
			t.Errorf("%v: %d %v, want %d", typ, got, ok, want)
		}
	}
	for _, dropped := range []pb.ReceiptType{pb.ReceiptType_RECEIPT_TYPE_READ_SELF, pb.ReceiptType_RECEIPT_TYPE_SENDER, pb.ReceiptType_RECEIPT_TYPE_RETRY, pb.ReceiptType_RECEIPT_TYPE_PLAYED_SELF} {
		if _, ok := ReceiptAck(dropped); ok {
			t.Errorf("%v must be dropped", dropped)
		}
	}
}

func TestNewAck(t *testing.T) {
	dm := NewAck(MessageID{FromMe: true, Chat: "1@c.us", ID: "A"}, "1@c.us", AckRead, json.RawMessage(`{}`))
	if dm.ID != "true_1@c.us_A" || dm.From != "1@c.us" || dm.To != nil || dm.Participant != nil || dm.AckName != "READ" {
		t.Fatalf("dm ack: %+v", dm)
	}
	g := NewAck(MessageID{FromMe: true, Chat: "9@g.us", ID: "B", Participant: "me@c.us"}, "2@c.us", AckDevice, nil)
	if g.ID != "true_9@g.us_B_me@c.us" || g.To == nil || *g.Participant != "2@c.us" {
		t.Fatalf("group ack: %+v", g)
	}
	if AckEvent("9@g.us") != "message.ack.group" || AckEvent("1@c.us") != "message.ack" {
		t.Fatal("ack event names")
	}
}

func TestReactionTargetID(t *testing.T) {
	k := &pb.MessageKey{Chat: jid("9@g.us"), Id: "T", Participant: jid("2:4@s.whatsapp.net")}
	if got := ReactionTargetID(k, testMe).String(); got != "false_9@g.us_T_2@c.us" {
		t.Fatalf("other's group message: %s", got)
	}
	k.FromMe = true
	if got := ReactionTargetID(k, testMe).String(); got != "true_9@g.us_T_5511999999999@c.us" {
		t.Fatalf("my group message: %s", got)
	}
	if got := ReactionTargetID(&pb.MessageKey{Chat: jid("1@s.whatsapp.net"), Id: "T"}, testMe).String(); got != "false_1@c.us_T" {
		t.Fatalf("dm: %s", got)
	}
}

func TestPollKeyAndDestination(t *testing.T) {
	// A peer votes on my DM poll: their key names me as the chat.
	id := PollKey("1@c.us", false, "5511999999999@s.whatsapp.net", false, "P", "", testMe)
	if id.String() != "true_1@c.us_P" {
		t.Fatalf("my DM poll: %s", id)
	}
	if d := Destination(id, testMe); d.From != "5511999999999@c.us" || d.To != "1@c.us" || !d.FromMe {
		t.Fatalf("destination of my DM poll: %+v", d)
	}
	// In a group the key names me as participant.
	id = PollKey("9@g.us", false, "9@g.us", false, "P", "5511999999999@s.whatsapp.net", testMe)
	if id.String() != "true_9@g.us_P_5511999999999@c.us" {
		t.Fatalf("my group poll: %s", id)
	}
	if d := Destination(id, testMe); d.From != "5511999999999@c.us" || d.To != "9@g.us" || d.Participant == nil {
		t.Fatalf("destination of my group poll: %+v", d)
	}
	// Someone else's poll stays as the key says.
	id = PollKey("1@c.us", false, "1@s.whatsapp.net", false, "P", "", testMe)
	if id.String() != "false_1@c.us_P" {
		t.Fatalf("their poll: %s", id)
	}
	if d := Destination(id, testMe); d.From != "1@c.us" || d.To != "5511999999999@c.us" {
		t.Fatalf("destination of their poll: %+v", d)
	}
	// My own vote: the key is already mine.
	if id := PollKey("1@c.us", true, "1@s.whatsapp.net", true, "P", "", testMe); id.String() != "true_1@c.us_P" {
		t.Fatalf("my vote: %s", id)
	}
}

func TestPresenceOf(t *testing.T) {
	on := &pb.PresenceUpdate{Jid: jid("1@s.whatsapp.net"), Online: proto.Bool(false), LastSeen: 1760000000999}
	chat, d, ok := PresenceOf(on)
	if !ok || chat != "1@c.us" || d.LastKnownPresence != "offline" || d.LastSeen == nil || *d.LastSeen != 1760000000 {
		t.Fatalf("real presence: %v %+v", chat, d)
	}
	typing := &pb.PresenceUpdate{Jid: jid("2@s.whatsapp.net"), ChatState: pb.ChatState_CHAT_STATE_RECORDING, Chat: jid("9@g.us")}
	chat, d, ok = PresenceOf(typing)
	if !ok || chat != "9@g.us" || d.Participant != "2@c.us" || d.LastKnownPresence != "recording" || d.LastSeen != nil {
		t.Fatalf("chat state: %v %+v", chat, d)
	}
	if _, _, ok := PresenceOf(&pb.PresenceUpdate{Jid: jid("9@g.us"), Online: proto.Bool(true)}); ok {
		t.Fatal("a group is never online")
	}
	if _, _, ok := PresenceOf(&pb.PresenceUpdate{Jid: jid("1@s.whatsapp.net")}); ok {
		t.Fatal("no presence and no chat state says nothing")
	}
}

func TestGroupMappers(t *testing.T) {
	u := &pb.GroupUpdate{Group: jid("9@g.us"), Action: &pb.GroupUpdate_Promote{Promote: &pb.GroupParticipantsChange{
		Participants: []*pb.GroupNotificationParticipant{{Jid: jid("1@lid"), PhoneNumber: jid("5511@s.whatsapp.net")}, {Jid: jid("2@s.whatsapp.net")}},
	}}}
	kind, members, ok := ParticipantsChange(u)
	if !ok || kind != "promote" || len(members) != 2 {
		t.Fatalf("change: %v %v", kind, ok)
	}
	ps := NotificationParticipants(kind, members)
	if ps[0].ID != "1@lid" || *ps[0].PN != "5511@c.us" || ps[0].Role != "admin" || *ps[1].PN != "2@c.us" {
		t.Fatalf("participants: %+v", ps)
	}
	if ParticipantRole("leave") != "left" || ParticipantRole("demote") != "participant" {
		t.Fatal("roles")
	}
	me := Me{ID: "5511@c.us", LID: "1@lid"}
	if !ParticipantIsMe(members[0], me) || ParticipantIsMe(members[1], me) {
		t.Fatal("ParticipantIsMe")
	}
	if _, ok := GroupUpdateOf(u); ok {
		t.Fatal("a promote is not a settings update")
	}
	u.Action = &pb.GroupUpdate_Announcement{Announcement: &pb.Empty{}}
	g, ok := GroupUpdateOf(u)
	if !ok || g.MembersCanSendMessages == nil || *g.MembersCanSendMessages || g.Subject != nil {
		t.Fatalf("announcement: %+v", g)
	}
	u.Action = &pb.GroupUpdate_Subject{Subject: &pb.GroupSubjectChange{Subject: "Novo"}}
	if g, ok := GroupUpdateOf(u); !ok || *g.Subject != "Novo" || g.ID != "9@g.us" {
		t.Fatalf("subject: %+v", g)
	}
	u.Action = &pb.GroupUpdate_Revoke{Revoke: &pb.Empty{}}
	if _, ok := GroupUpdateOf(u); ok {
		t.Fatal("an invite revoke is not a WAHA update")
	}
	info := GroupInfoFromMetadata(&pb.GroupMetadata{Id: jid("9@g.us"), Subject: proto.String("S"), IsAnnouncement: true,
		Participants: []*pb.GroupParticipant{{Jid: jid("1@s.whatsapp.net"), Type: pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_SUPERADMIN}}})
	if info.Subject != "S" || info.MembersCanSendMessages || len(info.Participants) != 1 || info.Participants[0].Role != "superadmin" {
		t.Fatalf("metadata: %+v", info)
	}
}

func TestCallEventOf(t *testing.T) {
	offer := &pb.CallEvent{CallId: "C", From: jid("1@s.whatsapp.net"), Timestamp: 1760000000500, Action: &pb.CallEvent_Offer{Offer: &pb.CallOffer{IsVideo: true}}}
	ev, d, ok := CallEventOf(offer)
	if !ok || ev != "call.received" || d.ID != "C" || d.From != "1@c.us" || !d.IsVideo || d.IsGroup || d.Timestamp != 1760000000.5 {
		t.Fatalf("offer: %v %+v", ev, d)
	}
	group := &pb.CallEvent{CallId: "G", Action: &pb.CallEvent_Offer{Offer: &pb.CallOffer{GroupJid: jid("9@g.us")}}}
	if _, d, _ := CallEventOf(group); !d.IsGroup || d.From != "9@g.us" {
		t.Fatalf("group offer: %+v", d)
	}
	if ev, _, _ := CallEventOf(&pb.CallEvent{Action: &pb.CallEvent_Accept{Accept: &pb.CallAcceptance{}}}); ev != "call.accepted" {
		t.Fatal(ev)
	}
	if ev, _, _ := CallEventOf(&pb.CallEvent{Action: &pb.CallEvent_Terminate{Terminate: &pb.CallTermination{Reason: proto.String("timeout")}}}); ev != "call.rejected" {
		t.Fatal(ev)
	}
	if _, _, ok := CallEventOf(&pb.CallEvent{Action: &pb.CallEvent_Terminate{Terminate: &pb.CallTermination{Reason: proto.String("accepted_elsewhere")}}}); ok {
		t.Fatal("handled elsewhere is dropped")
	}
	if _, _, ok := CallEventOf(&pb.CallEvent{Action: &pb.CallEvent_Transport{Transport: &pb.CallTransport{}}}); ok {
		t.Fatal("transport is not a WAHA event")
	}
}

func TestLabelsAndEngineEvent(t *testing.T) {
	if LabelColorHex(0) != "#ff9485" || LabelColorHex(19) != "#9368cf" || LabelColorHex(20) != "#000000" || LabelColorHex(-1) != "#000000" {
		t.Fatal("palette")
	}
	ev, ok := NewEngineEvent(&pb.EventEnvelope{Event: &pb.EventEnvelope_OfflineSyncCompleted{OfflineSyncCompleted: &pb.OfflineSyncCompleted{Count: 4}}})
	if !ok || ev.Event != "offline_sync_completed" || string(ev.Data) != `{"count":4}` {
		t.Fatalf("engine event: %+v %s", ev, ev.Data)
	}
	if _, ok := NewEngineEvent(&pb.EventEnvelope{}); ok {
		t.Fatal("an empty envelope is no event")
	}
}
