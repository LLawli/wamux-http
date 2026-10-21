package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

func (s *Server) registerCalls(r chi.Router) {
	write := Route{Action: ActSend, From: FromPath, Working: true}

	// The core wants the offer's peer and its call creator. WAHA's call.received
	// gives only `from`, so it goes as both, as whatsmeow's RejectCall (and so
	// GOWS) does.
	s.route(r, "POST", "/api/{session}/calls/reject", write, func(c *Ctx) error {
		v, err := requiredStrings(c, "from", "id")
		if err != nil {
			return err
		}
		jid, err := dirJID(v["from"])
		if err != nil {
			return err
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		if _, err := s.Core.Calls.RejectCall(c.R.Context(), &pb.RejectCallRequest{Account: acc, CallId: v["id"], Peer: jid, CallCreator: jid}); err != nil {
			return err
		}
		return c.Empty(http.StatusCreated)
	})
}
