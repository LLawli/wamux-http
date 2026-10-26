package waha

import (
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

// IsStatusBroadcast reports the chat statuses are posted to, as a JID or a
// WAHA chat id (both are "status@broadcast").
func IsStatusBroadcast(id string) bool { return id == "status@broadcast" }

// ChannelMessage is WAHA's ChannelMessage.
type ChannelMessage struct {
	Reactions map[string]uint64 `json:"reactions"`
	Message   Message           `json:"message"`
	ViewCount uint64            `json:"viewCount"`
}

// ChannelMessageFromPB maps one channel history row. `_data` is the core's
// row as protojson.
func ChannelMessageFromPB(channel string, r *pb.NewsletterMessage) ChannelMessage {
	m := r.GetMessage()
	// A history row names the channel only through the request; key it there
	// so the id is the channel's, as a live channel message would be.
	in := &pb.InboundMessage{
		Key:        &pb.MessageKey{Chat: &pb.Jid{Value: channel}, Id: m.GetKey().GetId(), FromMe: m.GetKey().GetFromMe()},
		Chat:       &pb.Jid{Value: channel},
		Timestamp:  m.GetTimestamp(),
		Text:       m.GetText(),
		Caption:    m.GetCaption(),
		Media:      m.GetMedia(),
		RawMessage: m.GetRawMessage(),
	}
	// Channel rows are not stored, so their media has no /api/files link.
	msg := NewMessage(in, MessageContext{})
	if raw, err := protojson.Marshal(r); err == nil {
		msg.Data = raw // the whole row: server id, reactions, votes
	}
	out := ChannelMessage{Reactions: map[string]uint64{}, Message: msg}
	for _, rc := range r.GetReactions() {
		out.Reactions[rc.GetCode()] += rc.GetCount()
	}
	return out
}
