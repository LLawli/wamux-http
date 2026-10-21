package httpapi

import (
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/media"
	"github.com/LLawli/wamux-http/internal/waha"
)

// Groups answer as GOWS does: a group is whatsmeow's GroupInfo
// (waha.GroupInfo), participant changes are whatsmeow's per-participant list.
//
// DELETE /groups/{id} has no core RPC and answers 501, as GOWS does.
func (s *Server) registerGroups(r chi.Router) {
	read := Route{Action: ActRead, From: FromPath, Working: true}
	write := Route{Action: ActSend, From: FromPath, Working: true}
	const base = "/api/{session}/groups"

	s.route(r, "POST", base, write, func(c *Ctx) error {
		var req struct {
			Name         string            `json:"name"`
			Participants []groupMemberBody `json:"participants"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		if strings.TrimSpace(req.Name) == "" {
			return ErrBadRequest([]string{"name must be a string"})
		}
		jids, err := participantJIDs(req.Participants)
		if err != nil {
			return err
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		res, err := s.Core.Groups.CreateGroup(c.R.Context(), &pb.CreateGroupRequest{Account: acc, Subject: req.Name, Participants: jids})
		if err != nil {
			return err
		}
		g := waha.GroupInfoFromPB(res.GetMetadata())
		if g.JID == "" {
			g.JID, g.Name = waha.FromPB(res.GetGroup()), req.Name
		}
		return c.JSON(http.StatusCreated, g)
	})

	s.route(r, "GET", base, read, func(c *Ctx) error {
		p, err := dirPagination(c, "id", "subject")
		if err != nil {
			return err
		}
		noParticipants, err := excludeParticipants(c)
		if err != nil {
			return err
		}
		groups, err := s.listGroups(c)
		if err != nil {
			return err
		}
		if p.SortBy != "" {
			key := func(g waha.GroupInfo) string { return g.JID }
			if p.SortBy == "subject" {
				key = func(g waha.GroupInfo) string { return g.Name }
			}
			sort.SliceStable(groups, func(i, j int) bool {
				if p.Desc {
					return key(groups[i]) > key(groups[j])
				}
				return key(groups[i]) < key(groups[j])
			})
		}
		out := []waha.GroupInfo{}
		for _, i := range p.apply(len(groups)) {
			g := groups[i]
			if noParticipants {
				g.Participants = nil
			}
			out = append(out, g)
		}
		return c.JSON(http.StatusOK, out)
	})

	s.route(r, "GET", base+"/count", read, func(c *Ctx) error {
		groups, err := s.listGroups(c)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]int{"count": len(groups)})
	})

	// The core keeps no group cache, so there is nothing to refresh.
	s.route(r, "POST", base+"/refresh", write, func(c *Ctx) error {
		return c.JSON(http.StatusOK, map[string]bool{"success": true})
	})

	s.route(r, "GET", base+"/join-info", read, func(c *Ctx) error {
		code := waha.ParseInviteCode(c.Query("code"))
		if code == "" {
			return ErrBadRequest([]string{"code must be a string"})
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		res, err := s.Core.Groups.PreviewInvite(c.R.Context(), &pb.PreviewInviteRequest{Account: acc, Code: code})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, waha.GroupInfoFromPB(res.GetMetadata()))
	})

	s.route(r, "POST", base+"/join", write, func(c *Ctx) error {
		var req struct {
			Code string `json:"code"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		code := waha.ParseInviteCode(req.Code)
		if code == "" {
			return ErrBadRequest([]string{"code must be a string"})
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		res, err := s.Core.Groups.JoinWithInvite(c.R.Context(), &pb.JoinWithInviteRequest{Account: acc, Code: code})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"id": waha.FromPB(res.GetGroup())})
	})

	s.route(r, "GET", base+"/{id}", read, func(c *Ctx) error {
		m, err := s.groupMetadata(c)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, waha.GroupInfoFromPB(m))
	})

	s.route(r, "POST", base+"/{id}/leave", write, func(c *Ctx) error {
		ref, err := groupRef(c)
		if err != nil {
			return err
		}
		if _, err := s.Core.Groups.LeaveGroup(c.R.Context(), ref); err != nil {
			return err
		}
		return c.Empty(http.StatusOK)
	})

	s.route(r, "GET", base+"/{id}/picture", read, func(c *Ctx) error {
		if err := dirBool(c, "refresh"); err != nil {
			return err
		}
		ref, err := groupRef(c)
		if err != nil {
			return err
		}
		url, err := s.dirPictureURL(c.R.Context(), ref.GetAccount(), ref.GetGroup())
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]*string{"url": url})
	})
	setPhoto := func(c *Ctx, image []byte) error {
		ref, err := groupRef(c)
		if err != nil {
			return err
		}
		if _, err := s.Core.Groups.SetGroupPhoto(c.R.Context(), &pb.SetGroupPhotoRequest{Account: ref.GetAccount(), Group: ref.GetGroup(), Image: image}); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]bool{"success": true})
	}
	s.route(r, "PUT", base+"/{id}/picture", write, func(c *Ctx) error {
		image, err := dirFile(c)
		if err != nil {
			return err
		}
		return setPhoto(c, image)
	})
	s.route(r, "DELETE", base+"/{id}/picture", write, func(c *Ctx) error {
		return setPhoto(c, nil) // an empty image removes the photo
	})

	setText := func(field string, rpc func(c *Ctx, req *pb.GroupTextRequest) error) Handler {
		return func(c *Ctx) error {
			var body map[string]any
			if err := c.Bind(&body); err != nil {
				return err
			}
			text, ok := body[field].(string)
			if !ok {
				return ErrBadRequest([]string{field + " must be a string"})
			}
			ref, err := groupRef(c)
			if err != nil {
				return err
			}
			if err := rpc(c, &pb.GroupTextRequest{Account: ref.GetAccount(), Group: ref.GetGroup(), Text: text}); err != nil {
				return err
			}
			return c.Empty(http.StatusOK)
		}
	}
	s.route(r, "PUT", base+"/{id}/description", write, setText("description", func(c *Ctx, req *pb.GroupTextRequest) error {
		_, err := s.Core.Groups.SetGroupDescription(c.R.Context(), req)
		return err
	}))
	s.route(r, "PUT", base+"/{id}/subject", write, setText("subject", func(c *Ctx, req *pb.GroupTextRequest) error {
		_, err := s.Core.Groups.SetGroupSubject(c.R.Context(), req)
		return err
	}))

	// Security settings: the GETs read the metadata, the PUTs write it and
	// answer as GOWS does.
	setting := func(path, field string, get func(m *pb.GroupMetadata) bool) {
		s.route(r, "GET", base+"/{id}/settings/security/"+path, read, func(c *Ctx) error {
			m, err := s.groupMetadata(c)
			if err != nil {
				return err
			}
			return c.JSON(http.StatusOK, map[string]bool{field: get(m)})
		})
	}
	toggle := func(path string, rpc func(c *Ctx, req *pb.GroupToggleRequest) error) {
		s.route(r, "PUT", base+"/{id}/settings/security/"+path, write, func(c *Ctx) error {
			var req struct {
				AdminsOnly bool `json:"adminsOnly"`
			}
			if err := c.Bind(&req); err != nil {
				return err
			}
			ref, err := groupRef(c)
			if err != nil {
				return err
			}
			if err := rpc(c, &pb.GroupToggleRequest{Account: ref.GetAccount(), Group: ref.GetGroup(), Enabled: req.AdminsOnly}); err != nil {
				return err
			}
			return c.Empty(http.StatusOK)
		})
	}
	setting("info-admin-only", "adminsOnly", (*pb.GroupMetadata).GetIsLocked)
	toggle("info-admin-only", func(c *Ctx, req *pb.GroupToggleRequest) error {
		_, err := s.Core.Groups.SetGroupLocked(c.R.Context(), req)
		return err
	})
	setting("messages-admin-only", "adminsOnly", (*pb.GroupMetadata).GetIsAnnouncement)
	toggle("messages-admin-only", func(c *Ctx, req *pb.GroupToggleRequest) error {
		_, err := s.Core.Groups.SetGroupAnnounce(c.R.Context(), req)
		return err
	})
	setting("member-add-mode", "membersCanAddNewMember", func(m *pb.GroupMetadata) bool {
		return m.GetMemberAddMode() == pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ALL_MEMBER_ADD
	})
	s.route(r, "PUT", base+"/{id}/settings/security/member-add-mode", write, func(c *Ctx) error {
		var req struct {
			MembersCanAddNewMember bool `json:"membersCanAddNewMember"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		ref, err := groupRef(c)
		if err != nil {
			return err
		}
		mode := pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ADMIN_ADD
		if req.MembersCanAddNewMember {
			mode = pb.GroupMemberAddMode_GROUP_MEMBER_ADD_MODE_ALL_MEMBER_ADD
		}
		if _, err := s.Core.Groups.SetGroupMemberAddMode(c.R.Context(), &pb.GroupMemberAddModeRequest{Account: ref.GetAccount(), Group: ref.GetGroup(), Mode: mode}); err != nil {
			return err
		}
		return c.Empty(http.StatusOK)
	})
	setting("membership-approval", "newMembersApprovalRequired", (*pb.GroupMetadata).GetMembershipApproval)
	// GOWS answers this one with true, not empty.
	s.route(r, "PUT", base+"/{id}/settings/security/membership-approval", write, func(c *Ctx) error {
		var req struct {
			NewMembersApprovalRequired bool `json:"newMembersApprovalRequired"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		ref, err := groupRef(c)
		if err != nil {
			return err
		}
		if _, err := s.Core.Groups.SetGroupMembershipApproval(c.R.Context(), &pb.GroupToggleRequest{Account: ref.GetAccount(), Group: ref.GetGroup(), Enabled: req.NewMembersApprovalRequired}); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, true)
	})

	s.route(r, "GET", base+"/{id}/participants/join-requests", read, func(c *Ctx) error {
		ref, err := groupRef(c)
		if err != nil {
			return err
		}
		res, err := s.Core.Groups.GetMembershipRequests(c.R.Context(), ref)
		if err != nil {
			return err
		}
		out := []waha.GroupJoinRequest{}
		for _, jr := range res.GetRequests() {
			out = append(out, waha.JoinRequestFromPB(jr))
		}
		return c.JSON(http.StatusOK, out)
	})
	joinRequests := func(path string, rpc func(c *Ctx, req *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error)) {
		s.route(r, "POST", base+"/{id}/participants/join-requests/"+path, write, func(c *Ctx) error {
			req, err := participantsRequest(c)
			if err != nil {
				return err
			}
			res, err := rpc(c, req)
			if err != nil {
				return err
			}
			out := []waha.GroupJoinRequestResult{}
			for _, ch := range res.GetResults() {
				out = append(out, waha.JoinRequestResultFromPB(ch))
			}
			return c.JSON(http.StatusOK, out)
		})
	}
	joinRequests("approve", func(c *Ctx, req *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
		return s.Core.Groups.ApproveMembershipRequests(c.R.Context(), req)
	})
	joinRequests("reject", func(c *Ctx, req *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
		return s.Core.Groups.RejectMembershipRequests(c.R.Context(), req)
	})

	// The invite link goes out as a bare string, which NestJS sends as text,
	// not as a JSON string.
	invite := func(method, path string, rt Route, rpc func(c *Ctx, ref *pb.GroupRef) (*pb.InviteLinkResponse, error)) {
		s.route(r, method, base+"/{id}/"+path, rt, func(c *Ctx) error {
			ref, err := groupRef(c)
			if err != nil {
				return err
			}
			res, err := rpc(c, ref)
			if err != nil {
				return err
			}
			c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
			c.W.WriteHeader(http.StatusOK)
			_, err = c.W.Write([]byte(waha.GroupInviteLink(res.GetLink())))
			return err
		})
	}
	invite("GET", "invite-code", read, func(c *Ctx, ref *pb.GroupRef) (*pb.InviteLinkResponse, error) {
		return s.Core.Groups.GetInviteLink(c.R.Context(), ref)
	})
	invite("POST", "invite-code/revoke", write, func(c *Ctx, ref *pb.GroupRef) (*pb.InviteLinkResponse, error) {
		return s.Core.Groups.RevokeInviteLink(c.R.Context(), ref)
	})

	s.route(r, "GET", base+"/{id}/participants", read, func(c *Ctx) error {
		m, err := s.groupMetadata(c)
		if err != nil {
			return err
		}
		out := []waha.GroupMember{}
		for _, p := range m.GetParticipants() {
			out = append(out, waha.GroupMemberFromPB(p))
		}
		return c.JSON(http.StatusOK, out)
	})
	s.route(r, "GET", base+"/{id}/participants/v2", read, func(c *Ctx) error {
		m, err := s.groupMetadata(c)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, waha.GroupParticipantsV2(m))
	})

	change := func(path string, rpc func(c *Ctx, req *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error)) {
		s.route(r, "POST", base+"/{id}/"+path, write, func(c *Ctx) error {
			req, err := participantsRequest(c)
			if err != nil {
				return err
			}
			res, err := rpc(c, req)
			if err != nil {
				return err
			}
			out := []waha.GroupMember{}
			for _, ch := range res.GetResults() {
				out = append(out, waha.GroupMemberFromChange(ch))
			}
			return c.JSON(http.StatusOK, out)
		})
	}
	change("participants/add", func(c *Ctx, req *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
		return s.Core.Groups.AddParticipants(c.R.Context(), req)
	})
	change("participants/remove", func(c *Ctx, req *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
		return s.Core.Groups.RemoveParticipants(c.R.Context(), req)
	})
	change("admin/promote", func(c *Ctx, req *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
		return s.Core.Groups.PromoteAdmins(c.R.Context(), req)
	})
	change("admin/demote", func(c *Ctx, req *pb.ParticipantsRequest) (*pb.ParticipantsResponse, error) {
		return s.Core.Groups.DemoteAdmins(c.R.Context(), req)
	})
}

// groupMemberBody is WAHA's Participant in a request.
type groupMemberBody struct {
	ID string `json:"id"`
}

// listGroups is every group the account is in, as GroupInfo. A summary the
// core sent without metadata still names the group.
func (s *Server) listGroups(c *Ctx) ([]waha.GroupInfo, error) {
	acc, err := c.Account()
	if err != nil {
		return nil, err
	}
	res, err := s.Core.Groups.ListParticipating(c.R.Context(), acc)
	if err != nil {
		return nil, err
	}
	out := []waha.GroupInfo{}
	for _, g := range res.GetGroups() {
		info := waha.GroupInfoFromPB(g.GetMetadata())
		if info.JID == "" {
			info.JID = waha.FromPB(g.GetJid())
		}
		if info.Name == "" {
			info.Name = g.GetSubject()
		}
		if g.GetMetadata() == nil {
			info.ParticipantCount = int(g.GetParticipants())
		}
		out = append(out, info)
	}
	return out, nil
}

func (s *Server) groupMetadata(c *Ctx) (*pb.GroupMetadata, error) {
	ref, err := groupRef(c)
	if err != nil {
		return nil, err
	}
	res, err := s.Core.Groups.GetGroupMetadata(c.R.Context(), ref)
	if err != nil {
		return nil, err
	}
	return res.GetMetadata(), nil
}

// groupRef is the route's account and the {id} group. A bare id gets the
// group server, which WAHA leaves to the engine to refuse.
func groupRef(c *Ctx) (*pb.GroupRef, error) {
	id := c.Param("id")
	if id != "" && !strings.Contains(id, "@") {
		id += "@g.us"
	}
	jid, err := dirJID(id)
	if err != nil {
		return nil, err
	}
	acc, err := c.Account()
	if err != nil {
		return nil, err
	}
	return &pb.GroupRef{Account: acc, Group: jid}, nil
}

// participantsRequest reads WAHA's ParticipantsRequest for the {id} group.
func participantsRequest(c *Ctx) (*pb.ParticipantsRequest, error) {
	var body struct {
		Participants []groupMemberBody `json:"participants"`
	}
	if err := c.Bind(&body); err != nil {
		return nil, err
	}
	if body.Participants == nil {
		return nil, ErrBadRequest([]string{"participants must be an array"})
	}
	jids, err := participantJIDs(body.Participants)
	if err != nil {
		return nil, err
	}
	ref, err := groupRef(c)
	if err != nil {
		return nil, err
	}
	return &pb.ParticipantsRequest{Account: ref.GetAccount(), Group: ref.GetGroup(), Participants: jids}, nil
}

func participantJIDs(ps []groupMemberBody) ([]*pb.Jid, error) {
	out := make([]*pb.Jid, 0, len(ps))
	for _, p := range ps {
		jid, err := dirJID(p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, jid)
	}
	return out, nil
}

// excludeParticipants reads `exclude`, repeated or comma separated.
func excludeParticipants(c *Ctx) (bool, error) {
	drop := false
	for _, v := range c.R.URL.Query()["exclude"] {
		for _, f := range strings.Split(v, ",") {
			switch f {
			case "":
			case "participants":
				drop = true
			default:
				return false, ErrBadRequest([]string{"each value in exclude must be one of the following values: , participants"})
			}
		}
	}
	return drop, nil
}

// dirFile reads a picture from the request's `file`, a URL (fetched behind
// the media layer's SSRF guard) or base64 data.
func dirFile(c *Ctx) ([]byte, error) {
	var req struct {
		File *media.File `json:"file"`
	}
	if err := c.Bind(&req); err != nil {
		return nil, err
	}
	if req.File == nil {
		return nil, ErrBadRequest([]string{"file must be an object"})
	}
	blob, err := c.S.Media.Load(c.R.Context(), req.File, media.KindImage)
	if err != nil {
		return nil, mediaError(err)
	}
	return blob.Data, nil
}
