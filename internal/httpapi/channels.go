package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/waha"
)

// defaultPreviewLimit is WAHA's PreviewChannelMessages default.
const defaultPreviewLimit = 10

// errInviteCode is the 501 for a channel named by invite code: the core
// resolves channels by jid only.
var errInviteCode = ErrNotImplemented("Looking a channel up by its invite code is not available in wamux; use the channel's @newsletter id.")

// Create, delete, follow, unfollow, mute, unmute and search have no core RPC
// and answer 501.
func (s *Server) registerChannels(r chi.Router) {
	read := Route{Action: ActRead, From: FromPath, Working: true}

	s.route(r, "GET", "/api/{session}/channels", read, func(c *Ctx) error {
		acc, err := c.Account()
		if err != nil {
			return err
		}
		res, err := s.Core.Newsletters.ListSubscribedNewsletters(c.R.Context(), acc)
		if err != nil {
			return err
		}
		role := c.Query("role")
		out := []waha.Channel{}
		for _, n := range res.GetNewsletters() {
			ch := waha.ChannelFromPB(n)
			if role == "" || ch.Role == role {
				out = append(out, ch)
			}
		}
		return c.JSON(http.StatusOK, out)
	})

	s.route(r, "GET", "/api/{session}/channels/{id}", read, func(c *Ctx) error {
		acc, jid, err := channelRef(c)
		if err != nil {
			return err
		}
		res, err := s.Core.Newsletters.GetNewsletterMetadata(c.R.Context(), &pb.JidRequest{Account: acc, Jid: jid})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, waha.ChannelFromPB(res))
	})

	s.route(r, "GET", "/api/{session}/channels/{id}/messages/preview", read, func(c *Ctx) error {
		limit, err := dirInt(c, "limit", defaultPreviewLimit)
		if err != nil {
			return err
		}
		if err := dirBool(c, "downloadMedia"); err != nil {
			return err
		}
		acc, jid, err := channelRef(c)
		if err != nil {
			return err
		}
		res, err := s.Core.Newsletters.GetNewsletterMessages(c.R.Context(), &pb.GetNewsletterMessagesRequest{Account: acc, Jid: jid, Count: uint32(max(limit, 0))})
		if err != nil {
			return err
		}
		out := []waha.ChannelMessage{}
		for _, m := range res.GetMessages() {
			out = append(out, waha.ChannelMessageFromPB(jid.GetValue(), m))
		}
		return c.JSON(http.StatusOK, out)
	})
}

// channelRef is the route's account and the {id} channel, which must be a
// `@newsletter` jid.
func channelRef(c *Ctx) (*pb.AccountRef, *pb.Jid, error) {
	id := c.Param("id")
	if !waha.IsNewsletter(id) {
		return nil, nil, errInviteCode
	}
	acc, err := c.Account()
	if err != nil {
		return nil, nil, err
	}
	return acc, &pb.Jid{Value: id}, nil
}
