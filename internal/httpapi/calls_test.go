package httpapi_test

import (
	"testing"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
)

func TestRejectCall(t *testing.T) {
	calls := &fakecore.Calls{}
	e := directoryEnv(t, fakecore.Options{Calls: calls})
	if r := e.Do("POST", "/api/default/calls/reject", map[string]string{"from": "5511111111111@c.us", "id": "CALL1"}); r.Status != 201 || len(r.Body) != 0 {
		t.Fatalf("reject: %d %q", r.Status, r.Body)
	}
	got := fakecore.Logged[*pb.RejectCallRequest](&calls.RequestLog)
	if len(got) != 1 || got[0].GetCallId() != "CALL1" || got[0].GetPeer().GetValue() != "5511111111111@s.whatsapp.net" || got[0].GetCallCreator().GetValue() != "5511111111111@s.whatsapp.net" {
		t.Fatalf("core got %v", got)
	}
	if r := e.Do("POST", "/api/default/calls/reject", map[string]string{"from": "5511111111111@c.us"}); r.Status != 400 || r.Map(t)["message"].([]any)[0] != "id should not be empty" {
		t.Fatalf("no id: %d %s", r.Status, r.Body)
	}
	if len(fakecore.Logged[*pb.RejectCallRequest](&calls.RequestLog)) != 1 {
		t.Fatal("an invalid reject reached the core")
	}
}
