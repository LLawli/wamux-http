package httpapi_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
)

const news = "120363111111111111@newsletter"

func seedNewsletters() *fakecore.Newsletters {
	return &fakecore.Newsletters{
		Channels: []*pb.Newsletter{
			{Jid: pbJid(news), Name: "News", Description: "Daily", SubscriberCount: 1200, PictureUrl: "/v/t61/news.jpg",
				Verification: pb.NewsletterVerification_NEWSLETTER_VERIFICATION_VERIFIED, Role: pb.NewsletterRole_NEWSLETTER_ROLE_SUBSCRIBER},
			{Jid: pbJid("120363222222222222@newsletter"), Name: "Mine", Role: pb.NewsletterRole_NEWSLETTER_ROLE_OWNER,
				Verification: pb.NewsletterVerification_NEWSLETTER_VERIFICATION_UNVERIFIED},
		},
		Messages: map[string][]*pb.NewsletterMessage{
			news: {
				{Message: &pb.InboundMessage{Key: &pb.MessageKey{Chat: pbJid(news), Id: "M2"}, Chat: pbJid(news), Timestamp: 1_700_000_100_000, Text: "Second"},
					ServerId: 2, Reactions: []*pb.NewsletterReactionCount{{Code: "👍", Count: 5}}},
				{Message: &pb.InboundMessage{Key: &pb.MessageKey{Chat: pbJid(news), Id: "M1"}, Chat: pbJid(news), Timestamp: 1_700_000_000_000, Caption: "First",
					Media: &pb.MediaDescriptor{MimeType: "image/jpeg"}}, ServerId: 1},
			},
		},
	}
}

func TestChannels(t *testing.T) {
	nl := seedNewsletters()
	e := directoryEnv(t, fakecore.Options{Newsletters: nl})

	r := e.Do("GET", "/api/default/channels", nil)
	var list []map[string]any
	if r.JSON(t, &list); r.Status != 200 || len(list) != 2 {
		t.Fatalf("list: %d %s", r.Status, r.Body)
	}
	first := list[0]
	if first["id"] != news || first["role"] != "SUBSCRIBER" || first["verified"] != true || first["subscribersCount"] != float64(1200) || first["picture"] != "https://mmg.whatsapp.net/v/t61/news.jpg" {
		t.Fatalf("channel: %v", first)
	}
	r = e.Do("GET", "/api/default/channels?role=OWNER", nil)
	list = nil
	if r.JSON(t, &list); len(list) != 1 || list[0]["name"] != "Mine" {
		t.Fatalf("role filter: %s", r.Body)
	}

	if r := e.Do("GET", "/api/default/channels/"+news, nil); r.Status != 200 || r.Map(t)["name"] != "News" {
		t.Fatalf("one: %d %s", r.Status, r.Body)
	}
	if got := fakecore.Logged[*pb.JidRequest](&nl.RequestLog); len(got) != 1 || got[0].GetJid().GetValue() != news {
		t.Fatalf("core got %v", got)
	}
	if r := e.Do("GET", "/api/default/channels/0029Va4K0PZ5a245NkngBA2M", nil); r.Status != 501 || !strings.Contains(r.Map(t)["message"].(string), "invite code") {
		t.Fatalf("invite code: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/channels/999@newsletter", nil); r.Status != 404 {
		t.Fatalf("unknown channel: %d %s", r.Status, r.Body)
	}

	r = e.Do("GET", "/api/default/channels/"+news+"/messages/preview?limit=1&downloadMedia=false", nil)
	var rows []map[string]any
	if r.JSON(t, &rows); r.Status != 200 || len(rows) != 1 {
		t.Fatalf("preview: %d %s", r.Status, r.Body)
	}
	msg := rows[0]["message"].(map[string]any)
	if msg["id"] != "false_"+news+"_M2" || msg["body"] != "Second" || msg["timestamp"] != float64(1_700_000_100) || msg["from"] != news {
		t.Fatalf("message: %v", msg)
	}
	if rows[0]["reactions"].(map[string]any)["👍"] != float64(5) {
		t.Fatalf("reactions: %v", rows[0]["reactions"])
	}
	reqs := fakecore.Logged[*pb.GetNewsletterMessagesRequest](&nl.RequestLog)
	if len(reqs) != 1 || reqs[0].GetCount() != 1 || reqs[0].GetJid().GetValue() != news {
		t.Fatalf("core got %v", reqs)
	}
	r = e.Do("GET", "/api/default/channels/"+news+"/messages/preview?downloadMedia=false", nil)
	rows = nil
	if r.JSON(t, &rows); len(rows) != 2 || rows[1]["message"].(map[string]any)["hasMedia"] != true {
		t.Fatalf("default limit takes both: %s", r.Body)
	}
	if reqs := fakecore.Logged[*pb.GetNewsletterMessagesRequest](&nl.RequestLog); reqs[1].GetCount() != 10 {
		t.Fatalf("default limit is 10: %v", reqs[1])
	}
	if r := e.Do("GET", "/api/default/channels/"+news+"/messages/preview?limit=many", nil); r.Status != 400 {
		t.Fatalf("bad limit: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/channels/"+news+"/messages/preview?downloadMedia=maybe", nil); r.Status != 400 {
		t.Fatalf("bad downloadMedia: %d %s", r.Status, r.Body)
	}
}

func TestChannelsWrite(t *testing.T) {
	nl := seedNewsletters()
	e := directoryEnv(t, fakecore.Options{Newsletters: nl})

	r := e.Do("POST", "/api/default/channels", map[string]string{"name": "Launches", "description": "What ships"})
	ch := r.Map(t)
	if r.Status != 201 || ch["name"] != "Launches" || ch["description"] != "What ships" || ch["role"] != "OWNER" || !strings.HasSuffix(ch["id"].(string), "@newsletter") {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/default/channels", map[string]string{"name": "Bare"}); r.Status != 201 {
		t.Fatalf("no description: %d %s", r.Status, r.Body)
	}
	reqs := fakecore.Logged[*pb.CreateNewsletterRequest](&nl.RequestLog)
	if len(reqs) != 2 || reqs[0].GetDescription() != "What ships" || reqs[1].Description != nil {
		t.Fatalf("core got %v", reqs)
	}
	// A channel needs a name: the core refuses it.
	if r := e.Do("POST", "/api/default/channels", map[string]string{}); r.Status != 400 {
		t.Fatalf("no name: %d %s", r.Status, r.Body)
	}
	// The core takes no picture: 501 before anything is created.
	if r := e.Do("POST", "/api/default/channels", map[string]any{"name": "Pic", "picture": map[string]string{"url": "https://example.com/a.jpg"}}); r.Status != 501 {
		t.Fatalf("picture: %d %s", r.Status, r.Body)
	}
	if n := len(fakecore.Logged[*pb.CreateNewsletterRequest](&nl.RequestLog)); n != 3 {
		t.Fatalf("a create with a picture reached the core: %d requests", n)
	}

	for _, c := range []struct {
		path  string
		check func() bool
	}{
		{"follow", func() bool { f, ok := nl.Following(news); return ok && f }},
		{"mute", func() bool { return nl.Muted(news) }},
		{"unmute", func() bool { return !nl.Muted(news) }},
		{"unfollow", func() bool { f, ok := nl.Following(news); return ok && !f }},
	} {
		if r := e.Do("POST", "/api/default/channels/"+news+"/"+c.path, nil); r.Status != 201 || string(r.Body) != "{}\n" || !c.check() {
			t.Fatalf("%s: %d %q", c.path, r.Status, r.Body)
		}
	}
	if r := e.Do("POST", "/api/default/channels/0029Va4K0PZ5a245NkngBA2M/follow", nil); r.Status != 501 {
		t.Fatalf("invite code: %d %s", r.Status, r.Body)
	}
}

// channelPoll is a POLL history row whose options are spelled as given.
func channelPoll(t *testing.T, id string, serverID uint64, options ...string) *pb.NewsletterMessage {
	t.Helper()
	poll := &waE2E.PollCreationMessage{Name: proto.String("Q"), SelectableOptionsCount: proto.Uint32(0)}
	for _, o := range options {
		poll.Options = append(poll.Options, &waE2E.PollCreationMessage_Option{OptionName: proto.String(o)})
	}
	raw, err := proto.Marshal(&waE2E.Message{PollCreationMessageV3: poll})
	if err != nil {
		t.Fatal(err)
	}
	return &pb.NewsletterMessage{
		Message:  &pb.InboundMessage{Key: &pb.MessageKey{Chat: pbJid(news), Id: id}, Chat: pbJid(news), RawMessage: raw},
		ServerId: serverID, Type: pb.NewsletterMessageType_NEWSLETTER_MESSAGE_TYPE_POLL,
	}
}

func hexes(hashes [][]byte) []string {
	var out []string
	for _, h := range hashes {
		out = append(out, hex.EncodeToString(h))
	}
	return out
}

func TestChannelPollVote(t *testing.T) {
	const (
		love   = "\u2764\ufe0f Love"                                 // variation selector
		family = "\U0001F468\u200d\U0001F469\u200d\U0001F467 Family" // ZWJ sequence
		// sha256 of each name, computed apart from the edge (Python hashlib).
		loveHash   = "9b278942a81f5717d98e3c62ca14f0cdf67a13cb8ccb4cf3dda5456af210ef1a"
		familyHash = "863382c9ec1d94df1833ddb75de19111a19c413ceb25dae87e0d99b36c99acfe"
	)
	// 400 rows, newest first: a poll on the second page and one past the
	// three pages the edge reads.
	nl := seedNewsletters()
	var rows []*pb.NewsletterMessage
	for sid := uint64(400); sid >= 1; sid-- {
		switch sid {
		case 250:
			rows = append(rows, channelPoll(t, "3EB0RECENT", sid, love, family, "No"))
		case 50:
			rows = append(rows, channelPoll(t, "3EB0OLD", sid, "Old"))
		default:
			rows = append(rows, &pb.NewsletterMessage{Message: &pb.InboundMessage{Key: &pb.MessageKey{Id: fmt.Sprintf("M%d", sid)}}, ServerId: sid})
		}
	}
	nl.Messages[news] = rows
	e := directoryEnv(t, fakecore.Options{Newsletters: nl})
	vote := func(body map[string]any) (int, string) {
		body["session"], body["chatId"] = "default", news
		r := e.Do("POST", "/api/sendPollVote", body)
		return r.Status, string(r.Body)
	}
	pages := func() []*pb.GetNewsletterMessagesRequest {
		return fakecore.Logged[*pb.GetNewsletterMessagesRequest](&nl.RequestLog)
	}

	// Found on the second page, by the message id alone, in either id form.
	code, body := vote(map[string]any{"pollMessageId": "false_" + news + "_3EB0RECENT", "votes": []string{family}})
	if code != 201 || !strings.Contains(body, `"id":"true_`+news+`_3EB0VOTE0001"`) {
		t.Fatalf("vote: %d %s", code, body)
	}
	if got, ok := nl.PollVote(250); !ok || strings.Join(hexes(got), ",") != familyHash {
		t.Fatalf("server holds %v (%v)", hexes(got), ok)
	}
	if p := pages(); len(p) != 2 || p[0].GetBefore() != 0 || p[0].GetCount() != 100 || p[1].GetBefore() != 301 {
		t.Fatalf("history pages: %v", p)
	}
	v := fakecore.Logged[*pb.SendNewsletterPollVoteRequest](&nl.RequestLog)[0]
	if v.GetJid().GetValue() != news || v.GetServerId() != 250 {
		t.Fatalf("vote request: %v", v)
	}

	// Changing the vote sends the whole selection, once per option.
	if code, body := vote(map[string]any{"pollMessageId": "3EB0RECENT", "votes": []string{love, family, love}}); code != 201 {
		t.Fatalf("change: %d %s", code, body)
	}
	if got, _ := nl.PollVote(250); strings.Join(hexes(got), ",") != loveHash+","+familyHash {
		t.Fatalf("changed to %v", hexes(got))
	}
	// A name retyped without its ZWJ is not an option of the poll.
	if code, body := vote(map[string]any{"pollMessageId": "3EB0RECENT", "votes": []string{"\U0001F468\U0001F469\U0001F467 Family"}}); code != 400 {
		t.Fatalf("retyped option: %d %s", code, body)
	}
	// An empty selection removes the vote.
	if code, body := vote(map[string]any{"pollMessageId": "3EB0RECENT", "votes": []string{}}); code != 201 {
		t.Fatalf("remove: %d %s", code, body)
	}
	if got, ok := nl.PollVote(250); !ok || len(got) != 0 {
		t.Fatalf("removed: %v %v", hexes(got), ok)
	}

	// Older than the pages read: not found after three pages.
	before := len(pages())
	if code, body := vote(map[string]any{"pollMessageId": "3EB0OLD", "votes": []string{"Old"}}); code != 422 || !strings.Contains(body, "pollServerId") {
		t.Fatalf("old poll: %d %s", code, body)
	}
	if n := len(pages()) - before; n != 3 {
		t.Fatalf("%d pages read for a poll that is not there", n)
	}
	// With its pollServerId the edge reads no history and hashes the name
	// as given.
	before = len(pages())
	if code, body := vote(map[string]any{"pollMessageId": "3EB0OLD", "pollServerId": 50, "votes": []string{"Old"}}); code != 201 {
		t.Fatalf("by server id: %d %s", code, body)
	}
	if got, _ := nl.PollVote(50); len(pages()) != before || len(got) != 1 {
		t.Fatalf("by server id: %d pages, %v", len(pages())-before, hexes(got))
	}
	for _, bad := range []any{"50", 1.5, 0, -3} {
		if code, _ := vote(map[string]any{"pollMessageId": "3EB0OLD", "pollServerId": bad, "votes": []string{"Old"}}); code != 400 {
			t.Fatalf("pollServerId %v: %d", bad, code)
		}
	}

	// A poll this edge sent: its options come from the store.
	r := e.Do("POST", "/api/sendPoll", map[string]any{"session": "default", "chatId": news, "poll": map[string]any{"name": "Mine", "options": []string{"a", "b"}}})
	if r.Status != 201 {
		t.Fatalf("sendPoll: %d %s", r.Status, r.Body)
	}
	if code, body := vote(map[string]any{"pollMessageId": r.Map(t)["id"], "pollServerId": 7, "votes": []string{"c"}}); code != 400 {
		t.Fatalf("an option the sent poll does not have: %d %s", code, body)
	}
}
