package fakecore

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// Calls is a CallService fake: it records each reject, as the core sends the
// stanza and answers nothing.
type Calls struct {
	pb.UnimplementedCallServiceServer
	RequestLog
}

func (c *Calls) RejectCall(_ context.Context, r *pb.RejectCallRequest) (*pb.Empty, error) {
	c.log(r)
	if r.GetCallId() == "" || r.GetPeer().GetValue() == "" || r.GetCallCreator().GetValue() == "" {
		return nil, status.Error(codes.InvalidArgument, "call_id, peer and call_creator are required")
	}
	return &pb.Empty{}, nil
}
