// Package sessions runs WAHA sessions on wamux accounts. The store holds each
// session's desired state; a supervisor in every edge process takes a lease
// on each started session it can, and the lease holder (the owner) keeps the
// account connected, follows its events and writes its status. Requests to
// change a session can land on any process: they write the store and call
// the core, and the owner follows.
package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LLawli/wamux-http/internal/core"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/events"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
	"google.golang.org/grpc/codes"
)

// WAHA session statuses.
const (
	StatusStopped   = "STOPPED"
	StatusStarting  = "STARTING"
	StatusScanQR    = "SCAN_QR_CODE"
	StatusWorking   = "WORKING"
	StatusFailed    = "FAILED"
	statusesKept    = 3
	leasePrefix     = "session:"
	reconnectMin    = time.Second
	reconnectMax    = time.Minute
	superviseEvery  = 2 * time.Second
	coreCallTimeout = 30 * time.Second
	logoutPoll      = 200 * time.Millisecond
	replayRing      = 256
)

// Ingestor turns the core's events into stored records and WAHA events. The
// manager hands it every envelope of a session it owns.
type Ingestor interface {
	Handle(ctx context.Context, sess store.Session, ev *pb.EventEnvelope) error
}

// Manager owns the sessions this process holds leases for.
type Manager struct {
	Store    store.Store
	Core     *core.Client
	Pub      *events.Publisher
	Ingest   Ingestor
	Worker   string
	LeaseTTL time.Duration
	Backfill bool // ask the phone for its history when connecting
	Log      *slog.Logger

	kick    chan struct{}
	mu      sync.Mutex
	runners map[string]*runner
	running sync.WaitGroup
}

// ErrExists is returned when creating a session whose name is taken.
var ErrExists = errors.New("session exists")

func (m *Manager) init() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kick == nil {
		m.kick = make(chan struct{}, 1)
		m.runners = map[string]*runner{}
	}
	if m.Log == nil {
		m.Log = slog.Default()
	}
	if m.LeaseTTL == 0 {
		m.LeaseTTL = 15 * time.Second
	}
}

// Kick makes the supervisor look at the store now instead of at its next tick.
func (m *Manager) Kick() {
	m.init()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Run supervises until ctx ends, then stops every runner and returns once
// they have all released their leases.
func (m *Manager) Run(ctx context.Context) {
	m.init()
	t := time.NewTicker(superviseEvery)
	defer t.Stop()
	for {
		m.supervise(ctx)
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, r := range m.runners {
				r.cancel()
			}
			m.mu.Unlock()
			m.running.Wait()
			return
		case <-t.C:
		case <-m.kick:
		}
	}
}

func (m *Manager) supervise(ctx context.Context) {
	all, err := m.Store.ListSessions(ctx)
	if err != nil {
		m.Log.Error("list sessions", "err", err)
		return
	}
	for _, s := range all {
		if !s.Started {
			continue
		}
		m.mu.Lock()
		_, running := m.runners[s.Name]
		m.mu.Unlock()
		if running {
			continue
		}
		ok, err := m.Store.AcquireLease(ctx, leasePrefix+s.Name, m.Worker, waha.NowMs(), m.LeaseTTL.Milliseconds())
		if err != nil || !ok {
			continue
		}
		rctx, cancel := context.WithCancel(ctx)
		r := &runner{m: m, name: s.Name, cancel: cancel, nudge: make(chan struct{}, 1)}
		m.mu.Lock()
		m.runners[s.Name] = r
		m.mu.Unlock()
		m.running.Add(1)
		go func(name string) {
			defer m.running.Done()
			defer func() {
				cancel()
				// Release before leaving the map: the other way round, a
				// supervise pass in between re-acquires the lease for a new
				// runner and this release then deletes it from under that one.
				_ = m.Store.ReleaseLease(context.WithoutCancel(ctx), leasePrefix+name, m.Worker)
				m.mu.Lock()
				delete(m.runners, name)
				m.mu.Unlock()
				// A start that landed while this runner was ending found it
				// still in the map; look again now that it is gone.
				m.Kick()
			}()
			r.run(rctx)
		}(s.Name)
	}
}

// nudge tells this process's runner of a session, if it has one, that the
// desired state changed, instead of leaving it to its next lease tick.
func (m *Manager) nudge(name string) {
	m.mu.Lock()
	r := m.runners[name]
	m.mu.Unlock()
	if r != nil {
		select {
		case r.nudge <- struct{}{}:
		default:
		}
	}
}

// Owner is the worker holding a session's lease, "" when nobody does.
func (m *Manager) Owner(ctx context.Context, name string) string {
	h, _ := m.Store.LeaseHolder(ctx, leasePrefix+name, waha.NowMs())
	return h
}

// Ref is the core's reference to a session's account.
func Ref(s store.Session) *pb.AccountRef {
	if s.AccountUUID != "" {
		return &pb.AccountRef{Ref: &pb.AccountRef_Uuid{Uuid: s.AccountUUID}}
	}
	return &pb.AccountRef{Ref: &pb.AccountRef_ExternalRef{ExternalRef: s.Name}}
}

// Create stores a new session, started or not.
func (m *Manager) Create(ctx context.Context, name string, cfg json.RawMessage, start bool) (store.Session, error) {
	now := waha.NowMs()
	s := store.Session{Name: name, Config: cfg, Status: StatusStopped, CreatedAt: now, UpdatedAt: now}
	if err := m.Store.CreateSession(ctx, s); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return s, ErrExists
		}
		return s, err
	}
	if start {
		return m.Start(ctx, name)
	}
	return s, nil
}

// Start marks a session started. Starting a started session changes nothing.
func (m *Manager) Start(ctx context.Context, name string) (store.Session, error) {
	s, err := m.Store.GetSession(ctx, name)
	if err != nil || s.Started {
		return s, err
	}
	started, at := true, waha.NowMs()
	if s, err = m.Store.UpdateSession(ctx, name, store.SessionPatch{Started: &started, StartedAt: &at}); err != nil {
		return s, err
	}
	s, err = m.SetStatus(ctx, s, StatusStarting, nil)
	m.nudge(name)
	m.Kick()
	return s, err
}

// Stop marks a session stopped and disconnects its account; pairing is kept.
func (m *Manager) Stop(ctx context.Context, name string) (store.Session, error) {
	s, err := m.Store.GetSession(ctx, name)
	if err != nil {
		return s, err
	}
	stopped, noQR := false, ""
	if s, err = m.Store.UpdateSession(ctx, name, store.SessionPatch{Started: &stopped, QR: &noQR}); err != nil {
		return s, err
	}
	m.nudge(name)
	if s.AccountUUID != "" {
		cctx, cancel := context.WithTimeout(ctx, coreCallTimeout)
		_, err := m.Core.Accounts.DisconnectAccount(cctx, Ref(s))
		cancel()
		if err != nil && core.CodeOf(err) != codes.NotFound {
			m.Log.Warn("disconnect on stop", "session", name, "err", err)
		}
	}
	if s.Status == StatusStopped {
		return s, nil
	}
	return m.SetStatus(ctx, s, StatusStopped, nil)
}

// Restart stops and starts a session.
func (m *Manager) Restart(ctx context.Context, name string) (store.Session, error) {
	if _, err := m.Stop(ctx, name); err != nil {
		return store.Session{}, err
	}
	return m.Start(ctx, name)
}

// Logout unpairs the account. A started session comes back to SCAN_QR_CODE.
func (m *Manager) Logout(ctx context.Context, name string) (store.Session, error) {
	s, err := m.Store.GetSession(ctx, name)
	if err != nil {
		return s, err
	}
	wasStarted := s.Started
	if err := m.unpair(ctx, s); err != nil {
		return s, err
	}
	if s, err = m.Stop(ctx, name); err != nil {
		return s, err
	}
	null := json.RawMessage(nil)
	if s, err = m.Store.UpdateSession(ctx, name, store.SessionPatch{Me: &null}); err != nil {
		return s, err
	}
	if wasStarted {
		// The core stamps the logout's LOGGED_OUT before it answers. A start
		// in that same millisecond would stamp started_at equal to it, and the
		// runner, which skips only events older than the start, would apply
		// the LOGGED_OUT to the new start and fail it. Let the clock move on.
		for after := waha.NowMs(); waha.NowMs() <= after; {
			time.Sleep(100 * time.Microsecond)
		}
		return m.Start(ctx, name)
	}
	return s, nil
}

// unpair tells WhatsApp to drop this device, so it leaves the phone's linked
// devices. The core does it only while the account is connected
// (FailedPrecondition otherwise, measured live), so it runs before a stop,
// and a stopped session is connected for it. An account never paired has
// nothing to drop.
func (m *Manager) unpair(ctx context.Context, s store.Session) error {
	if s.AccountUUID == "" || len(s.Me) == 0 || string(s.Me) == "null" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, coreCallTimeout)
	defer cancel()
	_, err := m.Core.Accounts.Logout(ctx, Ref(s))
	if core.CodeOf(err) == codes.FailedPrecondition {
		var connected bool
		if connected, err = m.connectFor(ctx, s); connected {
			_, err = m.Core.Accounts.Logout(ctx, Ref(s))
		}
		if err != nil {
			// Do not leave a stopped session connected.
			dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), coreCallTimeout)
			_, _ = m.Core.Accounts.DisconnectAccount(dctx, Ref(s))
			dcancel()
		}
	}
	if core.CodeOf(err) == codes.NotFound {
		return nil
	}
	return err
}

// connectFor connects a stopped session's account for one call and waits
// until it is up; connected is false when the core reports it already
// logged out, which leaves nothing to do. The caller's logout ends the
// connection, or the caller disconnects when it fails.
func (m *Manager) connectFor(ctx context.Context, s store.Session) (connected bool, err error) {
	st, err := m.Core.Accounts.ConnectAccount(ctx, &pb.ConnectAccountRequest{Account: Ref(s)})
	for err == nil {
		switch st.GetState() {
		case pb.ConnectionState_CONNECTED:
			return true, nil
		case pb.ConnectionState_LOGGED_OUT:
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(logoutPoll):
		}
		st, err = m.Core.Accounts.GetAccountStatus(ctx, Ref(s))
	}
	return false, err
}

// Delete unpairs the account, removes it from the core and forgets the
// session and everything stored for it.
func (m *Manager) Delete(ctx context.Context, name string) error {
	s, err := m.Store.GetSession(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil // WAHA's delete is idempotent
	}
	if err != nil {
		return err
	}
	if err := m.unpair(ctx, s); err != nil {
		m.Log.Warn("logout on delete", "session", name, "err", err)
	}
	if _, err := m.Stop(ctx, name); err != nil {
		return err
	}
	if s.AccountUUID != "" {
		cctx, cancel := context.WithTimeout(ctx, coreCallTimeout)
		defer cancel()
		if _, err := m.Core.Accounts.DeleteAccount(cctx, Ref(s)); err != nil && core.CodeOf(err) != codes.NotFound {
			return err
		}
	}
	return m.Store.PurgeSession(ctx, name)
}

// Update replaces a session's config; a started session is restarted, as
// WAHA does.
func (m *Manager) Update(ctx context.Context, name string, cfg json.RawMessage) (store.Session, error) {
	s, err := m.Store.GetSession(ctx, name)
	if err != nil {
		return s, err
	}
	wasStarted := s.Started
	if wasStarted {
		if _, err := m.Stop(ctx, name); err != nil {
			return s, err
		}
	}
	if s, err = m.Store.UpdateSession(ctx, name, store.SessionPatch{Config: &cfg}); err != nil {
		return s, err
	}
	if wasStarted {
		return m.Start(ctx, name)
	}
	return s, nil
}

// RequestCode asks the core for a phone-number pairing code.
func (m *Manager) RequestCode(ctx context.Context, s store.Session, phone string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, coreCallTimeout)
	defer cancel()
	stream, err := m.Core.Accounts.PairWithCode(cctx, &pb.PairWithCodeRequest{Account: Ref(s), PhoneNumber: phone, BackfillHistory: m.Backfill})
	if err != nil {
		return "", err
	}
	for {
		u, err := stream.Recv()
		if err != nil {
			return "", err
		}
		switch ev := u.GetEvent().(type) {
		case *pb.PairingUpdate_PairCode:
			return ev.PairCode, nil
		case *pb.PairingUpdate_Error:
			return "", fmt.Errorf("pairing: %s", ev.Error.GetMessage())
		case *pb.PairingUpdate_CodeError:
			return "", fmt.Errorf("pairing code refused: %s (%s)", ev.CodeError.GetRejection(), ev.CodeError.GetDetail())
		}
	}
}

// StatusPoint is one entry of WAHA's `statuses` history.
type StatusPoint struct {
	Status    string `json:"status"`
	Timestamp int64  `json:"timestamp"`
}

// StatusPayload is the body of a session.status event.
type StatusPayload struct {
	Name     string        `json:"name"`
	Status   string        `json:"status"`
	Statuses []StatusPoint `json:"statuses"`
	Data     any           `json:"data"`
}

// SetStatus records a status, keeps the last three in the history and
// publishes session.status.
func (m *Manager) SetStatus(ctx context.Context, s store.Session, status string, data any) (store.Session, error) {
	var points []StatusPoint
	_ = json.Unmarshal(s.Statuses, &points)
	points = append(points, StatusPoint{Status: status, Timestamp: waha.NowMs()})
	if len(points) > statusesKept {
		points = points[len(points)-statusesKept:]
	}
	raw, _ := json.Marshal(points)
	rm := json.RawMessage(raw)
	s, err := m.Store.UpdateSession(ctx, s.Name, store.SessionPatch{Status: &status, Statuses: &rm})
	if err != nil {
		return s, err
	}
	_, err = m.Pub.Publish(ctx, s, "session.status", StatusPayload{Name: s.Name, Status: status, Statuses: points, Data: data})
	return s, err
}
