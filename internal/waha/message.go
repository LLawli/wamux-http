package waha

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// WAHA's message acks (WAMessageAck).
const (
	AckError   = -1
	AckPending = 0
	AckServer  = 1
	AckDevice  = 2
	AckRead    = 3
	AckPlayed  = 4
)

var ackNames = map[int]string{
	AckError: "ERROR", AckPending: "PENDING", AckServer: "SERVER",
	AckDevice: "DEVICE", AckRead: "READ", AckPlayed: "PLAYED",
}

// AckName is an ack's WAHA name; an unknown value is "UNKNOWN".
func AckName(ack int) string {
	if n, ok := ackNames[ack]; ok {
		return n
	}
	return "UNKNOWN"
}

// AckFromName reads a WAHA ack name ("READ") back into its value.
func AckFromName(name string) (int, bool) {
	for v, n := range ackNames {
		if n == name {
			return v, true
		}
	}
	return 0, false
}

// Me is what the edge knows of the session's own account, read from the
// stored MeInfo.
type Me struct {
	ID       string `json:"id"`  // 5511999999999@c.us
	LID      string `json:"lid"` // 123456789012345@lid
	JID      string `json:"jid"` // 5511999999999:7@s.whatsapp.net, with the device
	PushName string `json:"pushName"`
}

// ParseMe reads a stored MeInfo; null or invalid gives a zero value.
func ParseMe(raw json.RawMessage) Me {
	var m Me
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// IsMe reports whether a WAHA id (any form) is the account itself.
func (m Me) IsMe(id string) bool {
	id = ChatIDFromJID(id)
	return id != "" && (id == m.ID || (m.LID != "" && id == m.LID))
}

// MediaOptions decides whether a received file gets a download URL, and
// builds it.
type MediaOptions struct {
	BaseURL   string
	Session   string
	Download  bool
	Mimetypes []string // prefixes; empty allows every type
}

// URL is the file's URL under /api/files, or nil when the policy keeps the
// file out (download off, or a mimetype the filter does not list).
func (o MediaOptions) URL(waID, mimetype string) *string {
	if !o.Download || waID == "" || !o.allows(mimetype) {
		return nil
	}
	u := strings.TrimRight(o.BaseURL, "/") + "/api/files/" + o.Session + "/" + waID + "." + MediaExtension(mimetype)
	return &u
}

func (o MediaOptions) allows(mimetype string) bool {
	if len(o.Mimetypes) == 0 {
		return true
	}
	for _, p := range o.Mimetypes {
		if strings.HasPrefix(mimetype, p) {
			return true
		}
	}
	return false
}

// mimeExtensions is the first extension the mime-db (WAHA's `mime-types`)
// gives each type WhatsApp commonly carries, so file names match WAHA's
// whatever the host's mime tables say.
var mimeExtensions = map[string]string{
	"audio/ogg":                     "oga",
	"audio/mpeg":                    "mpga",
	"audio/mp4":                     "m4a",
	"audio/aac":                     "aac",
	"audio/amr":                     "amr",
	"audio/wav":                     "wav",
	"image/jpeg":                    "jpeg",
	"image/png":                     "png",
	"image/webp":                    "webp",
	"image/gif":                     "gif",
	"video/mp4":                     "mp4",
	"video/3gpp":                    "3gp",
	"video/quicktime":               "qt",
	"video/webm":                    "webm",
	"application/pdf":               "pdf",
	"application/zip":               "zip",
	"application/was":               "zip",
	"application/json":              "json",
	"application/msword":            "doc",
	"application/vnd.ms-excel":      "xls",
	"application/vnd.ms-powerpoint": "ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/octet-stream": "bin",
	"text/plain":               "txt",
	"text/csv":                 "csv",
	"text/vcard":               "vcf",
}

// MediaExtension is the file extension for a mimetype ("audio/ogg;
// codecs=opus" gives "oga"), "bin" when nothing is known.
func MediaExtension(mimetype string) string {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(mimetype, ";", 2)[0]))
	if ext, ok := mimeExtensions[base]; ok {
		return ext
	}
	if exts, _ := mime.ExtensionsByType(base); len(exts) > 0 {
		return strings.TrimPrefix(exts[0], ".")
	}
	return "bin"
}

// Media is WAHA's WAMedia. URL is null when the file is not served.
type Media struct {
	URL      *string `json:"url"`
	Mimetype string  `json:"mimetype"`
	Filename *string `json:"filename"`
}

// Location is WAHA's WALocation; coordinates are strings, as WAHA sends them.
type Location struct {
	Live        bool   `json:"live"`
	Latitude    string `json:"latitude"`
	Longitude   string `json:"longitude"`
	Name        string `json:"name,omitempty"`
	Address     string `json:"address,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	Thumbnail   string `json:"thumbnail,omitempty"` // base64 JPEG
}

// ReplyTo is WAHA's ReplyToMessage: the quoted message as the reply carries it.
type ReplyTo struct {
	ID          string          `json:"id"`
	Participant *string         `json:"participant"`
	Body        *string         `json:"body"`
	HasMedia    bool            `json:"hasMedia"`
	Media       *Media          `json:"media"`
	Data        json.RawMessage `json:"_data,omitempty"`
}

// Message is WAHA's WAMessage as GOWS builds it. Field order is GOWS's;
// mediaUrl is absent, as on GOWS.
type Message struct {
	ID          string          `json:"id"`
	Timestamp   int64           `json:"timestamp"` // seconds
	From        string          `json:"from"`      // always the chat
	FromMe      bool            `json:"fromMe"`
	Source      string          `json:"source"` // "api" or "app"
	Body        *string         `json:"body"`
	To          *string         `json:"to"`          // the sender in a group, null otherwise
	Participant *string         `json:"participant"` // same as To
	HasMedia    bool            `json:"hasMedia"`
	Media       *Media          `json:"media"`
	Ack         int             `json:"ack"`
	Location    *Location       `json:"location"`
	VCards      []string        `json:"vCards"`
	AckName     string          `json:"ackName"`
	ReplyTo     *ReplyTo        `json:"replyTo"`
	Data        json.RawMessage `json:"_data"`
}

// SetAck sets the ack and its name together.
func (m *Message) SetAck(ack int) {
	m.Ack, m.AckName = ack, AckName(ack)
}

// MessageContext is what building a WAMessage needs beyond the message.
type MessageContext struct {
	Me    Me
	Media MediaOptions
}

// GroupLike reports a chat whose message ids carry the sender: groups and
// broadcast lists, status@broadcast included.
func GroupLike(chat string) bool {
	return IsGroup(chat) || strings.HasSuffix(chat, "@broadcast")
}

// InboundID is the serialized id of a core message: the key, plus the
// sender as participant in a group-like chat (WAHA's buildMessageId).
func InboundID(m *pb.InboundMessage, me Me) MessageID {
	k := m.GetKey()
	chat := FromPB(k.GetChat())
	if chat == "" {
		chat = FromPB(m.GetChat())
	}
	id := MessageID{FromMe: k.GetFromMe(), Chat: chat, ID: k.GetId()}
	if GroupLike(chat) {
		id.Participant = SenderOf(m, me)
	}
	return id
}

// SenderOf is who sent a core message, in WAHA form: the sender, else the
// key's participant, else the account itself for a message of mine, else the
// chat.
func SenderOf(m *pb.InboundMessage, me Me) string {
	if s := FromPB(m.GetSender()); s != "" {
		return s
	}
	if p := FromPB(m.GetKey().GetParticipant()); p != "" {
		return p
	}
	if m.GetKey().GetFromMe() && me.ID != "" {
		return me.ID
	}
	return FromPB(m.GetKey().GetChat())
}

// NewMessage builds the WAMessage for a core message. The ack is WAHA's live
// value: SERVER for mine, DEVICE for the others.
func NewMessage(m *pb.InboundMessage, mc MessageContext) Message {
	raw, _ := DecodeRaw(m.GetRawMessage())
	content := Content(raw)
	id := InboundID(m, mc.Me)
	out := Message{
		ID:        id.String(),
		Timestamp: m.GetTimestamp() / 1000,
		From:      id.Chat,
		FromMe:    id.FromMe,
		Source:    source(m, mc.Me),
		Body:      body(m, content),
		Location:  location(content),
		VCards:    vcards(content),
		Data:      ProtoJSON(m),
	}
	if GroupLike(id.Chat) {
		sender := SenderOf(m, mc.Me)
		out.To, out.Participant = &sender, &sender
	}
	if md := m.GetMedia(); md != nil {
		out.HasMedia = true
		out.Media = &Media{
			URL:      mc.Media.URL(id.ID, md.GetMimeType()),
			Mimetype: md.GetMimeType(),
			Filename: filename(content),
		}
	} else if mediaContent(content) != nil {
		out.HasMedia = true
	}
	if id.FromMe {
		out.SetAck(AckServer)
	} else {
		out.SetAck(AckDevice)
	}
	out.ReplyTo = replyTo(m, content)
	return out
}

// RefreshMedia recomputes media.url of a stored WAMessage for other download
// options (a read with downloadMedia=false, or another mimetype filter).
func RefreshMedia(body json.RawMessage, waID string, opts MediaOptions) (json.RawMessage, error) {
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	if m.Media == nil {
		return body, nil
	}
	m.Media.URL = opts.URL(waID, m.Media.Mimetype)
	return json.Marshal(m)
}

// ProtoJSON is a protobuf message as compact JSON, for `_data`; nil gives
// null.
func ProtoJSON(m proto.Message) json.RawMessage {
	if m == nil || !m.ProtoReflect().IsValid() {
		return json.RawMessage("null")
	}
	b, err := protojson.Marshal(m)
	if err != nil {
		return json.RawMessage("null")
	}
	return compactJSON(b)
}

func compactJSON(b []byte) json.RawMessage {
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(out.Bytes())
}

// source is "api" for my message sent from this edge's own device, "app"
// for anything else (WAHA's getSourceDeviceByMsg).
func source(m *pb.InboundMessage, me Me) string {
	if !m.GetKey().GetFromMe() {
		return "app"
	}
	sender := m.GetSender().GetValue()
	if sender == "" {
		return "api" // only the core's echo of our own send names no sender
	}
	if device(sender) == device(me.JID) {
		return "api"
	}
	return "app"
}

// device is a JID's device number ("0" when it has none).
func device(jid string) string {
	user, _, _ := strings.Cut(jid, "@")
	if _, d, ok := strings.Cut(user, ":"); ok {
		return d
	}
	return "0"
}

// Content unwraps the containers WhatsApp puts around the real content
// (ephemeral, view-once, document with caption, device-sent).
func Content(m *waE2E.Message) *waE2E.Message {
	for i := 0; m != nil && i < 8; i++ {
		var next *waE2E.Message
		switch {
		case m.GetEphemeralMessage().GetMessage() != nil:
			next = m.GetEphemeralMessage().GetMessage()
		case m.GetViewOnceMessage().GetMessage() != nil:
			next = m.GetViewOnceMessage().GetMessage()
		case m.GetViewOnceMessageV2().GetMessage() != nil:
			next = m.GetViewOnceMessageV2().GetMessage()
		case m.GetViewOnceMessageV2Extension().GetMessage() != nil:
			next = m.GetViewOnceMessageV2Extension().GetMessage()
		case m.GetDocumentWithCaptionMessage().GetMessage() != nil:
			next = m.GetDocumentWithCaptionMessage().GetMessage()
		case m.GetDeviceSentMessage().GetMessage() != nil:
			next = m.GetDeviceSentMessage().GetMessage()
		default:
			return m
		}
		m = next
	}
	if m == nil {
		return &waE2E.Message{}
	}
	return m
}

// body is the text WAHA shows for a message (its extractBody): the text,
// else the caption, else what a reply to buttons or a list carries.
func body(m *pb.InboundMessage, c *waE2E.Message) *string {
	if t := m.GetText(); t != "" {
		return &t
	}
	if t := m.GetCaption(); t != "" {
		return &t
	}
	return BodyOf(c)
}

// BodyOf extracts a WAMessage body from a WhatsApp message; nil when it has
// none.
func BodyOf(c *waE2E.Message) *string {
	c = Content(c)
	var parts []string
	switch {
	case c.GetConversation() != "":
		parts = []string{c.GetConversation()}
	case c.GetExtendedTextMessage().GetText() != "":
		parts = []string{c.GetExtendedTextMessage().GetText()}
	case caption(c) != "":
		parts = []string{caption(c)}
	case c.GetProtocolMessage().GetEditedMessage() != nil:
		return BodyOf(c.GetProtocolMessage().GetEditedMessage())
	case c.GetTemplateButtonReplyMessage().GetSelectedDisplayText() != "":
		parts = []string{c.GetTemplateButtonReplyMessage().GetSelectedDisplayText()}
	case c.GetButtonsResponseMessage().GetSelectedDisplayText() != "":
		parts = []string{c.GetButtonsResponseMessage().GetSelectedDisplayText()}
	case c.GetListMessage() != nil:
		l := c.GetListMessage()
		parts = []string{l.GetTitle(), l.GetDescription(), l.GetFooterText()}
	case c.GetListResponseMessage() != nil:
		l := c.GetListResponseMessage()
		parts = []string{l.GetTitle(), l.GetDescription()}
	}
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	s := strings.Join(kept, "\n")
	return &s
}

func caption(c *waE2E.Message) string {
	switch {
	case c.GetImageMessage().GetCaption() != "":
		return c.GetImageMessage().GetCaption()
	case c.GetVideoMessage().GetCaption() != "":
		return c.GetVideoMessage().GetCaption()
	case c.GetDocumentMessage().GetCaption() != "":
		return c.GetDocumentMessage().GetCaption()
	case c.GetPtvMessage().GetCaption() != "":
		return c.GetPtvMessage().GetCaption()
	}
	return ""
}

// mediaContent is the message's media part, nil when it has none.
func mediaContent(c *waE2E.Message) proto.Message {
	switch {
	case c.GetImageMessage() != nil:
		return c.GetImageMessage()
	case c.GetVideoMessage() != nil:
		return c.GetVideoMessage()
	case c.GetPtvMessage() != nil:
		return c.GetPtvMessage()
	case c.GetAudioMessage() != nil:
		return c.GetAudioMessage()
	case c.GetDocumentMessage() != nil:
		return c.GetDocumentMessage()
	case c.GetStickerMessage() != nil:
		return c.GetStickerMessage()
	}
	return nil
}

func filename(c *waE2E.Message) *string {
	if n := c.GetDocumentMessage().GetFileName(); n != "" {
		return &n
	}
	return nil
}

// location is WAHA's extractWALocation.
func location(c *waE2E.Message) *Location {
	if l := c.GetLocationMessage(); l != nil {
		return &Location{
			Latitude:    coord(l.GetDegreesLatitude()),
			Longitude:   coord(l.GetDegreesLongitude()),
			Name:        l.GetName(),
			Address:     l.GetAddress(),
			URL:         l.GetURL(),
			Description: l.GetComment(),
			Thumbnail:   b64(l.GetJPEGThumbnail()),
		}
	}
	if l := c.GetLiveLocationMessage(); l != nil {
		return &Location{
			Live:        true,
			Latitude:    coord(l.GetDegreesLatitude()),
			Longitude:   coord(l.GetDegreesLongitude()),
			Description: l.GetCaption(),
			Thumbnail:   b64(l.GetJPEGThumbnail()),
		}
	}
	return nil
}

// coord prints a coordinate as JavaScript's String(number) does.
func coord(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func b64(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// vcards is WAHA's extractVCards.
func vcards(c *waE2E.Message) []string {
	if v := c.GetContactMessage(); v != nil {
		return []string{v.GetVcard()}
	}
	if a := c.GetContactsArrayMessage(); a != nil {
		out := make([]string, 0, len(a.GetContacts()))
		for _, v := range a.GetContacts() {
			out = append(out, v.GetVcard())
		}
		return out
	}
	return nil
}

// ContextInfo finds the contextInfo of the message's content, whatever kind
// of message carries it.
func ContextInfo(c *waE2E.Message) *waE2E.ContextInfo {
	var found *waE2E.ContextInfo
	c.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Message() == nil || fd.IsList() || fd.IsMap() {
			return true
		}
		sub := v.Message()
		f := sub.Descriptor().Fields().ByName("contextInfo")
		if f == nil || !sub.Has(f) {
			return true
		}
		if ci, ok := sub.Get(f).Message().Interface().(*waE2E.ContextInfo); ok {
			found = ci
			return false
		}
		return true
	})
	return found
}

// replyTo is the quoted message: the core's quote, completed from the raw
// contextInfo. Quoted media gets no URL: the quoted copy is not a message
// the edge stored.
func replyTo(m *pb.InboundMessage, c *waE2E.Message) *ReplyTo {
	ci := ContextInfo(c)
	q := m.GetQuote()
	id := q.GetQuoted().GetId()
	if id == "" {
		id = ci.GetStanzaID()
	}
	if id == "" {
		return nil
	}
	r := &ReplyTo{ID: id}
	participant := FromPB(q.GetParticipant())
	if participant == "" {
		participant = FromPB(q.GetQuoted().GetParticipant())
	}
	if participant == "" && ci.GetParticipant() != "" {
		participant = ChatIDFromJID(ci.GetParticipant())
	}
	if participant != "" {
		r.Participant = &participant
	}
	if qm := ci.GetQuotedMessage(); qm != nil {
		r.Body = BodyOf(qm)
		r.HasMedia = mediaContent(Content(qm)) != nil
		r.Data = ProtoJSON(qm)
	}
	return r
}
