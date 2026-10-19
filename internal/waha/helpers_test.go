package waha

import pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"

func jid(v string) *pb.Jid { return &pb.Jid{Value: v} }
