package waha

import (
	"encoding/json"
	"strings"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// The webhook payloads other than WAMessage, in WAHA's shapes (GOWS
// engine). Each mapper below is pure: what needs the store is the ingest
// package's job.

// AckPayload is WAMessageAckBody (message.ack, message.ack.group).
type AckPayload struct {
	ID          string          `json:"id"`
	From        string          `json:"from"`
	To          *string         `json:"to"`
	Participant *string         `json:"participant"`
	FromMe      bool            `json:"fromMe"`
	Ack         int             `json:"ack"`
	AckName     string          `json:"ackName"`
	Data        json.RawMessage `json:"_data"`
}

// ReceiptAck maps a receipt type to the ack it reports, as GOWS's
// receiptToMessageAck does; false for the types WAHA drops.
func ReceiptAck(t pb.ReceiptType) (int, bool) {
	switch t {
	case pb.ReceiptType_RECEIPT_TYPE_UNSPECIFIED, pb.ReceiptType_RECEIPT_TYPE_DELIVERED, pb.ReceiptType_RECEIPT_TYPE_INACTIVE:
		return AckDevice, true
	case pb.ReceiptType_RECEIPT_TYPE_READ:
		return AckRead, true
	case pb.ReceiptType_RECEIPT_TYPE_PLAYED:
		return AckPlayed, true
	case pb.ReceiptType_RECEIPT_TYPE_SERVER_ERROR:
		return AckError, true
	}
	return 0, false
}

// AckEvent is the event an ack goes out as.
func AckEvent(chat string) string {
	if IsGroup(chat) {
		return "message.ack.group"
	}
	return "message.ack"
}

// NewAck builds one ack payload. id is the acked message's serialized id;
// reader is who sent the receipt, which WAHA puts in to and participant in a
// group.
func NewAck(id MessageID, reader string, ack int, data json.RawMessage) AckPayload {
	p := AckPayload{ID: id.String(), From: id.Chat, FromMe: id.FromMe, Ack: ack, AckName: AckName(ack), Data: data}
	if IsGroup(id.Chat) && reader != "" {
		p.To, p.Participant = &reader, &reader
	}
	return p
}

// Reaction is WAReaction.
type Reaction struct {
	Text      string `json:"text"`
	MessageID string `json:"messageId"`
}

// ReactionPayload is WAMessageReaction (message.reaction).
type ReactionPayload struct {
	ID          string   `json:"id"`
	Timestamp   int64    `json:"timestamp"`
	From        string   `json:"from"`
	FromMe      bool     `json:"fromMe"`
	Source      string   `json:"source"`
	To          *string  `json:"to"`
	Participant *string  `json:"participant"`
	Reaction    Reaction `json:"reaction"`
}

// ReactionTargetID is the serialized id of the message a reaction targets
// (GOWS's buildMessageIdFromKey): in a group the participant is the target's
// sender, or the account itself for its own message.
func ReactionTargetID(k *pb.MessageKey, me Me) MessageID {
	id := MessageID{FromMe: k.GetFromMe(), Chat: FromPB(k.GetChat()), ID: k.GetId()}
	if GroupLike(id.Chat) {
		switch {
		case k.GetFromMe() && me.ID != "":
			id.Participant = me.ID
		case FromPB(k.GetParticipant()) != "":
			id.Participant = FromPB(k.GetParticipant())
		default:
			id.Participant = id.Chat
		}
	}
	return id
}

// NewReaction builds the reaction payload from the reaction message's own
// WAMessage and its target. text is "" when the reaction was removed.
func NewReaction(msg Message, target MessageID, text string) ReactionPayload {
	return ReactionPayload{
		ID: msg.ID, Timestamp: msg.Timestamp, From: msg.From, FromMe: msg.FromMe, Source: msg.Source,
		To: msg.To, Participant: msg.Participant,
		Reaction: Reaction{Text: text, MessageID: target.String()},
	}
}

// RevokedPayload is WAMessageRevokedBody (message.revoked). Before is the
// revoked message when the edge stored it.
type RevokedPayload struct {
	After            Message         `json:"after"`
	Before           *Message        `json:"before"`
	RevokedMessageID string          `json:"revokedMessageId"`
	Data             json.RawMessage `json:"_data"`
}

// EditedPayload is WAMessageEditedBody (message.edited): the edit's own
// WAMessage, with the new text as body.
type EditedPayload struct {
	Message
	EditedMessageID string `json:"editedMessageId"`
}

// MessageDestination is WAHA's MessageDestination (a poll's address).
type MessageDestination struct {
	ID          string  `json:"id"`
	To          string  `json:"to"`
	From        string  `json:"from"`
	FromMe      bool    `json:"fromMe"`
	Participant *string `json:"participant,omitempty"`
}

// PollVote is WAHA's PollVote.
type PollVote struct {
	ID              string   `json:"id"`
	SelectedOptions []string `json:"selectedOptions"`
	Timestamp       int64    `json:"timestamp"`
	To              *string  `json:"to"`
	From            string   `json:"from"`
	FromMe          bool     `json:"fromMe"`
	Participant     *string  `json:"participant"`
}

// PollVotePayload is poll.vote and poll.vote.failed.
type PollVotePayload struct {
	Vote PollVote           `json:"vote"`
	Poll MessageDestination `json:"poll"`
	Data json.RawMessage    `json:"_data"`
}

// Destination is NOWEB's getDestination, which GOWS uses for a poll: in a
// group from is the participant and to the group; in a DM my message goes
// from me to the chat and the other's from the chat to me.
func Destination(id MessageID, me Me) MessageDestination {
	d := MessageDestination{ID: id.String(), FromMe: id.FromMe}
	meID := me.ID
	if meID == "" {
		meID = "me"
	}
	switch {
	case id.Participant != "":
		p := id.Participant
		d.To, d.From, d.Participant = id.Chat, p, &p
	case id.FromMe:
		d.To, d.From = id.Chat, meID
	default:
		d.To, d.From = meID, id.Chat
	}
	return d
}

// PollKey turns the poll-creation key a vote carries, which is written from
// the voter's side, into the poll's id from this account's side (GOWS's
// fixPollCreationKey).
func PollKey(chat string, voteFromMe bool, remoteJID string, fromMe bool, waID, participant string, me Me) MessageID {
	id := MessageID{FromMe: fromMe, Chat: ChatIDFromJID(remoteJID), ID: waID}
	if participant != "" {
		id.Participant = ChatIDFromJID(participant)
	}
	if voteFromMe {
		return id
	}
	switch {
	case me.IsMe(id.Chat):
		return MessageID{FromMe: true, Chat: chat, ID: waID}
	case id.Participant != "" && me.IsMe(id.Participant):
		return MessageID{FromMe: true, Chat: chat, ID: waID, Participant: me.ID}
	}
	if id.Chat == "" {
		id.Chat = chat
	}
	return id
}

// PresenceData is WAHAPresenceData. LastSeen is seconds, null when unknown.
type PresenceData struct {
	Participant       string `json:"participant"`
	LastSeen          *int64 `json:"lastSeen"`
	LastKnownPresence string `json:"lastKnownPresence"`
}

// ChatPresences is WAHAChatPresences (presence.update and the presence
// routes).
type ChatPresences struct {
	ID        string         `json:"id"`
	Presences []PresenceData `json:"presences"`
}

// PresenceOf maps a core presence update to the chat it belongs to and the
// WAHA presence; false when it says nothing WAHA reports. A real presence
// belongs to the contact's own chat; a chat state to the chat it happened in.
func PresenceOf(u *pb.PresenceUpdate) (chat string, d PresenceData, ok bool) {
	who := FromPB(u.GetJid())
	if who == "" {
		return "", d, false
	}
	d.Participant = who
	if u.Online != nil {
		if IsGroup(who) {
			return "", d, false // a group is never "online"
		}
		d.LastKnownPresence = "offline"
		if u.GetOnline() {
			d.LastKnownPresence = "online"
		}
		if u.GetLastSeen() > 0 {
			s := u.GetLastSeen() / 1000
			d.LastSeen = &s
		}
		return who, d, true
	}
	switch u.GetChatState() {
	case pb.ChatState_CHAT_STATE_COMPOSING:
		d.LastKnownPresence = "typing"
	case pb.ChatState_CHAT_STATE_RECORDING:
		d.LastKnownPresence = "recording"
	case pb.ChatState_CHAT_STATE_PAUSED:
		d.LastKnownPresence = "paused"
	default:
		return "", d, false
	}
	chat = FromPB(u.GetChat())
	if chat == "" {
		chat = who
	}
	return chat, d, true
}

// EventGroupParticipant is WAHA's GroupParticipant schema, as the group.v2
// events carry it (not the REST shape, which is whatsmeow's; see group.go).
type EventGroupParticipant struct {
	ID   string  `json:"id"`
	PN   *string `json:"pn"`
	Role string  `json:"role"` // left | participant | admin | superadmin
}

// EventGroupInfo is WAHA's GroupInfo schema, as the group.v2 events carry it
// (not the REST GroupInfo of group.go).
type EventGroupInfo struct {
	ID                         string                  `json:"id"`
	Subject                    string                  `json:"subject"`
	Description                string                  `json:"description"`
	Invite                     string                  `json:"invite,omitempty"`
	MembersCanAddNewMember     bool                    `json:"membersCanAddNewMember"`
	MembersCanSendMessages     bool                    `json:"membersCanSendMessages"`
	NewMembersApprovalRequired bool                    `json:"newMembersApprovalRequired"`
	Participants               []EventGroupParticipant `json:"participants"`
}

// GroupInfoPartial is the group of group.v2.update: only what changed.
type GroupInfoPartial struct {
	ID                         string  `json:"id"`
	Subject                    *string `json:"subject,omitempty"`
	Description                *string `json:"description,omitempty"`
	MembersCanAddNewMember     *bool   `json:"membersCanAddNewMember,omitempty"`
	MembersCanSendMessages     *bool   `json:"membersCanSendMessages,omitempty"`
	NewMembersApprovalRequired *bool   `json:"newMembersApprovalRequired,omitempty"`
}

// GroupID is WAHA's GroupId.
type GroupID struct {
	ID string `json:"id"`
}

// GroupJoinPayload is GroupV2JoinEvent.
type GroupJoinPayload struct {
	Timestamp int64           `json:"timestamp"` // ms, as GOWS sends it
	Group     EventGroupInfo  `json:"group"`
	Data      json.RawMessage `json:"_data"`
}

// GroupLeavePayload is GroupV2LeaveEvent.
type GroupLeavePayload struct {
	Timestamp int64           `json:"timestamp"`
	Group     GroupID         `json:"group"`
	Data      json.RawMessage `json:"_data"`
}

// GroupUpdatePayload is GroupV2UpdateEvent.
type GroupUpdatePayload struct {
	Timestamp int64            `json:"timestamp"`
	Group     GroupInfoPartial `json:"group"`
	Data      json.RawMessage  `json:"_data"`
}

// GroupParticipantsPayload is GroupV2ParticipantsEvent.
type GroupParticipantsPayload struct {
	Type         string                  `json:"type"` // join | leave | promote | demote
	Timestamp    int64                   `json:"timestamp"`
	Group        GroupID                 `json:"group"`
	Participants []EventGroupParticipant `json:"participants"`
	Data         json.RawMessage         `json:"_data"`
}

// ParticipantsChange is the kind and members of a membership action; ok is
// false for any other action.
func ParticipantsChange(u *pb.GroupUpdate) (kind string, members []*pb.GroupNotificationParticipant, ok bool) {
	switch a := u.GetAction().(type) {
	case *pb.GroupUpdate_Add:
		return "join", a.Add.GetParticipants(), true
	case *pb.GroupUpdate_Remove:
		return "leave", a.Remove.GetParticipants(), true
	case *pb.GroupUpdate_Promote:
		return "promote", a.Promote.GetParticipants(), true
	case *pb.GroupUpdate_Demote:
		return "demote", a.Demote.GetParticipants(), true
	}
	return "", nil, false
}

// ParticipantRole is the role WAHA reports for a membership change.
func ParticipantRole(kind string) string {
	switch kind {
	case "leave":
		return "left"
	case "promote":
		return "admin"
	}
	return "participant"
}

// NotificationParticipants maps the members of a membership change.
func NotificationParticipants(kind string, ps []*pb.GroupNotificationParticipant) []EventGroupParticipant {
	out := make([]EventGroupParticipant, 0, len(ps))
	for _, p := range ps {
		gp := EventGroupParticipant{ID: FromPB(p.GetJid()), Role: ParticipantRole(kind)}
		if pn := FromPB(p.GetPhoneNumber()); pn != "" {
			gp.PN = &pn
		} else if strings.HasSuffix(gp.ID, "@c.us") {
			id := gp.ID
			gp.PN = &id
		}
		out = append(out, gp)
	}
	return out
}

// ParticipantIsMe reports whether a notification member is the account.
func ParticipantIsMe(p *pb.GroupNotificationParticipant, me Me) bool {
	return me.IsMe(p.GetJid().GetValue()) || me.IsMe(p.GetPhoneNumber().GetValue()) || me.IsMe(p.GetLid().GetValue())
}

// GroupUpdateOf is the group.v2.update group for a settings action; false
// when the action changes nothing WAHA reports.
func GroupUpdateOf(u *pb.GroupUpdate) (GroupInfoPartial, bool) {
	g := GroupInfoPartial{ID: FromPB(u.GetGroup())}
	yes, no := true, false
	switch a := u.GetAction().(type) {
	case *pb.GroupUpdate_Subject:
		s := a.Subject.GetSubject()
		g.Subject = &s
	case *pb.GroupUpdate_Description:
		d := a.Description.GetDescription()
		g.Description = &d
	case *pb.GroupUpdate_Announcement:
		g.MembersCanSendMessages = &no
	case *pb.GroupUpdate_NotAnnouncement:
		g.MembersCanSendMessages = &yes
	case *pb.GroupUpdate_MembershipApprovalMode:
		v := a.MembershipApprovalMode.GetEnabled()
		g.NewMembersApprovalRequired = &v
	case *pb.GroupUpdate_MemberAddMode:
		v := a.MemberAddMode.GetMode() == pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ALL_MEMBER_ADD
		g.MembersCanAddNewMember = &v
	default:
		return g, false
	}
	return g, true
}

// GroupInfoFromMetadata maps the core's group metadata to EventGroupInfo.
func GroupInfoFromMetadata(m *pb.GroupMetadata) EventGroupInfo {
	g := EventGroupInfo{
		ID:                         FromPB(m.GetId()),
		Subject:                    m.GetSubject(),
		Description:                m.GetDescription(),
		MembersCanAddNewMember:     m.GetMemberAddMode() == pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ALL_MEMBER_ADD,
		MembersCanSendMessages:     !m.GetIsAnnouncement(),
		NewMembersApprovalRequired: m.GetMembershipApproval(),
		Participants:               []EventGroupParticipant{},
	}
	for _, p := range m.GetParticipants() {
		gp := EventGroupParticipant{ID: FromPB(p.GetJid()), Role: "participant"}
		switch p.GetType() {
		case pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_ADMIN:
			gp.Role = "admin"
		case pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_SUPERADMIN:
			gp.Role = "superadmin"
		}
		if pn := FromPB(p.GetPhoneNumber()); pn != "" {
			gp.PN = &pn
		}
		g.Participants = append(g.Participants, gp)
	}
	return g
}

// CallData is WAHA's CallData (call.received, call.accepted, call.rejected).
type CallData struct {
	ID        string          `json:"id"`
	From      string          `json:"from,omitempty"`
	Timestamp float64         `json:"timestamp"` // seconds, fractional as GOWS sends it
	IsVideo   bool            `json:"isVideo"`
	IsGroup   bool            `json:"isGroup"`
	Data      json.RawMessage `json:"_data"`
}

// CallEventOf maps a call action to its WAHA event and payload; false for
// the actions WAHA does not report. A terminate that only says the call was
// handled on another device is dropped, as GOWS does.
func CallEventOf(c *pb.CallEvent) (string, CallData, bool) {
	d := CallData{ID: c.GetCallId(), Timestamp: float64(c.GetTimestamp()) / 1000, Data: ProtoJSON(c)}
	group := FromPB(c.GetGroup().GetGroupJid())
	d.From = FromPB(c.GetFrom())
	var event string
	switch a := c.GetAction().(type) {
	case *pb.CallEvent_Offer:
		event = "call.received"
		d.IsVideo = a.Offer.GetIsVideo()
		if g := FromPB(a.Offer.GetGroupJid()); g != "" {
			group = g
		}
	case *pb.CallEvent_OfferNotice:
		event = "call.received"
		d.IsVideo = a.OfferNotice.GetIsVideo()
		if a.OfferNotice.GetIsGroup() && group == "" {
			group = d.From
		}
	case *pb.CallEvent_Accept:
		event = "call.accepted"
	case *pb.CallEvent_Reject:
		event = "call.rejected"
	case *pb.CallEvent_Terminate:
		switch a.Terminate.GetReason() {
		case "accepted_elsewhere", "rejected_elsewhere":
			return "", d, false
		}
		event = "call.rejected"
	default:
		return "", d, false
	}
	d.IsGroup = group != ""
	if d.From == "" {
		d.From = group
	}
	return event, d, true
}

// labelColors is WhatsApp's label palette, by color index, as WAHA's
// Label.toHex has it.
var labelColors = []string{
	"#ff9485", "#64c4ff", "#ffd429", "#dfaef0", "#99b6c1", "#55ccb3", "#ff9dff",
	"#d3a91d", "#6d7cce", "#d7e752", "#00d0e2", "#ffc5c7", "#93ceac", "#f74848",
	"#00a0f2", "#83e422", "#ffaf04", "#b5ebff", "#9ba6ff", "#9368cf",
}

// LabelColorHex is a label color index as hex; "#000000" past the palette.
func LabelColorHex(color int) string {
	if color < 0 || color >= len(labelColors) {
		return "#000000"
	}
	return labelColors[color]
}

// Label is WAHA's Label (label.upsert, label.deleted, the label routes).
type Label struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Color    int    `json:"color"`
	ColorHex string `json:"colorHex"`
}

// NewLabel builds a Label with its hex color.
func NewLabel(id, name string, color int) Label {
	return Label{ID: id, Name: name, Color: color, ColorHex: LabelColorHex(color)}
}

// LabelChatAssociation is label.chat.added and label.chat.deleted.
type LabelChatAssociation struct {
	LabelID string `json:"labelId"`
	ChatID  string `json:"chatId"`
	Label   *Label `json:"label"`
}

// ChatArchive is ChatArchiveEvent (chat.archive).
type ChatArchive struct {
	ID        string `json:"id"`
	Archived  bool   `json:"archived"`
	Timestamp int64  `json:"timestamp"` // seconds
}

// EngineEvent is EnginePayload (engine.event): a core event WAHA has no
// event for, under its name in the core's contract.
type EngineEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// NewEngineEvent wraps a core event envelope; the name is the event's case
// in the envelope ("call_log", "connection_notice", ...).
func NewEngineEvent(ev *pb.EventEnvelope) (EngineEvent, bool) {
	r := ev.ProtoReflect()
	fd := r.WhichOneof(r.Descriptor().Oneofs().ByName("event"))
	if fd == nil {
		return EngineEvent{}, false
	}
	data := ProtoJSON(r.Get(fd).Message().Interface())
	return EngineEvent{Event: string(fd.Name()), Data: data}, true
}
