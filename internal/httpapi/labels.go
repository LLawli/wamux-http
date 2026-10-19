package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// registerLabels serves the label reads from the store, as the phone synced
// them. Creating, editing, deleting and assigning labels have no core RPC:
// those routes answer 501.
func (s *Server) registerLabels(r chi.Router) {
	read := Route{Action: ActRead, From: FromPath}
	s.route(r, http.MethodGet, "/api/{session}/labels", read, func(c *Ctx) error {
		if _, err := c.knownSession(); err != nil {
			return err
		}
		ls, err := s.Store.ListLabels(c.R.Context(), c.Session)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, labelViews(ls))
	})
	s.route(r, http.MethodGet, "/api/{session}/labels/chats/{chatId}", read, func(c *Ctx) error {
		if _, err := c.knownSession(); err != nil {
			return err
		}
		chat, _, err := c.chatParam()
		if err != nil {
			return err
		}
		ls, err := s.Store.ChatLabels(c.R.Context(), c.Session, chat)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, labelViews(ls))
	})
	s.route(r, http.MethodGet, "/api/{session}/labels/{labelId}/chats", read, func(c *Ctx) error {
		if _, err := c.knownSession(); err != nil {
			return err
		}
		ids, err := s.Store.LabelChats(c.R.Context(), c.Session, c.Param("labelId"))
		if err != nil {
			return err
		}
		out := make([]waha.GroupID, 0, len(ids))
		for _, id := range ids {
			out = append(out, waha.GroupID{ID: id})
		}
		return c.JSON(http.StatusOK, out)
	})
}

func labelViews(ls []store.Label) []waha.Label {
	out := make([]waha.Label, 0, len(ls))
	for _, l := range ls {
		out = append(out, waha.NewLabel(l.ID, l.Name, l.Color))
	}
	return out
}
