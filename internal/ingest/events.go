package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/sessions"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// groupMetadataTimeout bounds the metadata lookup a group join makes; the
// join goes out with what the notification carried when it runs out.
const groupMetadataTimeout = 5 * time.Second

// onReceipt publishes one ack per message id (message.ack in a DM,
// message.ack.group in a group) and raises the stored acks. Receipt types
// WAHA drops (read-self, retry, ...) are dropped here too.
func (p *Processor) onReceipt(ctx context.Context, sess store.Session, r *pb.ReceiptEvent) (bool, error) {
	ack, ok := waha.ReceiptAck(r.GetType())
	if !ok {
		return true, nil
	}
	chat := waha.FromPB(r.GetChat())
	reader := waha.FromPB(r.GetSender())
	if err := p.setAck(ctx, sess.Name, r.GetMessageIds(), ack); err != nil {
		return true, err
	}
	if p.ignored(sess, chat) {
		return true, nil
	}
	me := waha.ParseMe(sess.Me)
	data := waha.ProtoJSON(r)
	for _, waID := range r.GetMessageIds() {
		id := p.ackedID(ctx, sess.Name, waID, chat, me)
		if !p.firstSeen(fmt.Sprintf("ack|%s|%s|%d|%s", sess.Name, id, ack, reader)) {
			continue
		}
		if err := p.publish(ctx, sess, waha.AckEvent(chat), waha.NewAck(id, reader, ack, data)); err != nil {
			return true, err
		}
	}
	return true, nil
}

// setAck raises stored acks; ERROR is the one value that lowers.
func (p *Processor) setAck(ctx context.Context, session string, waIDs []string, ack int) error {
	if ack == waha.AckError {
		return p.Store.SetAck(ctx, session, waIDs, ack)
	}
	return p.Store.UpdateAck(ctx, session, waIDs, ack)
}

// ackedID is the serialized id of an acked message: the stored one when the
// edge has it, else my message in that chat, as GOWS builds it (a receipt
// is about a message I sent; in a group the participant is me).
func (p *Processor) ackedID(ctx context.Context, session, waID, chat string, me waha.Me) waha.MessageID {
	if m, err := p.Store.GetMessageByWAID(ctx, session, waID); err == nil {
		if id, err := waha.ParseMessageID(m.ID); err == nil {
			return id
		}
	}
	id := waha.MessageID{FromMe: true, Chat: chat, ID: waID}
	if waha.GroupLike(chat) {
		id.Participant = me.ID
	}
	return id
}

// onServerAck turns the server's refusal of a sent message into an ERROR
// ack. A plain ack says nothing a WAHA client sees.
func (p *Processor) onServerAck(ctx context.Context, sess store.Session, a *pb.ServerAckEvent) (bool, error) {
	if a.GetError() == "" || (a.GetClass() != "" && a.GetClass() != "message") {
		return true, nil
	}
	chat := waha.FromPB(a.GetFrom())
	if m, err := p.Store.GetMessageByWAID(ctx, sess.Name, a.GetId()); err == nil {
		chat = m.ChatID
	}
	if chat == "" {
		return true, nil
	}
	if err := p.Store.SetAck(ctx, sess.Name, []string{a.GetId()}, waha.AckError); err != nil {
		return true, err
	}
	if p.ignored(sess, chat) {
		return true, nil
	}
	id := p.ackedID(ctx, sess.Name, a.GetId(), chat, waha.ParseMe(sess.Me))
	return true, p.publish(ctx, sess, waha.AckEvent(chat), waha.NewAck(id, "", waha.AckError, waha.ProtoJSON(a)))
}

// onPresence keeps the latest presence per chat and participant for the
// presence routes and publishes presence.update.
func (p *Processor) onPresence(ctx context.Context, sess store.Session, u *pb.PresenceUpdate) (bool, error) {
	chat, d, ok := waha.PresenceOf(u)
	if !ok {
		return false, nil
	}
	var lastSeen int64
	if d.LastSeen != nil {
		lastSeen = *d.LastSeen
	}
	if err := p.Store.PutPresence(ctx, store.Presence{
		Session: sess.Name, ChatID: chat, Participant: d.Participant, State: d.LastKnownPresence,
		LastSeen: lastSeen, UpdatedAt: waha.NowMs(),
	}); err != nil {
		return true, err
	}
	if p.ignored(sess, chat) {
		return true, nil
	}
	return true, p.publish(ctx, sess, "presence.update", waha.ChatPresences{ID: chat, Presences: []waha.PresenceData{d}})
}

// onGroup maps one group notification action: membership changes go out as
// group.v2.participants, and as group.v2.join / group.v2.leave (plus the
// legacy group.join / group.leave) when they concern this account; settings
// changes as group.v2.update.
func (p *Processor) onGroup(ctx context.Context, sess store.Session, u *pb.GroupUpdate) (bool, error) {
	group := waha.FromPB(u.GetGroup())
	if group == "" {
		return false, nil
	}
	ts := u.GetTimestamp()
	if ts == 0 {
		ts = waha.NowMs()
	}
	data := waha.ProtoJSON(u)
	me := waha.ParseMe(sess.Me)
	if create := u.GetCreate(); create != nil {
		info := waha.GroupInfoFromMetadata(create.GetMetadata())
		info.ID = group
		return true, p.joined(ctx, sess, waha.GroupJoinPayload{Timestamp: ts, Group: info, Data: data})
	}
	if kind, members, ok := waha.ParticipantsChange(u); ok {
		payload := waha.GroupParticipantsPayload{
			Type: kind, Timestamp: ts, Group: waha.GroupID{ID: group},
			Participants: waha.NotificationParticipants(kind, members), Data: data,
		}
		if err := p.publish(ctx, sess, "group.v2.participants", payload); err != nil {
			return true, err
		}
		mine := false
		for _, m := range members {
			mine = mine || waha.ParticipantIsMe(m, me)
		}
		switch {
		case mine && kind == "join":
			info := p.groupInfo(ctx, sess, u.GetGroup(), payload.Participants)
			return true, p.joined(ctx, sess, waha.GroupJoinPayload{Timestamp: ts, Group: info, Data: data})
		case mine && kind == "leave":
			left := waha.GroupLeavePayload{Timestamp: ts, Group: waha.GroupID{ID: group}, Data: data}
			if err := p.publish(ctx, sess, "group.v2.leave", left); err != nil {
				return true, err
			}
			return true, p.publish(ctx, sess, "group.leave", left)
		}
		return true, nil
	}
	if g, ok := waha.GroupUpdateOf(u); ok {
		if g.Subject != nil {
			if err := p.Store.PatchChat(ctx, sess.Name, group, store.ChatPatch{Name: g.Subject}); err != nil {
				return true, err
			}
		}
		return true, p.publish(ctx, sess, "group.v2.update", waha.GroupUpdatePayload{Timestamp: ts, Group: g, Data: data})
	}
	return false, nil
}

// joined names the group's chat and publishes group.v2.join and the legacy
// group.join.
func (p *Processor) joined(ctx context.Context, sess store.Session, payload waha.GroupJoinPayload) error {
	if payload.Group.Subject != "" {
		name := payload.Group.Subject
		if err := p.Store.PatchChat(ctx, sess.Name, payload.Group.ID, store.ChatPatch{Name: &name}); err != nil {
			return err
		}
	}
	if err := p.publish(ctx, sess, "group.v2.join", payload); err != nil {
		return err
	}
	return p.publish(ctx, sess, "group.join", payload)
}

// groupInfo asks the core for the group's metadata; when that fails the
// join carries what the notification had.
func (p *Processor) groupInfo(ctx context.Context, sess store.Session, group *pb.Jid, added []waha.EventGroupParticipant) waha.EventGroupInfo {
	ctx, cancel := context.WithTimeout(ctx, groupMetadataTimeout)
	defer cancel()
	res, err := p.Core.Groups.GetGroupMetadata(ctx, &pb.GroupRef{Account: sessions.Ref(sess), Group: group})
	if err == nil && res.GetMetadata() != nil {
		info := waha.GroupInfoFromMetadata(res.GetMetadata())
		info.ID = waha.FromPB(group)
		return info
	}
	p.logger().Info("group metadata unavailable for join", "session", sess.Name, "group", group.GetValue(), "err", err)
	return waha.EventGroupInfo{ID: waha.FromPB(group), MembersCanSendMessages: true, Participants: added}
}

func (p *Processor) onCall(ctx context.Context, sess store.Session, c *pb.CallEvent) (bool, error) {
	event, data, ok := waha.CallEventOf(c)
	if !ok {
		return false, nil
	}
	if p.ignored(sess, data.From) {
		return true, nil
	}
	return true, p.publish(ctx, sess, event, data)
}

// onLabel keeps the store's labels and publishes the label events. Like
// GOWS, a full app-state sync updates the store without publishing.
func (p *Processor) onLabel(ctx context.Context, sess store.Session, l *pb.LabelUpdate) (bool, error) {
	id := l.GetLabelId()
	switch a := l.GetAction().(type) {
	case *pb.LabelUpdate_Edit:
		cur, err := p.Store.GetLabel(ctx, sess.Name, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return true, err
		}
		cur.Session, cur.ID, cur.Deleted = sess.Name, id, a.Edit.GetDeleted()
		if a.Edit.Name != nil {
			cur.Name = a.Edit.GetName()
		}
		if a.Edit.Color != nil {
			cur.Color = int(a.Edit.GetColor())
		}
		if err := p.Store.UpsertLabel(ctx, cur); err != nil {
			return true, err
		}
		if l.GetFromFullSync() {
			return true, nil
		}
		event := "label.upsert"
		if cur.Deleted {
			event = "label.deleted"
		}
		return true, p.publish(ctx, sess, event, waha.NewLabel(id, cur.Name, cur.Color))
	case *pb.LabelUpdate_Chat:
		chat := waha.FromPB(a.Chat.GetChat())
		on := a.Chat.GetLabeled()
		if err := p.Store.SetChatLabel(ctx, sess.Name, id, chat, on); err != nil {
			return true, err
		}
		if l.GetFromFullSync() {
			return true, nil
		}
		payload := waha.LabelChatAssociation{LabelID: id, ChatID: chat}
		if cur, err := p.Store.GetLabel(ctx, sess.Name, id); err == nil && !cur.Deleted {
			label := waha.NewLabel(id, cur.Name, cur.Color)
			payload.Label = &label
		}
		event := "label.chat.deleted"
		if on {
			event = "label.chat.added"
		}
		return true, p.publish(ctx, sess, event, payload)
	}
	return false, nil
}

// onAppState applies archive, pin and read-state mutations to the stored
// chat; archive is also published, unless a full sync replays it.
func (p *Processor) onAppState(ctx context.Context, sess store.Session, u *pb.AppStateUpdate) (bool, error) {
	chat := waha.FromPB(u.GetChat())
	if chat == "" {
		return false, nil
	}
	switch a := u.GetAction().(type) {
	case *pb.AppStateUpdate_Archive:
		archived := a.Archive.GetArchived()
		if err := p.Store.PatchChat(ctx, sess.Name, chat, store.ChatPatch{Archived: &archived}); err != nil {
			return true, err
		}
		if u.GetFromFullSync() || p.ignored(sess, chat) {
			return true, nil
		}
		return true, p.publish(ctx, sess, "chat.archive", waha.ChatArchive{ID: chat, Archived: archived, Timestamp: u.GetTimestamp() / 1000})
	case *pb.AppStateUpdate_Pin:
		pinned := a.Pin.GetPinned()
		return true, p.Store.PatchChat(ctx, sess.Name, chat, store.ChatPatch{Pinned: &pinned})
	case *pb.AppStateUpdate_MarkRead:
		unread := 0
		if !a.MarkRead.GetRead() {
			// Marked unread: keep a real count, else show one unread.
			unread = 1
			if c, err := p.Store.GetChat(ctx, sess.Name, chat); err == nil && c.UnreadCount > 0 {
				unread = c.UnreadCount
			}
		}
		return true, p.Store.PatchChat(ctx, sess.Name, chat, store.ChatPatch{Unread: &unread})
	}
	return false, nil
}
