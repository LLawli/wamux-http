package fakecore

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// Newsletters is a NewsletterService fake over a fixed set of channels.
type Newsletters struct {
	pb.UnimplementedNewsletterServiceServer
	RequestLog

	Channels []*pb.Newsletter
	// Messages are each channel's history rows, newest first, by jid.
	Messages map[string][]*pb.NewsletterMessage

	mu     sync.Mutex
	muted  map[string]bool
	follow map[string]bool
	made   int
	votes  map[uint64][][]byte
	voted  int
}

func (n *Newsletters) ListSubscribedNewsletters(_ context.Context, r *pb.AccountRef) (*pb.NewsletterList, error) {
	n.log(r)
	return &pb.NewsletterList{Newsletters: n.Channels}, nil
}

func (n *Newsletters) GetNewsletterMetadata(_ context.Context, r *pb.JidRequest) (*pb.Newsletter, error) {
	n.log(r)
	for _, ch := range n.Channels {
		if ch.GetJid().GetValue() == r.GetJid().GetValue() {
			return ch, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "no channel %s", r.GetJid().GetValue())
}

// GetNewsletterMessages answers the first `count` rows older than `before`
// (all of them when it is 0), refusing a count of 0 as the core does.
func (n *Newsletters) GetNewsletterMessages(_ context.Context, r *pb.GetNewsletterMessagesRequest) (*pb.NewsletterMessageList, error) {
	n.log(r)
	if r.GetCount() == 0 {
		return nil, status.Error(codes.InvalidArgument, "count is required")
	}
	var rows []*pb.NewsletterMessage
	for _, row := range n.Messages[r.GetJid().GetValue()] {
		if r.GetBefore() == 0 || row.GetServerId() < r.GetBefore() {
			rows = append(rows, row)
		}
	}
	if len(rows) > int(r.GetCount()) {
		rows = rows[:r.GetCount()]
	}
	return &pb.NewsletterMessageList{Messages: rows}, nil
}

// SendNewsletterPollVote checks the hashes as the core does (32 bytes each,
// at most 1000, none repeated) and keeps the last selection per poll.
func (n *Newsletters) SendNewsletterPollVote(_ context.Context, r *pb.SendNewsletterPollVoteRequest) (*pb.SendNewsletterPollVoteResponse, error) {
	n.log(r)
	if r.GetServerId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	if len(r.GetOptionHashes()) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "too many option hashes")
	}
	seen := map[string]bool{}
	for _, h := range r.GetOptionHashes() {
		if len(h) != 32 {
			return nil, status.Errorf(codes.InvalidArgument, "an option hash is %d bytes, not 32", len(h))
		}
		if seen[string(h)] {
			return nil, status.Error(codes.InvalidArgument, "repeated option hash")
		}
		seen[string(h)] = true
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.votes == nil {
		n.votes = map[uint64][][]byte{}
	}
	n.votes[r.GetServerId()] = r.GetOptionHashes()
	n.voted++
	return &pb.SendNewsletterPollVoteResponse{StanzaId: fmt.Sprintf("3EB0VOTE%04d", n.voted)}, nil
}

// PollVote is the last selection sent for the poll with this server id; ok
// is false when none was.
func (n *Newsletters) PollVote(serverID uint64) (hashes [][]byte, ok bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	hashes, ok = n.votes[serverID]
	return hashes, ok
}

// Following reports whether the account follows jid through FollowNewsletter
// (true) or left it through UnfollowNewsletter (false); ok is false when
// neither ran.
func (n *Newsletters) Following(jid string) (following, ok bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	following, ok = n.follow[jid]
	return following, ok
}

// Muted reports whether jid was last muted.
func (n *Newsletters) Muted(jid string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.muted[jid]
}

func (n *Newsletters) setFollow(r *pb.JidRequest, on bool) (*pb.Empty, error) {
	n.log(r)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.follow == nil {
		n.follow = map[string]bool{}
	}
	n.follow[r.GetJid().GetValue()] = on
	return &pb.Empty{}, nil
}

func (n *Newsletters) FollowNewsletter(_ context.Context, r *pb.JidRequest) (*pb.Empty, error) {
	return n.setFollow(r, true)
}

func (n *Newsletters) UnfollowNewsletter(_ context.Context, r *pb.JidRequest) (*pb.Empty, error) {
	return n.setFollow(r, false)
}

func (n *Newsletters) MuteNewsletter(_ context.Context, r *pb.NewsletterMuteRequest) (*pb.Empty, error) {
	n.log(r)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.muted == nil {
		n.muted = map[string]bool{}
	}
	n.muted[r.GetJid().GetValue()] = r.GetMuted()
	return &pb.Empty{}, nil
}

// CreateNewsletter refuses an empty name, as the core does, and answers a
// new channel this account owns.
func (n *Newsletters) CreateNewsletter(_ context.Context, r *pb.CreateNewsletterRequest) (*pb.Newsletter, error) {
	n.log(r)
	if r.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "a channel needs a name")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.made++
	ch := &pb.Newsletter{
		Jid:          &pb.Jid{Value: fmt.Sprintf("12036399999999%04d@newsletter", n.made)},
		Name:         r.GetName(),
		Description:  r.GetDescription(),
		Role:         pb.NewsletterRole_NEWSLETTER_ROLE_OWNER,
		Verification: pb.NewsletterVerification_NEWSLETTER_VERIFICATION_UNVERIFIED,
	}
	n.Channels = append(n.Channels, ch)
	return ch, nil
}
