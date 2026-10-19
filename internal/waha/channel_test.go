package waha

import (
	"testing"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

func TestChannelFromPB(t *testing.T) {
	c := ChannelFromPB(&pb.Newsletter{
		Jid: jid("123@newsletter"), Name: "News", Description: "d", SubscriberCount: 42,
		PictureUrl: "/v/t61/pic", Verification: pb.NewsletterVerification_NEWSLETTER_VERIFICATION_VERIFIED,
		Role: pb.NewsletterRole_NEWSLETTER_ROLE_ADMIN,
	})
	if c.ID != "123@newsletter" || c.Name != "News" || c.SubscribersCount != 42 || !c.Verified || c.Role != ChannelAdmin {
		t.Fatalf("channel: %+v", c)
	}
	if c.Picture != "https://mmg.whatsapp.net/v/t61/pic" {
		t.Fatalf("a direct path becomes a URL: %q", c.Picture)
	}
	if g := ChannelFromPB(&pb.Newsletter{Jid: jid("1@newsletter")}); g.Role != ChannelGuest || g.Verified {
		t.Fatalf("no role reads GUEST: %+v", g)
	}
}

func TestChannelMessageFromPB(t *testing.T) {
	m := ChannelMessageFromPB("123@newsletter", &pb.NewsletterMessage{
		Message: &pb.InboundMessage{Key: &pb.MessageKey{Id: "AB12", FromMe: false}, Timestamp: 1_700_000_000_999, Caption: "photo",
			Media: &pb.MediaDescriptor{MimeType: "image/jpeg"}},
		ServerId:  7,
		Reactions: []*pb.NewsletterReactionCount{{Code: "👍", Count: 3}, {Code: "❤️", Count: 1}},
	})
	p := m.Message
	if p.ID != "false_123@newsletter_AB12" || p.From != "123@newsletter" || p.Timestamp != 1_700_000_000 {
		t.Fatalf("post: %+v", p)
	}
	if p.Body == nil || *p.Body != "photo" || !p.HasMedia || p.To != nil || p.Participant != nil || p.AckName != "DEVICE" {
		t.Fatalf("post fields: %+v", p)
	}
	if m.Reactions["👍"] != 3 || m.Reactions["❤️"] != 1 || len(p.Data) == 0 {
		t.Fatalf("reactions %v, data %s", m.Reactions, p.Data)
	}
	own := ChannelMessageFromPB("123@newsletter", &pb.NewsletterMessage{Message: &pb.InboundMessage{Key: &pb.MessageKey{Id: "X", FromMe: true}}})
	if own.Message.ID != "true_123@newsletter_X" || own.Message.Ack != ackServer || own.Message.Body != nil {
		t.Fatalf("own post: %+v", own.Message)
	}
}
