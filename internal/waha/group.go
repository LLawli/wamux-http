package waha

import (
	"strconv"
	"strings"
	"time"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// GroupInfo is a group the way GOWS answers it: whatsmeow's GroupInfo
// serialized by Go, PascalCase keys and RFC 3339 times (the zero time when
// unknown). WAHA's OpenAPI types these answers as a bare object, so this is
// the shape a GOWS client already parses. Every id is in WAHA form (`@c.us`),
// where GOWS would leave whatsmeow's `@s.whatsapp.net`.
type GroupInfo struct {
	JID      string `json:"JID"`
	OwnerJID string `json:"OwnerJID"`
	OwnerPN  string `json:"OwnerPN"`

	Name        string    `json:"Name"`
	NameSetAt   time.Time `json:"NameSetAt"`
	NameSetBy   string    `json:"NameSetBy"`
	NameSetByPN string    `json:"NameSetByPN"`

	Topic        string    `json:"Topic"`
	TopicID      string    `json:"TopicID"`
	TopicSetAt   time.Time `json:"TopicSetAt"`
	TopicSetBy   string    `json:"TopicSetBy"`
	TopicSetByPN string    `json:"TopicSetByPN"`
	TopicDeleted bool      `json:"TopicDeleted"`

	IsLocked          bool   `json:"IsLocked"`
	IsAnnounce        bool   `json:"IsAnnounce"`
	AnnounceVersionID string `json:"AnnounceVersionID"`
	IsEphemeral       bool   `json:"IsEphemeral"`
	DisappearingTimer uint32 `json:"DisappearingTimer"`
	IsIncognito       bool   `json:"IsIncognito"`

	IsParent                      bool   `json:"IsParent"`
	DefaultMembershipApprovalMode string `json:"DefaultMembershipApprovalMode"`
	LinkedParentJID               string `json:"LinkedParentJID"`
	IsDefaultSubGroup             bool   `json:"IsDefaultSubGroup"`
	IsJoinApprovalRequired        bool   `json:"IsJoinApprovalRequired"`

	AddressingMode       string    `json:"AddressingMode"`
	GroupCreated         time.Time `json:"GroupCreated"`
	CreatorCountryCode   string    `json:"CreatorCountryCode"`
	ParticipantVersionID string    `json:"ParticipantVersionID"`

	Participants     []GroupMember `json:"Participants,omitempty"`
	ParticipantCount int           `json:"ParticipantCount"`

	MemberAddMode          string `json:"MemberAddMode"`
	MemberLinkMode         string `json:"MemberLinkMode"`
	MemberShareHistoryMode string `json:"MemberShareHistoryMode"`
}

// GroupMember is whatsmeow's GroupParticipant, as GOWS returns it in a group,
// in `GET .../participants` and in the participant-change answers.
type GroupMember struct {
	JID          string           `json:"JID"`
	PhoneNumber  string           `json:"PhoneNumber"`
	LID          string           `json:"LID"`
	IsAdmin      bool             `json:"IsAdmin"`
	IsSuperAdmin bool             `json:"IsSuperAdmin"`
	DisplayName  string           `json:"DisplayName"`
	Error        int              `json:"Error"`
	AddRequest   *GroupAddRequest `json:"AddRequest"`
}

// GroupAddRequest is the invite a privacy setting asks for instead of a
// direct add.
type GroupAddRequest struct {
	Code       string    `json:"Code"`
	Expiration time.Time `json:"Expiration"`
}

// GroupParticipant is WAHA's GroupParticipant (`participants/v2`).
type GroupParticipant struct {
	ID   string `json:"id"`
	PN   string `json:"pn,omitempty"`
	Role string `json:"role"`
}

// GroupJoinRequest is WAHA's GroupJoinRequest, filled as GOWS fills it: the
// requester and the time, the rest null.
type GroupJoinRequest struct {
	RequesterID   string  `json:"requesterId"`
	AddedByID     *string `json:"addedById"`
	ParentGroupID *string `json:"parentGroupId"`
	RequestMethod *string `json:"requestMethod"`
	Timestamp     int64   `json:"timestamp"`
}

// GroupJoinRequestResult is WAHA's GroupJoinRequestResult.
type GroupJoinRequestResult struct {
	RequesterID string `json:"requesterId"`
	Success     bool   `json:"success"`
	Error       *int   `json:"error,omitempty"`
}

// The participant roles WAHA names.
const (
	RoleParticipant = "participant"
	RoleAdmin       = "admin"
	RoleSuperAdmin  = "superadmin"
)

// GroupInviteLinkPrefix is what WhatsApp puts before a group invite code.
const GroupInviteLinkPrefix = "https://chat.whatsapp.com/"

// timeMs is a core time (ms) as Go's zero time when unset, as whatsmeow
// leaves an unknown time.
func timeMs(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// GroupInfoFromPB maps the core's group metadata. A nil metadata gives the
// zero group.
func GroupInfoFromPB(m *pb.GroupMetadata) GroupInfo {
	g := GroupInfo{
		JID:                    FromPB(m.GetId()),
		OwnerJID:               FromPB(m.GetCreator()),
		OwnerPN:                FromPB(m.GetCreatorPn()),
		Name:                   m.GetSubject(),
		NameSetAt:              timeMs(m.GetSubjectTime()),
		NameSetBy:              FromPB(m.GetSubjectOwner()),
		NameSetByPN:            FromPB(m.GetSubjectOwnerPn()),
		Topic:                  m.GetDescription(),
		TopicID:                m.GetDescriptionId(),
		TopicSetAt:             timeMs(m.GetDescriptionTime()),
		TopicSetBy:             FromPB(m.GetDescriptionOwner()),
		TopicSetByPN:           FromPB(m.GetDescriptionOwnerPn()),
		IsLocked:               m.GetIsLocked(),
		IsAnnounce:             m.GetIsAnnouncement(),
		IsIncognito:            m.GetIsIncognito(),
		IsParent:               m.GetIsParentGroup(),
		LinkedParentJID:        FromPB(m.GetParentGroup()),
		IsDefaultSubGroup:      m.GetIsDefaultSubGroup(),
		AddressingMode:         addressingMode(m.GetAddressingMode()),
		GroupCreated:           timeMs(m.GetCreationTime()),
		CreatorCountryCode:     m.GetCreatorCountryCode(),
		ParticipantVersionID:   m.GetParticipantVersionId(),
		MemberAddMode:          memberAddMode(m.GetMemberAddMode()),
		MemberLinkMode:         memberLinkMode(m.GetMemberLinkMode()),
		MemberShareHistoryMode: shareHistoryMode(m.GetMemberShareHistoryMode()),
		IsJoinApprovalRequired: m.GetMembershipApproval(),
	}
	if e := m.GetEphemeral(); e != nil && e.GetExpirationSeconds() > 0 {
		g.IsEphemeral, g.DisappearingTimer = true, e.GetExpirationSeconds()
	}
	if m.GetParentMembershipApprovalRequired() {
		g.DefaultMembershipApprovalMode = "request_required"
	}
	for _, p := range m.GetParticipants() {
		g.Participants = append(g.Participants, GroupMemberFromPB(p))
	}
	g.ParticipantCount = len(g.Participants)
	if m != nil && m.Size != nil {
		g.ParticipantCount = int(m.GetSize())
	}
	return g
}

// GroupMemberFromPB maps one roster entry. The phone and lid forms are filled
// from the jid itself when the roster did not repeat them.
func GroupMemberFromPB(p *pb.GroupParticipant) GroupMember {
	jid := FromPB(p.GetJid())
	gm := GroupMember{
		JID:          jid,
		PhoneNumber:  FromPB(p.GetPhoneNumber()),
		LID:          FromPB(p.GetLid()),
		IsAdmin:      isAdmin(p.GetType()),
		IsSuperAdmin: p.GetType() == pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_SUPERADMIN,
		DisplayName:  p.GetDetails().GetDisplayName(),
	}
	fillForms(jid, &gm.PhoneNumber, &gm.LID)
	return gm
}

func fillForms(jid string, pn, lid *string) {
	switch {
	case *pn == "" && IsPhoneChatID(jid):
		*pn = jid
	case *lid == "" && IsLIDChatID(jid):
		*lid = jid
	}
}

func isAdmin(t pb.GroupParticipantType) bool {
	return t == pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_ADMIN || t == pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_SUPERADMIN
}

// GroupParticipantsV2 is WAHA's `participants/v2` list for a group: the id the
// group addresses each member by (`@c.us` or `@lid`), the phone form when
// known, and the role.
func GroupParticipantsV2(m *pb.GroupMetadata) []GroupParticipant {
	out := []GroupParticipant{}
	for _, p := range m.GetParticipants() {
		gm := GroupMemberFromPB(p)
		role := RoleParticipant
		switch {
		case gm.IsSuperAdmin:
			role = RoleSuperAdmin
		case gm.IsAdmin:
			role = RoleAdmin
		}
		out = append(out, GroupParticipant{ID: gm.JID, PN: gm.PhoneNumber, Role: role})
	}
	return out
}

// GroupMemberFromChange maps one per-participant verdict of an add, remove,
// promote or demote, in the shape GOWS relays whatsmeow's answer.
func GroupMemberFromChange(c *pb.ParticipantChange) GroupMember {
	jid := FromPB(c.GetJid())
	gm := GroupMember{JID: jid, PhoneNumber: FromPB(c.GetPhoneNumber()), DisplayName: c.GetUsername()}
	fillForms(jid, &gm.PhoneNumber, &gm.LID)
	if code, ok := errorCode(c.GetError()); ok {
		gm.Error = code
	}
	if ar := c.GetAddRequest(); ar != nil {
		gm.AddRequest = &GroupAddRequest{Code: ar.GetCode()}
		if ar.GetExpiration() > 0 {
			gm.AddRequest.Expiration = time.Unix(int64(ar.GetExpiration()), 0).UTC()
		}
	}
	return gm
}

// errorCode reads the server's numeric error ("403"); ok is false for an
// empty or non-numeric one.
func errorCode(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// JoinRequestFromPB maps one pending membership request.
func JoinRequestFromPB(r *pb.MembershipRequest) GroupJoinRequest {
	return GroupJoinRequest{RequesterID: FromPB(r.GetJid()), Timestamp: r.GetRequestTime() / 1000}
}

// JoinRequestResultFromPB maps one verdict of an approve or reject.
func JoinRequestResultFromPB(c *pb.ParticipantChange) GroupJoinRequestResult {
	r := GroupJoinRequestResult{RequesterID: FromPB(c.GetJid()), Success: c.GetError() == ""}
	if code, ok := errorCode(c.GetError()); ok {
		r.Error = &code
	}
	return r
}

// ParseInviteCode is WAHA's parseInviteCode: the last path segment of a link,
// without query or fragment; a bare code passes through.
func ParseInviteCode(link string) string {
	path, _, _ := strings.Cut(link, "?")
	path, _, _ = strings.Cut(path, "#")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// GroupInviteLink turns what the core answers for an invite into the link
// WAHA returns; a full link passes through.
func GroupInviteLink(s string) string {
	if s == "" || strings.Contains(s, "://") {
		return s
	}
	return GroupInviteLinkPrefix + s
}

func addressingMode(m pb.GroupAddressingMode) string {
	switch m {
	case pb.GroupAddressingMode_GROUP_ADDRESSING_MODE_PN:
		return "pn"
	case pb.GroupAddressingMode_GROUP_ADDRESSING_MODE_LID:
		return "lid"
	}
	return ""
}

func memberAddMode(m pb.GroupMemberAddMode) string {
	switch m {
	case pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ADMIN_ADD:
		return "admin_add"
	case pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ALL_MEMBER_ADD:
		return "all_member_add"
	}
	return ""
}

func memberLinkMode(m pb.GroupMemberLinkMode) string {
	switch m {
	case pb.GroupMemberLinkMode_GROUP_MEMBER_LINK_MODE_ADMIN_LINK:
		return "admin_link"
	case pb.GroupMemberLinkMode_GROUP_MEMBER_LINK_MODE_ALL_MEMBER_LINK:
		return "all_member_link"
	}
	return ""
}

func shareHistoryMode(m pb.GroupMemberShareHistoryMode) string {
	switch m {
	case pb.GroupMemberShareHistoryMode_GROUP_MEMBER_SHARE_HISTORY_MODE_ADMIN_SHARE:
		return "admin_share"
	case pb.GroupMemberShareHistoryMode_GROUP_MEMBER_SHARE_HISTORY_MODE_ALL_MEMBER_SHARE:
		return "all_member_share"
	}
	return ""
}
