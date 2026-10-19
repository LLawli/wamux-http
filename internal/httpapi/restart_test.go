package httpapi_test

import (
	"strings"
	"testing"
	"time"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/testenv"
)

func inbound(id, text string) *pb.EventEnvelope {
	peer := &pb.Jid{Value: "5511888888888@s.whatsapp.net"}
	return &pb.EventEnvelope{Event: &pb.EventEnvelope_Message{Message: &pb.InboundMessage{
		Key: &pb.MessageKey{Chat: peer, Id: id}, Chat: peer, Sender: peer,
		Timestamp: time.Now().UnixMilli(), Text: text,
	}}}
}

// A message the core receives while no edge process follows the session (a
// deploy, a lease handover) is still in the core's replay ring: the next
// owner must process it, and must not process again what was done before.
func TestEventsWhileTheEdgeRestartsAreNotLost(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	hook := e.NewHook(nil)
	uuid := e.WorkingSession("default", meJID, map[string]any{
		"webhooks": []map[string]any{{"url": hook.URL, "events": []string{"message"}}},
	})
	e.Core.Emit(uuid, inbound("BEFORE", "before the restart"))
	e.Eventually("first message", 5*time.Second, func() bool { return len(hook.Events("message")) == 1 })

	e.Restart()
	// The account stays connected in the core; the message arrives before
	// the new process has subscribed.
	e.Core.Emit(uuid, inbound("DURING", "while nobody listened"))

	e.Eventually("the missed message", 10*time.Second, func() bool {
		r := e.Do("GET", "/api/default/chats/5511888888888@c.us/messages?limit=10", nil)
		return r.Status == 200 && strings.Contains(string(r.Body), "DURING")
	})
	e.Eventually("its webhook", 5*time.Second, func() bool { return len(hook.Events("message")) >= 2 })
	time.Sleep(300 * time.Millisecond)
	ids := map[string]int{}
	for _, ev := range hook.Events("message") {
		ids[ev["payload"].(map[string]any)["id"].(string)]++
	}
	for id, n := range ids {
		if n != 1 {
			t.Fatalf("message %s was delivered %d times across the restart: %v", id, n, ids)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("messages delivered: %v", ids)
	}
}
