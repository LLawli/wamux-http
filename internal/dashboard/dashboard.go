// Package dashboard serves the web panel at /dashboard: pair sessions,
// start and stop them, edit their webhooks and metadata, and manage API keys.
//
// The page is rendered on the server and kept live with Datastar: the
// browser holds the admin key (sessionStorage) and a few view signals, opens
// one event stream (/dashboard/ui) that renders the view and pushes changes,
// and posts each action, which answers with the parts of the page it
// changed. Every action goes through the edge's own WAHA API, in process,
// with the browser's key, so the panel follows the same rules as any client.
package dashboard

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LLawli/wamux-http/internal/events"
)

// Base is where the panel is mounted.
const Base = "/dashboard"

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Handler is the panel.
type Handler struct {
	API  http.Handler // the edge's WAHA API
	Tail *events.Tail // every event, for live status
	Log  *slog.Logger

	QRPoll    time.Duration // how often a shown QR is checked for a new one
	Keepalive time.Duration // how often an idle stream sends a comment

	tpl *template.Template
}

// New builds the panel on the API handler.
func New(apiHandler http.Handler, tail *events.Tail, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	h := &Handler{API: apiHandler, Tail: tail, Log: log, QRPoll: 2 * time.Second, Keepalive: 25 * time.Second}
	h.tpl = template.Must(template.New("").Funcs(funcs()).ParseFS(templateFS, "templates/*.html"))
	return h
}

// Routes is the panel's router, to mount at Base.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", h.shell)
	static, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix(Base+"/static/", cacheFor(time.Hour, http.FileServerFS(static))))
	r.Get("/ui", h.ui)
	r.Post("/sessions", h.createSession)
	r.Post("/sessions/{name}/{action:start|stop|restart|logout|delete}", h.control)
	r.Post("/sessions/{name}/code", h.pairCode)
	r.Post("/sessions/{name}/account", h.account)
	r.Post("/sessions/{name}/picture", h.picture)
	r.Post("/sessions/{name}/webhooks", h.webhooks)
	r.Post("/sessions/{name}/metadata", h.metadata)
	r.Post("/keys", h.createKey)
	r.Post("/keys/{id}/toggle", h.toggleKey)
	r.Post("/keys/{id}/revoke", h.revokeKey)
	return r
}

func cacheFor(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(d.Seconds())))
		next.ServeHTTP(w, r)
	})
}

// --- rendering ---

func (h *Handler) render(name string, data any) string {
	var b bytes.Buffer
	if err := h.tpl.ExecuteTemplate(&b, name, data); err != nil {
		h.Log.Error("dashboard template", "template", name, "err", err)
		return `<div id="toast"><div class="toast bad" id="t-` + strconv.FormatInt(time.Now().UnixNano(), 36) + `">render failed</div></div>`
	}
	return b.String()
}

func (h *Handler) shell(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, h.render("shell", nil))
}

// toast shows a short message; kind is "ok" or "bad".
func (h *Handler) toast(s *sse, kind, msg string) {
	_ = s.patchElements(h.render("toast", map[string]string{
		"Kind": kind, "Msg": msg, "ID": "t-" + strconv.FormatInt(time.Now().UnixNano(), 36),
	}), patchOpts{})
}

// --- auth ---

// begin opens the answer and checks the admin key the browser sent. When
// the key does not open /api/keys, the login takes the page and ok is false.
func (h *Handler) begin(w http.ResponseWriter, r *http.Request) (s *sse, a api, ok bool) {
	a = api{h: h.API, key: r.Header.Get("X-Api-Key")}
	s = newSSE(w)
	if a.do(r.Context(), "GET", "/api/keys", nil).ok() {
		return s, a, true
	}
	_ = s.patchElements(h.render("login", nil), patchOpts{})
	if a.key != "" {
		_ = s.patchSignals(map[string]string{"_key": "", "_err": "invalid key"}, false)
	}
	return s, a, false
}

// readState reads the view signals: the query of a GET, the JSON body of a
// POST.
func readState(r *http.Request) state {
	var st state
	if r.Method == http.MethodGet {
		_ = json.Unmarshal([]byte(r.URL.Query().Get("datastar")), &st)
	} else if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&st)
	}
	return st.normal()
}

// navigate moves the browser to another view: it sets the view signals and
// bumps _go, which reopens the stream.
func navigate(s *sse, st state) {
	_ = s.patchSignals(map[string]any{"view": st.View, "sel": st.Sel, "tab": st.Tab, "_go": time.Now().UnixMilli()}, false)
}

// --- the stream ---

// page is everything a view renders.
type page struct {
	State    state
	Core     coreInfo
	Sessions []session
	Detail   *detail
	Keys     *keysPage
}

// detail is the right-hand side of the sessions view.
type detail struct {
	Session  session
	New      bool // the new-session form
	Tab      string
	QR       template.URL
	qrRaw    string
	Code     string
	CodeErr  string
	Profile  *profile
	Webhooks []webhookForm
	Metadata []pair
	Name     string // new session
	Start    bool   // new session
	Error    string
}

// CodeParts is the pairing code in blocks of four, as the phone shows it.
func (d detail) CodeParts() []string {
	code := strings.ReplaceAll(d.Code, "-", "")
	if len(code) != 8 {
		return []string{d.Code}
	}
	return []string{code[:4], code[4:]}
}

func (h *Handler) load(ctx context.Context, a api, st state) (page, error) {
	p := page{State: st, Core: a.core(ctx)}
	if st.View == viewKeys {
		kp, err := h.keysPage(ctx, a)
		p.Keys = &kp
		return p, err
	}
	list, err := a.sessions(ctx)
	if err != nil {
		return p, err
	}
	p.Sessions = list
	p.Detail = h.detail(ctx, a, st, list)
	for i := range p.Sessions {
		p.Sessions[i].Selected = p.Sessions[i].Name == st.Sel
	}
	return p, nil
}

func (h *Handler) detail(ctx context.Context, a api, st state, list []session) *detail {
	if st.Sel == newSession {
		return &detail{New: true, Start: true}
	}
	for _, s := range list {
		if s.Name != st.Sel {
			continue
		}
		d := &detail{Session: s, Tab: st.Tab}
		tabs := s.Tabs()
		if !slices.Contains(tabs, d.Tab) {
			d.Tab = tabs[0]
		}
		cfg := parseConfig(s.Config)
		switch d.Tab {
		case tabPair:
			if s.Status == statusScanQR {
				d.qrRaw, d.QR = a.qr(ctx, s.Name)
			}
		case tabAccount:
			if s.Status == statusWorking {
				if pr, err := a.profile(ctx, s.Name); err == nil {
					d.Profile = &pr
				}
			}
		case tabWebhooks:
			d.Webhooks = webhooksFromConfig(cfg)
		case tabMetadata:
			d.Metadata = metadataFromConfig(cfg)
		}
		return d
	}
	return nil
}

// ui renders the view the signals name and, for the sessions view, stays
// open to push status changes and new QR codes.
func (h *Handler) ui(w http.ResponseWriter, r *http.Request) {
	st := readState(r)
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, err := h.load(ctx, a, st)
	if err != nil {
		h.toast(s, "bad", err.Error())
	}
	_ = s.patchElements(h.render("app", p), patchOpts{})
	if st.View != viewSessions {
		return
	}
	h.watch(ctx, s, a, p)
}

func (h *Handler) watch(ctx context.Context, s *sse, a api, p page) {
	evs, cancel := h.Tail.Subscribe(64)
	defer cancel()
	qrTick := time.NewTicker(h.QRPoll)
	defer qrTick.Stop()
	keep := time.NewTicker(h.Keepalive)
	defer keep.Stop()
	shapeOf := func(list []session) string {
		for _, x := range list {
			if x.Name == p.State.Sel {
				return x.shape()
			}
		}
		return ""
	}
	shape, lastQR := shapeOf(p.Sessions), ""
	if p.Detail != nil {
		lastQR = p.Detail.qrRaw
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-evs:
			if !open {
				return
			}
			if ev.Type != "session.status" {
				continue
			}
			np, err := h.load(ctx, a, p.State)
			if err != nil {
				continue
			}
			if s.patchElements(h.render("list", np), patchOpts{}) != nil {
				return
			}
			if now := shapeOf(np.Sessions); now != shape && np.Detail != nil {
				shape = now
				// The forms of webhooks and metadata keep what is being typed:
				// only the head (status, actions, tabs) follows.
				name := "detail"
				if np.Detail.Tab == tabWebhooks || np.Detail.Tab == tabMetadata {
					name = "detail-head"
				}
				_ = s.patchElements(h.render(name, np), patchOpts{})
				lastQR = np.Detail.qrRaw
			}
			p.Sessions, p.Detail = np.Sessions, np.Detail
		case <-qrTick.C:
			d := p.Detail
			if d == nil || d.Tab != tabPair || d.Session.Status != statusScanQR {
				continue
			}
			raw, img := a.qr(ctx, d.Session.Name)
			if raw == "" || raw == lastQR {
				continue
			}
			lastQR = raw
			if s.patchElements(h.render("qr", img), patchOpts{}) != nil {
				return
			}
		case <-keep.C:
			if _, err := io.WriteString(s.w, ": keepalive\n\n"); err != nil {
				return
			}
			if s.f != nil {
				s.f.Flush()
			}
		}
	}
}

// refresh renders the sessions view parts an action changed.
func (h *Handler) refresh(ctx context.Context, s *sse, a api, st state, parts ...string) {
	p, err := h.load(ctx, a, st)
	if err != nil {
		h.toast(s, "bad", err.Error())
		return
	}
	for _, part := range parts {
		_ = s.patchElements(h.render(part, p), patchOpts{})
	}
}

// --- session actions ---

func sessionPath(name string, rest ...string) string {
	return "/api/sessions/" + url.PathEscape(name) + strings.Join(rest, "")
}

func (h *Handler) control(w http.ResponseWriter, r *http.Request) {
	st := readState(r)
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name, action := chi.URLParam(r, "name"), chi.URLParam(r, "action")
	if action == "delete" {
		res := a.do(ctx, "DELETE", sessionPath(name), nil)
		if !res.ok() {
			h.toast(s, "bad", res.message())
			return
		}
		navigate(s, state{View: viewSessions})
		return
	}
	res := a.do(ctx, "POST", sessionPath(name, "/", action), nil)
	if !res.ok() {
		h.toast(s, "bad", res.message())
	}
	st.Sel = name
	h.refresh(ctx, s, a, st, "list", "detail")
}

func (h *Handler) pairCode(w http.ResponseWriter, r *http.Request) {
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	phone := r.PostFormValue("phone")
	res := a.do(r.Context(), "POST", "/api/"+url.PathEscape(name)+"/auth/request-code", map[string]string{"phoneNumber": phone})
	d := detail{Session: session{Name: name}}
	var v struct {
		Code string `json:"code"`
	}
	if res.ok() && res.decode(&v) == nil {
		d.Code = v.Code
	} else {
		d.CodeErr = res.message()
	}
	_ = s.patchElements(h.render("pair-code", d), patchOpts{})
}

func (h *Handler) account(w http.ResponseWriter, r *http.Request) {
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	base := "/api/" + url.PathEscape(name) + "/profile/"
	if v := strings.TrimSpace(r.PostFormValue("name")); v != "" {
		if res := a.do(ctx, "PUT", base+"name", map[string]string{"name": v}); !res.ok() {
			h.toast(s, "bad", res.message())
			return
		}
	}
	if v := strings.TrimSpace(r.PostFormValue("about")); v != "" {
		if res := a.do(ctx, "PUT", base+"status", map[string]string{"status": v}); !res.ok() {
			h.toast(s, "bad", res.message())
			return
		}
	}
	h.toast(s, "ok", "saved")
	h.refresh(ctx, s, a, state{View: viewSessions, Sel: name, Tab: tabAccount}, "list", "detail")
}

// maxPicture bounds a profile picture upload.
const maxPicture = 8 << 20

func (h *Handler) picture(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPicture+64<<10)
	parseErr := r.ParseMultipartForm(maxPicture)
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	if parseErr != nil {
		h.toast(s, "bad", "picture too large or unreadable")
		return
	}
	f, hdr, err := r.FormFile("picture")
	if err != nil {
		h.toast(s, "bad", "no picture")
		return
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		h.toast(s, "bad", err.Error())
		return
	}
	file := map[string]string{"mimetype": hdr.Header.Get("Content-Type"), "data": base64.StdEncoding.EncodeToString(data), "filename": hdr.Filename}
	if res := a.do(ctx, "PUT", "/api/"+url.PathEscape(name)+"/profile/picture", map[string]any{"file": file}); !res.ok() {
		h.toast(s, "bad", res.message())
		return
	}
	h.toast(s, "ok", "saved")
	h.refresh(ctx, s, a, state{View: viewSessions, Sel: name, Tab: tabAccount}, "detail")
}

// formOp reads an editor button: ?op=...&i=...&j=...
func formOp(r *http.Request) (op string, i, j int) {
	q := r.URL.Query()
	return q.Get("op"), atoi(q.Get("i")), atoi(q.Get("j"))
}

// findSession is one session of the list, for its stored config.
func (a api) findSession(ctx context.Context, name string) (session, error) {
	list, err := a.sessions(ctx)
	if err != nil {
		return session{}, err
	}
	for _, s := range list {
		if s.Name == name {
			return s, nil
		}
	}
	return session{}, fmt.Errorf("session %s not found", name)
}

func (h *Handler) webhooks(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	sess, err := a.findSession(ctx, name)
	if err != nil {
		h.toast(s, "bad", err.Error())
		return
	}
	list := webhooksFromForm(r.PostForm)
	op, i, j := formOp(r)
	d := detail{Session: sess, Tab: tabWebhooks}
	if op != "save" {
		d.Webhooks = webhookOp(list, op, i, j)
		_ = s.patchElements(h.render("webhooks", d), patchOpts{})
		return
	}
	var out []map[string]any
	for _, f := range list {
		wh, err := webhookJSON(f)
		if err != nil {
			d.Webhooks = list
			h.toast(s, "bad", err.Error())
			_ = s.patchElements(h.render("webhooks", d), patchOpts{})
			return
		}
		out = append(out, wh)
	}
	var value any = out
	if len(out) == 0 {
		value = nil
	}
	res := a.do(ctx, "PUT", sessionPath(name), map[string]any{"config": configWith(sess.Config, "webhooks", value)})
	if !res.ok() {
		d.Webhooks = list
		h.toast(s, "bad", res.message())
		_ = s.patchElements(h.render("webhooks", d), patchOpts{})
		return
	}
	h.toast(s, "ok", "saved")
	h.refresh(ctx, s, a, state{View: viewSessions, Sel: name, Tab: tabWebhooks}, "detail")
}

func (h *Handler) metadata(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := chi.URLParam(r, "name")
	sess, err := a.findSession(ctx, name)
	if err != nil {
		h.toast(s, "bad", err.Error())
		return
	}
	list := pairsFromForm(r.PostForm, "mk", "mv")
	op, i, _ := formOp(r)
	d := detail{Session: sess, Tab: tabMetadata}
	if op != "save" {
		d.Metadata = pairsOp(list, op, i)
		_ = s.patchElements(h.render("metadata", d), patchOpts{})
		return
	}
	var value any
	if md := metadataJSON(list); len(md) > 0 {
		value = md
	}
	res := a.do(ctx, "PUT", sessionPath(name), map[string]any{"config": configWith(sess.Config, "metadata", value)})
	if !res.ok() {
		d.Metadata = list
		h.toast(s, "bad", res.message())
		_ = s.patchElements(h.render("metadata", d), patchOpts{})
		return
	}
	h.toast(s, "ok", "saved")
	h.refresh(ctx, s, a, state{View: viewSessions, Sel: name, Tab: tabMetadata}, "detail")
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	op, i, j := formOp(r)
	d := detail{
		New:      true,
		Name:     strings.TrimSpace(r.PostFormValue("name")),
		Start:    r.PostFormValue("start") != "",
		Metadata: pairsFromForm(r.PostForm, "mk", "mv"),
		Webhooks: webhooksFromForm(r.PostForm),
	}
	if op != "create" {
		d.Metadata = pairsOp(d.Metadata, op, i)
		d.Webhooks = webhookOp(d.Webhooks, op, i, j)
		_ = s.patchElements(h.render("detail-new", d), patchOpts{})
		return
	}
	cfg := map[string]any{}
	if md := metadataJSON(d.Metadata); len(md) > 0 {
		cfg["metadata"] = md
	}
	var hooks []map[string]any
	for _, f := range d.Webhooks {
		wh, err := webhookJSON(f)
		if err != nil {
			h.toast(s, "bad", err.Error())
			_ = s.patchElements(h.render("detail-new", d), patchOpts{})
			return
		}
		hooks = append(hooks, wh)
	}
	if len(hooks) > 0 {
		cfg["webhooks"] = hooks
	}
	body := map[string]any{"name": d.Name, "start": d.Start}
	if len(cfg) > 0 {
		body["config"] = cfg
	}
	res := a.do(ctx, "POST", "/api/sessions", body)
	if !res.ok() {
		h.toast(s, "bad", res.message())
		_ = s.patchElements(h.render("detail-new", d), patchOpts{})
		return
	}
	var created session
	_ = res.decode(&created)
	navigate(s, state{View: viewSessions, Sel: created.Name})
}
