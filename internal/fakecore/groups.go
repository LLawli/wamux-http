package fakecore

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// Groups is a GroupService fake: an in-memory set of groups the RPCs change
// the way WhatsApp would, answering per participant like the core.
type Groups struct {
	pb.UnimplementedGroupServiceServer
	RequestLog

	mu      sync.Mutex
	groups  map[string]*pb.GroupMetadata // by jid
	links   map[string]string            // group jid -> invite code
	invites map[string]*pb.GroupMetadata // invite code -> group not joined yet
	pending map[string][]*pb.MembershipRequest
	photos  map[string][]byte
	refuse  map[string]string // participant jid -> the server's error code
	seq     int
}

// NewGroups builds the fake with the groups the account is already in.
func NewGroups(gs ...*pb.GroupMetadata) *Groups {
	g := &Groups{
		groups: map[string]*pb.GroupMetadata{}, links: map[string]string{},
		invites: map[string]*pb.GroupMetadata{}, pending: map[string][]*pb.MembershipRequest{},
		photos: map[string][]byte{}, refuse: map[string]string{},
	}
	for _, m := range gs {
		g.groups[m.GetId().GetValue()] = proto.Clone(m).(*pb.GroupMetadata)
	}
	return g
}

// Group returns a copy of a group, nil when the account is not in it.
func (g *Groups) Group(jid string) *pb.GroupMetadata {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.groups[jid]; ok {
		return proto.Clone(m).(*pb.GroupMetadata)
	}
	return nil
}

// Photo is a group's photo as last set (nil when removed or never set).
func (g *Groups) Photo(jid string) []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.photos[jid]
}

// Invite makes an invite code lead to a group the account is not in.
func (g *Groups) Invite(code string, m *pb.GroupMetadata) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.invites[code] = proto.Clone(m).(*pb.GroupMetadata)
}

// Request adds a pending membership request to a group.
func (g *Groups) Request(group string, r *pb.MembershipRequest) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pending[group] = append(g.pending[group], r)
}

// Refuse makes the server answer code for a participant in any change.
func (g *Groups) Refuse(jid, code string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refuse[jid] = code
}

func (g *Groups) find(ref interface{ GetGroup() *pb.Jid }) (*pb.GroupMetadata, error) {
	m, ok := g.groups[ref.GetGroup().GetValue()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "group %s not found", ref.GetGroup().GetValue())
	}
	return m, nil
}

func member(jid *pb.Jid, t pb.GroupParticipantType) *pb.GroupParticipant {
	return &pb.GroupParticipant{Jid: jid, Type: t}
}

func (g *Groups) CreateGroup(_ context.Context, r *pb.CreateGroupRequest) (*pb.GroupJidResponse, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	var jid *pb.Jid
	for jid == nil || g.groups[jid.GetValue()] != nil {
		g.seq++
		jid = &pb.Jid{Value: fmt.Sprintf("1203630000000000%02d@g.us", g.seq)}
	}
	m := &pb.GroupMetadata{Id: jid, Subject: proto.String(r.GetSubject()), AddressingMode: pb.GroupAddressingMode_GROUP_ADDRESSING_MODE_PN}
	for _, p := range r.GetParticipants() {
		m.Participants = append(m.Participants, member(p, pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_MEMBER))
	}
	g.groups[jid.GetValue()] = m
	return &pb.GroupJidResponse{Group: jid, Metadata: proto.Clone(m).(*pb.GroupMetadata)}, nil
}

// change applies one participant change and answers per participant: a
// refused one carries its error and is left alone.
func (g *Groups) change(r *pb.ParticipantsRequest, apply func(m *pb.GroupMetadata, jid *pb.Jid)) (*pb.ParticipantsResponse, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	m, err := g.find(r)
	if err != nil {
		return nil, err
	}
	out := &pb.ParticipantsResponse{}
	for _, p := range r.GetParticipants() {
		res := &pb.ParticipantChange{Jid: p, Status: "200"}
		if code, ok := g.refuse[p.GetValue()]; ok {
			res.Status, res.Error = code, code
			if code == "403" {
				res.AddRequest = &pb.AddRequestInfo{Code: "ADDREQ" + code, Expiration: 1760000000}
			}
		} else {
			apply(m, p)
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

func setType(t pb.GroupParticipantType) func(m *pb.GroupMetadata, jid *pb.Jid) {
	return func(m *pb.GroupMetadata, jid *pb.Jid) {
		for _, p := range m.GetParticipants() {
			if p.GetJid().GetValue() == jid.GetValue() {
				p.Type = t
			}
		}
	}
}

func (g *Groups) AddParticipants(_ context.Context, r *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
	return g.change(r, func(m *pb.GroupMetadata, jid *pb.Jid) {
		m.Participants = append(m.Participants, member(jid, pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_MEMBER))
	})
}

func (g *Groups) RemoveParticipants(_ context.Context, r *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
	return g.change(r, func(m *pb.GroupMetadata, jid *pb.Jid) {
		kept := m.Participants[:0]
		for _, p := range m.Participants {
			if p.GetJid().GetValue() != jid.GetValue() {
				kept = append(kept, p)
			}
		}
		m.Participants = kept
	})
}

func (g *Groups) PromoteAdmins(_ context.Context, r *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
	return g.change(r, setType(pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_ADMIN))
}

func (g *Groups) DemoteAdmins(_ context.Context, r *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
	return g.change(r, setType(pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_MEMBER))
}

func (g *Groups) edit(m proto.Message, ref interface{ GetGroup() *pb.Jid }, apply func(*pb.GroupMetadata)) (*pb.Empty, error) {
	g.log(m)
	g.mu.Lock()
	defer g.mu.Unlock()
	md, err := g.find(ref)
	if err != nil {
		return nil, err
	}
	apply(md)
	return &pb.Empty{}, nil
}

func (g *Groups) SetGroupSubject(_ context.Context, r *pb.GroupTextRequest) (*pb.Empty, error) {
	return g.edit(r, r, func(m *pb.GroupMetadata) { m.Subject = proto.String(r.GetText()) })
}

func (g *Groups) SetGroupDescription(_ context.Context, r *pb.GroupTextRequest) (*pb.Empty, error) {
	return g.edit(r, r, func(m *pb.GroupMetadata) { m.Description = proto.String(r.GetText()) })
}

func (g *Groups) SetGroupAnnounce(_ context.Context, r *pb.GroupToggleRequest) (*pb.Empty, error) {
	return g.edit(r, r, func(m *pb.GroupMetadata) { m.IsAnnouncement = r.GetEnabled() })
}

func (g *Groups) SetGroupLocked(_ context.Context, r *pb.GroupToggleRequest) (*pb.Empty, error) {
	return g.edit(r, r, func(m *pb.GroupMetadata) { m.IsLocked = r.GetEnabled() })
}

func (g *Groups) SetGroupPhoto(_ context.Context, r *pb.SetGroupPhotoRequest) (*pb.Empty, error) {
	return g.edit(r, r, func(m *pb.GroupMetadata) {
		if len(r.GetImage()) == 0 {
			delete(g.photos, m.GetId().GetValue())
		} else {
			g.photos[m.GetId().GetValue()] = r.GetImage()
		}
	})
}

func (g *Groups) GetGroupMetadata(_ context.Context, r *pb.GroupRef) (*pb.GroupMetadataResponse, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	m, err := g.find(r)
	if err != nil {
		return nil, err
	}
	return &pb.GroupMetadataResponse{Metadata: proto.Clone(m).(*pb.GroupMetadata)}, nil
}

func (g *Groups) link(r *pb.GroupRef, revoke bool) (*pb.InviteLinkResponse, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	m, err := g.find(r)
	if err != nil {
		return nil, err
	}
	jid := m.GetId().GetValue()
	if _, ok := g.links[jid]; !ok || revoke {
		g.seq++
		g.links[jid] = fmt.Sprintf("INVITE%04d", g.seq)
	}
	return &pb.InviteLinkResponse{Link: "https://chat.whatsapp.com/" + g.links[jid]}, nil
}

func (g *Groups) GetInviteLink(_ context.Context, r *pb.GroupRef) (*pb.InviteLinkResponse, error) {
	return g.link(r, false)
}

func (g *Groups) RevokeInviteLink(_ context.Context, r *pb.GroupRef) (*pb.InviteLinkResponse, error) {
	return g.link(r, true)
}

func (g *Groups) invite(code string) (*pb.GroupMetadata, error) {
	m, ok := g.invites[code]
	if !ok {
		return nil, status.Error(codes.NotFound, "invite not found")
	}
	return m, nil
}

func (g *Groups) JoinWithInvite(_ context.Context, r *pb.JoinWithInviteRequest) (*pb.GroupJidResponse, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	m, err := g.invite(r.GetCode())
	if err != nil {
		return nil, err
	}
	g.groups[m.GetId().GetValue()] = proto.Clone(m).(*pb.GroupMetadata)
	return &pb.GroupJidResponse{Group: m.GetId(), JoinOutcome: pb.JoinOutcome_JOIN_OUTCOME_JOINED}, nil
}

func (g *Groups) PreviewInvite(_ context.Context, r *pb.PreviewInviteRequest) (*pb.GroupMetadataResponse, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	m, err := g.invite(r.GetCode())
	if err != nil {
		return nil, err
	}
	return &pb.GroupMetadataResponse{Metadata: proto.Clone(m).(*pb.GroupMetadata)}, nil
}

func (g *Groups) ListParticipating(_ context.Context, r *pb.AccountRef) (*pb.ListGroupsResponse, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	out := &pb.ListGroupsResponse{}
	for _, m := range g.groups {
		out.Groups = append(out.Groups, &pb.GroupSummary{Jid: m.GetId(), Subject: m.GetSubject(),
			Participants: uint32(len(m.GetParticipants())), Metadata: proto.Clone(m).(*pb.GroupMetadata)})
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].GetJid().GetValue() < out.Groups[j].GetJid().GetValue() })
	return out, nil
}

func (g *Groups) LeaveGroup(_ context.Context, r *pb.GroupRef) (*pb.Empty, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := g.find(r); err != nil {
		return nil, err
	}
	delete(g.groups, r.GetGroup().GetValue())
	return &pb.Empty{}, nil
}

func (g *Groups) GetMembershipRequests(_ context.Context, r *pb.GroupRef) (*pb.MembershipRequestsResponse, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := g.find(r); err != nil {
		return nil, err
	}
	return &pb.MembershipRequestsResponse{Requests: g.pending[r.GetGroup().GetValue()]}, nil
}

// settle approves or rejects pending requests; a jid with no request gets the
// server's "404".
func (g *Groups) settle(r *pb.ParticipantsRequest, approve bool) (*pb.ParticipantsResponse, error) {
	g.log(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	m, err := g.find(r)
	if err != nil {
		return nil, err
	}
	group := m.GetId().GetValue()
	out := &pb.ParticipantsResponse{}
	for _, p := range r.GetParticipants() {
		res := &pb.ParticipantChange{Jid: p, Status: "200"}
		found := false
		kept := g.pending[group][:0]
		for _, req := range g.pending[group] {
			if req.GetJid().GetValue() == p.GetValue() {
				found = true
				continue
			}
			kept = append(kept, req)
		}
		g.pending[group] = kept
		switch {
		case !found:
			res.Status, res.Error = "404", "404"
		case approve:
			m.Participants = append(m.Participants, member(p, pb.GroupParticipantType_GROUP_PARTICIPANT_TYPE_MEMBER))
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

func (g *Groups) ApproveMembershipRequests(_ context.Context, r *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
	return g.settle(r, true)
}

func (g *Groups) RejectMembershipRequests(_ context.Context, r *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
	return g.settle(r, false)
}
