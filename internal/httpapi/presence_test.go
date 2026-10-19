package httpapi_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/testenv"
)

func TestPresenceRoutes(t *testing.T) {
	cs := &contacts{}
	ie := startIngest(t, testenv.Options{Core: fakecore.Options{Contacts: cs}}, nil)
	presence := func(u *pb.PresenceUpdate) { ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Presence{Presence: u}}) }
	presence(&pb.PresenceUpdate{Jid: jid(peerJID), Online: proto.Bool(false), LastSeen: 1760000000000})
	presence(&pb.PresenceUpdate{Jid: jid("5511777777777@s.whatsapp.net"), Chat: jid(groupID), ChatState: pb.ChatState_CHAT_STATE_COMPOSING})
	presence(&pb.PresenceUpdate{Jid: jid("5511777777777@s.whatsapp.net"), Chat: jid(groupID), ChatState: pb.ChatState_CHAT_STATE_PAUSED})
	ie.barrier()

	var all []map[string]any
	r := ie.Do("GET", "/api/default/presence", nil)
	r.JSON(t, &all)
	if r.Status != 200 || len(all) != 2 {
		t.Fatalf("all presences: %d %s", r.Status, r.Body)
	}
	byChat := map[string][]any{}
	for _, c := range all {
		byChat[c["id"].(string)] = c["presences"].([]any)
	}
	if g := byChat[groupID]; len(g) != 1 || g[0].(map[string]any)["lastKnownPresence"] != "paused" {
		t.Fatalf("group presence keeps the latest per participant: %v", g)
	}
	if p := byChat[peerID]; len(p) != 1 || p[0].(map[string]any)["lastSeen"] != float64(1760000000) {
		t.Fatalf("peer presence: %v", p)
	}

	one := ie.Do("GET", "/api/default/presence/5511888888888", nil)
	if one.Status != 200 || one.Map(t)["id"] != peerID {
		t.Fatalf("one presence: %d %s", one.Status, one.Body)
	}
	if r := ie.Do("POST", "/api/default/presence/"+groupID+"/subscribe", nil); r.Status != 201 {
		t.Fatalf("subscribe: %d %s", r.Status, r.Body)
	}
	if _, subs := cs.counts(); !same(subs, peerJID, groupID) {
		t.Fatalf("subscriptions: %v", subs)
	}
	empty := ie.Do("GET", "/api/default/presence/5511000000000@c.us", nil).Map(t)
	if ps := empty["presences"].([]any); len(ps) != 0 {
		t.Fatalf("no presence yet: %v", empty)
	}

	if r := ie.Do("POST", "/api/default/presence", map[string]string{"presence": "online"}); r.Status != 201 {
		t.Fatalf("online: %d %s", r.Status, r.Body)
	}
	if r := ie.Do("POST", "/api/default/presence", map[string]string{"presence": "typing", "chatId": peerID}); r.Status != 201 {
		t.Fatalf("typing: %d %s", r.Status, r.Body)
	}
	sent := fakecore.Requests[*pb.SendPresenceRequest](ie.Core)
	if len(sent) != 2 || sent[0].GetState() != pb.PresenceState_PRESENCE_STATE_AVAILABLE || sent[0].GetChat() != nil ||
		sent[1].GetState() != pb.PresenceState_PRESENCE_STATE_COMPOSING || sent[1].GetChat().GetValue() != peerJID {
		t.Fatalf("presence requests: %v", sent)
	}
	for _, bad := range []map[string]string{
		{"presence": "typing"},
		{"presence": "offline", "chatId": peerID},
		{"presence": "dancing"},
	} {
		r := ie.Do("POST", "/api/default/presence", bad)
		if r.Status != 400 {
			t.Fatalf("%v: %d %s", bad, r.Status, r.Body)
		}
	}
	if d := ie.Do("POST", "/api/default/presence", map[string]string{"presence": "typing"}).Map(t)["detail"]; d != "'typing' presence requires 'chatId' field." {
		t.Fatalf("WAHA's detail: %v", d)
	}
}
