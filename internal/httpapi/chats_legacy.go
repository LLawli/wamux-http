package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LLawli/wamux-http/internal/waha"
)

// registerChatsLegacy serves the deprecated GET /api/messages, the chat's
// messages with the session and chat in the query.
func (s *Server) registerChatsLegacy(r chi.Router) {
	s.route(r, http.MethodGet, "/api/messages", Route{Action: ActRead, From: FromQuery}, func(c *Ctx) error {
		chatID := c.Query("chatId")
		if chatID == "" {
			return ErrBadRequest([]string{"chatId must be a string"})
		}
		jid, err := waha.ToPB(chatID)
		if err != nil {
			return ErrBadRequest("Invalid chatId: " + chatID)
		}
		return s.listMessages(c, waha.FromPB(jid))
	})
}
