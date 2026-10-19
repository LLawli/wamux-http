package fakecore

import (
	"context"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// statusJID is where statuses go.
var statusJID = &pb.Jid{Value: "status@broadcast"}

func (s *messaging) SendInteractiveReply(_ context.Context, r *pb.SendInteractiveReplyRequest) (*pb.SendResult, error) {
	s.c.record(r)
	if r.GetQuote() == nil {
		return nil, status.Error(codes.InvalidArgument, "an interactive reply must quote the offer")
	}
	return s.sent(r.GetAccount(), r.GetTo(), func(m *pb.InboundMessage) { m.Quote = r.GetQuote() })
}

func (s *messaging) SendPollVote(_ context.Context, r *pb.SendPollVoteRequest) (*pb.SendResult, error) {
	s.c.record(r)
	if len(r.GetMessageSecret()) != 32 {
		return nil, status.Error(codes.InvalidArgument, "message_secret must be 32 bytes")
	}
	return s.sent(r.GetAccount(), r.GetChat(), nil)
}

func (s *messaging) PostStatusText(_ context.Context, r *pb.PostStatusTextRequest) (*pb.SendResult, error) {
	s.c.record(r)
	return s.sent(r.GetAccount(), statusJID, func(m *pb.InboundMessage) { m.Text = r.GetText() })
}

func (s *messaging) PostStatusMedia(stream pb.MessagingService_PostStatusMediaServer) error {
	var hdr *pb.PostStatusMediaHeader
	var size int
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h := chunk.GetHeader(); h != nil {
			hdr = h
		}
		size += len(chunk.GetChunk())
	}
	if hdr == nil {
		return status.Error(codes.InvalidArgument, "missing header")
	}
	s.c.record(hdr)
	s.c.record(&pb.MediaMeta{MimeType: hdr.GetMimeType(), FileLength: uint64(size)})
	res, err := s.sent(hdr.GetAccount(), statusJID, func(m *pb.InboundMessage) {
		m.Caption = hdr.GetCaption()
		m.Media = &pb.MediaDescriptor{MimeType: hdr.GetMimeType(), MediaType: hdr.GetMediaType(), FileLength: uint64(size)}
	})
	if err != nil {
		return err
	}
	return stream.SendAndClose(res)
}

func (s *messaging) RevokeStatus(_ context.Context, r *pb.RevokeStatusRequest) (*pb.SendResult, error) {
	s.c.record(r)
	return s.sent(r.GetAccount(), statusJID, func(m *pb.InboundMessage) {
		m.IsDelete = true
		m.ProtocolTarget = &pb.MessageKey{Chat: statusJID, Id: r.GetMessageId(), FromMe: true}
	})
}

func (s *messaging) GetStatusAudience(_ context.Context, r *pb.AccountRef) (*pb.StatusAudienceResponse, error) {
	s.c.record(r)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if _, err := s.c.resolve(r); err != nil {
		return nil, err
	}
	return &pb.StatusAudienceResponse{}, nil
}

// MediaFiles is a MediaService for Options.Media: DownloadMedia answers the
// bytes registered for a descriptor's direct_path, as a meta frame and then
// chunks, the way the core streams a download.
type MediaFiles struct {
	pb.UnimplementedMediaServiceServer

	mu    sync.Mutex
	files map[string][]byte
	reqs  []*pb.DownloadMediaRequest
}

// NewMediaFiles returns an empty MediaFiles.
func NewMediaFiles() *MediaFiles { return &MediaFiles{files: map[string][]byte{}} }

// Put registers the bytes a direct_path downloads as.
func (m *MediaFiles) Put(directPath string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[directPath] = data
}

// Requests is every DownloadMedia request received so far.
func (m *MediaFiles) Requests() []*pb.DownloadMediaRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*pb.DownloadMediaRequest(nil), m.reqs...)
}

// downloadChunk is the size of each streamed chunk.
const downloadChunk = 16 << 10

// DownloadMedia streams a registered file.
func (m *MediaFiles) DownloadMedia(r *pb.DownloadMediaRequest, stream pb.MediaService_DownloadMediaServer) error {
	m.mu.Lock()
	m.reqs = append(m.reqs, r)
	data, ok := m.files[r.GetDescriptor_().GetDirectPath()]
	m.mu.Unlock()
	if !ok {
		return status.Error(codes.NotFound, "media not found")
	}
	if err := stream.Send(&pb.MediaChunk{Part: &pb.MediaChunk_Meta{Meta: &pb.MediaMeta{
		MimeType: r.GetDescriptor_().GetMimeType(), FileLength: uint64(len(data))}}}); err != nil {
		return err
	}
	for off := 0; off < len(data); off += downloadChunk {
		end := min(off+downloadChunk, len(data))
		if err := stream.Send(&pb.MediaChunk{Part: &pb.MediaChunk_Chunk{Chunk: data[off:end]}}); err != nil {
			return err
		}
	}
	return nil
}
