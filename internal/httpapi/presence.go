package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// presenceStates maps WAHA's presence to the core's. online and offline are
// the account's own presence; the others are a chat state in one chat.
var presenceStates = map[string]pb.PresenceState{
	"online":    pb.PresenceState_PRESENCE_STATE_AVAILABLE,
	"offline":   pb.PresenceState_PRESENCE_STATE_UNAVAILABLE,
	"typing":    pb.PresenceState_PRESENCE_STATE_COMPOSING,
	"recording": pb.PresenceState_PRESENCE_STATE_RECORDING,
	"paused":    pb.PresenceState_PRESENCE_STATE_PAUSED,
}

// registerPresence serves the Presence tag. The presences read back are the
// ones the ingest kept from presence events.
func (s *Server) registerPresence(r chi.Router) {
	write := Route{Action: ActSend, From: FromPath, Working: true}
	s.route(r, http.MethodPost, "/api/{session}/presence", write, s.setPresence)
	s.route(r, http.MethodGet, "/api/{session}/presence", Route{Action: ActRead, From: FromPath}, func(c *Ctx) error {
		if _, err := c.knownSession(); err != nil {
			return err
		}
		ps, err := s.Store.ListPresences(c.R.Context(), c.Session, "")
		if err != nil {
			return err
		}
		out := []waha.ChatPresences{}
		for _, p := range ps {
			if len(out) == 0 || out[len(out)-1].ID != p.ChatID {
				out = append(out, waha.ChatPresences{ID: p.ChatID, Presences: []waha.PresenceData{}})
			}
			last := &out[len(out)-1]
			last.Presences = append(last.Presences, presenceData(p))
		}
		return c.JSON(http.StatusOK, out)
	})
	s.route(r, http.MethodGet, "/api/{session}/presence/{chatId}", Route{Action: ActRead, From: FromPath, Working: true}, func(c *Ctx) error {
		chat, err := s.subscribePresence(c)
		if err != nil {
			return err
		}
		ps, err := s.Store.ListPresences(c.R.Context(), c.Session, chat)
		if err != nil {
			return err
		}
		out := waha.ChatPresences{ID: chat, Presences: []waha.PresenceData{}}
		for _, p := range ps {
			out.Presences = append(out.Presences, presenceData(p))
		}
		return c.JSON(http.StatusOK, out)
	})
	s.route(r, http.MethodPost, "/api/{session}/presence/{chatId}/subscribe", write, func(c *Ctx) error {
		if _, err := s.subscribePresence(c); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, struct{}{})
	})
}

func presenceData(p store.Presence) waha.PresenceData {
	d := waha.PresenceData{Participant: p.Participant, LastKnownPresence: p.State}
	if p.LastSeen > 0 {
		seen := p.LastSeen
		d.LastSeen = &seen
	}
	return d
}

// subscribePresence asks the server for the chat's presence updates and
// returns the chat in WAHA form.
func (s *Server) subscribePresence(c *Ctx) (string, error) {
	chat, jid, err := c.chatParam()
	if err != nil {
		return "", err
	}
	acc, err := c.Account()
	if err != nil {
		return "", err
	}
	_, err = s.Core.Contacts.SubscribePresence(c.R.Context(), &pb.SubscribePresenceRequest{Account: acc, Jid: jid})
	return chat, err
}

// setPresence is POST /api/{session}/presence, with WAHA's checks: online
// and offline take no chatId, the chat states need one.
func (s *Server) setPresence(c *Ctx) error {
	var body struct {
		ChatID   string `json:"chatId"`
		Presence string `json:"presence"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	state, ok := presenceStates[body.Presence]
	if !ok {
		return ErrBadRequest([]string{"presence must be one of the following values: offline, online, typing, recording, paused"})
	}
	global := body.Presence == "online" || body.Presence == "offline"
	switch {
	case global && body.ChatID != "":
		return &Error{Status: http.StatusBadRequest, Body: map[string]string{
			"detail": "'" + body.Presence + "' presence works on the global scope and doesn't require 'chatId' field.",
		}}
	case !global && body.ChatID == "":
		return &Error{Status: http.StatusBadRequest, Body: map[string]string{
			"detail": "'" + body.Presence + "' presence requires 'chatId' field.",
		}}
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	req := &pb.SendPresenceRequest{Account: acc, State: state}
	if !global {
		if req.Chat, err = waha.ToPB(body.ChatID); err != nil {
			return ErrBadRequest("Invalid chatId: " + body.ChatID)
		}
	}
	if _, err := s.Core.Messaging.SendPresence(c.R.Context(), req); err != nil {
		return err
	}
	return c.Empty(http.StatusCreated)
}
