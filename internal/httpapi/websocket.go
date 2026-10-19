package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/LLawli/wamux-http/internal/waha"
)

// wsPing is how often the server pings a WebSocket client, as WAHA does.
const wsPing = 60 * time.Second

// registerWebSocket serves /ws: every event of the selected sessions, one
// text frame each, in the same envelope the webhooks get. The key comes
// from the query only, as in WAHA.
func (s *Server) registerWebSocket(r chi.Router) {
	r.Get("/ws", func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		q := req.URL.Query()
		p, err := s.authenticateWS(req)
		if err != nil {
			_ = conn.Close(websocket.StatusPolicyViolation, "Unauthorized")
			return
		}
		session := q.Get("session")
		if session == "" {
			session = "*"
		}
		if session == "*" && !p.Admin {
			session = p.Session // a session key only ever sees its own
		}
		if session != "*" && !p.Allows(ActRead, session) {
			_ = conn.Close(websocket.StatusPolicyViolation, "Forbidden")
			return
		}
		var selected []string
		for _, v := range q["events"] {
			for _, e := range strings.Split(v, ",") {
				if e = strings.TrimSpace(e); e != "" {
					selected = append(selected, e)
				}
			}
		}
		if len(selected) == 0 {
			selected = []string{"*"}
		}
		s.streamWS(req.Context(), conn, session, selected)
	})
}

func (s *Server) authenticateWS(req *http.Request) (Principal, error) {
	// authenticate reads the header first; a WebSocket client cannot set
	// one in a browser, and WAHA reads the query only.
	r := req.Clone(req.Context())
	r.Header.Del("X-Api-Key")
	return s.authenticate(req.Context(), r)
}

func (s *Server) streamWS(ctx context.Context, conn *websocket.Conn, session string, selected []string) {
	ctx = conn.CloseRead(ctx) // client messages are ignored; this notices a close
	events, cancel := s.Tail.Subscribe(256)
	defer cancel()
	ping := time.NewTicker(wsPing)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, wsPing)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				_ = conn.CloseNow()
				return
			}
		case ev, ok := <-events:
			if !ok {
				return
			}
			if session != "*" && ev.Session != session {
				continue
			}
			if !waha.EventMatches(selected, ev.Type) {
				continue
			}
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, ev.Body)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}
