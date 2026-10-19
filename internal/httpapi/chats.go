package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/sessions"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// WAHA's defaults for the chats routes (chats.dto.ts, convertors.ts).
const (
	defaultChatsLimit     = 20
	defaultMessagesLimit  = 10
	defaultReadDays       = 7
	defaultReadLimitDM    = 30
	defaultReadLimitGroup = 100
	pictureCacheTTL       = 24 * time.Hour
	dayMs                 = 24 * 60 * 60 * 1000
	allChats              = "all" // GET /api/{session}/chats/all/messages
)

// registerChats serves the Chats tag. Reads come from the store, so they
// work while the session is stopped; writes go through the core and need
// the session WORKING. Pinning a message and clearing a chat have no core
// RPC and answer 501.
func (s *Server) registerChats(r chi.Router) {
	read := Route{Action: ActRead, From: FromPath}
	write := Route{Action: ActSend, From: FromPath, Working: true}
	s.route(r, http.MethodGet, "/api/{session}/chats", read, s.listChats)
	s.route(r, http.MethodGet, "/api/{session}/chats/overview", read, s.chatsOverview)
	s.route(r, http.MethodPost, "/api/{session}/chats/overview", read, s.chatsOverviewPost)
	s.route(r, http.MethodDelete, "/api/{session}/chats/{chatId}", write, s.deleteChat)
	s.route(r, http.MethodGet, "/api/{session}/chats/{chatId}/picture", Route{Action: ActRead, From: FromPath, Working: true}, s.chatPicture)
	s.route(r, http.MethodGet, "/api/{session}/chats/{chatId}/messages", read, s.chatMessages)
	s.route(r, http.MethodPost, "/api/{session}/chats/{chatId}/messages/read", write, s.readChat)
	s.route(r, http.MethodGet, "/api/{session}/chats/{chatId}/messages/{messageId}", read, s.chatMessage)
	s.route(r, http.MethodDelete, "/api/{session}/chats/{chatId}/messages/{messageId}", write, s.deleteChatMessage)
	s.route(r, http.MethodPut, "/api/{session}/chats/{chatId}/messages/{messageId}", write, s.editChatMessage)
	s.route(r, http.MethodPost, "/api/{session}/chats/{chatId}/archive", write, s.archiveChat(true))
	s.route(r, http.MethodPost, "/api/{session}/chats/{chatId}/unarchive", write, s.archiveChat(false))
	s.route(r, http.MethodPost, "/api/{session}/chats/{chatId}/unread", write, s.unreadChat)
	s.registerChatsLegacy(r)
}

// knownSession is the route's session for a store read: it need not be
// running, but it must exist (WAHA's 422 otherwise).
func (c *Ctx) knownSession() (store.Session, error) {
	sess, err := c.Sess()
	var he *Error
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		return sess, ErrSessionMissing(c.Session)
	}
	return sess, err
}

// chatParam is the {chatId} path parameter in WAHA form and as the core's
// JID ("5511999999999", "...@c.us" and "...@s.whatsapp.net" are the same
// chat).
func (c *Ctx) chatParam() (string, *pb.Jid, error) {
	jid, err := waha.ToPB(c.Param("chatId"))
	if err != nil {
		return "", nil, ErrBadRequest("Invalid chatId: " + c.Param("chatId"))
	}
	return waha.FromPB(jid), jid, nil
}

// queryInt reads an optional number; class-validator's message otherwise.
func (c *Ctx) queryInt(name string, def int) (int, error) {
	v := c.Query(name)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, ErrBadRequest([]string{name + " must be a number conforming to the specified constraints"})
	}
	return int(f), nil
}

// queryBool reads an optional boolean the way WAHA's BooleanString does.
func (c *Ctx) queryBool(name string) (*bool, error) {
	var b bool
	switch c.Query(name) {
	case "":
		return nil, nil
	case "true", "True", "1":
		b = true
	case "false", "False", "0":
	default:
		return nil, ErrBadRequest([]string{name + " must be a boolean value"})
	}
	return &b, nil
}

// queryList reads a list given as repeated parameters, comma-separated, or
// both.
func (c *Ctx) queryList(name string) []string {
	var out []string
	for _, v := range c.R.URL.Query()[name] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// mediaOptions are the download options of a message read: the server's,
// overridden by downloadMedia and downloadMediaMimetypes.
func (c *Ctx) mediaOptions() (waha.MediaOptions, error) {
	o := waha.MediaOptions{BaseURL: c.S.Config.BaseURL, Session: c.Session, Download: c.S.Config.DownloadMedia, Mimetypes: c.S.Config.MediaMimetypes}
	dl, err := c.queryBool("downloadMedia")
	if err != nil {
		return o, err
	}
	if dl != nil {
		o.Download = *dl
	}
	if m := c.queryList("downloadMediaMimetypes"); len(m) > 0 {
		o.Mimetypes = m
	}
	return o, nil
}

// messageBodies renders stored messages for a read, with media URLs for the
// read's options.
func messageBodies(ms []store.Message, opts waha.MediaOptions) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(ms))
	for _, m := range ms {
		b, err := waha.RefreshMedia(m.Body, m.WAID, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// chatView is a chat as GOWS lists it.
type chatView struct {
	ID                    string  `json:"id"`
	Name                  *string `json:"name"`
	ConversationTimestamp int64   `json:"conversationTimestamp"` // seconds
}

// chatName is the chat's own name, else the contact's saved name, else its
// push name; nil when nothing is known.
func (s *Server) chatName(ctx context.Context, ch store.Chat) *string {
	if ch.Name != "" {
		return &ch.Name
	}
	if ct, err := s.Store.GetContact(ctx, ch.Session, ch.ID); err == nil {
		for _, n := range []string{ct.Name, ct.PushName} {
			if n != "" {
				return &n
			}
		}
	}
	return nil
}

func (s *Server) chatView(ctx context.Context, ch store.Chat) chatView {
	return chatView{ID: ch.ID, Name: s.chatName(ctx, ch), ConversationTimestamp: ch.ConversationTS / 1000}
}

// listChats is GET /api/{session}/chats: GOWS sorts by id, ascending, unless
// told otherwise.
func (s *Server) listChats(c *Ctx) error {
	if _, err := c.knownSession(); err != nil {
		return err
	}
	f := store.ChatFilter{SortBy: "id", Ascending: c.Query("sortOrder") != "desc"}
	switch by := c.Query("sortBy"); by {
	case "":
	case "conversationTimestamp", "id", "name":
		f.SortBy = by
	default:
		return ErrBadRequest([]string{"sortBy must be one of the following values: conversationTimestamp, id, name"})
	}
	var err error
	if f.Limit, err = c.queryInt("limit", defaultChatsLimit); err != nil {
		return err
	}
	if f.Offset, err = c.queryInt("offset", 0); err != nil {
		return err
	}
	chats, err := s.Store.ListChats(c.R.Context(), c.Session, f)
	if err != nil {
		return err
	}
	out := make([]chatView, 0, len(chats))
	for _, ch := range chats {
		out = append(out, s.chatView(c.R.Context(), ch))
	}
	return c.JSON(http.StatusOK, out)
}

// chatSummary is WAHA's ChatSummary.
type chatSummary struct {
	ID          string          `json:"id"`
	Name        *string         `json:"name"`
	Picture     *string         `json:"picture"`
	LastMessage json.RawMessage `json:"lastMessage"`
	Chat        chatView        `json:"_chat"`
}

func (s *Server) chatsOverview(c *Ctx) error {
	limit, err := c.queryInt("limit", defaultChatsLimit)
	if err != nil {
		return err
	}
	offset, err := c.queryInt("offset", 0)
	if err != nil {
		return err
	}
	return s.overview(c, http.StatusOK, limit, offset, c.queryList("ids"))
}

func (s *Server) chatsOverviewPost(c *Ctx) error {
	var body struct {
		Pagination struct {
			Limit  *int `json:"limit"`
			Offset int  `json:"offset"`
		} `json:"pagination"`
		Filter struct {
			IDs []string `json:"ids"`
		} `json:"filter"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	limit := defaultChatsLimit
	if body.Pagination.Limit != nil {
		limit = *body.Pagination.Limit
	}
	return s.overview(c, http.StatusCreated, limit, body.Pagination.Offset, body.Filter.IDs)
}

// overview is the chats overview: newest conversation first, each chat with
// its name, picture and last message (media never downloaded, as in WAHA).
func (s *Server) overview(c *Ctx, status, limit, offset int, ids []string) error {
	sess, err := c.knownSession()
	if err != nil {
		return err
	}
	ctx := c.R.Context()
	f := store.ChatFilter{Limit: limit, Offset: offset, SortBy: "conversationTimestamp"}
	for _, id := range ids {
		jid, err := waha.ToPB(id)
		if err != nil {
			return ErrBadRequest("Invalid chatId: " + id)
		}
		f.IDs = append(f.IDs, waha.FromPB(jid))
	}
	chats, err := s.Store.ListChats(ctx, c.Session, f)
	if err != nil {
		return err
	}
	noMedia := waha.MediaOptions{}
	out := make([]chatSummary, 0, len(chats))
	for _, ch := range chats {
		view := s.chatView(ctx, ch)
		sum := chatSummary{ID: ch.ID, Name: view.Name, Chat: view, LastMessage: json.RawMessage("null")}
		last, err := s.Store.ListMessages(ctx, c.Session, ch.ID, store.MessageFilter{Limit: 1})
		if err != nil {
			return err
		}
		if bodies, err := messageBodies(last, noMedia); err == nil && len(bodies) == 1 {
			sum.LastMessage = bodies[0]
		}
		if sess.Status == sessions.StatusWorking {
			sum.Picture = s.picture(ctx, sess, ch.ID, false)
		}
		out = append(out, sum)
	}
	return c.JSON(status, out)
}

// pictureCacheMax bounds the picture cache.
const pictureCacheMax = 10_000

// pictures caches profile picture URLs per session and chat for a day, as
// WAHA does (a miss is cached too: asking again costs a server round trip).
var pictures = struct {
	sync.Mutex
	m map[string]cachedPicture
}{m: map[string]cachedPicture{}}

type cachedPicture struct {
	url *string
	at  time.Time
}

// picture is a chat's picture URL, nil when it has none or the server would
// not say.
func (s *Server) picture(ctx context.Context, sess store.Session, chat string, refresh bool) *string {
	key := fmt.Sprintf("%p|%s|%s", s, sess.Name, chat) // per server: tests run several in one process
	pictures.Lock()
	hit, ok := pictures.m[key]
	pictures.Unlock()
	if ok && !refresh && time.Since(hit.at) < pictureCacheTTL {
		return hit.url
	}
	var url *string
	if jid, err := waha.ToPB(chat); err == nil {
		res, err := s.Core.Contacts.GetProfilePicture(ctx, &pb.JidRequest{Account: sessions.Ref(sess), Jid: jid})
		if err == nil && res.GetUrl() != "" {
			u := res.GetUrl()
			url = &u
		} else if err != nil {
			s.Log.Debug("profile picture", "session", sess.Name, "chat", chat, "err", err)
		}
	}
	pictures.Lock()
	if len(pictures.m) >= pictureCacheMax {
		// Bounded: expired entries go first; a cache still full of live
		// ones (somebody walking made-up chat ids) starts over.
		for k, v := range pictures.m {
			if time.Since(v.at) >= pictureCacheTTL {
				delete(pictures.m, k)
			}
		}
		if len(pictures.m) >= pictureCacheMax {
			clear(pictures.m)
		}
	}
	pictures.m[key] = cachedPicture{url: url, at: time.Now()}
	pictures.Unlock()
	return url
}

func (s *Server) chatPicture(c *Ctx) error {
	chat, _, err := c.chatParam()
	if err != nil {
		return err
	}
	refresh, err := c.queryBool("refresh")
	if err != nil {
		return err
	}
	sess, err := c.Sess()
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]*string{"url": s.picture(c.R.Context(), sess, chat, refresh != nil && *refresh)})
}

func (s *Server) chatMessages(c *Ctx) error {
	chat := allChats
	if c.Param("chatId") != allChats {
		var err error
		if chat, _, err = c.chatParam(); err != nil {
			return err
		}
	}
	return s.listMessages(c, chat)
}

// listMessages is a chat's messages with WAHA's filters, newest first by
// default. chat "all" reads every chat.
func (s *Server) listMessages(c *Ctx, chat string) error {
	if _, err := c.knownSession(); err != nil {
		return err
	}
	var f store.MessageFilter
	var err error
	if f.Limit, err = c.queryInt("limit", defaultMessagesLimit); err != nil {
		return err
	}
	if f.Offset, err = c.queryInt("offset", 0); err != nil {
		return err
	}
	switch c.Query("sortBy") {
	case "", "timestamp", "messageTimestamp":
	default:
		return ErrBadRequest([]string{"sortBy must be one of the following values: timestamp, messageTimestamp"})
	}
	switch c.Query("sortOrder") {
	case "", "desc":
	case "asc":
		f.Ascending = true
	default:
		return ErrBadRequest([]string{"sortOrder must be one of the following values: desc, asc"})
	}
	// WAHA's timestamp filters are seconds; the store keeps ms.
	if v := c.Query("filter.timestamp.gte"); v != "" {
		n, err := c.queryInt("filter.timestamp.gte", 0)
		if err != nil {
			return err
		}
		ms := int64(n) * 1000
		f.TimestampGTE = &ms
	}
	if v := c.Query("filter.timestamp.lte"); v != "" {
		n, err := c.queryInt("filter.timestamp.lte", 0)
		if err != nil {
			return err
		}
		ms := int64(n)*1000 + 999
		f.TimestampLTE = &ms
	}
	if f.FromMe, err = c.queryBool("filter.fromMe"); err != nil {
		return err
	}
	if name := c.Query("filter.ack"); name != "" {
		ack, ok := waha.AckFromName(name)
		if !ok {
			return ErrBadRequest([]string{"filter.ack must be one of the following values: ERROR, PENDING, SERVER, DEVICE, READ, PLAYED"})
		}
		f.Ack = &ack
	}
	opts, err := c.mediaOptions()
	if err != nil {
		return err
	}
	if chat == allChats {
		chat = ""
	}
	ms, err := s.Store.ListMessages(c.R.Context(), c.Session, chat, f)
	if err != nil {
		return err
	}
	bodies, err := messageBodies(ms, opts)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, bodies)
}

// findMessage looks a message up by WAHA's serialized id or by the bare
// WhatsApp id (WAHA accepts both here).
func (s *Server) findMessage(c *Ctx, messageID string) (store.Message, error) {
	ctx := c.R.Context()
	if id, err := waha.ParseMessageID(messageID); err == nil {
		m, err := s.Store.GetMessage(ctx, c.Session, id.String())
		if err == nil || !errors.Is(err, store.ErrNotFound) {
			return m, err
		}
		return s.Store.GetMessageByWAID(ctx, c.Session, id.ID)
	}
	return s.Store.GetMessageByWAID(ctx, c.Session, messageID)
}

func (s *Server) chatMessage(c *Ctx) error {
	if _, err := c.knownSession(); err != nil {
		return err
	}
	opts, err := c.mediaOptions()
	if err != nil {
		return err
	}
	m, err := s.findMessage(c, c.Param("messageId"))
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound("Message not found")
	}
	if err != nil {
		return err
	}
	bodies, err := messageBodies([]store.Message{m}, opts)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, bodies[0])
}

// messageKey is the core key of the message a write acts on: from WAHA's
// serialized id, else from the stored message with that WhatsApp id, else
// my message with that id in the path's chat.
func (s *Server) messageKey(c *Ctx, chat *pb.Jid, messageID string) (*pb.MessageKey, error) {
	if id, err := waha.ParseMessageID(messageID); err == nil {
		k, err := id.Key()
		if err != nil {
			return nil, ErrBadRequest("Invalid messageId: " + messageID)
		}
		return k, nil
	}
	if strings.Contains(messageID, "_") || messageID == "" {
		return nil, ErrBadRequest("Message id be in format false_11111111111@c.us_AAAAAAAAAAAAAAAAAAAA[_participant]")
	}
	if m, err := s.Store.GetMessageByWAID(c.R.Context(), c.Session, messageID); err == nil {
		if id, err := waha.ParseMessageID(m.ID); err == nil {
			if k, err := id.Key(); err == nil {
				return k, nil
			}
		}
	}
	return &pb.MessageKey{Chat: chat, Id: messageID, FromMe: true}, nil
}

// sendResponse is WAHA's {id, _data} answer to a write that puts a message
// in a chat: the id is mine, in that chat.
func sendResponse(sess store.Session, chat string, res *pb.SendResult) map[string]any {
	id := waha.MessageID{FromMe: true, Chat: chat, ID: res.GetKey().GetId()}
	if waha.GroupLike(chat) {
		id.Participant = waha.ParseMe(sess.Me).ID
	}
	return map[string]any{"id": id.String(), "_data": waha.ProtoJSON(res)}
}

// deleteChatMessage revokes a message for everyone and drops it from the
// store.
func (s *Server) deleteChatMessage(c *Ctx) error {
	chat, jid, err := c.chatParam()
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	key, err := s.messageKey(c, jid, c.Param("messageId"))
	if err != nil {
		return err
	}
	res, err := s.Core.Messaging.DeleteMessage(c.R.Context(), &pb.DeleteMessageRequest{Account: acc, Target: key, ForEveryone: true})
	if err != nil {
		return err
	}
	if m, err := s.Store.GetMessageByWAID(c.R.Context(), c.Session, key.GetId()); err == nil {
		if err := s.Store.DeleteMessage(c.R.Context(), c.Session, m.ID); err != nil {
			return err
		}
	}
	sess, _ := c.Sess()
	return c.JSON(http.StatusOK, sendResponse(sess, chat, res))
}

func (s *Server) editChatMessage(c *Ctx) error {
	chat, jid, err := c.chatParam()
	if err != nil {
		return err
	}
	var body struct {
		Text *string `json:"text"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.Text == nil {
		return ErrBadRequest([]string{"text must be a string"})
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	key, err := s.messageKey(c, jid, c.Param("messageId"))
	if err != nil {
		return err
	}
	res, err := s.Core.Messaging.EditMessage(c.R.Context(), &pb.EditMessageRequest{Account: acc, Target: key, NewText: *body.Text})
	if err != nil {
		return err
	}
	sess, _ := c.Sess()
	return c.JSON(http.StatusOK, sendResponse(sess, chat, res))
}

// success is the body WAHA's chat actions answer with.
var success = map[string]bool{"success": true}

func (s *Server) deleteChat(c *Ctx) error {
	chat, jid, err := c.chatParam()
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	if _, err := s.Core.Messaging.DeleteChat(c.R.Context(), &pb.DeleteChatRequest{Account: acc, Chat: jid}); err != nil {
		return err
	}
	if err := s.Store.DeleteChat(c.R.Context(), c.Session, chat); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, success)
}

func (s *Server) archiveChat(archived bool) Handler {
	return func(c *Ctx) error {
		chat, jid, err := c.chatParam()
		if err != nil {
			return err
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		if _, err := s.Core.Messaging.ArchiveChat(c.R.Context(), &pb.ArchiveChatRequest{Account: acc, Chat: jid, Archived: archived}); err != nil {
			return err
		}
		if err := s.Store.PatchChat(c.R.Context(), c.Session, chat, store.ChatPatch{Archived: &archived}); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, success)
	}
}

func (s *Server) unreadChat(c *Ctx) error {
	chat, jid, err := c.chatParam()
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	if _, err := s.Core.Messaging.MarkUnread(c.R.Context(), &pb.MarkReadRequest{Account: acc, Chat: jid}); err != nil {
		return err
	}
	one := 1
	if ch, err := s.Store.GetChat(c.R.Context(), c.Session, chat); err == nil && ch.UnreadCount > 0 {
		one = ch.UnreadCount
	}
	if err := s.Store.PatchChat(c.R.Context(), c.Session, chat, store.ChatPatch{Unread: &one}); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, success)
}

// readChat sends read receipts for the chat's unread incoming messages
// (DEVICE acks of the last `days` days, at most `messages`), one receipt
// per sender, and marks the chat read on the account's other devices.
func (s *Server) readChat(c *Ctx) error {
	chat, jid, err := c.chatParam()
	if err != nil {
		return err
	}
	limit, err := c.queryInt("messages", 0)
	if err != nil {
		return err
	}
	if limit <= 0 {
		limit = defaultReadLimitDM
		if waha.IsGroup(chat) {
			limit = defaultReadLimitGroup
		}
	}
	days, err := c.queryInt("days", defaultReadDays)
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	ids, err := s.readUnread(c.R.Context(), c.Session, acc, chat, jid, limit, days)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string][]string{"ids": ids})
}

// readUnread reads a chat the way WAHA does: a receipt for each unread
// incoming message of the last `days` (at most `limit`), one MarkRead per
// sender, then the chat marked read on the account's devices; the stored
// acks and unread count follow. It returns the serialized ids it read.
func (s *Server) readUnread(ctx context.Context, session string, acc *pb.AccountRef, chat string, jid *pb.Jid, limit, days int) ([]string, error) {
	no, device := false, waha.AckDevice
	after := time.Now().UnixMilli() - int64(days)*dayMs
	ms, err := s.Store.ListMessages(ctx, session, chat, store.MessageFilter{Limit: limit, FromMe: &no, Ack: &device, TimestampGTE: &after})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ms))
	waIDs := make([]string, 0, len(ms))
	bySender := map[string][]string{}
	var senders []string
	for _, m := range ms {
		ids, waIDs = append(ids, m.ID), append(waIDs, m.WAID)
		sender := ""
		if id, err := waha.ParseMessageID(m.ID); err == nil {
			sender = id.Participant
		}
		if _, ok := bySender[sender]; !ok {
			senders = append(senders, sender)
		}
		bySender[sender] = append(bySender[sender], m.WAID)
	}
	for _, sender := range senders {
		req := &pb.MarkReadRequest{Account: acc, Chat: jid, MessageIds: bySender[sender]}
		if sender != "" {
			if req.Sender, err = waha.ToPB(sender); err != nil {
				return nil, err
			}
		}
		if _, err := s.Core.Messaging.MarkRead(ctx, req); err != nil {
			return nil, err
		}
	}
	if _, err := s.Core.Messaging.MarkChatRead(ctx, &pb.MarkReadRequest{Account: acc, Chat: jid}); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateAck(ctx, session, waIDs, waha.AckRead); err != nil {
		return nil, err
	}
	zero := 0
	if err := s.Store.PatchChat(ctx, session, chat, store.ChatPatch{Unread: &zero}); err != nil {
		return nil, err
	}
	return ids, nil
}
