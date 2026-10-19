package httpapi_test

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/testenv"
)

const peerPicture = "https://pps.whatsapp.net/v/peer.jpg"

// contacts is a fake ContactService: the peer has a picture, nobody else
// does, and presence subscriptions are recorded.
type contacts struct {
	pb.UnimplementedContactServiceServer
	mu         sync.Mutex
	pictures   int
	subscribed []string
}

func (c *contacts) GetProfilePicture(_ context.Context, r *pb.JidRequest) (*pb.ProfilePictureResponse, error) {
	c.mu.Lock()
	c.pictures++
	c.mu.Unlock()
	if r.GetJid().GetValue() == peerJID {
		return &pb.ProfilePictureResponse{Url: peerPicture}, nil
	}
	return nil, status.Error(codes.NotFound, "no picture")
}

func (c *contacts) SubscribePresence(_ context.Context, r *pb.SubscribePresenceRequest) (*pb.Empty, error) {
	c.mu.Lock()
	c.subscribed = append(c.subscribed, r.GetJid().GetValue())
	c.mu.Unlock()
	return &pb.Empty{}, nil
}

func (c *contacts) counts() (int, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pictures, append([]string(nil), c.subscribed...)
}

// seedChats stores a DM with the peer (theirs, then mine), a group with two
// senders, and an image, through the event stream.
func seedChats(t *testing.T, ie *ingestEnv) {
	base := time.Now().Add(-time.Hour).UnixMilli()
	at := func(m *pb.InboundMessage, offset int64) *pb.InboundMessage {
		m.Timestamp = base + offset
		return m
	}
	ie.emitMsg(at(text(t, peerJID, peerJID, "P1", "oi", false), 0))
	ie.emitMsg(at(text(t, peerJID, meJID, "P2", "olá", true), 1000))
	ie.emitMsg(at(text(t, groupID, "5511777777777@s.whatsapp.net", "G1", "a", false), 2000))
	ie.emitMsg(at(text(t, groupID, "5511666666666@s.whatsapp.net", "G2", "b", false), 3000))
	ie.emitMsg(at(&pb.InboundMessage{
		Key: &pb.MessageKey{Chat: jid(peerJID), Id: "P3"}, Chat: jid(peerJID), Sender: jid(peerJID),
		Media:      &pb.MediaDescriptor{MimeType: "image/jpeg"},
		RawMessage: rawMsg(t, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg")}}),
	}, 4000))
	ie.wait("message", byID("false_"+peerID+"_P3"))
}

func ids(t *testing.T, r testenv.Response) []string {
	t.Helper()
	var list []map[string]any
	r.JSON(t, &list)
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, m["id"].(string))
	}
	return out
}

func same(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestChatsList(t *testing.T) {
	cs := &contacts{}
	ie := startIngest(t, testenv.Options{Core: fakecore.Options{Contacts: cs}}, nil)
	seedChats(t, ie)

	r := ie.Do("GET", "/api/default/chats", nil)
	if r.Status != 200 || !same(ids(t, r), groupID, peerID) {
		t.Fatalf("chats by id: %d %s", r.Status, r.Body)
	}
	var list []map[string]any
	r.JSON(t, &list)
	if list[1]["name"] != "Bia" || list[0]["name"] != nil {
		t.Fatalf("chat names: %v", list)
	}
	if got := ids(t, ie.Do("GET", "/api/default/chats?sortBy=conversationTimestamp&sortOrder=desc&limit=1", nil)); !same(got, peerID) {
		t.Fatalf("newest first, limit 1: %v", got)
	}
	if r := ie.Do("GET", "/api/default/chats?sortBy=bogus", nil); r.Status != 400 {
		t.Fatalf("bad sortBy: %d", r.Status)
	}
	if r := ie.Do("GET", "/api/nope/chats", nil); r.Status != 422 || r.Map(t)["error"] != `Session "nope" does not exist` {
		t.Fatalf("unknown session: %d %s", r.Status, r.Body)
	}

	ov := ie.Do("GET", "/api/default/chats/overview", nil)
	var sums []map[string]any
	ov.JSON(t, &sums)
	if ov.Status != 200 || len(sums) != 2 || sums[0]["id"] != peerID || sums[0]["picture"] != peerPicture || sums[1]["picture"] != nil {
		t.Fatalf("overview: %d %s", ov.Status, ov.Body)
	}
	last := sums[0]["lastMessage"].(map[string]any)
	if last["id"] != "false_"+peerID+"_P3" || last["media"].(map[string]any)["url"] != nil {
		t.Fatalf("overview last message, never with a media url: %v", last)
	}
	if got := ids(t, ie.Do("GET", "/api/default/chats/overview?ids="+url.QueryEscape(groupID), nil)); !same(got, groupID) {
		t.Fatalf("overview ids: %v", got)
	}
	post := ie.Do("POST", "/api/default/chats/overview", map[string]any{"pagination": map[string]any{"limit": 1, "offset": 1}, "filter": map[string]any{}})
	if post.Status != 201 || !same(ids(t, post), groupID) {
		t.Fatalf("POST overview: %d %s", post.Status, post.Body)
	}
	before, _ := cs.counts()

	pic := ie.Do("GET", "/api/default/chats/"+peerID+"/picture", nil)
	if pic.Status != 200 || pic.Map(t)["url"] != peerPicture {
		t.Fatalf("picture: %d %s", pic.Status, pic.Body)
	}
	if n, _ := cs.counts(); n != before {
		t.Fatalf("picture not served from the cache: %d calls, %d before", n, before)
	}
	ie.Do("GET", "/api/default/chats/"+peerID+"/picture?refresh=true", nil)
	if n, _ := cs.counts(); n != before+1 {
		t.Fatalf("refresh did not ask again: %d", n)
	}
	if r := ie.Do("GET", "/api/default/chats/"+groupID+"/picture", nil); r.Status != 200 || r.Map(t)["url"] != nil {
		t.Fatalf("no picture: %d %s", r.Status, r.Body)
	}
}

func TestChatMessages(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	seedChats(t, ie)
	path := "/api/default/chats/" + peerID + "/messages"

	if got := ids(t, ie.Do("GET", path, nil)); !same(got, "false_"+peerID+"_P3", "true_"+peerID+"_P2", "false_"+peerID+"_P1") {
		t.Fatalf("newest first: %v", got)
	}
	if got := ids(t, ie.Do("GET", path+"?sortOrder=asc&limit=2&offset=1", nil)); !same(got, "true_"+peerID+"_P2", "false_"+peerID+"_P3") {
		t.Fatalf("asc paged: %v", got)
	}
	if got := ids(t, ie.Do("GET", path+"?filter.fromMe=true", nil)); !same(got, "true_"+peerID+"_P2") {
		t.Fatalf("fromMe: %v", got)
	}
	if got := ids(t, ie.Do("GET", path+"?filter.ack=SERVER", nil)); !same(got, "true_"+peerID+"_P2") {
		t.Fatalf("ack: %v", got)
	}
	p2 := ie.stored("P2")
	gte := (p2.Timestamp / 1000) + 1 // seconds, after P2
	if got := ids(t, ie.Do("GET", path+"?filter.timestamp.gte="+strconv.FormatInt(gte, 10), nil)); !same(got, "false_"+peerID+"_P3") {
		t.Fatalf("timestamp gte: %v", got)
	}
	if got := ids(t, ie.Do("GET", path+"?filter.timestamp.lte="+strconv.FormatInt(p2.Timestamp/1000, 10), nil)); len(got) == 0 || got[0] != "true_"+peerID+"_P2" {
		t.Fatalf("timestamp lte: %v", got)
	}
	for _, bad := range []string{"?filter.ack=NOPE", "?limit=x", "?filter.fromMe=maybe", "?sortOrder=up"} {
		if r := ie.Do("GET", path+bad, nil); r.Status != 400 {
			t.Fatalf("%s: %d", bad, r.Status)
		}
	}
	// Media URL follows the read's downloadMedia.
	var msgs []map[string]any
	ie.Do("GET", path+"?limit=1", nil).JSON(t, &msgs)
	if msgs[0]["media"].(map[string]any)["url"] != ie.URL+"/api/files/default/P3.jpeg" {
		t.Fatalf("media url: %v", msgs[0]["media"])
	}
	ie.Do("GET", path+"?limit=1&downloadMedia=false", nil).JSON(t, &msgs)
	if msgs[0]["media"].(map[string]any)["url"] != nil {
		t.Fatalf("downloadMedia=false: %v", msgs[0]["media"])
	}
	ie.Do("GET", path+"?limit=1&downloadMediaMimetypes=audio", nil).JSON(t, &msgs)
	if msgs[0]["media"].(map[string]any)["url"] != nil {
		t.Fatalf("mimetype filter: %v", msgs[0]["media"])
	}
	if got := ids(t, ie.Do("GET", "/api/default/chats/all/messages?limit=100", nil)); len(got) != 5 {
		t.Fatalf("all chats: %v", got)
	}

	one := ie.Do("GET", path+"/true_"+peerID+"_P2", nil)
	if one.Status != 200 || one.Map(t)["body"] != "olá" {
		t.Fatalf("one message: %d %s", one.Status, one.Body)
	}
	if r := ie.Do("GET", path+"/P1", nil); r.Status != 200 || r.Map(t)["id"] != "false_"+peerID+"_P1" {
		t.Fatalf("by bare id: %d %s", r.Status, r.Body)
	}
	if r := ie.Do("GET", path+"/NOPE", nil); r.Status != 404 || r.Map(t)["message"] != "Message not found" {
		t.Fatalf("missing message: %d %s", r.Status, r.Body)
	}

	legacy := ie.Do("GET", "/api/messages?session=default&limit=1&chatId="+url.QueryEscape("5511888888888"), nil)
	if legacy.Status != 200 || !same(ids(t, legacy), "false_"+peerID+"_P3") {
		t.Fatalf("legacy: %d %s", legacy.Status, legacy.Body)
	}
	if r := ie.Do("GET", "/api/messages?chatId=1", nil); r.Status != 400 || r.Map(t)["message"] != "Session name is required" {
		t.Fatalf("legacy without session: %d %s", r.Status, r.Body)
	}
	if r := ie.Do("GET", "/api/messages?session=default", nil); r.Status != 400 {
		t.Fatalf("legacy without chatId: %d %s", r.Status, r.Body)
	}
}

func TestChatWrites(t *testing.T) {
	ie := startIngest(t, testenv.Options{}, nil)
	seedChats(t, ie)
	ctx := context.Background()

	// Read: one receipt per group sender, then the chat on my devices.
	r := ie.Do("POST", "/api/default/chats/"+groupID+"/messages/read", nil)
	var read struct{ IDs []string }
	r.JSON(t, &read)
	if r.Status != 201 || len(read.IDs) != 2 {
		t.Fatalf("read: %d %s", r.Status, r.Body)
	}
	reqs := fakecore.Requests[*pb.MarkReadRequest](ie.Core)
	var receipts, chatReads int
	for _, q := range reqs {
		if len(q.GetMessageIds()) > 0 {
			receipts++
			if q.GetSender() == nil || q.GetChat().GetValue() != groupID {
				t.Fatalf("group receipt without its sender: %v", q)
			}
		} else {
			chatReads++
		}
	}
	if receipts != 2 || chatReads != 1 {
		t.Fatalf("read requests: %d receipts, %d chat reads", receipts, chatReads)
	}
	if st := ie.stored("G1"); st.Ack != 3 {
		t.Fatalf("read message ack: %d", st.Ack)
	}
	r = ie.Do("POST", "/api/default/chats/"+groupID+"/messages/read", nil)
	r.JSON(t, &read)
	if len(read.IDs) != 0 {
		t.Fatalf("read twice: %v", read.IDs)
	}

	// Archive, unarchive, unread.
	if r := ie.Do("POST", "/api/default/chats/"+peerID+"/archive", nil); r.Status != 201 || r.Map(t)["success"] != true {
		t.Fatalf("archive: %d %s", r.Status, r.Body)
	}
	if ch, _ := ie.App.Store.GetChat(ctx, "default", peerID); !ch.Archived {
		t.Fatal("archive not stored")
	}
	ie.Do("POST", "/api/default/chats/"+peerID+"/unarchive", nil)
	arch := fakecore.Requests[*pb.ArchiveChatRequest](ie.Core)
	if len(arch) != 2 || !arch[0].GetArchived() || arch[1].GetArchived() || arch[1].GetChat().GetValue() != peerJID {
		t.Fatalf("archive requests: %v", arch)
	}
	if ch, _ := ie.App.Store.GetChat(ctx, "default", peerID); ch.Archived {
		t.Fatal("unarchive not stored")
	}
	if r := ie.Do("POST", "/api/default/chats/"+peerID+"/unread", nil); r.Status != 201 {
		t.Fatalf("unread: %d %s", r.Status, r.Body)
	}
	if ch, _ := ie.App.Store.GetChat(ctx, "default", peerID); ch.UnreadCount == 0 {
		t.Fatal("unread not stored")
	}

	// Edit and delete one message.
	if r := ie.Do("PUT", "/api/default/chats/"+peerID+"/messages/true_"+peerID+"_P2", map[string]string{}); r.Status != 400 {
		t.Fatalf("edit without text: %d", r.Status)
	}
	ed := ie.Do("PUT", "/api/default/chats/"+peerID+"/messages/true_"+peerID+"_P2", map[string]string{"text": "editado"})
	if ed.Status != 200 {
		t.Fatalf("edit: %d %s", ed.Status, ed.Body)
	}
	edits := fakecore.Requests[*pb.EditMessageRequest](ie.Core)
	if len(edits) != 1 || edits[0].GetNewText() != "editado" || edits[0].GetTarget().GetId() != "P2" || !edits[0].GetTarget().GetFromMe() {
		t.Fatalf("edit request: %v", edits)
	}
	if id, _ := ed.Map(t)["id"].(string); len(id) < len("true_"+peerID+"_") || id[:len("true_"+peerID+"_")] != "true_"+peerID+"_" {
		t.Fatalf("edit answer id: %s", ed.Body)
	}
	del := ie.Do("DELETE", "/api/default/chats/"+peerID+"/messages/P2", nil)
	if del.Status != 200 {
		t.Fatalf("delete message: %d %s", del.Status, del.Body)
	}
	dels := fakecore.Requests[*pb.DeleteMessageRequest](ie.Core)
	if len(dels) != 1 || !dels[0].GetForEveryone() || dels[0].GetTarget().GetId() != "P2" || !dels[0].GetTarget().GetFromMe() {
		t.Fatalf("delete request: %v", dels)
	}
	if _, err := ie.App.Store.GetMessage(ctx, "default", "true_"+peerID+"_P2"); err == nil {
		t.Fatal("deleted message still stored")
	}

	// Delete the chat.
	if r := ie.Do("DELETE", "/api/default/chats/"+peerID, nil); r.Status != 200 {
		t.Fatalf("delete chat: %d %s", r.Status, r.Body)
	}
	if dc := fakecore.Requests[*pb.DeleteChatRequest](ie.Core); len(dc) != 1 || dc[0].GetChat().GetValue() != peerJID {
		t.Fatalf("delete chat request: %v", dc)
	}
	ie.Eventually("chat gone", 2*time.Second, func() bool {
		_, err := ie.App.Store.GetChat(ctx, "default", peerID)
		return errors.Is(err, store.ErrNotFound)
	})

	// No core RPC: WAHA's 501.
	if r := ie.Do("POST", "/api/default/chats/"+groupID+"/messages/false_"+groupID+"_G1_"+otherID+"/pin", map[string]int{"duration": 86400}); r.Status != 501 {
		t.Fatalf("pin: %d", r.Status)
	}
	if r := ie.Do("DELETE", "/api/default/chats/"+groupID+"/messages", nil); r.Status != 501 {
		t.Fatalf("clear messages: %d", r.Status)
	}
	// Writes need the account connected.
	if r := ie.Do("POST", "/api/nope/chats/"+peerID+"/archive", nil); r.Status != 422 {
		t.Fatalf("write on an unknown session: %d", r.Status)
	}
}
