package httpapi_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/testenv"
)

func init() {
	// GOWS answers the group list as an array (PaginatorInMemory over the
	// engine's list); WAHA's OpenAPI types it as an object.
	testenv.Divergences["GET /api/{session}/groups 200"] = "GOWS answers an array"
}

const (
	family = "120363000000000001@g.us"
	work   = "120363000000000002@g.us"
)

func pbJid(v string) *pb.Jid { return &pb.Jid{Value: v} }

func seedGroups() *fakecore.Groups {
	return fakecore.NewGroups(
		&pb.GroupMetadata{
			Id: pbJid(family), Subject: proto.String("Family"), Description: proto.String("Weekend"),
			AddressingMode: pb.GroupAddressingMode_GROUP_ADDRESSING_MODE_LID,
			IsLocked:       true, MembershipApproval: true,
			MemberAddMode: pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ALL_MEMBER_ADD,
			Participants: []*pb.GroupParticipant{
				{Jid: pbJid("111@lid"), PhoneNumber: pbJid("5511999999999@s.whatsapp.net"), Type: pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_SUPERADMIN},
				{Jid: pbJid("222@lid"), Type: pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_MEMBER},
			},
		},
		&pb.GroupMetadata{
			Id: pbJid(work), Subject: proto.String("Acme"), IsAnnouncement: true,
			AddressingMode: pb.GroupAddressingMode_GROUP_ADDRESSING_MODE_PN,
			MemberAddMode:  pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ADMIN_ADD,
			Participants: []*pb.GroupParticipant{
				{Jid: pbJid("5511999999999@s.whatsapp.net"), Type: pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_ADMIN},
			},
		},
	)
}

func TestGroupsRead(t *testing.T) {
	groups := seedGroups()
	e := directoryEnv(t, fakecore.Options{Groups: groups})

	r := e.Do("GET", "/api/default/groups", nil)
	var list []map[string]any
	if r.JSON(t, &list); r.Status != 200 || len(list) != 2 || list[0]["JID"] != family || list[1]["Name"] != "Acme" {
		t.Fatalf("list: %d %s", r.Status, r.Body)
	}
	if strings.Contains(string(r.Body), "s.whatsapp.net") {
		t.Fatalf("ids must be @c.us: %s", r.Body)
	}
	r = e.Do("GET", "/api/default/groups?sortBy=subject&limit=1&exclude=participants", nil)
	list = nil
	if r.JSON(t, &list); len(list) != 1 || list[0]["Name"] != "Acme" || list[0]["Participants"] != nil || list[0]["ParticipantCount"] != float64(1) {
		t.Fatalf("sorted by subject, one, without participants: %s", r.Body)
	}
	r = e.Do("GET", "/api/default/groups?sortBy=id&sortOrder=desc", nil)
	list = nil
	if r.JSON(t, &list); list[0]["JID"] != work {
		t.Fatalf("by id desc: %s", r.Body)
	}
	if r := e.Do("GET", "/api/default/groups?sortBy=size", nil); r.Status != 400 {
		t.Fatalf("bad sortBy: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/groups?exclude=owner", nil); r.Status != 400 {
		t.Fatalf("bad exclude: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/groups/count", nil); r.Status != 200 || string(r.Body) != `{"count":2}`+"\n" {
		t.Fatalf("count: %d %s", r.Status, r.Body)
	}

	g := e.Do("GET", "/api/default/groups/"+family, nil).Map(t)
	if g["Name"] != "Family" || g["Topic"] != "Weekend" || g["IsLocked"] != true || g["AddressingMode"] != "lid" {
		t.Fatalf("group: %v", g)
	}
	ref := fakecore.Logged[*pb.GroupRef](&groups.RequestLog)
	if last := ref[len(ref)-1]; last.GetGroup().GetValue() != family || last.GetAccount().GetUuid() == "" {
		t.Fatalf("core got %v", last)
	}
	if r := e.Do("GET", "/api/default/groups/120363000000000001", nil); r.Status != 200 {
		t.Fatalf("a bare group id gets @g.us: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/groups/999@g.us", nil); r.Status != 404 {
		t.Fatalf("unknown group: %d %s", r.Status, r.Body)
	}

	if r := e.Do("GET", "/api/default/groups/"+family+"/participants/v2", nil); r.Status != 200 ||
		string(r.Body) != `[{"id":"111@lid","pn":"5511999999999@c.us","role":"superadmin"},{"id":"222@lid","role":"participant"}]`+"\n" {
		t.Fatalf("v2: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/groups/"+work+"/participants/v2", nil); string(r.Body) != `[{"id":"5511999999999@c.us","pn":"5511999999999@c.us","role":"admin"}]`+"\n" {
		t.Fatalf("v2 phone group: %s", r.Body)
	}
	var members []map[string]any
	r = e.Do("GET", "/api/default/groups/"+family+"/participants", nil)
	if r.JSON(t, &members); len(members) != 2 || members[0]["IsSuperAdmin"] != true || members[0]["PhoneNumber"] != "5511999999999@c.us" {
		t.Fatalf("participants: %s", r.Body)
	}

	settings := map[string]string{
		"/settings/security/info-admin-only":     `{"adminsOnly":true}`,
		"/settings/security/messages-admin-only": `{"adminsOnly":false}`,
		"/settings/security/member-add-mode":     `{"membersCanAddNewMember":true}`,
		"/settings/security/membership-approval": `{"newMembersApprovalRequired":true}`,
	}
	for path, want := range settings {
		if r := e.Do("GET", "/api/default/groups/"+family+path, nil); r.Status != 200 || string(r.Body) != want+"\n" {
			t.Fatalf("%s: %d %s", path, r.Status, r.Body)
		}
	}
	if r := e.Do("GET", "/api/default/groups/"+work+"/settings/security/messages-admin-only", nil); string(r.Body) != `{"adminsOnly":true}`+"\n" {
		t.Fatalf("announce group: %s", r.Body)
	}

	// The invite link goes out as text, as NestJS sends a bare string.
	r = e.Do("GET", "/api/default/groups/"+family+"/invite-code", nil)
	first := string(r.Body)
	if r.Status != 200 || !strings.HasPrefix(first, "https://chat.whatsapp.com/INVITE") || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("invite: %d %q %s", r.Status, r.Body, r.Header.Get("Content-Type"))
	}
	if r := e.Do("POST", "/api/default/groups/"+family+"/invite-code/revoke", nil); r.Status != 200 || string(r.Body) == first {
		t.Fatalf("revoke must give a new link: %d %s", r.Status, r.Body)
	}
}

func TestGroupsWrite(t *testing.T) {
	groups := seedGroups()
	e := directoryEnv(t, fakecore.Options{Groups: groups})

	r := e.Do("POST", "/api/default/groups", map[string]any{"name": "New", "participants": []map[string]string{{"id": "5511888888888@c.us"}, {"id": "5511777777777"}}})
	if r.Status != 201 {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	created := r.Map(t)
	id, _ := created["JID"].(string)
	if !strings.HasSuffix(id, "@g.us") || created["Name"] != "New" || len(created["Participants"].([]any)) != 2 {
		t.Fatalf("created: %v", created)
	}
	cr := fakecore.Logged[*pb.CreateGroupRequest](&groups.RequestLog)
	if len(cr) != 1 || cr[0].GetSubject() != "New" || cr[0].GetParticipants()[1].GetValue() != "5511777777777@s.whatsapp.net" {
		t.Fatalf("core got %v", cr)
	}
	if r := e.Do("POST", "/api/default/groups", map[string]any{"participants": []any{}}); r.Status != 400 {
		t.Fatalf("no name: %d %s", r.Status, r.Body)
	}

	if r := e.Do("PUT", "/api/default/groups/"+family+"/subject", map[string]string{"subject": "Clan"}); r.Status != 200 || len(r.Body) != 0 {
		t.Fatalf("subject: %d %s", r.Status, r.Body)
	}
	if r := e.Do("PUT", "/api/default/groups/"+family+"/description", map[string]string{"description": "Sundays"}); r.Status != 200 {
		t.Fatalf("description: %d %s", r.Status, r.Body)
	}
	if g := groups.Group(family); g.GetSubject() != "Clan" || g.GetDescription() != "Sundays" {
		t.Fatalf("group after edits: %v", g)
	}
	if r := e.Do("PUT", "/api/default/groups/"+family+"/subject", map[string]any{"subject": 1}); r.Status != 400 {
		t.Fatalf("subject not a string: %d %s", r.Status, r.Body)
	}

	if r := e.Do("PUT", "/api/default/groups/"+family+"/settings/security/info-admin-only", map[string]bool{"adminsOnly": false}); r.Status != 200 {
		t.Fatalf("locked: %d %s", r.Status, r.Body)
	}
	if r := e.Do("PUT", "/api/default/groups/"+family+"/settings/security/messages-admin-only", map[string]bool{"adminsOnly": true}); r.Status != 200 {
		t.Fatalf("announce: %d %s", r.Status, r.Body)
	}
	if g := groups.Group(family); g.GetIsLocked() || !g.GetIsAnnouncement() {
		t.Fatalf("toggles: locked %v announce %v", g.GetIsLocked(), g.GetIsAnnouncement())
	}
	if r := e.Do("GET", "/api/default/groups/"+family+"/settings/security/info-admin-only", nil); string(r.Body) != `{"adminsOnly":false}`+"\n" {
		t.Fatalf("read back: %s", r.Body)
	}

	// Participant changes answer per participant, as the core relays them.
	groups.Refuse("5511666666666@s.whatsapp.net", "403")
	r = e.Do("POST", "/api/default/groups/"+family+"/participants/add", map[string]any{"participants": []map[string]string{{"id": "5511555555555@c.us"}, {"id": "5511666666666@c.us"}}})
	var res []map[string]any
	if r.JSON(t, &res); r.Status != 200 || len(res) != 2 {
		t.Fatalf("add: %d %s", r.Status, r.Body)
	}
	if res[0]["JID"] != "5511555555555@c.us" || res[0]["Error"] != float64(0) || res[1]["Error"] != float64(403) || res[1]["AddRequest"].(map[string]any)["Code"] != "ADDREQ403" {
		t.Fatalf("add verdicts: %s", r.Body)
	}
	if r := e.Do("POST", "/api/default/groups/"+family+"/admin/promote", map[string]any{"participants": []map[string]string{{"id": "222@lid"}}}); r.Status != 200 {
		t.Fatalf("promote: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/groups/"+family+"/participants/v2", nil); !strings.Contains(string(r.Body), `{"id":"222@lid","role":"admin"}`) || !strings.Contains(string(r.Body), `"5511555555555@c.us"`) {
		t.Fatalf("after add and promote: %s", r.Body)
	}
	if r := e.Do("POST", "/api/default/groups/"+family+"/admin/demote", map[string]any{"participants": []map[string]string{{"id": "222@lid"}}}); r.Status != 200 {
		t.Fatalf("demote: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/default/groups/"+family+"/participants/remove", map[string]any{"participants": []map[string]string{{"id": "222@lid"}}}); r.Status != 200 {
		t.Fatalf("remove: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/groups/"+family+"/participants/v2", nil); strings.Contains(string(r.Body), "222@lid") {
		t.Fatalf("removed participant still listed: %s", r.Body)
	}
	pr := fakecore.Logged[*pb.ParticipantsRequest](&groups.RequestLog)
	if len(pr) != 4 || pr[0].GetParticipants()[0].GetValue() != "5511555555555@s.whatsapp.net" || pr[1].GetParticipants()[0].GetValue() != "222@lid" {
		t.Fatalf("core got %v", pr)
	}
	if r := e.Do("POST", "/api/default/groups/"+family+"/participants/add", map[string]any{}); r.Status != 400 {
		t.Fatalf("no participants: %d %s", r.Status, r.Body)
	}

	// Join requests.
	groups.Request(family, &pb.MembershipRequest{Jid: pbJid("5511444444444@s.whatsapp.net"), RequestTime: proto.Int64(1_700_000_000_500)})
	groups.Request(family, &pb.MembershipRequest{Jid: pbJid("5511333333333@s.whatsapp.net")})
	r = e.Do("GET", "/api/default/groups/"+family+"/participants/join-requests", nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), `{"requesterId":"5511444444444@c.us","addedById":null,"parentGroupId":null,"requestMethod":null,"timestamp":1700000000}`) {
		t.Fatalf("join requests: %d %s", r.Status, r.Body)
	}
	r = e.Do("POST", "/api/default/groups/"+family+"/participants/join-requests/approve", map[string]any{"participants": []map[string]string{{"id": "5511444444444@c.us"}, {"id": "5511000000000@c.us"}}})
	if r.Status != 200 || string(r.Body) != `[{"requesterId":"5511444444444@c.us","success":true},{"requesterId":"5511000000000@c.us","success":false,"error":404}]`+"\n" {
		t.Fatalf("approve: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/default/groups/"+family+"/participants/join-requests/reject", map[string]any{"participants": []map[string]string{{"id": "5511333333333@c.us"}}}); r.Status != 200 || !strings.Contains(string(r.Body), `"success":true`) {
		t.Fatalf("reject: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/groups/"+family+"/participants/join-requests", nil); string(r.Body) != "[]\n" {
		t.Fatalf("requests settled: %s", r.Body)
	}

	// Pictures: base64 only for now.
	img := []byte("\xff\xd8\xff fake jpeg")
	if r := e.Do("PUT", "/api/default/groups/"+family+"/picture", map[string]any{"file": map[string]string{"mimetype": "image/jpeg", "data": base64.StdEncoding.EncodeToString(img)}}); r.Status != 200 || string(r.Body) != `{"success":true}`+"\n" {
		t.Fatalf("set picture: %d %s", r.Status, r.Body)
	}
	if string(groups.Photo(family)) != string(img) {
		t.Fatalf("core got photo %q", groups.Photo(family))
	}
	if r := e.Do("PUT", "/api/default/groups/"+family+"/picture", map[string]any{"file": map[string]string{"mimetype": "image/jpeg", "url": "https://example.com/a.jpg"}}); r.Status != 400 || r.Map(t)["message"] != "file.url is not supported yet" {
		t.Fatalf("url picture: %d %s", r.Status, r.Body)
	}
	if r := e.Do("PUT", "/api/default/groups/"+family+"/picture", map[string]any{"file": map[string]string{"data": "%%%"}}); r.Status != 400 {
		t.Fatalf("bad base64: %d %s", r.Status, r.Body)
	}
	if r := e.Do("DELETE", "/api/default/groups/"+family+"/picture", nil); r.Status != 200 || groups.Photo(family) != nil {
		t.Fatalf("delete picture: %d %s", r.Status, r.Body)
	}
	if photos := fakecore.Logged[*pb.SetGroupPhotoRequest](&groups.RequestLog); len(photos) != 2 || len(photos[1].GetImage()) != 0 {
		t.Fatalf("photo requests: %v", photos)
	}

	// Refresh has nothing to do; leave drops the group.
	if r := e.Do("POST", "/api/default/groups/refresh", nil); r.Status != 200 || string(r.Body) != `{"success":true}`+"\n" {
		t.Fatalf("refresh: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/default/groups/"+work+"/leave", nil); r.Status != 200 || len(r.Body) != 0 || groups.Group(work) != nil {
		t.Fatalf("leave: %d %s", r.Status, r.Body)
	}
}

func TestGroupsPictureAndInvites(t *testing.T) {
	groups := seedGroups()
	contacts := fakecore.NewContacts("Ana")
	contacts.Pictures[family] = "https://pps.whatsapp.net/family.jpg"
	e := directoryEnv(t, fakecore.Options{Groups: groups, Contacts: contacts})

	if r := e.Do("GET", "/api/default/groups/"+family+"/picture?refresh=true", nil); r.Status != 200 || r.Map(t)["url"] != "https://pps.whatsapp.net/family.jpg" {
		t.Fatalf("picture: %d %s", r.Status, r.Body)
	}
	withDivergence("GET /api/{session}/groups/{id}/picture 200", "WAHA answers url null for a group with no picture", func() {
		if r := e.Do("GET", "/api/default/groups/"+work+"/picture", nil); r.Status != 200 || string(r.Body) != `{"url":null}`+"\n" {
			t.Fatalf("no picture: %d %s", r.Status, r.Body)
		}
	})

	preview := &pb.GroupMetadata{Id: pbJid("120363000000000099@g.us"), Subject: proto.String("Open club"), Size: proto.Uint32(57)}
	groups.Invite("CLUB123", preview)
	r := e.Do("GET", "/api/default/groups/join-info?code=https://chat.whatsapp.com/CLUB123?s=sw", nil)
	if g := r.Map(t); r.Status != 200 || g["Name"] != "Open club" || g["ParticipantCount"] != float64(57) {
		t.Fatalf("join-info: %d %s", r.Status, r.Body)
	}
	if pi := fakecore.Logged[*pb.PreviewInviteRequest](&groups.RequestLog); len(pi) != 1 || pi[0].GetCode() != "CLUB123" {
		t.Fatalf("core got %v", pi)
	}
	if r := e.Do("POST", "/api/default/groups/join", map[string]string{"code": "https://chat.whatsapp.com/CLUB123"}); r.Status != 200 || string(r.Body) != `{"id":"120363000000000099@g.us"}`+"\n" {
		t.Fatalf("join: %d %s", r.Status, r.Body)
	}
	if groups.Group("120363000000000099@g.us") == nil {
		t.Fatal("joined group missing")
	}
	if r := e.Do("POST", "/api/default/groups/join", map[string]string{"code": "NOPE"}); r.Status != 404 {
		t.Fatalf("unknown invite: %d %s", r.Status, r.Body)
	}
}

func TestGroupsNeedWorkingSession(t *testing.T) {
	e := testenv.Start(t, testenv.Options{Core: fakecore.Options{Groups: seedGroups()}})
	if r := e.Do("GET", "/api/missing/groups", nil); r.Status != 422 || r.Map(t)["session"] != "missing" {
		t.Fatalf("unknown session: %d %s", r.Status, r.Body)
	}
}
