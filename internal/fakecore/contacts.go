package fakecore

import (
	"context"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// Contacts is a ContactService fake. Fill the exported maps before the test
// starts the environment; the account's own push name and picture change
// through the RPCs.
type Contacts struct {
	pb.UnimplementedContactServiceServer
	RequestLog

	// Registered maps a checked jid to the jid WhatsApp answers for it; a
	// jid not in it is not on WhatsApp.
	Registered map[string]string
	// Pictures maps a jid to its picture URL; any other jid has none
	// (NotFound, as the server answers).
	Pictures map[string]string
	// Abouts maps a jid to its about text; any other jid is hidden by
	// privacy (PermissionDenied).
	Abouts map[string]string
	// Lids are the persisted LID mappings.
	Lids []*pb.LidPnMapping

	mu       sync.Mutex
	pushName string
	picture  []byte
	about    string
	blocked  map[string]bool
}

// NewContacts builds the fake with the account's push name.
func NewContacts(pushName string) *Contacts {
	return &Contacts{pushName: pushName, Registered: map[string]string{}, Pictures: map[string]string{}, Abouts: map[string]string{}, blocked: map[string]bool{}}
}

// PushName is the account's current push name.
func (c *Contacts) PushName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pushName
}

// Picture is the account's own picture as last set (nil when removed).
func (c *Contacts) Picture() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.picture
}

func (c *Contacts) CheckOnWhatsApp(_ context.Context, r *pb.CheckOnWhatsAppRequest) (*pb.CheckOnWhatsAppResponse, error) {
	c.log(r)
	out := &pb.CheckOnWhatsAppResponse{}
	for _, j := range r.GetJids() {
		res := &pb.CheckResult{Query: j}
		if jid, ok := c.Registered[j.GetValue()]; ok {
			res.IsOnWhatsapp, res.Jid = true, &pb.Jid{Value: jid}
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

func (c *Contacts) GetProfilePicture(_ context.Context, r *pb.JidRequest) (*pb.ProfilePictureResponse, error) {
	c.log(r)
	url, ok := c.Pictures[r.GetJid().GetValue()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no profile picture")
	}
	return &pb.ProfilePictureResponse{Url: url}, nil
}

func (c *Contacts) SetProfilePicture(_ context.Context, r *pb.SetProfilePictureRequest) (*pb.Empty, error) {
	c.log(r)
	c.mu.Lock()
	c.picture = r.GetImage()
	c.mu.Unlock()
	return &pb.Empty{}, nil
}

func (c *Contacts) RemoveProfilePicture(_ context.Context, r *pb.AccountRef) (*pb.Empty, error) {
	c.log(r)
	c.mu.Lock()
	c.picture = nil
	c.mu.Unlock()
	return &pb.Empty{}, nil
}

func (c *Contacts) GetPushName(_ context.Context, r *pb.AccountRef) (*pb.PushNameResponse, error) {
	c.log(r)
	return &pb.PushNameResponse{PushName: c.PushName()}, nil
}

func (c *Contacts) SetPushName(_ context.Context, r *pb.SetPushNameRequest) (*pb.Empty, error) {
	c.log(r)
	c.mu.Lock()
	c.pushName = r.GetPushName()
	c.mu.Unlock()
	return &pb.Empty{}, nil
}

func (c *Contacts) GetAbout(_ context.Context, r *pb.JidRequest) (*pb.AboutResponse, error) {
	c.log(r)
	about, ok := c.Abouts[r.GetJid().GetValue()]
	if !ok {
		return nil, status.Error(codes.PermissionDenied, "about hidden by privacy settings")
	}
	return &pb.AboutResponse{About: about}, nil
}

// ResolveLidPn answers one result per jid, looking either side up.
func (c *Contacts) ResolveLidPn(_ context.Context, r *pb.ResolveLidPnRequest) (*pb.ResolveLidPnResponse, error) {
	c.log(r)
	out := &pb.ResolveLidPnResponse{}
	for _, j := range r.GetJids() {
		res := &pb.LidPnResult{Query: j}
		for _, m := range c.Lids {
			if m.GetLid().GetValue() == j.GetValue() || m.GetPn().GetValue() == j.GetValue() {
				res.Found, res.Mapping = true, m
				break
			}
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

func (c *Contacts) ListLidMappings(_ context.Context, r *pb.AccountRef) (*pb.ListLidMappingsResponse, error) {
	c.log(r)
	return &pb.ListLidMappingsResponse{Mappings: c.Lids}, nil
}

// About is the account's own about text as last set.
func (c *Contacts) About() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.about
}

// Blocked reports whether jid is on the account's blocklist.
func (c *Contacts) Blocked(jid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blocked[jid]
}

func (c *Contacts) SetAbout(_ context.Context, r *pb.SetAboutRequest) (*pb.Empty, error) {
	c.log(r)
	c.mu.Lock()
	c.about = r.GetAbout()
	c.mu.Unlock()
	return &pb.Empty{}, nil
}

// mapped reports whether jid has a persisted LID<->PN mapping.
func (c *Contacts) mapped(jid string) bool {
	for _, m := range c.Lids {
		if m.GetLid().GetValue() == jid || m.GetPn().GetValue() == jid {
			return true
		}
	}
	return false
}

// toggleBlock is the core's rule: a phone jid or a LID only, and a block
// (or an unblock by phone jid) needs the pair's mapping.
func (c *Contacts) toggleBlock(r *pb.JidRequest, block bool) (*pb.Empty, error) {
	c.log(r)
	jid := r.GetJid().GetValue()
	lid := strings.HasSuffix(jid, "@lid")
	if !lid && !strings.HasSuffix(jid, "@s.whatsapp.net") {
		return nil, status.Errorf(codes.InvalidArgument, "%s is neither a phone jid nor a LID", jid)
	}
	if (block || !lid) && !c.mapped(jid) {
		return nil, status.Errorf(codes.FailedPrecondition, "no LID<->PN mapping known for %s", jid)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if block {
		c.blocked[jid] = true
	} else {
		delete(c.blocked, jid)
	}
	return &pb.Empty{}, nil
}

func (c *Contacts) BlockContact(_ context.Context, r *pb.JidRequest) (*pb.Empty, error) {
	return c.toggleBlock(r, true)
}

func (c *Contacts) UnblockContact(_ context.Context, r *pb.JidRequest) (*pb.Empty, error) {
	return c.toggleBlock(r, false)
}
