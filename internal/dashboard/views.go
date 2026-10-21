package dashboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"slices"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// Session statuses as WAHA names them.
const (
	statusWorking = "WORKING"
	statusScanQR  = "SCAN_QR_CODE"
	statusStart   = "STARTING"
	statusStopped = "STOPPED"
	statusFailed  = "FAILED"
)

// The panel's views and the tabs of a session.
const (
	viewSessions = "sessions"
	viewKeys     = "keys"
	newSession   = "+new" // the "sel" of the new-session form

	tabPair     = "pair"
	tabAccount  = "account"
	tabWebhooks = "webhooks"
	tabMetadata = "metadata"
)

// state is what the browser is looking at, from its signals.
type state struct {
	View string `json:"view"`
	Sel  string `json:"sel"`
	Tab  string `json:"tab"`
}

func (s state) normal() state {
	if s.View != viewKeys {
		s.View = viewSessions
	}
	return s
}

// me is WAHA's MeInfo, the account a session is paired with.
type me struct {
	ID       string `json:"id"`
	PushName string `json:"pushName"`
	LID      string `json:"lid"`
}

// Phone is the account's number, from its id.
func (m me) Phone() string {
	user, _, _ := strings.Cut(m.ID, "@")
	user, _, _ = strings.Cut(user, ":")
	if user == "" {
		return ""
	}
	return "+" + user
}

// session is one row of the session list.
type session struct {
	Name     string          `json:"name"`
	Status   string          `json:"status"`
	Config   json.RawMessage `json:"config"`
	Me       *me             `json:"me"`
	Selected bool            `json:"-"`
}

// Tone is the status color's class.
func (s session) Tone() string {
	switch s.Status {
	case statusWorking:
		return "ok"
	case statusScanQR:
		return "warn"
	case statusStart:
		return "info"
	case statusFailed:
		return "bad"
	}
	return "off"
}

// Running is whether stop (rather than start) is the session's action.
func (s session) Running() bool { return s.Status != statusStopped && s.Status != statusFailed }

// Paired is whether the session has an account.
func (s session) Paired() bool { return s.Me != nil && s.Me.ID != "" }

// Search is the text the list filter matches.
func (s session) Search() string {
	t := s.Name
	if s.Paired() {
		t += " " + s.Me.PushName + " " + s.Me.Phone()
	}
	return strings.ToLower(t)
}

// Tabs are the session's tabs, the first being its default.
func (s session) Tabs() []string {
	if s.Paired() {
		return []string{tabAccount, tabWebhooks, tabMetadata}
	}
	return []string{tabPair, tabWebhooks, tabMetadata}
}

// shape is what decides which detail a session shows: when it changes, the
// stream renders the detail again.
func (s session) shape() string { return s.Status + "|" + fmt.Sprint(s.Paired()) }

func (a api) sessions(ctx context.Context) ([]session, error) {
	r := a.do(ctx, "GET", "/api/sessions?all=true", nil)
	if !r.ok() {
		return nil, fmt.Errorf("%s", r.message())
	}
	var out []session
	if err := r.decode(&out); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(x, y session) int { return strings.Compare(x.Name, y.Name) })
	return out, nil
}

// qr is the session's current QR, as its raw value and a PNG data URL.
func (a api) qr(ctx context.Context, name string) (raw string, img template.URL) {
	r := a.do(ctx, "GET", "/api/"+url.PathEscape(name)+"/auth/qr?format=raw", nil)
	var v struct {
		Value string `json:"value"`
	}
	if !r.ok() || r.decode(&v) != nil || v.Value == "" {
		return "", ""
	}
	return v.Value, qrImage(v.Value)
}

func qrImage(value string) template.URL {
	png, err := qrcode.Encode(value, qrcode.Medium, 320)
	if err != nil {
		return ""
	}
	// A data URL of a PNG the edge itself encoded: safe to mark as a URL.
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
}

// profile is the paired account as GET /api/{session}/profile answers.
type profile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func (a api) profile(ctx context.Context, name string) (profile, error) {
	r := a.do(ctx, "GET", "/api/"+url.PathEscape(name)+"/profile", nil)
	var p profile
	if !r.ok() {
		return p, fmt.Errorf("%s", r.message())
	}
	return p, r.decode(&p)
}

// coreInfo is the header's core status, from /health.
type coreInfo struct {
	Up      bool
	Version string
}

func (a api) core(ctx context.Context) coreInfo {
	var h struct {
		Details map[string]struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		} `json:"details"`
	}
	r := a.do(ctx, "GET", "/health", nil)
	if r.decode(&h) != nil {
		return coreInfo{}
	}
	c := h.Details["core"]
	return coreInfo{Up: c.Status == "up", Version: c.Version}
}

// key is one API key as /api/keys lists it.
type key struct {
	ID       string          `json:"id"`
	Key      string          `json:"key"`
	IsActive bool            `json:"isActive"`
	IsAdmin  bool            `json:"isAdmin"`
	Sessions []string        `json:"sessions"`
	Actions  map[string]bool `json:"actions"`
}

// keyActions are a session key's actions, in the panel's order.
var keyActions = []struct{ Name, Label string }{
	{"read", "read"}, {"send", "send"}, {"control", "control"},
	{"setting", "settings"}, {"app", "apps"}, {"delete", "delete"},
}

// defaultActions is what a session key without actions may do (WAHA's).
var defaultActions = map[string]bool{"read": true, "send": true, "control": true, "setting": true, "app": true}

// Grants is each action and whether the key has it.
func (k key) Grants() []grant {
	acts := k.Actions
	if acts == nil {
		acts = defaultActions
	}
	var out []grant
	for _, a := range keyActions {
		out = append(out, grant{Name: a.Name, Label: a.Label, On: k.IsAdmin || acts[a.Name]})
	}
	return out
}

type grant struct {
	Name, Label string
	On          bool
}

func (a api) keys(ctx context.Context) ([]key, error) {
	r := a.do(ctx, "GET", "/api/keys", nil)
	if !r.ok() {
		return nil, fmt.Errorf("%s", r.message())
	}
	var out []key
	return out, r.decode(&out)
}
