package waha

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

func lidGroup() *pb.GroupMetadata {
	return &pb.GroupMetadata{
		Id:                 jid("120363000000000001@g.us"),
		Subject:            proto.String("Family"),
		SubjectTime:        proto.Int64(1_700_000_000_000),
		SubjectOwner:       jid("5511999999999@s.whatsapp.net"),
		Description:        proto.String("Weekend plans"),
		DescriptionId:      proto.String("DESC1"),
		Creator:            jid("5511999999999:3@s.whatsapp.net"),
		CreationTime:       proto.Int64(1_600_000_000_000),
		AddressingMode:     pb.GroupAddressingMode_GROUP_ADDRESSING_MODE_LID,
		IsLocked:           true,
		IsAnnouncement:     false,
		MembershipApproval: true,
		MemberAddMode:      pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ALL_MEMBER_ADD,
		Ephemeral:          &pb.GroupEphemeralSettings{ExpirationSeconds: proto.Uint32(86400)},
		Participants: []*pb.GroupParticipant{
			{Jid: jid("111111111111111@lid"), PhoneNumber: jid("5511999999999@s.whatsapp.net"), Type: pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_SUPERADMIN},
			{Jid: jid("222222222222222@lid"), Type: pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_ADMIN,
				Details: &pb.GroupParticipantDetails{DisplayName: proto.String("Bia")}},
			{Jid: jid("5511888888888@s.whatsapp.net"), Lid: jid("333333333333333@lid"), Type: pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_MEMBER},
		},
	}
}

func TestGroupInfoFromPB(t *testing.T) {
	g := GroupInfoFromPB(lidGroup())
	if g.JID != "120363000000000001@g.us" || g.Name != "Family" || g.Topic != "Weekend plans" || g.TopicID != "DESC1" {
		t.Fatalf("names: %+v", g)
	}
	if g.OwnerJID != "5511999999999@c.us" || g.NameSetBy != "5511999999999@c.us" {
		t.Fatalf("ids must be in WAHA form with no device: owner %q, name set by %q", g.OwnerJID, g.NameSetBy)
	}
	if !g.NameSetAt.Equal(time.UnixMilli(1_700_000_000_000)) || !g.GroupCreated.Equal(time.UnixMilli(1_600_000_000_000)) || !g.TopicSetAt.IsZero() {
		t.Fatalf("times: %v %v %v", g.NameSetAt, g.GroupCreated, g.TopicSetAt)
	}
	if !g.IsLocked || g.IsAnnounce || !g.IsJoinApprovalRequired || g.MemberAddMode != "all_member_add" || g.AddressingMode != "lid" {
		t.Fatalf("settings: %+v", g)
	}
	if !g.IsEphemeral || g.DisappearingTimer != 86400 {
		t.Fatalf("ephemeral: %v %d", g.IsEphemeral, g.DisappearingTimer)
	}
	if g.ParticipantCount != 3 || len(g.Participants) != 3 {
		t.Fatalf("participants: %d %d", g.ParticipantCount, len(g.Participants))
	}
	super, admin, plain := g.Participants[0], g.Participants[1], g.Participants[2]
	if super.JID != "111111111111111@lid" || super.LID != "111111111111111@lid" || super.PhoneNumber != "5511999999999@c.us" || !super.IsAdmin || !super.IsSuperAdmin {
		t.Fatalf("superadmin: %+v", super)
	}
	if !admin.IsAdmin || admin.IsSuperAdmin || admin.PhoneNumber != "" || admin.DisplayName != "Bia" {
		t.Fatalf("admin: %+v", admin)
	}
	if plain.JID != "5511888888888@c.us" || plain.PhoneNumber != "5511888888888@c.us" || plain.LID != "333333333333333@lid" || plain.IsAdmin {
		t.Fatalf("member: %+v", plain)
	}

	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s.whatsapp.net") {
		t.Fatalf("a whatsmeow id leaked: %s", raw)
	}
	for _, key := range []string{`"JID":`, `"IsLocked":true`, `"Participants":[`, `"TopicSetAt":"0001-01-01T00:00:00Z"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("GOWS shape lacks %s: %s", key, raw)
		}
	}
}

func TestGroupInfoFromPBSize(t *testing.T) {
	m := lidGroup()
	m.Size = proto.Uint32(40)
	if g := GroupInfoFromPB(m); g.ParticipantCount != 40 {
		t.Fatalf("size should win over the roster length: %d", g.ParticipantCount)
	}
	if g := GroupInfoFromPB(nil); g.JID != "" || g.Participants != nil {
		t.Fatalf("nil metadata: %+v", g)
	}
}

func TestGroupParticipantsV2(t *testing.T) {
	got := GroupParticipantsV2(lidGroup())
	want := []GroupParticipant{
		{ID: "111111111111111@lid", PN: "5511999999999@c.us", Role: RoleSuperAdmin},
		{ID: "222222222222222@lid", Role: RoleAdmin},
		{ID: "5511888888888@c.us", PN: "5511888888888@c.us", Role: RoleParticipant},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("participant %d: got %+v want %+v", i, got[i], want[i])
		}
	}
	if got := GroupParticipantsV2(&pb.GroupMetadata{}); got == nil || len(got) != 0 {
		t.Fatalf("no roster must be an empty list, not null: %#v", got)
	}
}

func TestGroupMemberFromChange(t *testing.T) {
	ok := GroupMemberFromChange(&pb.ParticipantChange{Jid: jid("5511777777777@s.whatsapp.net"), Status: "200"})
	if ok.JID != "5511777777777@c.us" || ok.Error != 0 || ok.AddRequest != nil {
		t.Fatalf("ok: %+v", ok)
	}
	refused := GroupMemberFromChange(&pb.ParticipantChange{Jid: jid("444@lid"), PhoneNumber: jid("5511666666666@s.whatsapp.net"),
		Error: "403", AddRequest: &pb.AddRequestInfo{Code: "CODE", Expiration: 1_760_000_000}})
	if refused.Error != 403 || refused.LID != "444@lid" || refused.PhoneNumber != "5511666666666@c.us" {
		t.Fatalf("refused: %+v", refused)
	}
	if refused.AddRequest == nil || refused.AddRequest.Code != "CODE" || !refused.AddRequest.Expiration.Equal(time.Unix(1_760_000_000, 0)) {
		t.Fatalf("add request: %+v", refused.AddRequest)
	}
	if odd := GroupMemberFromChange(&pb.ParticipantChange{Jid: jid("1@lid"), Error: "not-a-number"}); odd.Error != 0 {
		t.Fatalf("a non-numeric error has no code: %+v", odd)
	}
}

func TestJoinRequests(t *testing.T) {
	jr := JoinRequestFromPB(&pb.MembershipRequest{Jid: jid("5511555555555@s.whatsapp.net"), RequestTime: proto.Int64(1_700_000_123_456)})
	if jr.RequesterID != "5511555555555@c.us" || jr.Timestamp != 1_700_000_123 || jr.AddedByID != nil {
		t.Fatalf("request: %+v", jr)
	}
	raw, _ := json.Marshal(jr)
	if !strings.Contains(string(raw), `"addedById":null`) || !strings.Contains(string(raw), `"requestMethod":null`) {
		t.Fatalf("GOWS sends the unknown fields as null: %s", raw)
	}
	res := JoinRequestResultFromPB(&pb.ParticipantChange{Jid: jid("1@lid"), Error: "404"})
	if res.Success || res.Error == nil || *res.Error != 404 {
		t.Fatalf("failed result: %+v", res)
	}
	if res := JoinRequestResultFromPB(&pb.ParticipantChange{Jid: jid("1@lid")}); !res.Success || res.Error != nil {
		t.Fatalf("ok result: %+v", res)
	}
}

func TestInviteCodes(t *testing.T) {
	for in, want := range map[string]string{
		"https://chat.whatsapp.com/AbC123?s=sw&p=a": "AbC123",
		"https://chat.whatsapp.com/AbC123#x":        "AbC123",
		"AbC123":                                    "AbC123",
	} {
		if got := ParseInviteCode(in); got != want {
			t.Fatalf("ParseInviteCode(%q) = %q", in, got)
		}
	}
	if got := GroupInviteLink("AbC"); got != "https://chat.whatsapp.com/AbC" {
		t.Fatalf("bare code: %q", got)
	}
	if got := GroupInviteLink("https://chat.whatsapp.com/AbC"); got != "https://chat.whatsapp.com/AbC" {
		t.Fatalf("full link: %q", got)
	}
}
