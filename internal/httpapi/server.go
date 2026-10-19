// Package httpapi serves WAHA's HTTP API. Each WAHA tag has its own file and
// registers its routes through Server.route, which authenticates, resolves
// the session the route acts on, checks the key's policy and, for routes that
// need a running session, waits for it the way WAHA does.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LLawli/wamux-http/contract"
	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/core"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/events"
	"github.com/LLawli/wamux-http/internal/media"
	"github.com/LLawli/wamux-http/internal/sessions"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// Server is the HTTP API and everything its handlers reach.
type Server struct {
	Config   *config.Config
	Store    store.Store
	Core     *core.Client
	Sessions *sessions.Manager
	Pub      *events.Publisher
	Media    *media.Service
	Tail     *events.Tail
	Log      *slog.Logger
	Started  time.Time
	Shutdown func() // POST /api/server/stop; nil disables the route

	registered map[string]bool // "METHOD /path" of every route an area registered

	// How long a working-session route waits for the session to exist and
	// to be WORKING before answering 422 (WAHA: 5 s and 10 s).
	WaitExists, WaitStatus time.Duration
}

// SessionFrom says where a route reads its session name.
type SessionFrom int

// Where a session name can come from.
const (
	NoSession SessionFrom = iota
	FromPath              // the {session} path parameter
	FromBody              // the "session" field of the JSON body
	FromQuery             // the "session" query parameter
)

// Route is one route's policy.
type Route struct {
	Action  Action
	From    SessionFrom
	Working bool // needs the session in WORKING
}

// Ctx is what a handler gets.
type Ctx struct {
	W         http.ResponseWriter
	R         *http.Request
	S         *Server
	Principal Principal
	Session   string // the session name the route acts on ("" for NoSession)
	Body      []byte // the request body, read once

	sess *store.Session
}

// Handler is a route's implementation. A returned error becomes the response.
type Handler func(c *Ctx) error

// Router builds the whole HTTP handler.
func (s *Server) Router() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.WaitExists == 0 {
		s.WaitExists = 5 * time.Second
	}
	if s.WaitStatus == 0 {
		s.WaitStatus = 10 * time.Second
	}
	s.registered = map[string]bool{}
	r := chi.NewRouter()
	r.Use(s.recoverer)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, ErrNotFound(fmt.Sprintf("Cannot %s %s", r.Method, r.URL.Path)).Body)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, ErrNotFound(fmt.Sprintf("Cannot %s %s", r.Method, r.URL.Path)).Body)
	})
	for _, register := range []func(chi.Router){
		s.registerObservability,
		s.registerSessions,
		s.registerPairing,
		s.registerKeys,
		s.registerProfile,
		s.registerChatting,
		s.registerChats,
		s.registerContacts,
		s.registerGroups,
		s.registerPresence,
		s.registerStatus,
		s.registerChannels,
		s.registerLabels,
		s.registerMedia,
		s.registerCalls,
		s.registerEvents,
		s.registerApps,
		s.registerFiles,
		s.registerWebSocket,
	} {
		register(r)
	}
	// Every WAHA operation no area implements answers WAHA's 501, behind
	// authentication like any other route. Coverage() lists them.
	for _, op := range contract.Operations() {
		if !s.registered[op.Method+" "+op.Path] {
			s.route(r, op.Method, op.Path, Route{Action: ActRetrieve}, func(*Ctx) error {
				return ErrNotImplemented("")
			})
			s.registered[op.Method+" "+op.Path] = false
		}
	}
	return r
}

// Coverage lists the WAHA operations that answer 501, as "METHOD /path".
func (s *Server) Coverage() []string {
	var out []string
	for _, op := range contract.Operations() {
		if !s.registered[op.Method+" "+op.Path] {
			out = append(out, op.Method+" "+op.Path)
		}
	}
	return out
}

// route registers one API route behind authentication and policy.
func (s *Server) route(r chi.Router, method, path string, rt Route, h Handler) {
	if s.registered == nil {
		s.registered = map[string]bool{}
	}
	if _, done := s.registered[method+" "+path]; !done {
		s.registered[method+" "+path] = true
	}
	r.MethodFunc(method, path, func(w http.ResponseWriter, req *http.Request) {
		c := &Ctx{W: w, R: req, S: s}
		if err := s.serve(c, rt, h); err != nil {
			e := fromError(err, req)
			if e.Status >= 500 {
				s.Log.Error("request failed", "method", req.Method, "path", req.URL.Path, "status", e.Status, "err", err)
			}
			writeJSON(w, e.Status, e.Body)
		}
	})
}

func (s *Server) serve(c *Ctx, rt Route, h Handler) error {
	ctx := c.R.Context()
	p, err := s.authenticate(ctx, c.R)
	if err != nil {
		return err
	}
	c.Principal = p
	if c.R.Body != nil && c.R.Method != http.MethodGet {
		body, err := io.ReadAll(http.MaxBytesReader(c.W, c.R.Body, s.Config.MaxRequestBytes))
		if err != nil {
			return ErrBadRequest("request body too large or unreadable")
		}
		c.Body = body
	}
	switch rt.From {
	case FromPath:
		c.Session = chi.URLParam(c.R, "session")
	case FromBody:
		var b struct {
			Session any `json:"session"`
		}
		_ = json.Unmarshal(c.Body, &b)
		if name, ok := b.Session.(string); ok {
			c.Session = name
		}
	case FromQuery:
		c.Session = c.R.URL.Query().Get("session")
	}
	if rt.From != NoSession && rt.From != FromPath && strings.TrimSpace(c.Session) == "" {
		return ErrBadRequest("Session name is required")
	}
	if !p.Allows(rt.Action, c.Session) {
		return ErrForbidden()
	}
	if rt.Working {
		sess, err := s.waitStatus(ctx, c.Session, sessions.StatusWorking)
		if err != nil {
			return err
		}
		c.sess = &sess
	}
	return h(c)
}

// waitStatus waits for the session to exist and be in one of the expected
// statuses, polling like WAHA, and answers WAHA's 422s when it does not get
// there.
func (s *Server) waitStatus(ctx context.Context, name string, expected ...string) (store.Session, error) {
	deadline := time.Now().Add(s.WaitExists)
	for {
		sess, err := s.Store.GetSession(ctx, name)
		if err == nil {
			if !sess.Started {
				return sess, ErrSessionStatus(name, sessions.StatusStopped, expected...)
			}
			break
		}
		if !errors.Is(err, store.ErrNotFound) {
			return sess, err
		}
		if time.Now().After(deadline) {
			return sess, ErrSessionMissing(name)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return sess, err
		}
	}
	deadline = time.Now().Add(s.WaitStatus)
	for {
		sess, err := s.Store.GetSession(ctx, name)
		if err != nil {
			return sess, err
		}
		for _, want := range expected {
			if sess.Status == want {
				return sess, nil
			}
		}
		if time.Now().After(deadline) || !sess.Started {
			return sess, ErrSessionStatus(name, sess.Status, expected...)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return sess, err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Sess is the route's session, loaded once. A route that is not Working gets
// the 404 of WAHA's session routes for an unknown session.
func (c *Ctx) Sess() (store.Session, error) {
	if c.sess != nil {
		return *c.sess, nil
	}
	sess, err := c.S.Store.GetSession(c.R.Context(), c.Session)
	if errors.Is(err, store.ErrNotFound) {
		return sess, ErrSessionNotFound()
	}
	if err != nil {
		return sess, err
	}
	c.sess = &sess
	return sess, nil
}

// Account is the core's reference to the route's session account.
func (c *Ctx) Account() (*pb.AccountRef, error) {
	sess, err := c.Sess()
	if err != nil {
		return nil, err
	}
	return sessions.Ref(sess), nil
}

// Bind decodes the JSON body into v; a body that is not JSON is a 400.
func (c *Ctx) Bind(v any) error {
	if len(c.Body) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(c.Body)))
	if c.S.Config.HTTPStrictMode {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return ErrBadRequest([]string{err.Error()})
	}
	return nil
}

// Param is a path parameter.
func (c *Ctx) Param(name string) string { return chi.URLParam(c.R, name) }

// Query is a query parameter.
func (c *Ctx) Query(name string) string { return c.R.URL.Query().Get(name) }

// JSON writes a JSON response.
func (c *Ctx) JSON(status int, v any) error {
	writeJSON(c.W, status, v)
	return nil
}

// Empty writes a status with no body, as NestJS does for a handler that
// returns nothing.
func (c *Ctx) Empty(status int) error {
	c.W.WriteHeader(status)
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.Log.Error("panic", "path", r.URL.Path, "panic", v)
				writeJSON(w, 500, ErrInternal(fmt.Errorf("%v", v), r).Body)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// environment is WAHA's WAHAEnvironment for this process.
func environment() waha.Environment { return Environment("") }

// Environment builds the environment block for a worker.
func Environment(worker string) waha.Environment {
	env := waha.Environment{
		Version:  config.Version,
		Engine:   config.Engine,
		Tier:     "PLUS",
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	if worker != "" {
		env.Worker.ID = &worker
	}
	return env
}
