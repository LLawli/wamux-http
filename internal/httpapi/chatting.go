package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/media"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// The chatting tag. WAHA's controller validates almost none of these bodies
// (docs/waha-behavior.md 1c) and neither does this edge, beyond answering
// 400 for what no send can do without (a chatId, a text, a file). Operations
// the core has no mechanism for stay unregistered and answer WAHA's 501:
// sendLocation, sendButtons, sendList, forwardMessage and new-message-id.
func (s *Server) registerChatting(r chi.Router) {
	send := Route{Action: ActSend, From: FromBody, Working: true}
	s.route(r, http.MethodPost, "/api/sendText", send, s.sendText)
	s.route(r, http.MethodGet, "/api/sendText", Route{Action: ActSend, From: FromQuery, Working: true}, s.sendTextGet)
	s.route(r, http.MethodPost, "/api/reply", send, s.sendText)
	s.route(r, http.MethodPost, "/api/sendLinkPreview", send, s.sendLinkPreview)
	s.route(r, http.MethodPost, "/api/send/link-custom-preview", send, s.sendLinkCustomPreview)
	s.route(r, http.MethodPost, "/api/sendImage", send, s.sendMediaAs(media.KindImage))
	s.route(r, http.MethodPost, "/api/sendFile", send, s.sendMediaAs(media.KindDocument))
	s.route(r, http.MethodPost, "/api/sendVoice", send, s.sendMediaAs(media.KindVoice))
	s.route(r, http.MethodPost, "/api/sendVideo", send, s.sendMediaAs(media.KindVideo))
	s.route(r, http.MethodPost, "/api/sendSticker", send, s.sendMediaAs(media.KindSticker))
	s.route(r, http.MethodPost, "/api/sendContactVcard", send, s.sendContactVcard)
	s.route(r, http.MethodPost, "/api/sendPoll", send, s.sendPoll)
	s.route(r, http.MethodPost, "/api/sendPollVote", send, s.sendPollVote)
	s.route(r, http.MethodPost, "/api/send/buttons/reply", send, s.sendButtonsReply)
	s.route(r, http.MethodPut, "/api/reaction", send, s.setReaction)
	s.route(r, http.MethodPut, "/api/star", send, s.setStar)
	s.route(r, http.MethodPost, "/api/sendSeen", send, s.sendSeen)
	s.route(r, http.MethodPost, "/api/startTyping", send, s.typing(pb.PresenceState_PRESENCE_STATE_COMPOSING))
	s.route(r, http.MethodPost, "/api/stopTyping", send, s.typing(pb.PresenceState_PRESENCE_STATE_PAUSED))
}

// --- shared by every send ---

// me is the part of the session's MeInfo a send needs.
type me struct {
	ID  string `json:"id"`  // <number>@c.us
	LID string `json:"lid"` // <id>@lid, when known
}

func (c *Ctx) me() me {
	var m me
	if sess, err := c.Sess(); err == nil {
		_ = json.Unmarshal(sess.Me, &m)
	}
	return m
}

// myJID is this account's JID in the namespace of a chat: the LID in a
// `@lid` chat when it is known, the phone JID otherwise.
func (c *Ctx) myJID(chat string) string {
	m := c.me()
	id := m.ID
	if strings.HasSuffix(chat, "@lid") && m.LID != "" {
		id = m.LID
	}
	j, _ := waha.JIDFromChatID(id)
	return j
}

// chatJID is the core JID for a request's chatId; a missing or unusable one
// is a 400 rather than WAHA's 500.
func chatJID(chatID string) (*pb.Jid, error) {
	if strings.TrimSpace(chatID) == "" {
		return nil, ErrBadRequest("chatId is required")
	}
	j, err := waha.ToPB(chatID)
	if err != nil {
		return nil, ErrBadRequest(fmt.Sprintf("invalid chatId %q", chatID))
	}
	return j, nil
}

func jids(ids []string, field string) ([]*pb.Jid, error) {
	out := make([]*pb.Jid, 0, len(ids))
	for _, id := range ids {
		j, err := waha.ToPB(id)
		if err != nil {
			return nil, ErrBadRequest(fmt.Sprintf("invalid %s %q", field, id))
		}
		out = append(out, j)
	}
	return out, nil
}

func mentions(ids []string) ([]*pb.Mention, error) {
	js, err := jids(ids, "mention")
	if err != nil || len(js) == 0 {
		return nil, err
	}
	out := make([]*pb.Mention, len(js))
	for i, j := range js {
		out[i] = &pb.Mention{Jid: j}
	}
	return out, nil
}

// sentDetail is what a send knows about the message it sent, beyond the key.
type sentDetail struct {
	Body     string
	HasMedia bool
}

// SentMessage is WAHA's answer to a send. GOWS answers only `id` and
// `_data`; WAHA's document (and its other engines) answer a WAMessage. This
// is both: a WAMessage, with `_data` the core's own result.
type SentMessage struct {
	ID          string          `json:"id"`
	Timestamp   int64           `json:"timestamp"`
	From        string          `json:"from"`
	FromMe      bool            `json:"fromMe"`
	Source      string          `json:"source"`
	To          *string         `json:"to"`
	Participant *string         `json:"participant"`
	Body        *string         `json:"body"`
	HasMedia    bool            `json:"hasMedia"`
	Ack         int             `json:"ack"`
	AckName     string          `json:"ackName"`
	Data        json.RawMessage `json:"_data"`
}

// sent builds the answer to a send: the serialized id of the new message,
// from me, in the chat it went to, with my id as the participant in a group
// or a status (messageResponse in GOWS). The core gives no send time (its
// server_timestamp is always 0), so the time is the edge's.
func sent(c *Ctx, to *pb.Jid, key *pb.MessageKey, result proto.Message, detail ...sentDetail) SentMessage {
	chat := waha.FromPB(key.GetChat())
	if chat == "" {
		chat = waha.FromPB(to)
	}
	id := waha.MessageID{FromMe: true, Chat: chat, ID: key.GetId()}
	out := SentMessage{From: chat, FromMe: true, Source: "api", Ack: 1, AckName: "SERVER", Timestamp: time.Now().Unix()}
	if waha.IsGroup(chat) || strings.HasSuffix(chat, "@broadcast") {
		me := c.me().ID
		id.Participant = me
		out.To, out.Participant = &me, &me
	}
	out.ID = id.String()
	if len(detail) > 0 {
		out.HasMedia = detail[0].HasMedia
		if detail[0].Body != "" {
			body := detail[0].Body
			out.Body = &body
		}
	}
	data, err := protojson.Marshal(result)
	if err != nil {
		data = []byte("null")
	}
	out.Data = data
	return out
}

// bareID is the WhatsApp id inside a serialized message id, or the string
// itself when it is already bare (WAHA's soft parsing).
func bareID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "_") {
		return s, nil
	}
	m, err := waha.ParseMessageID(s)
	if err != nil {
		return "", errBadMessageID()
	}
	return m.ID, nil
}

// errBadMessageID is WAHA's text for an id it cannot parse (a 500 there).
func errBadMessageID() *Error {
	return ErrBadRequest("Message id be in format false_11111111111@c.us_AAAAAAAAAAAAAAAAAAAA[_participant]")
}

// stored is a message this edge holds.
type stored struct {
	msg    store.Message
	in     *pb.InboundMessage
	sender string // core JID of the author, device dropped; "" when unknown
}

// lookup finds a stored message by WhatsApp id; ok is false when there is
// none.
func (s *Server) lookup(ctx context.Context, session, waID string) (st stored, ok bool, err error) {
	m, err := s.Store.GetMessageByWAID(ctx, session, waID)
	if errors.Is(err, store.ErrNotFound) {
		return stored{}, false, nil
	}
	if err != nil {
		return stored{}, false, err
	}
	st = stored{msg: m, in: &pb.InboundMessage{}}
	if err := proto.Unmarshal(m.Raw, st.in); err == nil {
		st.sender = plainJID(st.in.GetSender().GetValue())
	}
	return st, true, nil
}

// plainJID drops the device from a JID ("5511...:7@s.whatsapp.net").
func plainJID(j string) string {
	if j == "" {
		return ""
	}
	out, err := waha.JIDFromChatID(waha.ChatIDFromJID(j))
	if err != nil {
		return ""
	}
	return out
}

// quote builds the quote for reply_to. The quoted message's chat comes from
// the stored message when this edge has it, else from a full WAHA id
// (false_<chat>_<id>[_participant]), else it is the request's chat. When it
// is another chat, the core sends a private reply (to a group message) or a
// reply to a status (status@broadcast); GOWS always quotes in the request's
// chat. raw is the quoted message's wa.Message.
func (s *Server) quote(c *Ctx, to *pb.Jid, replyTo string) (q *pb.QuoteContext, raw []byte, err error) {
	replyTo = strings.TrimSpace(replyTo)
	if replyTo == "" {
		return nil, nil, nil
	}
	mid := waha.MessageID{ID: replyTo}
	if strings.Contains(replyTo, "_") {
		if mid, err = waha.ParseMessageID(replyTo); err != nil {
			return nil, nil, errBadMessageID()
		}
	}
	chat := to
	if mid.Chat != "" {
		if j, err := waha.JIDFromChatID(mid.Chat); err == nil {
			chat = &pb.Jid{Value: j}
		}
	}
	key := &pb.MessageKey{Chat: chat, Id: mid.ID, FromMe: mid.FromMe}
	q = &pb.QuoteContext{Quoted: key}
	st, ok, err := s.lookup(c.R.Context(), c.Session, mid.ID)
	if err != nil {
		return nil, nil, err
	}
	author := ""
	switch {
	case ok:
		if stored := st.in.GetChat(); stored.GetValue() != "" {
			key.Chat = stored
		}
		key.FromMe = st.msg.FromMe
		author = st.sender
		if author == "" && st.msg.FromMe {
			author = c.myJID(key.Chat.GetValue())
		}
		raw = st.in.GetRawMessage()
	case mid.Participant != "":
		author, _ = waha.JIDFromChatID(mid.Participant)
	case mid.FromMe:
		author = c.myJID(key.Chat.GetValue())
	case !waha.IsGroup(key.Chat.GetValue()) && !waha.IsStatusBroadcast(key.Chat.GetValue()):
		author = key.Chat.GetValue()
	}
	// Only a group message or a status is quoted from another chat. A DM's
	// other id is the same conversation (a peer's LID chat and its phone
	// chat), and naming it would send a reply from another chat.
	if c := key.Chat.GetValue(); c != to.GetValue() && !waha.IsGroup(c) && !waha.IsStatusBroadcast(c) {
		key.Chat = to
	}
	if author != "" {
		q.Participant = &pb.Jid{Value: author}
		if waha.IsGroup(key.Chat.GetValue()) || waha.IsStatusBroadcast(key.Chat.GetValue()) {
			key.Participant = &pb.Jid{Value: author}
		}
	}
	return q, raw, nil
}

// mediaError maps a media failure: the caller's input is a 400, a URL the
// guard refused or that could not be fetched is a 422, the rest a 500.
func mediaError(err error) error {
	var fe *media.FetchError
	switch {
	case errors.Is(err, media.ErrBadInput):
		return ErrBadRequest(err.Error())
	case errors.Is(err, media.ErrRefused), errors.Is(err, media.ErrTooLarge), errors.As(err, &fe):
		return ErrUnprocessable(err.Error())
	}
	return err
}

// --- text ---

type textRequest struct {
	ChatID      string   `json:"chatId"`
	Text        string   `json:"text"`
	ReplyTo     string   `json:"reply_to"`
	Mentions    []string `json:"mentions"`
	LinkPreview *bool    `json:"linkPreview"`
}

func (s *Server) sendText(c *Ctx) error {
	var req textRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	return s.doSendText(c, req, http.StatusCreated, nil)
}

// sendTextGet is the deprecated GET /api/sendText?phone=&text=&session=.
func (s *Server) sendTextGet(c *Ctx) error {
	return s.doSendText(c, textRequest{ChatID: c.Query("phone"), Text: c.Query("text")}, http.StatusOK, nil)
}

// doSendText sends a text. preview, when set, is the card to attach;
// otherwise GOWS's rule applies: a preview of the first URL unless
// linkPreview is false.
func (s *Server) doSendText(c *Ctx, req textRequest, status int, preview *pb.LinkPreview) error {
	to, err := chatJID(req.ChatID)
	if err != nil {
		return err
	}
	if req.Text == "" {
		return ErrBadRequest("text is required")
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	ms, err := mentions(req.Mentions)
	if err != nil {
		return err
	}
	q, _, err := s.quote(c, to, req.ReplyTo)
	if err != nil {
		return err
	}
	if preview == nil && (req.LinkPreview == nil || *req.LinkPreview) {
		preview = s.Media.LinkPreview(c.R.Context(), req.Text)
	}
	res, err := s.Core.Messaging.SendText(c.R.Context(), &pb.SendTextRequest{
		Account: acc, To: to, Text: req.Text, Mentions: ms, Quote: q, LinkPreview: preview,
	})
	if err != nil {
		return err
	}
	return c.JSON(status, sent(c, to, res.GetKey(), res, sentDetail{Body: req.Text}))
}

// sendLinkPreview is the deprecated {chatId, url, title}: the URL as the
// text, with its preview under the given title.
func (s *Server) sendLinkPreview(c *Ctx) error {
	var req struct {
		ChatID string `json:"chatId"`
		URL    string `json:"url"`
		Title  string `json:"title"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if _, err := chatJID(req.ChatID); err != nil {
		return err
	}
	if req.URL == "" {
		return ErrBadRequest("url is required")
	}
	preview := s.Media.LinkPreview(c.R.Context(), req.URL)
	if preview == nil {
		preview = &pb.LinkPreview{MatchedText: req.URL}
	}
	if req.Title != "" {
		preview.Title = req.Title
	}
	return s.doSendText(c, textRequest{ChatID: req.ChatID, Text: req.URL}, http.StatusCreated, preview)
}

// sendLinkCustomPreview sends a text with a preview the caller supplies;
// the image, when given, becomes the card's JPEG thumbnail.
func (s *Server) sendLinkCustomPreview(c *Ctx) error {
	var req struct {
		ChatID  string `json:"chatId"`
		Text    string `json:"text"`
		ReplyTo string `json:"reply_to"`
		Preview *struct {
			URL         string      `json:"url"`
			Title       string      `json:"title"`
			Description string      `json:"description"`
			Image       *media.File `json:"image"`
		} `json:"preview"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	// This route has WAHA's validation pipe.
	var problems []string
	if req.ChatID == "" {
		problems = append(problems, "chatId must be a string")
	}
	if req.Text == "" {
		problems = append(problems, "text must be a string")
	}
	if req.Preview == nil {
		problems = append(problems, "preview should not be null or undefined")
	} else {
		for _, f := range [][2]string{{"url", req.Preview.URL}, {"title", req.Preview.Title}, {"description", req.Preview.Description}} {
			if f[1] == "" {
				problems = append(problems, "preview."+f[0]+" must be a string")
			}
		}
	}
	if len(problems) > 0 {
		return ErrBadRequest(problems)
	}
	if !strings.Contains(req.Text, req.Preview.URL) {
		return ErrBadRequest(`"text" must include the URL provided in the "preview.url"`)
	}
	preview := &pb.LinkPreview{MatchedText: req.Preview.URL, Title: req.Preview.Title, Description: req.Preview.Description}
	if img := req.Preview.Image; img != nil && (img.URL != "" || img.Data != "") {
		blob, err := s.Media.Load(c.R.Context(), img, media.KindImage)
		if err != nil {
			return mediaError(err)
		}
		preview.JpegThumbnail = s.Media.Thumbnail(c.R.Context(), blob.Data)
	}
	return s.doSendText(c, textRequest{ChatID: req.ChatID, Text: req.Text, ReplyTo: req.ReplyTo}, http.StatusCreated, preview)
}

// --- media ---

type mediaRequest struct {
	ChatID   string      `json:"chatId"`
	File     *media.File `json:"file"`
	Caption  string      `json:"caption"`
	ReplyTo  string      `json:"reply_to"`
	Mentions []string    `json:"mentions"`
	Convert  bool        `json:"convert"`
	AsNote   bool        `json:"asNote"`
}

// mediaChunk is the size of each chunk streamed to the core.
const mediaChunk = 64 << 10

// sendMediaAs is sendImage, sendFile, sendVoice, sendVideo and sendSticker.
func (s *Server) sendMediaAs(kind media.Kind) Handler {
	return func(c *Ctx) error {
		var req mediaRequest
		if err := c.Bind(&req); err != nil {
			return err
		}
		to, err := chatJID(req.ChatID)
		if err != nil {
			return err
		}
		if req.File == nil {
			return ErrBadRequest("file is required")
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		ms, err := mentions(req.Mentions)
		if err != nil {
			return err
		}
		q, _, err := s.quote(c, to, req.ReplyTo)
		if err != nil {
			return err
		}
		ctx := c.R.Context()
		blob, err := s.Media.Load(ctx, req.File, kind)
		if err != nil {
			return mediaError(err)
		}
		hdr := &pb.SendMediaHeader{
			Account: acc, To: to, MimeType: blob.Mimetype, Caption: req.Caption,
			Mentions: ms, Quote: q, Filename: blob.Filename,
		}
		conv := s.Media.Converter()
		switch kind {
		case media.KindImage:
			hdr.MediaType = pb.MediaType_MEDIA_TYPE_IMAGE
		case media.KindDocument:
			hdr.MediaType = pb.MediaType_MEDIA_TYPE_DOCUMENT
		case media.KindSticker:
			hdr.MediaType = pb.MediaType_MEDIA_TYPE_STICKER
		case media.KindVoice:
			hdr.MediaType, hdr.Ptt = pb.MediaType_MEDIA_TYPE_AUDIO, true
			if req.Convert {
				if blob.Data, err = conv.Voice(ctx, blob.Data); err != nil {
					return err
				}
				hdr.MimeType = media.MimeVoice
			}
			// Best effort, as in Baileys: a voice note without a waveform
			// still plays.
			if secs, wave, err := conv.VoiceInfo(ctx, blob.Data); err == nil {
				hdr.Seconds, hdr.Waveform = secs, wave
			}
		case media.KindVideo:
			hdr.MediaType, hdr.Ptv = pb.MediaType_MEDIA_TYPE_VIDEO, req.AsNote
			if req.Convert {
				if blob.Data, err = conv.Video(ctx, blob.Data); err != nil {
					return err
				}
				hdr.MimeType = media.MimeVideo
			}
			if secs, err := conv.Duration(ctx, blob.Data); err == nil {
				hdr.Seconds = secs
			}
		}
		res, err := s.streamMedia(ctx, hdr, blob.Data)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, sent(c, to, res.GetKey(), res, sentDetail{Body: hdr.GetCaption(), HasMedia: true}))
	}
}

// streamMedia sends a header and the bytes in chunks. A failed Send means
// the core ended the stream; its status comes from CloseAndRecv.
func (s *Server) streamMedia(ctx context.Context, hdr *pb.SendMediaHeader, data []byte) (*pb.SendResult, error) {
	stream, err := s.Core.Messaging.SendMedia(ctx)
	if err != nil {
		return nil, err
	}
	if err := sendChunks(data, func(chunk []byte) error {
		return stream.Send(&pb.SendMediaChunk{Part: &pb.SendMediaChunk_Chunk{Chunk: chunk}})
	}, func() error {
		return stream.Send(&pb.SendMediaChunk{Part: &pb.SendMediaChunk_Header{Header: hdr}})
	}); err != nil {
		return nil, err
	}
	return stream.CloseAndRecv()
}

// sendChunks sends a header and then data in mediaChunk pieces. io.EOF from
// a send is the server closing the stream early, which the caller learns
// about from CloseAndRecv, so it is not an error here.
func sendChunks(data []byte, chunk func([]byte) error, header func() error) error {
	if err := header(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	for off := 0; off < len(data); off += mediaChunk {
		if err := chunk(data[off:min(off+mediaChunk, len(data))]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return nil
}

// --- contacts ---

type vcardContact struct {
	FullName     string `json:"fullName"`
	Organization string `json:"organization"`
	PhoneNumber  string `json:"phoneNumber"`
	WhatsappID   string `json:"whatsappId"`
	VCard        string `json:"vcard"`
}

// card is GOWS's display name and toVcardV3: the given vcard verbatim, or
// one built from the fields.
func (v vcardContact) card() (name, vcard string) {
	vcard = v.VCard
	if vcard == "" {
		var b strings.Builder
		b.WriteString("BEGIN:VCARD\nVERSION:3.0\nFN:" + v.FullName + "\n")
		if v.Organization != "" {
			b.WriteString("ORG:" + v.Organization + ";\n")
		}
		waid := v.WhatsappID
		if waid == "" {
			waid = strings.Map(func(r rune) rune {
				if r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, v.PhoneNumber)
		}
		b.WriteString("TEL;type=CELL;type=VOICE;waid=" + waid + ":" + v.PhoneNumber + "\nEND:VCARD")
		vcard = b.String()
	}
	name = v.FullName
	if name == "" {
		for _, line := range strings.Split(vcard, "\n") {
			if after, ok := strings.CutPrefix(strings.TrimSpace(line), "FN:"); ok {
				name = after
				break
			}
		}
	}
	return name, vcard
}

// sendContactVcard sends each contact as its own message (the core sends one
// contact per message, WAHA one message with them all) and answers the
// last one.
func (s *Server) sendContactVcard(c *Ctx) error {
	var req struct {
		ChatID   string         `json:"chatId"`
		Contacts []vcardContact `json:"contacts"`
		ReplyTo  string         `json:"reply_to"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	to, err := chatJID(req.ChatID)
	if err != nil {
		return err
	}
	if len(req.Contacts) == 0 {
		return ErrBadRequest("contacts is required")
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	q, _, err := s.quote(c, to, req.ReplyTo)
	if err != nil {
		return err
	}
	var res *pb.SendResult
	for _, ct := range req.Contacts {
		name, vcard := ct.card()
		if res, err = s.Core.Messaging.SendContact(c.R.Context(), &pb.SendContactRequest{
			Account: acc, To: to, DisplayName: name, Vcard: vcard, Quote: q,
		}); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusCreated, sent(c, to, res.GetKey(), res))
}

// --- polls ---

func (s *Server) sendPoll(c *Ctx) error {
	var req struct {
		ChatID string `json:"chatId"`
		Poll   *struct {
			Name            string   `json:"name"`
			Options         []string `json:"options"`
			MultipleAnswers bool     `json:"multipleAnswers"`
		} `json:"poll"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	// This route has WAHA's validation pipe.
	if req.Poll == nil {
		return ErrBadRequest([]string{"poll should not be null or undefined"})
	}
	if req.Poll.Name == "" || len(req.Poll.Options) == 0 {
		return ErrBadRequest([]string{"poll.name must be a string", "poll.options must be an array"})
	}
	to, err := chatJID(req.ChatID)
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	// A chat takes how many options may be picked; a channel, as WA Web
	// publishes it, 1 for a single answer and 0 for several.
	selectable := uint32(1)
	switch {
	case req.Poll.MultipleAnswers && waha.IsNewsletter(to.GetValue()):
		selectable = 0
	case req.Poll.MultipleAnswers:
		selectable = uint32(len(req.Poll.Options))
	}
	res, err := s.Core.Messaging.SendPoll(c.R.Context(), &pb.SendPollRequest{
		Account: acc, To: to, Name: req.Poll.Name, Options: req.Poll.Options, SelectableCount: selectable,
	})
	if err != nil {
		return err
	}
	// The secret is what votes are keyed by, and nobody else keeps it.
	if err := s.Store.PutPoll(c.R.Context(), c.Session, store.Poll{
		WAID: res.GetKey().GetId(), Chat: to.GetValue(), Creator: c.myJID(to.GetValue()),
		Options: req.Poll.Options, Secret: res.GetMessageSecret(),
	}); err != nil {
		s.Log.Error("store poll", "session", c.Session, "id", res.GetKey().GetId(), "err", err)
	}
	return c.JSON(http.StatusCreated, sent(c, to, res.GetKey(), res))
}

// sendPollVote votes in a poll this edge sent or stored: the secret, the
// creator and the options come from the store, never from the caller.
func (s *Server) sendPollVote(c *Ctx) error {
	var req struct {
		ChatID        string `json:"chatId"`
		PollMessageID string `json:"pollMessageId"`
		PollServerID  any    `json:"pollServerId"`
		Votes         []any  `json:"votes"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.PollMessageID == "" {
		return ErrBadRequest([]string{"pollMessageId must be a string"})
	}
	if req.Votes == nil {
		return ErrBadRequest([]string{"votes must be an array"})
	}
	serverID, err := pollServerID(req.PollServerID)
	if err != nil {
		return err
	}
	to, err := chatJID(req.ChatID)
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	// WAHA's examples send a list of option names; its schema says a list of
	// lists. Both are taken.
	var votes []string
	for _, v := range req.Votes {
		switch x := v.(type) {
		case string:
			votes = append(votes, x)
		case []any:
			for _, y := range x {
				if name, ok := y.(string); ok {
					votes = append(votes, name)
				}
			}
		default:
			return ErrBadRequest([]string{"each value in votes must be a string"})
		}
	}
	id, err := bareID(req.PollMessageID)
	if err != nil {
		return err
	}
	if waha.IsNewsletter(to.GetValue()) {
		return s.sendChannelPollVote(c, acc, to, id, serverID, votes)
	}
	poll, err := s.pollFor(c, id)
	if err != nil {
		return err
	}
	if err := checkVotes(poll.Options, votes); err != nil {
		return err
	}
	res, err := s.Core.Messaging.SendPollVote(c.R.Context(), &pb.SendPollVoteRequest{
		Account: acc, Chat: to, PollId: id, PollCreator: &pb.Jid{Value: poll.Creator},
		MessageSecret: poll.Secret, Options: votes,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, sent(c, to, res.GetKey(), res))
}

// checkVotes refuses a vote for a name the poll does not have, when the
// options are known. The comparison is exact: a channel vote names each
// option by the hash of its spelling, so a retyped emoji would be a vote the
// server does not count.
func checkVotes(options, votes []string) error {
	if len(options) == 0 {
		return nil
	}
	for _, v := range votes {
		if !slices.Contains(options, v) {
			return ErrBadRequest(fmt.Sprintf("%q is not an option of the poll", v))
		}
	}
	return nil
}

// pollServerID reads WAHA's optional pollServerId, 0 when absent.
func pollServerID(v any) (uint64, error) {
	if v == nil {
		return 0, nil
	}
	// A server id is far below 2^53, where a JSON number stops being exact.
	n, ok := v.(float64)
	if !ok || n < 1 || n > 1<<53 || n != math.Trunc(n) {
		return 0, ErrBadRequest([]string{numberMessage("pollServerId")})
	}
	return uint64(n), nil
}

// The history read that finds a channel poll the caller gave no
// pollServerId for: newest first, at most channelPollPages pages.
const (
	channelPollPage  = 100
	channelPollPages = 3
)

// sendChannelPollVote votes in a channel poll. A channel is not E2E, so
// there is no secret: the vote names the poll by its server id and each
// option by the sha256 of its name. Every vote carries the whole selection;
// an empty one removes the vote.
func (s *Server) sendChannelPollVote(c *Ctx, acc *pb.AccountRef, channel *pb.Jid, id string, serverID uint64, votes []string) error {
	ctx := c.R.Context()
	poll, err := s.knownPoll(c, id)
	if err != nil {
		return err
	}
	if serverID == 0 {
		row, err := s.channelPollRow(ctx, acc, channel, id)
		if err != nil {
			return err
		}
		serverID = row.GetServerId()
		if len(poll.Options) == 0 {
			poll.Options = pollOptions(row.GetMessage().GetRawMessage())
		}
	}
	if err := checkVotes(poll.Options, votes); err != nil {
		return err
	}
	// The core refuses a repeated hash; a selection is a set.
	var hashes [][]byte
	for i, v := range votes {
		if slices.Contains(votes[:i], v) {
			continue
		}
		h := sha256.Sum256([]byte(v))
		hashes = append(hashes, h[:])
	}
	res, err := s.Core.Newsletters.SendNewsletterPollVote(ctx, &pb.SendNewsletterPollVoteRequest{
		Account: acc, Jid: channel, ServerId: serverID, OptionHashes: hashes,
	})
	if err != nil {
		return err
	}
	// A vote puts no message in the channel: the id is the vote stanza's.
	return c.JSON(http.StatusCreated, sent(c, channel, &pb.MessageKey{Chat: channel, Id: res.GetStanzaId(), FromMe: true}, res))
}

// channelPollRow pages a channel's history back to the poll with this
// message id, which is where its server id is.
func (s *Server) channelPollRow(ctx context.Context, acc *pb.AccountRef, channel *pb.Jid, id string) (*pb.NewsletterMessage, error) {
	var before uint64
	for range channelPollPages {
		res, err := s.Core.Newsletters.GetNewsletterMessages(ctx, &pb.GetNewsletterMessagesRequest{
			Account: acc, Jid: channel, Count: channelPollPage, Before: before,
		})
		if err != nil {
			return nil, err
		}
		oldest := before
		for _, row := range res.GetMessages() {
			if row.GetMessage().GetKey().GetId() == id {
				return row, nil
			}
			if oldest == 0 || row.GetServerId() < oldest {
				oldest = row.GetServerId()
			}
		}
		if oldest == 0 || oldest == before {
			break // the start of the channel
		}
		before = oldest
	}
	return nil, ErrUnprocessable(fmt.Sprintf("Poll with id '%s' not found in the channel's last %d messages, pass its pollServerId",
		id, channelPollPage*channelPollPages))
}

// pollOptions are the option names of the poll in a raw wa.Message, as it
// spells them.
func pollOptions(raw []byte) []string {
	m, err := waha.DecodeRaw(raw)
	if err != nil {
		return nil
	}
	var out []string
	for _, o := range pollCreation(m).GetOptions() {
		out = append(out, o.GetOptionName())
	}
	return out
}

// pollFor gathers what a vote in a chat poll needs, which includes the
// secret and the creator.
func (s *Server) pollFor(c *Ctx, id string) (store.Poll, error) {
	poll, err := s.knownPoll(c, id)
	if err != nil {
		return poll, err
	}
	if len(poll.Secret) == 0 || poll.Creator == "" {
		return poll, ErrUnprocessable(fmt.Sprintf("Poll with id '%s' not found", id))
	}
	return poll, nil
}

// knownPoll is what this edge has on a poll: what was stored when it was
// sent, completed by the stored poll message (a received poll carries its
// secret and options in its wa.Message).
func (s *Server) knownPoll(c *Ctx, id string) (store.Poll, error) {
	ctx := c.R.Context()
	poll, err := s.Store.GetPoll(ctx, c.Session, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return poll, err
	}
	if len(poll.Secret) == 0 || len(poll.Options) == 0 || poll.Creator == "" {
		st, ok, err := s.lookup(ctx, c.Session, id)
		if err != nil {
			return poll, err
		}
		if ok {
			if poll.Creator == "" {
				poll.Creator = st.sender
				if st.msg.FromMe {
					poll.Creator = c.myJID(st.in.GetChat().GetValue())
				}
			}
			if len(poll.Options) == 0 {
				poll.Options = pollOptions(st.in.GetRawMessage())
			}
			if raw, err := waha.DecodeRaw(st.in.GetRawMessage()); err == nil && len(poll.Secret) == 0 {
				poll.Secret = raw.GetMessageContextInfo().GetMessageSecret()
			}
		}
	}
	return poll, nil
}

// pollCreation is the poll in a message, in whichever version it came.
func pollCreation(m *waE2E.Message) *waE2E.PollCreationMessage {
	for _, p := range []*waE2E.PollCreationMessage{
		m.GetPollCreationMessage(), m.GetPollCreationMessageV2(), m.GetPollCreationMessageV3(),
		m.GetPollCreationMessageV5(), m.GetPollCreationMessageV6(),
	} {
		if p != nil {
			return p
		}
	}
	return nil
}

// --- reactions, stars, read receipts, typing ---

func (s *Server) setReaction(c *Ctx) error {
	var req struct {
		MessageID string `json:"messageId"`
		Reaction  string `json:"reaction"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	id, err := waha.ParseMessageID(req.MessageID)
	if err != nil {
		return errBadMessageID()
	}
	key, err := s.targetKey(c, id)
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	res, err := s.Core.Messaging.SendReaction(c.R.Context(), &pb.SendReactionRequest{Account: acc, Target: key, Emoji: req.Reaction})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, sent(c, key.GetChat(), res.GetKey(), res))
}

// targetKey is the core key of a message named by a serialized id; my own
// message in a group names me as its participant.
func (s *Server) targetKey(c *Ctx, id waha.MessageID) (*pb.MessageKey, error) {
	key, err := id.Key()
	if err != nil {
		return nil, errBadMessageID()
	}
	if waha.IsGroup(id.Chat) && key.GetParticipant() == nil && id.FromMe {
		key.Participant = &pb.Jid{Value: c.myJID(key.GetChat().GetValue())}
	}
	return key, nil
}

// setStar stars or unstars a message. GOWS answers 501; the core can do it.
// A bare id is taken with the chatId, the stored message saying whose it is.
func (s *Server) setStar(c *Ctx) error {
	var req struct {
		MessageID string `json:"messageId"`
		ChatID    string `json:"chatId"`
		Star      bool   `json:"star"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.MessageID == "" {
		return ErrBadRequest("messageId is required")
	}
	var key *pb.MessageKey
	if strings.Contains(req.MessageID, "_") {
		id, err := waha.ParseMessageID(req.MessageID)
		if err != nil {
			return errBadMessageID()
		}
		if key, err = s.targetKey(c, id); err != nil {
			return err
		}
	} else {
		to, err := chatJID(req.ChatID)
		if err != nil {
			return err
		}
		key = &pb.MessageKey{Chat: to, Id: req.MessageID}
		st, ok, err := s.lookup(c.R.Context(), c.Session, req.MessageID)
		if err != nil {
			return err
		}
		if ok {
			key.FromMe = st.msg.FromMe
			if waha.IsGroup(to.GetValue()) && st.sender != "" {
				key.Participant = &pb.Jid{Value: st.sender}
			}
		}
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	if _, err := s.Core.Messaging.StarMessage(c.R.Context(), &pb.StarMessageRequest{Account: acc, Target: key, Starred: req.Star}); err != nil {
		return err
	}
	return c.Empty(http.StatusOK)
}

// sendSeen sends read receipts. With ids, one receipt per sender for the
// incoming ones (mine are skipped, as in WAHA). Without, for the chat's
// unread incoming messages this edge stored, and the chat is marked read on
// my other devices too.
func (s *Server) sendSeen(c *Ctx) error {
	var req struct {
		ChatID      string   `json:"chatId"`
		MessageID   string   `json:"messageId"`
		MessageIDs  []string `json:"messageIds"`
		Participant string   `json:"participant"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	to, err := chatJID(req.ChatID)
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	ids := req.MessageIDs
	if req.MessageID != "" {
		ids = append(ids, req.MessageID)
	}
	if len(ids) == 0 {
		chat := waha.FromPB(to)
		limit := defaultReadLimitDM
		if waha.IsGroup(chat) {
			limit = defaultReadLimitGroup
		}
		read, err := s.readUnread(c.R.Context(), c.Session, acc, chat, to, limit, defaultReadDays)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, map[string]any{"ids": read})
	}
	if err := s.markRead(c, acc, ids, req.Participant); err != nil {
		return err
	}
	waIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if m, err := waha.ParseMessageID(id); err == nil {
			waIDs = append(waIDs, m.ID)
		} else {
			waIDs = append(waIDs, id)
		}
	}
	if err := s.Store.UpdateAck(c.R.Context(), c.Session, waIDs, waha.AckRead); err != nil {
		return err
	}
	return c.Empty(http.StatusCreated)
}

// markRead sends one MarkRead per (chat, sender) for the serialized ids not
// from me. participant is the sender for ids that name none.
func (s *Server) markRead(c *Ctx, acc *pb.AccountRef, ids []string, participant string) error {
	type batch struct{ chat, sender string }
	batches := map[batch][]string{}
	var order []batch
	for _, raw := range ids {
		id, err := waha.ParseMessageID(raw)
		if err != nil {
			return errBadMessageID()
		}
		if id.FromMe {
			continue
		}
		b := batch{}
		if b.chat, err = waha.JIDFromChatID(id.Chat); err != nil {
			return errBadMessageID()
		}
		sender := id.Participant
		if sender == "" {
			sender = participant
		}
		if sender != "" {
			b.sender, _ = waha.JIDFromChatID(sender)
		}
		if _, seen := batches[b]; !seen {
			order = append(order, b)
		}
		batches[b] = append(batches[b], id.ID)
	}
	for _, b := range order {
		r := &pb.MarkReadRequest{Account: acc, Chat: &pb.Jid{Value: b.chat}, MessageIds: batches[b]}
		if b.sender != "" {
			r.Sender = &pb.Jid{Value: b.sender}
		}
		if _, err := s.Core.Messaging.MarkRead(c.R.Context(), r); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) typing(state pb.PresenceState) Handler {
	return func(c *Ctx) error {
		var req struct {
			ChatID string `json:"chatId"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		to, err := chatJID(req.ChatID)
		if err != nil {
			return err
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		if _, err := s.Core.Messaging.SendPresence(c.R.Context(), &pb.SendPresenceRequest{Account: acc, Chat: to, State: state}); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, map[string]any{"result": true})
	}
}

// --- interactive replies ---

// sendButtonsReply answers a buttons message by the button's id. GOWS
// answers 501; the core can do it. The offer's own message goes along when
// this edge stored it, as the official client sends it.
func (s *Server) sendButtonsReply(c *Ctx) error {
	var req struct {
		ChatID              string `json:"chatId"`
		ReplyTo             string `json:"replyTo"`
		SelectedDisplayText string `json:"selectedDisplayText"`
		SelectedButtonID    string `json:"selectedButtonID"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	// This route has a validation pipe in WAHA.
	var problems []string
	if req.ChatID == "" {
		problems = append(problems, "chatId must be a string")
	}
	if req.SelectedDisplayText == "" {
		problems = append(problems, "selectedDisplayText must be a string")
	}
	if req.SelectedButtonID == "" {
		problems = append(problems, "selectedButtonID must be a string")
	}
	if len(problems) > 0 {
		return ErrBadRequest(problems)
	}
	to, err := chatJID(req.ChatID)
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	q, raw, err := s.quote(c, to, req.ReplyTo)
	if err != nil {
		return err
	}
	res, err := s.Core.Messaging.SendInteractiveReply(c.R.Context(), &pb.SendInteractiveReplyRequest{
		Account: acc, To: to, Quote: q, QuotedMessage: raw,
		Reply: &pb.SendInteractiveReplyRequest_Button{Button: &pb.ButtonReply{
			SelectedId: req.SelectedButtonID, DisplayText: req.SelectedDisplayText,
		}},
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, sent(c, to, res.GetKey(), res))
}
