// Package testenv runs the whole edge for integration tests: a fake core on a
// Unix socket, a SQLite store in a temp dir, the background loops and the
// HTTP API on an httptest server.
package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLawli/wamux-http/internal/app"
	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/core"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/store/sqlite"
)

// AdminKey is the admin key every test environment uses.
const AdminKey = "test-admin-key-0123456789"

// Env is one running edge.
type Env struct {
	T    testing.TB
	URL  string
	Core *fakecore.Core
	App  *app.App
	HTTP *httptest.Server
}

// Options tunes an environment.
type Options struct {
	Core   fakecore.Options
	Config func(*config.Config)
}

// Start brings an edge up and tears it down when the test ends.
func Start(t testing.TB, opts Options) *Env {
	t.Helper()
	fc := fakecore.Start(t, opts.Core)
	dir := t.TempDir()
	cfg := &config.Config{
		Listen:          "127.0.0.1:0",
		APIKey:          AdminKey,
		CoreSocket:      fc.Socket,
		DatabaseURL:     "sqlite://" + filepath.Join(dir, "edge.db"),
		WorkerID:        "test-worker",
		FilesFolder:     filepath.Join(dir, "files"),
		DownloadMedia:   true,
		EventRetention:  time.Hour,
		WebhookTimeout:  5 * time.Second,
		LeaseTTL:        3 * time.Second,
		LogLevel:        "error",
		MaxRequestBytes: 64 << 20,
	}
	if opts.Config != nil {
		opts.Config(cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	st, err := sqlite.Open(ctx, filepath.Join(dir, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	cc, err := core.Dial(fc.Socket)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := app.Wire(cfg, st, cc, log)
	a.Server.WaitExists, a.Server.WaitStatus = 300*time.Millisecond, 2*time.Second
	a.Webhooks.Poll = 20 * time.Millisecond
	a.Tail.Every = 20 * time.Millisecond
	srv := httptest.NewServer(a.Server.Router())
	cfg.BaseURL = srv.URL
	a.Start(ctx)
	t.Cleanup(func() {
		srv.Close()
		cancel()
		time.Sleep(50 * time.Millisecond) // let the loops see the cancel
		_ = cc.Close()
		_ = st.Close()
	})
	return &Env{T: t, URL: srv.URL, Core: fc, App: a, HTTP: srv}
}

// Response is a finished request.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body into v, failing the test on error.
func (r Response) JSON(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
}

// Map decodes the body as an object.
func (r Response) Map(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	r.JSON(t, &m)
	return m
}

// Do sends a request with the admin key. body may be nil, a string, []byte
// or any value to marshal.
func (e *Env) Do(method, path string, body any) Response {
	return e.DoKey(AdminKey, method, path, body)
}

// DoKey sends a request with the given key ("" sends none).
func (e *Env) DoKey(key, method, path string, body any) Response {
	e.T.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.T.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.URL+path, rd)
	if err != nil {
		e.T.Fatal(err)
	}
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	CheckContract(e.T, req, resp.StatusCode, resp.Header, raw)
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

// Eventually polls cond until it holds or the timeout passes.
func (e *Env) Eventually(what string, timeout time.Duration, cond func() bool) {
	e.T.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			e.T.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Status is the session's current WAHA status, via the API.
func (e *Env) Status(session string) string {
	r := e.Do("GET", "/api/sessions/"+session, nil)
	if r.Status != 200 {
		return ""
	}
	var info struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(r.Body, &info)
	return info.Status
}

// WorkingSession creates a session, starts it, pairs it as jid and waits for
// WORKING. It returns the core's account uuid.
func (e *Env) WorkingSession(name, jid string, config any) string {
	e.T.Helper()
	body := map[string]any{"name": name, "start": true}
	if config != nil {
		body["config"] = config
	}
	if r := e.Do("POST", "/api/sessions", body); r.Status != 201 {
		e.T.Fatalf("create session: %d %s", r.Status, r.Body)
	}
	e.Eventually("SCAN_QR_CODE", 5*time.Second, func() bool { return e.Status(name) == "SCAN_QR_CODE" })
	if err := e.Core.ScanQR(name, jid, "Tester"); err != nil {
		e.T.Fatal(err)
	}
	e.Eventually("WORKING", 5*time.Second, func() bool { return e.Status(name) == "WORKING" })
	acc, _ := e.Core.Account(name)
	return acc.UUID
}

// Hook is a webhook receiver that records every request.
type Hook struct {
	URL    string
	mu     sync.Mutex
	reqs   []HookRequest
	status func(n int, r *http.Request) int
}

// HookRequest is one received webhook.
type HookRequest struct {
	Header http.Header
	Body   []byte
}

// Event decodes the request as a WAHA envelope.
func (h HookRequest) Event() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(h.Body, &m)
	return m
}

// NewHook starts a receiver. status, if set, picks the answer to the n-th
// request (0-based); default 200.
func (e *Env) NewHook(status func(n int, r *http.Request) int) *Hook {
	h := &Hook{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		n := len(h.reqs)
		h.reqs = append(h.reqs, HookRequest{Header: r.Header.Clone(), Body: body})
		h.mu.Unlock()
		code := 200
		if h.status != nil {
			code = h.status(n, r)
		}
		w.WriteHeader(code)
	}))
	e.T.Cleanup(srv.Close)
	h.URL = srv.URL
	return h
}

// Requests is every request received so far.
func (h *Hook) Requests() []HookRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]HookRequest(nil), h.reqs...)
}

// Events is the received envelopes whose event is name ("" for all).
func (h *Hook) Events(name string) []map[string]any {
	var out []map[string]any
	for _, r := range h.Requests() {
		ev := r.Event()
		if name == "" || ev["event"] == name {
			out = append(out, ev)
		}
	}
	return out
}
