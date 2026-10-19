package ingest

import (
	"context"

	"go.mau.fi/whatsmeow/proto/waHistorySync"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// onHistorySync stores a history chunk: chats with their names and flags,
// their messages, and contact names. History publishes no webhooks, as in
// WAHA.
func (p *Processor) onHistorySync(ctx context.Context, sess store.Session, h *pb.HistorySyncEvent) (bool, error) {
	hs, err := waha.DecodeHistory(h.GetRaw())
	if err != nil {
		p.logger().Warn("undecodable history sync", "session", sess.Name, "err", err)
		return true, nil
	}
	mc := p.messageContext(sess)
	for _, pn := range hs.GetPushnames() {
		if err := p.contact(ctx, sess.Name, waha.ChatIDFromJID(pn.GetID()), "", pn.GetPushname()); err != nil {
			return true, err
		}
	}
	for _, conv := range hs.GetConversations() {
		chat := waha.ChatIDFromJID(conv.GetID())
		if chat == "" {
			continue
		}
		var ins []*pb.InboundMessage
		var built []waha.Message
		var lastTS int64
		var lastID string
		for _, hm := range conv.GetMessages() {
			w := hm.GetMessage()
			in, ok := waha.FromWebMessage(w, mc.Me)
			if !ok {
				continue
			}
			msg := waha.NewMessage(in, mc)
			if w.Status != nil {
				msg.SetAck(waha.HistoryAck(w.GetStatus()))
			}
			ins, built = append(ins, in), append(built, msg)
			if in.GetTimestamp() >= lastTS {
				lastTS, lastID = in.GetTimestamp(), msg.ID
			}
		}
		if err := p.storeMessages(ctx, sess.Name, ins, built); err != nil {
			return true, err
		}
		if ts := int64(conv.GetConversationTimestamp()) * 1000; ts > lastTS {
			lastTS = ts
		}
		if err := p.Store.TouchChat(ctx, sess.Name, chat, "", lastTS, lastID, 0); err != nil {
			return true, err
		}
		if err := p.historyFlags(ctx, sess.Name, chat, conv); err != nil {
			return true, err
		}
	}
	return true, nil
}

// historyFlags applies the name, archive, pin and unread state a history
// conversation carries, and keeps a DM's name as the contact's saved name.
func (p *Processor) historyFlags(ctx context.Context, session, chat string, conv *waHistorySync.Conversation) error {
	name := conv.GetName()
	if name == "" {
		name = conv.GetDisplayName()
	}
	// Only what the conversation states: an absent flag is not "false".
	var patch store.ChatPatch
	if conv.Archived != nil {
		archived := conv.GetArchived()
		patch.Archived = &archived
	}
	if conv.Pinned != nil {
		pinned := conv.GetPinned() > 0
		patch.Pinned = &pinned
	}
	if conv.UnreadCount != nil {
		unread := int(conv.GetUnreadCount())
		patch.Unread = &unread
	}
	if name != "" {
		patch.Name = &name
		if !waha.GroupLike(chat) {
			if err := p.contact(ctx, session, chat, name, ""); err != nil {
				return err
			}
		}
	}
	return p.Store.PatchChat(ctx, session, chat, patch)
}
