package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/sessions"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// sessionNameRe is WAHA's SessionName rule (max 54 characters).
var sessionNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{0,54}$`)

const sessionNameMessage = "Session name can only contain alphanumeric characters, hyphens, and underscores (a-z, A-Z, 0-9, -, _) or be empty"

// SessionInfo is WAHA's SessionInfo (a session in a list or on its own).
type SessionInfo struct {
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	Config         json.RawMessage `json:"config"`
	Me             json.RawMessage `json:"me"`
	AssignedWorker string          `json:"assignedWorker,omitempty"`
	Presence       any             `json:"presence"`
	Timestamps     Timestamps      `json:"timestamps"`
	Engine         map[string]any  `json:"engine,omitempty"`
}

// Timestamps is SessionInfo.timestamps.
type Timestamps struct {
	Activity *int64 `json:"activity"`
}

// SessionDTO is what the session-changing routes answer.
type SessionDTO struct {
	Name   string          `json:"name"`
	Status string          `json:"status"`
	Config json.RawMessage `json:"config"`
}

func orNull(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

func (s *Server) sessionInfo(c *Ctx, sess store.Session) SessionInfo {
	return SessionInfo{
		Name:           sess.Name,
		Status:         sess.Status,
		Config:         orNull(sess.Config),
		Me:             orNull(sess.Me),
		AssignedWorker: s.Sessions.Owner(c.R.Context(), sess.Name),
	}
}

func dto(sess store.Session) SessionDTO {
	return SessionDTO{Name: sess.Name, Status: sess.Status, Config: orNull(sess.Config)}
}

// sessionErr maps the manager's errors onto WAHA's session-route answers.
func sessionErr(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ErrSessionNotFound()
	case errors.Is(err, sessions.ErrExists):
		return err
	}
	return err
}

// validConfig checks the parts of a SessionConfig the edge acts on and
// returns WAHA-style validation messages.
func validConfig(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return []string{"config must be an object"}
	}
	var cfg waha.SessionConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return []string{"config: " + err.Error()}
	}
	var msgs []string
	for i, wh := range cfg.Webhooks {
		if !strings.HasPrefix(wh.URL, "http://") && !strings.HasPrefix(wh.URL, "https://") {
			msgs = append(msgs, fmt.Sprintf("config.webhooks.%d.url must be a URL address", i))
		}
		for _, e := range wh.Events {
			if e == "*" || strings.Contains(e, "*") {
				continue
			}
			known := false
			for _, k := range waha.AllEvents {
				known = known || k == e
			}
			if !known {
				msgs = append(msgs, fmt.Sprintf("config.webhooks.%d.events: unknown event %q", i, e))
			}
		}
		if wh.Retries != nil {
			switch wh.Retries.Policy {
			case "", "linear", "exponential", "constant":
			default:
				msgs = append(msgs, fmt.Sprintf("config.webhooks.%d.retries.policy must be one of the following values: linear, exponential, constant", i))
			}
		}
	}
	return msgs
}

func (s *Server) registerSessions(r chi.Router) {
	s.route(r, "GET", "/api/sessions", Route{Action: ActList}, func(c *Ctx) error {
		all := truthyQuery(c.Query("all"))
		list, err := s.Store.ListSessions(c.R.Context())
		if err != nil {
			return err
		}
		out := []SessionInfo{}
		for _, sess := range list {
			if !c.Principal.Covers(sess.Name) {
				continue
			}
			if !all && !sess.Started {
				continue // WAHA lists only running sessions unless all=true
			}
			out = append(out, s.sessionInfo(c, sess))
		}
		return c.JSON(http.StatusOK, out)
	})

	s.route(r, "POST", "/api/sessions", Route{Action: ActManage}, func(c *Ctx) error {
		var req struct {
			Name   *string         `json:"name"`
			Start  bool            `json:"start"`
			Config json.RawMessage `json:"config"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		name := ""
		if req.Name != nil {
			name = *req.Name
		}
		if !sessionNameRe.MatchString(name) {
			return ErrBadRequest([]string{sessionNameMessage})
		}
		if msgs := validConfig(req.Config); len(msgs) > 0 {
			return ErrBadRequest(msgs)
		}
		if name == "" {
			name = waha.NewPrefixedID("session_")
		}
		// WAHA starts only when `start` is true, whatever its docs say.
		sess, err := s.Sessions.Create(c.R.Context(), name, req.Config, req.Start)
		if errors.Is(err, sessions.ErrExists) {
			return ErrUnprocessable(fmt.Sprintf("Session '%s' already exists. Use PUT to update it.", name))
		}
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, s.sessionInfo(c, sess))
	})

	s.route(r, "GET", "/api/sessions/{session}", Route{Action: ActRead, From: FromPath}, func(c *Ctx) error {
		sess, err := c.Sess()
		if err != nil {
			return err
		}
		info := s.sessionInfo(c, sess)
		info.Engine = map[string]any{}
		if sess.Started {
			info.Engine = map[string]any{"engine": config.Engine}
		}
		return c.JSON(http.StatusOK, info)
	})

	s.route(r, "PUT", "/api/sessions/{session}", Route{Action: ActSetting, From: FromPath}, func(c *Ctx) error {
		var req struct {
			Config json.RawMessage `json:"config"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		if msgs := validConfig(req.Config); len(msgs) > 0 {
			return ErrBadRequest(msgs)
		}
		sess, err := s.Sessions.Update(c.R.Context(), c.Session, req.Config)
		if err != nil {
			return sessionErr(err)
		}
		return c.JSON(http.StatusOK, dto(sess))
	})

	s.route(r, "DELETE", "/api/sessions/{session}", Route{Action: ActDelete, From: FromPath}, func(c *Ctx) error {
		if err := s.Sessions.Delete(c.R.Context(), c.Session); err != nil {
			return err
		}
		return c.Empty(http.StatusOK)
	})

	s.route(r, "GET", "/api/sessions/{session}/me", Route{Action: ActRead, From: FromPath}, func(c *Ctx) error {
		sess, err := s.Store.GetSession(c.R.Context(), c.Session)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !sess.Started) {
			return ErrNotFound(fmt.Sprintf("We didn't find a session with name '%s'.\nPlease start it first by using POST /api/sessions/%s/start request", c.Session, c.Session))
		}
		if err != nil {
			return err
		}
		if len(sess.Me) == 0 {
			return c.Empty(http.StatusOK)
		}
		return c.JSON(http.StatusOK, sess.Me)
	})

	control := func(path string, do func(c *Ctx) (store.Session, error), missingOK bool) {
		s.route(r, "POST", path, Route{Action: ActControl, From: FromPath}, func(c *Ctx) error {
			sess, err := do(c)
			if errors.Is(err, store.ErrNotFound) {
				if missingOK {
					return c.Empty(http.StatusCreated)
				}
				return ErrSessionNotFound()
			}
			if err != nil {
				return err
			}
			return c.JSON(http.StatusCreated, dto(sess))
		})
	}
	control("/api/sessions/{session}/start", func(c *Ctx) (store.Session, error) { return s.Sessions.Start(c.R.Context(), c.Session) }, false)
	control("/api/sessions/{session}/stop", func(c *Ctx) (store.Session, error) { return s.Sessions.Stop(c.R.Context(), c.Session) }, true)
	control("/api/sessions/{session}/logout", func(c *Ctx) (store.Session, error) { return s.Sessions.Logout(c.R.Context(), c.Session) }, false)
	control("/api/sessions/{session}/restart", func(c *Ctx) (store.Session, error) { return s.Sessions.Restart(c.R.Context(), c.Session) }, false)

	// Deprecated forms that name the session in the body.
	s.route(r, "POST", "/api/sessions/start", Route{Action: ActRetrieve}, func(c *Ctx) error {
		var req struct {
			Name   string          `json:"name"`
			Config json.RawMessage `json:"config"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		if !c.Principal.Allows(ActControl, req.Name) {
			return ErrForbidden()
		}
		if req.Name == "" || !sessionNameRe.MatchString(req.Name) {
			return ErrBadRequest([]string{sessionNameMessage})
		}
		ctx := c.R.Context()
		_, err := s.Sessions.Create(ctx, req.Name, req.Config, true)
		if errors.Is(err, sessions.ErrExists) {
			if _, err = s.Sessions.Update(ctx, req.Name, req.Config); err == nil {
				_, err = s.Sessions.Start(ctx, req.Name)
			}
		}
		if err != nil {
			return err
		}
		sess, err := s.Store.GetSession(ctx, req.Name)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, dto(sess))
	})
	s.route(r, "POST", "/api/sessions/stop", Route{Action: ActRetrieve}, func(c *Ctx) error {
		var req struct {
			Name   string `json:"name"`
			Logout bool   `json:"logout"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		if !c.Principal.Allows(ActControl, req.Name) {
			return ErrForbidden()
		}
		var err error
		if req.Logout {
			_, err = s.Sessions.Logout(c.R.Context(), req.Name)
			if err == nil {
				_, err = s.Sessions.Stop(c.R.Context(), req.Name)
			}
		} else {
			_, err = s.Sessions.Stop(c.R.Context(), req.Name)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return c.Empty(http.StatusCreated)
	})
	s.route(r, "POST", "/api/sessions/logout", Route{Action: ActRetrieve}, func(c *Ctx) error {
		var req struct {
			Name string `json:"name"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		if !c.Principal.Allows(ActControl, req.Name) {
			return ErrForbidden()
		}
		if err := s.Sessions.Delete(c.R.Context(), req.Name); err != nil {
			return err
		}
		return c.Empty(http.StatusCreated)
	})
}

func truthyQuery(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (s *Server) registerPairing(r chi.Router) {
	s.route(r, "GET", "/api/{session}/auth/qr", Route{Action: ActControl, From: FromPath}, func(c *Ctx) error {
		sess, err := s.waitStatus(c.R.Context(), c.Session, sessions.StatusScanQR)
		if err != nil {
			return err
		}
		if sess.QR == "" {
			return ErrSessionStatus(c.Session, sess.Status, sessions.StatusScanQR)
		}
		if c.Query("format") == "raw" {
			return c.JSON(http.StatusOK, map[string]string{"value": sess.QR})
		}
		png, err := qrcode.Encode(sess.QR, qrcode.Medium, 512)
		if err != nil {
			return err
		}
		if strings.Contains(c.R.Header.Get("Accept"), "application/json") {
			return c.JSON(http.StatusOK, map[string]string{"mimetype": "image/png", "data": base64.StdEncoding.EncodeToString(png)})
		}
		c.W.Header().Set("Content-Type", "image/png")
		c.W.WriteHeader(http.StatusOK)
		_, err = c.W.Write(png)
		return err
	})

	s.route(r, "POST", "/api/{session}/auth/request-code", Route{Action: ActControl, From: FromPath}, func(c *Ctx) error {
		var req struct {
			PhoneNumber string `json:"phoneNumber"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		phone := onlyDigits(req.PhoneNumber)
		if phone == "" {
			return ErrBadRequest([]string{"phoneNumber must be a string"})
		}
		sess, err := s.waitStatus(c.R.Context(), c.Session, sessions.StatusScanQR)
		if err != nil {
			var he *Error
			if errors.As(err, &he) && he.Status == http.StatusUnprocessableEntity && sess.Name != "" {
				return ErrUnprocessable("Can request code only in SCAN_QR_CODE status. The current status is " + sess.Status)
			}
			return err
		}
		code, err := s.Sessions.RequestCode(c.R.Context(), sess, phone)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, map[string]string{"code": code})
	})
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
