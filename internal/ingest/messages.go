package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/sessions"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// onMessage routes a message: reactions, poll votes, edits and revokes have
// events of their own; any other protocol message is not a chat message;
// the rest is stored and published as message.any (and message when it is
// not mine). A message from an ignored chat is stored but not published.
func (p *Processor) onMessage(ctx context.Context, sess store.Session, m *pb.InboundMessage) (bool, error) {
	raw, err := waha.DecodeRaw(m.GetRawMessage())
	if err != nil {
		p.logger().Warn("undecodable raw message", "session", sess.Name, "id", m.GetKey().GetId(), "err", err)
	}
	content := waha.Content(raw)
	mc := p.messageContext(sess)
	publish := !p.ignored(sess, waha.InboundID(m, mc.Me).Chat)
	pm := content.GetProtocolMessage()
	switch {
	case m.GetReactionTarget() != nil || content.GetReactionMessage() != nil:
		return true, p.reaction(ctx, sess, mc, m, content, publish)
	case content.GetPollUpdateMessage() != nil:
		return true, p.pollVote(ctx, sess, mc, m, content, publish)
	case m.GetIsEdit() || (pm != nil && pm.GetType() == waE2E.ProtocolMessage_MESSAGE_EDIT):
		return true, p.edited(ctx, sess, mc, m, content, publish)
	case m.GetIsDelete() || (pm != nil && pm.GetType() == waE2E.ProtocolMessage_REVOKE):
		return true, p.revoked(ctx, sess, mc, m, content, publish)
	case pm != nil || !hasContent(m, raw):
		return false, nil // key shares, history notices, settings: engine.event
	}
	return true, p.regular(ctx, sess, mc, m, publish)
}

// hasContent tells a chat message from a stanza that only carries keys or
// context. A message with no raw payload at all is taken as content: the
// core projected it and kept nothing else.
func hasContent(m *pb.InboundMessage, raw *waE2E.Message) bool {
	if m.GetText() != "" || m.GetCaption() != "" || m.GetMedia() != nil || m.GetStickerPack() != nil || len(m.GetRawMessage()) == 0 {
		return true
	}
	found := false
	raw.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		switch fd.Name() {
		case "messageContextInfo", "senderKeyDistributionMessage":
			return true
		}
		found = true
		return false
	})
	return found
}

// regular stores a chat message and publishes it.
func (p *Processor) regular(ctx context.Context, sess store.Session, mc waha.MessageContext, m *pb.InboundMessage, publish bool) error {
	msg := waha.NewMessage(m, mc)
	_, err := p.Store.GetMessage(ctx, sess.Name, msg.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	fresh := err != nil
	if err := p.storeMessages(ctx, sess.Name, []*pb.InboundMessage{m}, []waha.Message{msg}); err != nil {
		return err
	}
	unread := 0 // a replay is not news
	switch {
	case msg.FromMe:
		unread = -1 // a message I sent from any device means I have seen the chat
	case fresh:
		unread = 1
	}
	if err := p.Store.TouchChat(ctx, sess.Name, msg.From, "", m.GetTimestamp(), msg.ID, unread); err != nil {
		return err
	}
	if !msg.FromMe {
		if err := p.contact(ctx, sess.Name, waha.SenderOf(m, mc.Me), "", m.GetPushName()); err != nil {
			return err
		}
	}
	if !publish || !p.firstSeen("msg|"+sess.Name+"|"+strconv.FormatBool(msg.FromMe)+"|"+m.GetKey().GetId()) {
		return nil
	}
	if err := p.publish(ctx, sess, "message.any", msg); err != nil {
		return err
	}
	if !msg.FromMe {
		return p.publish(ctx, sess, "message", msg)
	}
	return nil
}

// storeMessages upserts messages with their WAMessage bodies. Raw is the
// core's InboundMessage, which the files route decodes to download media.
func (p *Processor) storeMessages(ctx context.Context, session string, ms []*pb.InboundMessage, built []waha.Message) error {
	out := make([]store.Message, 0, len(ms))
	for i, m := range ms {
		body, err := json.Marshal(built[i])
		if err != nil {
			return err
		}
		raw, err := proto.Marshal(m)
		if err != nil {
			return err
		}
		out = append(out, store.Message{
			Session: session, ID: built[i].ID, WAID: m.GetKey().GetId(), ChatID: built[i].From,
			Timestamp: m.GetTimestamp(), FromMe: built[i].FromMe, Ack: built[i].Ack, Body: body, Raw: raw,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return p.Store.UpsertMessages(ctx, out)
}

func (p *Processor) reaction(ctx context.Context, sess store.Session, mc waha.MessageContext, m *pb.InboundMessage, content *waE2E.Message, publish bool) error {
	target, text := m.GetReactionTarget(), m.GetReaction()
	if rm := content.GetReactionMessage(); rm != nil {
		if target == nil {
			target = keyFromWA(rm.GetKey())
		}
		if text == "" {
			text = rm.GetText()
		}
	}
	if !publish {
		return nil
	}
	msg := waha.NewMessage(m, mc)
	return p.publish(ctx, sess, "message.reaction", waha.NewReaction(msg, waha.ReactionTargetID(target, mc.Me), text))
}

// keyFromWA converts a raw WhatsApp message key to the core's.
func keyFromWA(k interface {
	GetRemoteJID() string
	GetFromMe() bool
	GetID() string
	GetParticipant() string
}) *pb.MessageKey {
	out := &pb.MessageKey{Id: k.GetID(), FromMe: k.GetFromMe()}
	if k.GetRemoteJID() != "" {
		out.Chat = &pb.Jid{Value: k.GetRemoteJID()}
	}
	if k.GetParticipant() != "" {
		out.Participant = &pb.Jid{Value: k.GetParticipant()}
	}
	return out
}

// protocolTarget is the raw id of the message an edit or revoke acts on.
func protocolTarget(m *pb.InboundMessage, content *waE2E.Message) string {
	if id := m.GetProtocolTarget().GetId(); id != "" {
		return id
	}
	return content.GetProtocolMessage().GetKey().GetID()
}

// edited publishes message.edited and rewrites the stored original's body.
func (p *Processor) edited(ctx context.Context, sess store.Session, mc waha.MessageContext, m *pb.InboundMessage, content *waE2E.Message, publish bool) error {
	msg := waha.NewMessage(m, mc)
	text := m.GetText()
	if text == "" {
		if b := waha.BodyOf(content.GetProtocolMessage().GetEditedMessage()); b != nil {
			text = *b
		}
	}
	msg.Body = &text
	target := protocolTarget(m, content)
	if orig, err := p.Store.GetMessageByWAID(ctx, sess.Name, target); err == nil {
		var om waha.Message
		if json.Unmarshal(orig.Body, &om) == nil {
			om.Body = &text
			if body, err := json.Marshal(om); err == nil {
				orig.Body, orig.Raw = body, nil // nil keeps the stored raw
				if err := p.Store.UpsertMessages(ctx, []store.Message{orig}); err != nil {
					return err
				}
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if !publish {
		return nil
	}
	return p.publish(ctx, sess, "message.edited", waha.EditedPayload{Message: msg, EditedMessageID: target})
}

// revoked publishes message.revoked, with the stored original as before,
// and drops the original from the store.
func (p *Processor) revoked(ctx context.Context, sess store.Session, mc waha.MessageContext, m *pb.InboundMessage, content *waE2E.Message, publish bool) error {
	target := protocolTarget(m, content)
	payload := waha.RevokedPayload{After: waha.NewMessage(m, mc), RevokedMessageID: target, Data: waha.ProtoJSON(m)}
	if orig, err := p.Store.GetMessageByWAID(ctx, sess.Name, target); err == nil {
		var before waha.Message
		if json.Unmarshal(orig.Body, &before) == nil {
			payload.Before = &before
		}
		if err := p.Store.DeleteMessage(ctx, sess.Name, orig.ID); err != nil {
			return err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if !publish {
		return nil
	}
	return p.publish(ctx, sess, "message.revoked", payload)
}

// pollVote opens a vote with the core (it holds the identity store the key
// derivation needs) and publishes poll.vote, or poll.vote.failed when the
// poll or its secret is unknown or the vote does not open.
func (p *Processor) pollVote(ctx context.Context, sess store.Session, mc waha.MessageContext, m *pb.InboundMessage, content *waE2E.Message, publish bool) error {
	if !publish {
		return nil
	}
	upd := content.GetPollUpdateMessage()
	voteID := waha.InboundID(m, mc.Me)
	pk := upd.GetPollCreationMessageKey()
	pollID := waha.PollKey(voteID.Chat, voteID.FromMe, pk.GetRemoteJID(), pk.GetFromMe(), pk.GetID(), pk.GetParticipant(), mc.Me)
	vote := waha.PollVote{
		ID: voteID.String(), SelectedOptions: []string{}, Timestamp: upd.GetSenderTimestampMS(),
		From: voteID.Chat, FromMe: voteID.FromMe,
	}
	if voteID.Participant != "" {
		sender := voteID.Participant
		vote.To, vote.Participant = &sender, &sender
	}
	payload := waha.PollVotePayload{Vote: vote, Poll: waha.Destination(pollID, mc.Me), Data: waha.ProtoJSON(m)}
	selected, err := p.openVote(ctx, sess, mc.Me, pollID, m, upd)
	if err != nil {
		p.logger().Info("poll vote not opened", "session", sess.Name, "poll", pollID.ID, "err", err)
		return p.publish(ctx, sess, "poll.vote.failed", payload)
	}
	payload.Vote.SelectedOptions = selected
	return p.publish(ctx, sess, "poll.vote", payload)
}

var errNoPoll = errors.New("poll message not stored")

// openVote tallies one vote and returns the options it selects.
func (p *Processor) openVote(ctx context.Context, sess store.Session, me waha.Me, pollID waha.MessageID, m *pb.InboundMessage, upd *waE2E.PollUpdateMessage) ([]string, error) {
	stored, err := p.Store.GetMessageByWAID(ctx, sess.Name, pollID.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errNoPoll
	}
	if err != nil {
		return nil, err
	}
	var in pb.InboundMessage
	if err := proto.Unmarshal(stored.Raw, &in); err != nil {
		return nil, err
	}
	raw, err := waha.DecodeRaw(in.GetRawMessage())
	if err != nil {
		return nil, err
	}
	options := pollOptions(raw)
	if len(options) == 0 {
		return nil, errNoPoll
	}
	secret, err := p.Store.PollSecret(ctx, sess.Name, pollID.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		secret = raw.GetMessageContextInfo().GetMessageSecret()
	}
	if len(secret) == 0 {
		return nil, errors.New("poll secret unknown")
	}
	creator := pollCreator(pollID, &in, me)
	voter := voterFor(creator, m, me)
	tally, err := p.Core.Messaging.AggregatePollVotes(ctx, &pb.AggregatePollVotesRequest{
		Account: sessions.Ref(sess), PollId: pollID.ID, PollCreator: &pb.Jid{Value: creator},
		MessageSecret: secret, Options: options,
		Votes: []*pb.PollVote{{Voter: &pb.Jid{Value: voter}, EncPayload: upd.GetVote().GetEncPayload(), EncIv: upd.GetVote().GetEncIV()}},
	})
	if err != nil {
		return nil, err
	}
	if tally.GetUndecryptable() > 0 {
		return nil, errors.New("vote did not open")
	}
	selected := []string{}
	for _, r := range tally.GetResults() {
		if len(r.GetVoters()) > 0 {
			selected = append(selected, r.GetOption())
		}
	}
	return selected, nil
}

// pollOptions are the option names of a poll-creation message, in order.
func pollOptions(raw *waE2E.Message) []string {
	c := waha.Content(raw)
	for _, pc := range []*waE2E.PollCreationMessage{
		c.GetPollCreationMessage(), c.GetPollCreationMessageV2(), c.GetPollCreationMessageV3(),
		c.GetPollCreationMessageV5(), c.GetPollCreationMessageV6(),
	} {
		if pc == nil {
			continue
		}
		var out []string
		for _, o := range pc.GetOptions() {
			out = append(out, o.GetOptionName())
		}
		return out
	}
	return nil
}

// pollCreator is the JID of who sent the poll, device-less.
func pollCreator(pollID waha.MessageID, poll *pb.InboundMessage, me waha.Me) string {
	var id string
	switch {
	case pollID.FromMe:
		id = me.ID
		if strings.HasSuffix(poll.GetSender().GetValue(), "@lid") && me.LID != "" {
			id = me.LID
		}
	case pollID.Participant != "":
		id = pollID.Participant
	default:
		id = pollID.Chat
	}
	jid, _ := waha.JIDFromChatID(id)
	return jid
}

// voterFor is the vote's sender in the creator's namespace: both @lid or
// both phone JIDs, or the vote cannot open (see the core's PollVote).
func voterFor(creator string, m *pb.InboundMessage, me waha.Me) string {
	wantLID := strings.HasSuffix(creator, "@lid")
	candidates := []string{waha.SenderOf(m, me), waha.FromPB(m.GetSenderAlt())}
	if m.GetKey().GetFromMe() {
		candidates = append(candidates, me.ID, me.LID)
	}
	for _, c := range candidates {
		if c != "" && strings.HasSuffix(c, "@lid") == wantLID {
			jid, _ := waha.JIDFromChatID(c)
			return jid
		}
	}
	jid, _ := waha.JIDFromChatID(candidates[0])
	return jid
}
