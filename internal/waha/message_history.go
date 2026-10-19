package waha

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// DecodeHistory decodes a history sync blob (the core relays it verbatim).
func DecodeHistory(raw []byte) (*waHistorySync.HistorySync, error) {
	h := &waHistorySync.HistorySync{}
	return h, proto.Unmarshal(raw, h)
}

// rawMedia is what every WhatsApp media message carries to be downloaded.
type rawMedia interface {
	proto.Message
	GetDirectPath() string
	GetMediaKey() []byte
	GetFileEncSHA256() []byte
	GetFileSHA256() []byte
	GetFileLength() uint64
	GetMimetype() string
}

// MediaDescriptorOf builds the core's media descriptor from a raw message's
// media part, as the core would have for a live message; nil without media.
func MediaDescriptorOf(c *waE2E.Message) *pb.MediaDescriptor {
	c = Content(c)
	var m rawMedia
	var t pb.MediaType
	switch {
	case c.GetImageMessage() != nil:
		m, t = c.GetImageMessage(), pb.MediaType_MEDIA_TYPE_IMAGE
	case c.GetVideoMessage() != nil:
		m, t = c.GetVideoMessage(), pb.MediaType_MEDIA_TYPE_VIDEO
	case c.GetPtvMessage() != nil:
		m, t = c.GetPtvMessage(), pb.MediaType_MEDIA_TYPE_VIDEO
	case c.GetAudioMessage() != nil:
		m, t = c.GetAudioMessage(), pb.MediaType_MEDIA_TYPE_AUDIO
	case c.GetDocumentMessage() != nil:
		m, t = c.GetDocumentMessage(), pb.MediaType_MEDIA_TYPE_DOCUMENT
	case c.GetStickerMessage() != nil:
		m, t = c.GetStickerMessage(), pb.MediaType_MEDIA_TYPE_STICKER
	default:
		return nil
	}
	return &pb.MediaDescriptor{
		DirectPath: m.GetDirectPath(), MediaKey: m.GetMediaKey(), FileEncSha256: m.GetFileEncSHA256(),
		FileSha256: m.GetFileSHA256(), FileLength: m.GetFileLength(), MimeType: m.GetMimetype(), MediaType: t,
	}
}

// IsChatMessage reports a message that belongs in a chat's history, rather
// than a reaction, a vote or a protocol message (each has its own event,
// which a history sync does not replay).
func IsChatMessage(c *waE2E.Message) bool {
	c = Content(c)
	return c.GetReactionMessage() == nil && c.GetPollUpdateMessage() == nil && c.GetProtocolMessage() == nil
}

// FromWebMessage turns a history-sync message into the core's
// InboundMessage, so history and live messages are built and stored the
// same way (the files route then serves history media too). False for
// stubs without content and for messages that are not chat messages.
func FromWebMessage(w *waWeb.WebMessageInfo, me Me) (*pb.InboundMessage, bool) {
	k := w.GetKey()
	content := w.GetMessage()
	if k.GetID() == "" || k.GetRemoteJID() == "" || content == nil || !IsChatMessage(content) {
		return nil, false
	}
	key := &pb.MessageKey{Chat: &pb.Jid{Value: k.GetRemoteJID()}, Id: k.GetID(), FromMe: k.GetFromMe()}
	participant := w.GetParticipant()
	if participant == "" {
		participant = k.GetParticipant()
	}
	if participant != "" {
		key.Participant = &pb.Jid{Value: participant}
	}
	in := &pb.InboundMessage{
		Key:       key,
		Chat:      key.Chat,
		Timestamp: int64(w.GetMessageTimestamp()) * 1000,
		PushName:  w.GetPushName(),
		Media:     MediaDescriptorOf(content),
	}
	switch {
	case k.GetFromMe() && me.JID != "":
		in.Sender = &pb.Jid{Value: me.JID}
	case participant != "":
		in.Sender = key.Participant
	case !k.GetFromMe():
		in.Sender = key.Chat
	}
	c := Content(content)
	if t := c.GetConversation(); t != "" {
		in.Text = t
	} else {
		in.Text = c.GetExtendedTextMessage().GetText()
	}
	in.Caption = caption(c)
	if raw, err := proto.Marshal(content); err == nil {
		in.RawMessage = raw
	}
	return in, true
}

// HistoryAck maps a history message's status (ERROR=0 ... PLAYED=5) to
// WAHA's ack (ERROR=-1 ... PLAYED=4).
func HistoryAck(s waWeb.WebMessageInfo_Status) int {
	return int(s) - 1
}
