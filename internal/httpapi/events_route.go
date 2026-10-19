package httpapi

import "github.com/go-chi/chi/v5"

// registerEvents leaves POST /api/{session}/events (send an event message)
// to WAHA's 501: the core has no RPC for event messages.
func (s *Server) registerEvents(chi.Router) {}
