package httpapi_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/testenv"
)

func TestLabelRoutes(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	label := func(l *pb.LabelUpdate) { ie.emit(&pb.EventEnvelope{Event: &pb.EventEnvelope_Label{Label: l}}) }
	label(&pb.LabelUpdate{LabelId: "1", Action: &pb.LabelUpdate_Edit{Edit: &pb.LabelEdit{Name: proto.String("Novo"), Color: proto.Int32(0)}}})
	label(&pb.LabelUpdate{LabelId: "2", Action: &pb.LabelUpdate_Edit{Edit: &pb.LabelEdit{Name: proto.String("Pago"), Color: proto.Int32(5)}}})
	label(&pb.LabelUpdate{LabelId: "3", Action: &pb.LabelUpdate_Edit{Edit: &pb.LabelEdit{Name: proto.String("Velho"), Color: proto.Int32(1)}}})
	label(&pb.LabelUpdate{LabelId: "3", Action: &pb.LabelUpdate_Edit{Edit: &pb.LabelEdit{Deleted: proto.Bool(true)}}})
	label(&pb.LabelUpdate{LabelId: "2", Action: &pb.LabelUpdate_Chat{Chat: &pb.LabelChatAssociation{Chat: jid(peerJID), Labeled: proto.Bool(true)}}})
	label(&pb.LabelUpdate{LabelId: "2", Action: &pb.LabelUpdate_Chat{Chat: &pb.LabelChatAssociation{Chat: jid(groupID), Labeled: proto.Bool(true)}}})
	ie.barrier()

	var labels []map[string]any
	r := ie.Do("GET", "/api/default/labels", nil)
	r.JSON(t, &labels)
	if r.Status != 200 || len(labels) != 2 || labels[1]["name"] != "Pago" || labels[1]["colorHex"] != "#55ccb3" {
		t.Fatalf("labels (the deleted one left out): %d %s", r.Status, r.Body)
	}
	r = ie.Do("GET", "/api/default/labels/chats/"+peerID, nil)
	r.JSON(t, &labels)
	if r.Status != 200 || len(labels) != 1 || labels[0]["id"] != "2" {
		t.Fatalf("chat labels: %d %s", r.Status, r.Body)
	}
	if got := ids(t, ie.Do("GET", "/api/default/labels/2/chats", nil)); !same(got, groupID, peerID) {
		t.Fatalf("label chats: %v", got)
	}
	if got := ids(t, ie.Do("GET", "/api/default/labels/9/chats", nil)); len(got) != 0 {
		t.Fatalf("unknown label chats: %v", got)
	}
	if r := ie.Do("GET", "/api/nope/labels", nil); r.Status != 422 {
		t.Fatalf("unknown session: %d", r.Status)
	}
	// Label writes have no core RPC.
	for _, w := range []struct{ method, path string }{
		{"POST", "/api/default/labels"}, {"PUT", "/api/default/labels/1"}, {"DELETE", "/api/default/labels/1"}, {"PUT", "/api/default/labels/chats/" + peerID},
	} {
		if r := ie.Do(w.method, w.path, map[string]any{"name": "x"}); r.Status != 501 {
			t.Fatalf("%s %s: %d", w.method, w.path, r.Status)
		}
	}
}
