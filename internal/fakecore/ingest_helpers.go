package fakecore

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// BadVoteIV marks a vote the fake cannot open.
const BadVoteIV = "bad"

// AggregatePollVotes tallies votes in a way a test can predict, since the
// fake has no keys: a vote's enc_payload is the chosen option names in
// clear, newline-separated, and a vote whose enc_iv is BadVoteIV counts as
// undecryptable. A missing secret is refused, as the core refuses one that
// is not 32 bytes. Last vote per voter wins.
func (s *messaging) AggregatePollVotes(_ context.Context, r *pb.AggregatePollVotesRequest) (*pb.PollTally, error) {
	s.c.record(r)
	if len(r.GetMessageSecret()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "message_secret must be 32 bytes")
	}
	choice := map[string][]string{}
	var order []string
	tally := &pb.PollTally{}
	for _, v := range r.GetVotes() {
		if string(v.GetEncIv()) == BadVoteIV {
			tally.Undecryptable++
			continue
		}
		voter := v.GetVoter().GetValue()
		if _, ok := choice[voter]; !ok {
			order = append(order, voter)
		}
		choice[voter] = nil
		if len(v.GetEncPayload()) > 0 {
			choice[voter] = strings.Split(string(v.GetEncPayload()), "\n")
		}
	}
	for _, opt := range r.GetOptions() {
		res := &pb.PollOptionResult{Option: opt}
		for _, voter := range order {
			for _, c := range choice[voter] {
				if c == opt {
					res.Voters = append(res.Voters, &pb.Jid{Value: voter})
				}
			}
		}
		tally.Results = append(tally.Results, res)
	}
	return tally, nil
}
