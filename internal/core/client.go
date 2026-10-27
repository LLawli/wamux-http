// Package core is the only package that speaks gRPC to the wamux daemon. It
// knows nothing about WAHA: it hands out the generated service clients and
// turns a gRPC status into an *Error the rest of the edge can branch on.
package core

import (
	"context"
	"errors"
	"fmt"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// Client bundles one connection to the core's socket and every service on it.
type Client struct {
	conn        *grpc.ClientConn
	Accounts    pb.AccountServiceClient
	Messaging   pb.MessagingServiceClient
	Events      pb.EventServiceClient
	Contacts    pb.ContactServiceClient
	Groups      pb.GroupServiceClient
	Media       pb.MediaServiceClient
	Newsletters pb.NewsletterServiceClient
	Calls       pb.CallServiceClient
	Admin       pb.AdminServiceClient
}

// maxRecvBytes bounds one message from the core. gRPC's default of 4 MiB is
// smaller than a history sync event can be (4.67 MB measured live, which
// broke the event stream); the socket is local and the core trusted, so
// the bound only stops a runaway.
const maxRecvBytes = 256 << 20

// Dial connects to the core's Unix socket. The connection is lazy: a missing
// socket shows up as Unavailable on the first call, not here, so the edge can
// start before the core does.
func Dial(socketPath string) (*Client, error) {
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(unaryTrailers),
		grpc.WithStreamInterceptor(streamTrailers),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxRecvBytes)),
	)
	if err != nil {
		return nil, fmt.Errorf("dial core at %s: %w", socketPath, err)
	}
	return &Client{
		conn:        conn,
		Accounts:    pb.NewAccountServiceClient(conn),
		Messaging:   pb.NewMessagingServiceClient(conn),
		Events:      pb.NewEventServiceClient(conn),
		Contacts:    pb.NewContactServiceClient(conn),
		Groups:      pb.NewGroupServiceClient(conn),
		Media:       pb.NewMediaServiceClient(conn),
		Newsletters: pb.NewNewsletterServiceClient(conn),
		Calls:       pb.NewCallServiceClient(conn),
		Admin:       pb.NewAdminServiceClient(conn),
	}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Error is a failed core call. WACode and WAText are the WhatsApp server's own
// verdict when the core relayed one (the wa-code / wa-text trailers), empty
// when the failure was the core's or the transport's.
type Error struct {
	Code    codes.Code
	Message string
	WACode  string
	WAText  string
}

func (e *Error) Error() string {
	if e.WACode != "" {
		return fmt.Sprintf("core: %s: %s (wa-code %s)", e.Code, e.Message, e.WACode)
	}
	return fmt.Sprintf("core: %s: %s", e.Code, e.Message)
}

// AsError unwraps an *Error from err.
func AsError(err error) (*Error, bool) {
	var ce *Error
	ok := errors.As(err, &ce)
	return ce, ok
}

// CodeOf is the gRPC code of err: OK for nil, Unknown for an error that did
// not come from the core.
func CodeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	if ce, ok := AsError(err); ok {
		return ce.Code
	}
	return codes.Unknown
}

func convert(err error, md metadata.MD) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	return &Error{
		Code:    st.Code(),
		Message: st.Message(),
		WACode:  first(md, "wa-code"),
		WAText:  first(md, "wa-text"),
	}
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func unaryTrailers(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	var md metadata.MD
	opts = append(opts, grpc.Trailer(&md))
	return convert(invoker(ctx, method, req, reply, cc, opts...), md)
}

func streamTrailers(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	s, err := streamer(ctx, desc, cc, method, opts...)
	if err != nil {
		return nil, convert(err, nil)
	}
	return &trailerStream{ClientStream: s}, nil
}

// trailerStream converts the error that ends a stream, reading the trailers
// the server sent with it.
type trailerStream struct{ grpc.ClientStream }

func (s *trailerStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	return convert(err, s.Trailer())
}
