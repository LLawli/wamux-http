// Package fakecore is an in-process wamux daemon on a Unix socket, for tests.
// It keeps accounts and an event bus the way the real core does, records
// every request, and lets a test drive what the phone would do (scan the QR,
// send a message, log out).
//
// Services with no built-in behavior answer Unimplemented unless the test
// passes its own implementation in Options.
package fakecore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// SubscribeDelay is how long SubscribeEvents takes to register a subscriber
// after answering.
var SubscribeDelay = 50 * time.Millisecond

const ringSize = 512

// Options overrides services the fake does not model itself.
type Options struct {
	Contacts    pb.ContactServiceServer
	Groups      pb.GroupServiceServer
	Media       pb.MediaServiceServer
	Newsletters pb.NewsletterServiceServer
}

// Account is the fake's view of one account.
type Account struct {
	UUID, ExternalRef string
	State             pb.ConnectionState
	JID, PushName     string
	Paired            bool
}

// Core is the running fake.
type Core struct {
	Socket string

	srv *grpc.Server
	mu  sync.Mutex
	acc map[string]*Account // by uuid
	seq int64
	ids int
	sub []chan *pb.EventEnvelope
	// ring keeps each account's recent events, written before the broadcast,
	// as the core's replay ring is.
	ring map[string][]*pb.EventEnvelope
	req  []proto.Message // every request, in order
}

// Start serves the fake on a socket in t's temp dir until the test ends.
func Start(t testing.TB, opts Options) *Core {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "wamux.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{Socket: sock, srv: grpc.NewServer(), acc: map[string]*Account{}}
	pb.RegisterAccountServiceServer(c.srv, &accounts{c: c})
	pb.RegisterEventServiceServer(c.srv, &events{c: c})
	pb.RegisterMessagingServiceServer(c.srv, &messaging{c: c})
	pb.RegisterAdminServiceServer(c.srv, admin{})
	pb.RegisterContactServiceServer(c.srv, orDefault[pb.ContactServiceServer](opts.Contacts, pb.UnimplementedContactServiceServer{}))
	pb.RegisterGroupServiceServer(c.srv, orDefault[pb.GroupServiceServer](opts.Groups, pb.UnimplementedGroupServiceServer{}))
	pb.RegisterMediaServiceServer(c.srv, orDefault[pb.MediaServiceServer](opts.Media, pb.UnimplementedMediaServiceServer{}))
	pb.RegisterNewsletterServiceServer(c.srv, orDefault[pb.NewsletterServiceServer](opts.Newsletters, pb.UnimplementedNewsletterServiceServer{}))
	go func() { _ = c.srv.Serve(lis) }()
	t.Cleanup(c.srv.Stop)
	return c
}

func orDefault[T any](v, def T) T {
	if any(v) == nil {
		return def
	}
	return v
}

// Requests returns every request received so far whose type is T.
func Requests[T proto.Message](c *Core) []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []T
	for _, r := range c.req {
		if v, ok := r.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

func (c *Core) record(m proto.Message) {
	c.mu.Lock()
	c.req = append(c.req, proto.Clone(m))
	c.mu.Unlock()
}

// Account returns a copy of an account, by uuid or external ref.
func (c *Core) Account(ref string) (Account, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.find(ref)
	if a == nil {
		return Account{}, false
	}
	return *a, true
}

func (c *Core) find(ref string) *Account {
	if a, ok := c.acc[ref]; ok {
		return a
	}
	for _, a := range c.acc {
		if a.ExternalRef != "" && a.ExternalRef == ref {
			return a
		}
	}
	return nil
}

func (c *Core) resolve(r *pb.AccountRef) (*Account, error) {
	var key string
	switch {
	case r.GetUuid() != "":
		key = r.GetUuid()
	case r.GetExternalRef() != "":
		key = r.GetExternalRef()
	default:
		return nil, status.Error(codes.InvalidArgument, "missing account")
	}
	if a := c.find(key); a != nil {
		return a, nil
	}
	return nil, status.Errorf(codes.NotFound, "account %s not found", key)
}

// Emit publishes an event for an account to every subscriber.
func (c *Core) Emit(uuid string, ev *pb.EventEnvelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emitLocked(uuid, ev)
}

func (c *Core) emitLocked(uuid string, ev *pb.EventEnvelope) {
	c.seq++
	ev.AccountUuid = uuid
	if c.ring == nil {
		c.ring = map[string][]*pb.EventEnvelope{}
	}
	ev.MonotonicSeq = c.seq
	if ev.TsUnixMs == 0 {
		ev.TsUnixMs = time.Now().UnixMilli()
	}
	c.ring[uuid] = append(c.ring[uuid], proto.Clone(ev).(*pb.EventEnvelope))
	if n := len(c.ring[uuid]); n > ringSize {
		c.ring[uuid] = c.ring[uuid][n-ringSize:]
	}
	for _, ch := range c.sub {
		select {
		case ch <- proto.Clone(ev).(*pb.EventEnvelope):
		default: // a subscriber that stopped reading loses events, like a lagged one
		}
	}
}

func (c *Core) setState(a *Account, s pb.ConnectionState) {
	a.State = s
	c.emitLocked(a.UUID, &pb.EventEnvelope{Event: &pb.EventEnvelope_Connection{Connection: &pb.ConnectionStateChanged{State: s}}})
}

// ScanQR plays the phone scanning the QR: the account pairs as jid and
// connects.
func (c *Core) ScanQR(ref, jid, pushName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.find(ref)
	if a == nil {
		return fmt.Errorf("no account %s", ref)
	}
	a.Paired, a.JID, a.PushName = true, jid, pushName
	c.emitLocked(a.UUID, &pb.EventEnvelope{Event: &pb.EventEnvelope_Pairing{Pairing: &pb.PairingUpdate{
		Event: &pb.PairingUpdate_Paired{Paired: &pb.PairedInfo{Jid: &pb.Jid{Value: jid}, Platform: "android"}}}}})
	c.setState(a, pb.ConnectionState_CONNECTED)
	return nil
}

// Disconnect plays a terminal state change (LOGGED_OUT, BANNED, DISCONNECTED).
func (c *Core) Disconnect(ref string, s pb.ConnectionState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a := c.find(ref); a != nil {
		if s == pb.ConnectionState_LOGGED_OUT {
			a.Paired, a.JID = false, ""
		}
		c.setState(a, s)
	}
}

func (c *Core) nextID() string {
	c.ids++
	return fmt.Sprintf("3EB0%016X", c.ids)
}

func status2(a *Account) *pb.AccountStatus {
	st := &pb.AccountStatus{Uuid: a.UUID, State: a.State, PushName: a.PushName}
	if a.JID != "" {
		st.Jid = &pb.Jid{Value: a.JID}
	}
	return st
}

// --- AccountService ---

type accounts struct {
	pb.UnimplementedAccountServiceServer
	c *Core
}

func (s *accounts) CreateAccount(_ context.Context, r *pb.CreateAccountRequest) (*pb.Account, error) {
	s.c.record(r)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if r.ExternalRef != nil && s.c.find(r.GetExternalRef()) != nil {
		return nil, status.Errorf(codes.AlreadyExists, "external_ref %q taken", r.GetExternalRef())
	}
	a := &Account{UUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", len(s.c.acc)+1), ExternalRef: r.GetExternalRef(), State: pb.ConnectionState_DISCONNECTED}
	s.c.acc[a.UUID] = a
	return &pb.Account{Uuid: a.UUID, ExternalRef: a.ExternalRef, State: a.State}, nil
}

func (s *accounts) ListAccounts(_ context.Context, r *pb.ListAccountsRequest) (*pb.ListAccountsResponse, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	out := &pb.ListAccountsResponse{}
	for _, a := range s.c.acc {
		acc := &pb.Account{Uuid: a.UUID, ExternalRef: a.ExternalRef, State: a.State, PushName: a.PushName}
		if a.JID != "" {
			acc.Jid = &pb.Jid{Value: a.JID}
		}
		out.Accounts = append(out.Accounts, acc)
	}
	return out, nil
}

func (s *accounts) GetAccountStatus(_ context.Context, r *pb.AccountRef) (*pb.AccountStatus, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	a, err := s.c.resolve(r)
	if err != nil {
		return nil, err
	}
	return status2(a), nil
}

// ConnectAccount connects a paired account; an unpaired one starts the QR
// flow on the event bus, as the real core does.
func (s *accounts) ConnectAccount(_ context.Context, r *pb.ConnectAccountRequest) (*pb.AccountStatus, error) {
	s.c.record(r)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	a, err := s.c.resolve(r.GetAccount())
	if err != nil {
		return nil, err
	}
	if a.State == pb.ConnectionState_CONNECTED || a.State == pb.ConnectionState_CONNECTING {
		return status2(a), nil
	}
	if a.Paired {
		s.c.setState(a, pb.ConnectionState_CONNECTED)
	} else {
		s.c.setState(a, pb.ConnectionState_CONNECTING)
		s.c.emitLocked(a.UUID, &pb.EventEnvelope{Event: &pb.EventEnvelope_Pairing{Pairing: &pb.PairingUpdate{
			Event: &pb.PairingUpdate_QrCode{QrCode: "2@fake-qr-" + a.UUID}}}})
	}
	return status2(a), nil
}

func (s *accounts) PairWithCode(r *pb.PairWithCodeRequest, stream pb.AccountService_PairWithCodeServer) error {
	s.c.record(r)
	s.c.mu.Lock()
	a, err := s.c.resolve(r.GetAccount())
	if err == nil && a.State != pb.ConnectionState_CONNECTING {
		s.c.setState(a, pb.ConnectionState_CONNECTING)
	}
	s.c.mu.Unlock()
	if err != nil {
		return err
	}
	return stream.Send(&pb.PairingUpdate{Event: &pb.PairingUpdate_PairCode{PairCode: "ABCD-EFGH"}})
}

func (s *accounts) DisconnectAccount(_ context.Context, r *pb.AccountRef) (*pb.AccountStatus, error) {
	s.c.record(r)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	a, err := s.c.resolve(r)
	if err != nil {
		return nil, err
	}
	if a.State != pb.ConnectionState_DISCONNECTED {
		s.c.setState(a, pb.ConnectionState_DISCONNECTED)
	}
	return status2(a), nil
}

func (s *accounts) Logout(_ context.Context, r *pb.AccountRef) (*pb.Empty, error) {
	s.c.record(r)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	a, err := s.c.resolve(r)
	if err != nil {
		return nil, err
	}
	a.Paired, a.JID = false, ""
	s.c.setState(a, pb.ConnectionState_LOGGED_OUT)
	return &pb.Empty{}, nil
}

func (s *accounts) DeleteAccount(_ context.Context, r *pb.AccountRef) (*pb.Empty, error) {
	s.c.record(r)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	a, err := s.c.resolve(r)
	if err != nil {
		return nil, err
	}
	delete(s.c.acc, a.UUID)
	return &pb.Empty{}, nil
}

// --- EventService ---

type events struct {
	pb.UnimplementedEventServiceServer
	c *Core
}

// SubscribeEvents registers the subscriber only after a delay, like the core,
// whose forwarder task subscribes after the RPC has answered: an edge that
// does not replay the ring loses what is emitted in between.
func (s *events) SubscribeEvents(r *pb.SubscribeRequest, stream pb.EventService_SubscribeEventsServer) error {
	s.c.record(r)
	_ = stream.SendHeader(nil)
	time.Sleep(SubscribeDelay)
	ch := make(chan *pb.EventEnvelope, 1024)
	s.c.mu.Lock()
	s.c.sub = append(s.c.sub, ch)
	var replay []*pb.EventEnvelope
	if n := int(r.GetReplayFromRing()); n > 0 {
		for uuid, evs := range s.c.ring {
			if want := r.GetAccount(); want != nil {
				if a := s.c.find(want.GetUuid() + want.GetExternalRef()); a == nil || a.UUID != uuid {
					continue
				}
			}
			if len(evs) > n {
				evs = evs[len(evs)-n:]
			}
			replay = append(replay, evs...)
		}
	}
	s.c.mu.Unlock()
	for _, ev := range replay {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	defer func() {
		s.c.mu.Lock()
		for i, x := range s.c.sub {
			if x == ch {
				s.c.sub = append(s.c.sub[:i], s.c.sub[i+1:]...)
				break
			}
		}
		s.c.mu.Unlock()
	}()
	want := r.GetAccount()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev := <-ch:
			if want != nil {
				s.c.mu.Lock()
				a := s.c.find(want.GetUuid() + want.GetExternalRef())
				s.c.mu.Unlock()
				if a == nil || a.UUID != ev.AccountUuid {
					continue
				}
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// --- MessagingService: sends answer with a key and echo, like the core ---

type messaging struct {
	pb.UnimplementedMessagingServiceServer
	c *Core
}

func (s *messaging) sent(acc *pb.AccountRef, to *pb.Jid, fill func(m *pb.InboundMessage)) (*pb.SendResult, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	a, err := s.c.resolve(acc)
	if err != nil {
		return nil, err
	}
	if a.State != pb.ConnectionState_CONNECTED {
		return nil, status.Error(codes.FailedPrecondition, "account not connected")
	}
	key := &pb.MessageKey{Chat: to, Id: s.c.nextID(), FromMe: true}
	now := time.Now().UnixMilli()
	echo := &pb.InboundMessage{Key: key, Chat: to, Sender: &pb.Jid{Value: a.JID}, Timestamp: now}
	if fill != nil {
		fill(echo)
	}
	s.c.emitLocked(a.UUID, &pb.EventEnvelope{Event: &pb.EventEnvelope_Message{Message: echo}})
	// The core always answers server_timestamp 0; the send time is the
	// ServerAckEvent's.
	return &pb.SendResult{Key: key}, nil
}

func (s *messaging) SendText(_ context.Context, r *pb.SendTextRequest) (*pb.SendResult, error) {
	s.c.record(r)
	return s.sent(r.GetAccount(), r.GetTo(), func(m *pb.InboundMessage) { m.Text = r.GetText(); m.Quote = r.GetQuote() })
}

func (s *messaging) SendMedia(stream pb.MessagingService_SendMediaServer) error {
	var hdr *pb.SendMediaHeader
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
	res, err := s.sent(hdr.GetAccount(), hdr.GetTo(), func(m *pb.InboundMessage) {
		m.Caption = hdr.GetCaption()
		m.Media = &pb.MediaDescriptor{MimeType: hdr.GetMimeType(), MediaType: hdr.GetMediaType(), FileLength: uint64(size), DirectPath: "/fake/" + hdr.GetFilename()}
	})
	if err != nil {
		return err
	}
	return stream.SendAndClose(res)
}

func (s *messaging) SendReaction(_ context.Context, r *pb.SendReactionRequest) (*pb.SendResult, error) {
	s.c.record(r)
	return s.sent(r.GetAccount(), r.GetTarget().GetChat(), func(m *pb.InboundMessage) { m.Reaction = r.GetEmoji(); m.ReactionTarget = r.GetTarget() })
}

func (s *messaging) EditMessage(_ context.Context, r *pb.EditMessageRequest) (*pb.SendResult, error) {
	s.c.record(r)
	return s.sent(r.GetAccount(), r.GetTarget().GetChat(), func(m *pb.InboundMessage) { m.IsEdit, m.Text, m.ProtocolTarget = true, r.GetNewText(), r.GetTarget() })
}

func (s *messaging) DeleteMessage(_ context.Context, r *pb.DeleteMessageRequest) (*pb.SendResult, error) {
	s.c.record(r)
	return s.sent(r.GetAccount(), r.GetTarget().GetChat(), func(m *pb.InboundMessage) { m.IsDelete, m.ProtocolTarget = true, r.GetTarget() })
}

func (s *messaging) SendContact(_ context.Context, r *pb.SendContactRequest) (*pb.SendResult, error) {
	s.c.record(r)
	return s.sent(r.GetAccount(), r.GetTo(), nil)
}

func (s *messaging) SendPoll(_ context.Context, r *pb.SendPollRequest) (*pb.SendPollResult, error) {
	s.c.record(r)
	res, err := s.sent(r.GetAccount(), r.GetTo(), nil)
	if err != nil {
		return nil, err
	}
	return &pb.SendPollResult{Key: res.GetKey(), MessageSecret: make([]byte, 32)}, nil
}

func (s *messaging) empty(m proto.Message, acc *pb.AccountRef) (*pb.Empty, error) {
	s.c.record(m)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if _, err := s.c.resolve(acc); err != nil {
		return nil, err
	}
	return &pb.Empty{}, nil
}

func (s *messaging) SendPresence(_ context.Context, r *pb.SendPresenceRequest) (*pb.Empty, error) {
	return s.empty(r, r.GetAccount())
}
func (s *messaging) MarkRead(_ context.Context, r *pb.MarkReadRequest) (*pb.Empty, error) {
	return s.empty(r, r.GetAccount())
}
func (s *messaging) MarkChatRead(_ context.Context, r *pb.MarkReadRequest) (*pb.Empty, error) {
	return s.empty(r, r.GetAccount())
}
func (s *messaging) MarkUnread(_ context.Context, r *pb.MarkReadRequest) (*pb.Empty, error) {
	return s.empty(r, r.GetAccount())
}
func (s *messaging) StarMessage(_ context.Context, r *pb.StarMessageRequest) (*pb.Empty, error) {
	return s.empty(r, r.GetAccount())
}
func (s *messaging) ArchiveChat(_ context.Context, r *pb.ArchiveChatRequest) (*pb.Empty, error) {
	return s.empty(r, r.GetAccount())
}
func (s *messaging) PinChat(_ context.Context, r *pb.PinChatRequest) (*pb.Empty, error) {
	return s.empty(r, r.GetAccount())
}
func (s *messaging) MuteChat(_ context.Context, r *pb.MuteChatRequest) (*pb.Empty, error) {
	return s.empty(r, r.GetAccount())
}
func (s *messaging) DeleteChat(_ context.Context, r *pb.DeleteChatRequest) (*pb.Empty, error) {
	return s.empty(r, r.GetAccount())
}

// --- AdminService ---

type admin struct {
	pb.UnimplementedAdminServiceServer
}

func (admin) Check(context.Context, *pb.Empty) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Serving: true, Ready: true, Version: "0.2.0-fake", BlobFormat: pb.BlobFormat_BLOB_FORMAT_PROTOBUF}, nil
}

func (admin) GetMetrics(context.Context, *pb.Empty) (*pb.MetricsResponse, error) {
	return &pb.MetricsResponse{Prometheus: "# fake\n"}, nil
}
