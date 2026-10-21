package httpapi_test

import (
	"strings"
	"testing"

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
