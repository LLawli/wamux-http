package fakecore

import (
	"context"

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

// GetNewsletterMessages answers the first `count` rows, refusing a count of
// 0 as the core does.
func (n *Newsletters) GetNewsletterMessages(_ context.Context, r *pb.GetNewsletterMessagesRequest) (*pb.NewsletterMessageList, error) {
	n.log(r)
	if r.GetCount() == 0 {
		return nil, status.Error(codes.InvalidArgument, "count is required")
	}
	rows := n.Messages[r.GetJid().GetValue()]
	if len(rows) > int(r.GetCount()) {
		rows = rows[:r.GetCount()]
	}
	return &pb.NewsletterMessageList{Messages: rows}, nil
}
