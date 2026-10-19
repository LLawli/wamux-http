package waha

import (
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// Channel is WAHA's Channel (a `@newsletter`), filled as GOWS's `toChannel`.
type Channel struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Invite           string `json:"invite"`
	Preview          string `json:"preview,omitempty"`
	Picture          string `json:"picture,omitempty"`
	Role             string `json:"role"`
	Description      string `json:"description"`
	Verified         bool   `json:"verified"`
	SubscribersCount uint64 `json:"subscribersCount"`
}

// The channel roles WAHA names; GOWS reads anything else as GUEST.
const (
	ChannelOwner      = "OWNER"
	ChannelAdmin      = "ADMIN"
	ChannelSubscriber = "SUBSCRIBER"
	ChannelGuest      = "GUEST"
)

// mediaHost is where WhatsApp serves a picture the server names by its direct
// path, as GOWS resolves it.
const mediaHost = "https://mmg.whatsapp.net"

// ChannelFromPB maps one newsletter. The core does not relay the invite code,
// so `invite` is empty.
func ChannelFromPB(n *pb.Newsletter) Channel {
	c := Channel{
		ID:               FromPB(n.GetJid()),
		Name:             n.GetName(),
		Description:      n.GetDescription(),
		Verified:         n.GetVerification() == pb.NewsletterVerification_NEWSLETTER_VERIFICATION_VERIFIED,
		SubscribersCount: n.GetSubscriberCount(),
		Picture:          n.GetPictureUrl(),
		Role:             ChannelGuest,
	}
	if strings.HasPrefix(c.Picture, "/") {
		c.Picture = mediaHost + c.Picture
	}
	switch n.GetRole() {
	case pb.NewsletterRole_NEWSLETTER_ROLE_OWNER:
		c.Role = ChannelOwner
	case pb.NewsletterRole_NEWSLETTER_ROLE_ADMIN:
		c.Role = ChannelAdmin
	case pb.NewsletterRole_NEWSLETTER_ROLE_SUBSCRIBER:
		c.Role = ChannelSubscriber
	}
	return c
}

// IsNewsletter reports whether an id (either form) is a channel.
func IsNewsletter(id string) bool { return strings.HasSuffix(id, "@newsletter") }

// ChannelMessage is WAHA's ChannelMessage.
type ChannelMessage struct {
	Reactions map[string]uint64 `json:"reactions"`
	Message   ChannelPost       `json:"message"`
	ViewCount uint64            `json:"viewCount"`
}

// ChannelPost is the WAMessage of one channel history row, with the fields
// GOWS fills for it: no sender inside a channel, so `to` and `participant` are
// null, and media is not downloaded (`media` null).
type ChannelPost struct {
	ID          string          `json:"id"`
	Timestamp   int64           `json:"timestamp"`
	From        string          `json:"from"`
	FromMe      bool            `json:"fromMe"`
	Source      string          `json:"source"`
	To          *string         `json:"to"`
	Participant *string         `json:"participant"`
	Body        *string         `json:"body"`
	HasMedia    bool            `json:"hasMedia"`
	Media       any             `json:"media"`
	Ack         int             `json:"ack"`
	AckName     string          `json:"ackName"`
	Data        json.RawMessage `json:"_data,omitempty"`
}

// Acks a received or own channel row gets, as live messages do.
const (
	ackServer = 1
	ackDevice = 2
)

// ChannelMessageFromPB maps one channel history row. `_data` is the core's
// row as protojson.
func ChannelMessageFromPB(channel string, r *pb.NewsletterMessage) ChannelMessage {
	m := r.GetMessage()
	key := &pb.MessageKey{Chat: &pb.Jid{Value: channel}, Id: m.GetKey().GetId(), FromMe: m.GetKey().GetFromMe()}
	post := ChannelPost{
		ID:        MessageIDFromKey(key).String(),
		Timestamp: m.GetTimestamp() / 1000,
		From:      ChatIDFromJID(channel),
		FromMe:    key.FromMe,
		Source:    "app",
		HasMedia:  m.GetMedia() != nil,
		Ack:       ackDevice,
		AckName:   "DEVICE",
	}
	if post.FromMe {
		post.Ack, post.AckName = ackServer, "SERVER"
	}
	if body := m.GetText(); body != "" {
		post.Body = &body
	} else if body := m.GetCaption(); body != "" {
		post.Body = &body
	}
	if raw, err := protojson.Marshal(r); err == nil {
		post.Data = raw
	}
	out := ChannelMessage{Reactions: map[string]uint64{}, Message: post}
	for _, rc := range r.GetReactions() {
		out.Reactions[rc.GetCode()] += rc.GetCount()
	}
	return out
}
