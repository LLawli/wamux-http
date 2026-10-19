package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/LLawli/wamux-http/internal/core"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// runner keeps one owned session's account connected and its status true.
type runner struct {
	m       *Manager
	name    string
	cancel  context.CancelFunc // ends the runner
	nudge   chan struct{}      // the desired state changed in this process
	backoff time.Duration
	lid     string // the account's LID, learned at pairing

	mu        sync.Mutex
	follow    context.CancelFunc // ends the current connection, to reconnect
	followFor int64              // the session's started_at that connection serves
}

// errReleased ends a runner whose session is no longer ours to run.
var errReleased = errors.New("session released")

func (r *runner) run(ctx context.Context) {
	log := r.m.Log.With("session", r.name)
	// The lease is renewed by its own loop, never by the one that processes
	// events: a long history sync or a slow core call there must not let the
	// lease lapse and hand the session to a second owner. Losing the lease,
	// or the session being stopped or deleted, cancels everything below.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go r.keepLease(ctx, cancel)
	for ctx.Err() == nil {
		fctx, fcancel := context.WithCancel(ctx)
		r.mu.Lock()
		r.follow = fcancel
		r.mu.Unlock()
		err := r.connectAndFollow(fctx)
		restarted := fctx.Err() != nil && ctx.Err() == nil
		fcancel()
		if errors.Is(err, errReleased) || ctx.Err() != nil {
			return
		}
		if restarted {
			continue // started again: reconnect for the new start at once
		}
		r.backoff = min(max(r.backoff*2, reconnectMin), reconnectMax)
		log.Warn("session stream ended, retrying", "err", err, "in", r.backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.backoff):
		}
	}
}

// keepLease renews the lease every third of its TTL, and on a nudge. It
// cancels the runner once the session is no longer this process's to run,
// and the current connection when the session was started again since it
// was made (a stop and a start between two ticks, as a logout does).
func (r *runner) keepLease(ctx context.Context, cancel context.CancelFunc) {
	t := time.NewTicker(r.m.LeaseTTL / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.nudge:
		}
		s, err := r.still(ctx)
		if errors.Is(err, errReleased) {
			cancel()
			return
		}
		if err != nil {
			r.m.Log.Warn("lease renewal", "session", r.name, "err", err)
			continue
		}
		r.mu.Lock()
		if r.follow != nil && r.followFor != 0 && s.StartedAt != r.followFor {
			r.follow()
		}
		r.mu.Unlock()
	}
}

// still re-reads the session and renews the lease; errReleased when the
// session was stopped, deleted, or the lease went to someone else.
func (r *runner) still(ctx context.Context) (store.Session, error) {
	s, err := r.m.Store.GetSession(ctx, r.name)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !s.Started) {
		return s, errReleased
	}
	if err != nil {
		return s, err
	}
	ok, err := r.m.Store.AcquireLease(ctx, leasePrefix+r.name, r.m.Worker, waha.NowMs(), r.m.LeaseTTL.Milliseconds())
	if err != nil {
		return s, err
	}
	if !ok {
		return s, errReleased
	}
	return s, nil
}

// ensureAccount makes sure the core has an account for the session.
func (r *runner) ensureAccount(ctx context.Context, s store.Session) (store.Session, error) {
	if s.AccountUUID != "" {
		_, err := r.m.Core.Accounts.GetAccountStatus(ctx, Ref(s))
		if err == nil {
			return s, nil
		}
		if core.CodeOf(err) != codes.NotFound {
			return s, err
		}
	}
	ref := r.name
	acc, err := r.m.Core.Accounts.CreateAccount(ctx, &pb.CreateAccountRequest{ExternalRef: &ref})
	if core.CodeOf(err) == codes.AlreadyExists {
		st, gerr := r.m.Core.Accounts.GetAccountStatus(ctx, &pb.AccountRef{Ref: &pb.AccountRef_ExternalRef{ExternalRef: ref}})
		if gerr != nil {
			return s, gerr
		}
		acc, err = &pb.Account{Uuid: st.GetUuid()}, nil
	}
	if err != nil {
		return s, err
	}
	uuid := acc.GetUuid()
	return r.m.Store.UpdateSession(ctx, r.name, store.SessionPatch{AccountUUID: &uuid})
}

func (r *runner) connectAndFollow(ctx context.Context) error {
	s, err := r.still(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.followFor = s.StartedAt
	r.mu.Unlock()
	cctx, ccancel := context.WithTimeout(ctx, coreCallTimeout)
	s, err = r.ensureAccount(cctx, s)
	ccancel()
	if err != nil {
		return err
	}
	// Subscribe before connecting so the first QR is never missed, and ask for
	// the core's replay ring: the core registers a subscriber only after it
	// answers, and an event emitted while no process followed the session (a
	// deploy, a lease handover) is in the ring too, which an event reaches
	// before its broadcast. What this session already processed is skipped by
	// its checkpoint; with no checkpoint yet, by the time of the subscription.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fresh := freshFilter(s.EventSeq, s.EventTS, time.Now().UnixMilli())
	stream, err := r.m.Core.Events.SubscribeEvents(sctx, &pb.SubscribeRequest{
		Selector:       &pb.SubscribeRequest_Account{Account: Ref(s)},
		ReplayFromRing: replayRing,
	})
	if err != nil {
		return err
	}
	evs := make(chan *pb.EventEnvelope, 256)
	errc := make(chan error, 1)
	go func() {
		for {
			ev, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			if !fresh(ev) {
				continue
			}
			select {
			case evs <- ev:
			case <-sctx.Done():
				return
			}
		}
	}()
	cctx, ccancel = context.WithTimeout(ctx, coreCallTimeout)
	st, err := r.m.Core.Accounts.ConnectAccount(cctx, &pb.ConnectAccountRequest{Account: Ref(s), BackfillHistory: r.m.Backfill})
	ccancel()
	if err != nil {
		return err
	}
	if s, err = r.applyState(ctx, s, st.GetState(), st); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case ev := <-evs:
			s, err = r.handle(ctx, s, ev)
			// The event is done even when handling it ends the stream (a
			// disconnect asks for a reconnect): checkpoint it first, or the
			// reconnect replays it and ends the stream again, forever.
			if seq := ev.GetMonotonicSeq(); seq > 0 {
				if cerr := r.m.Store.SetEventCheckpoint(context.WithoutCancel(ctx), r.name, seq, ev.GetTsUnixMs()); cerr != nil {
					r.m.Log.Warn("event checkpoint", "session", r.name, "err", cerr)
				}
			}
			if err != nil {
				return err
			}
		}
	}
}

// freshFilter decides which events of a new subscription to process. With a
// checkpoint, an event is new when its seq is past the checkpoint's or its
// time is later: the seq restarts when the core restarts, and the time keeps
// such events apart from the ones already processed. Without one, only what
// is emitted from now on counts, so a first owner does not replay history.
func freshFilter(seq, ts, now int64) func(*pb.EventEnvelope) bool {
	if seq == 0 && ts == 0 {
		return func(ev *pb.EventEnvelope) bool { return ev.GetTsUnixMs() >= now }
	}
	return func(ev *pb.EventEnvelope) bool {
		return ev.GetMonotonicSeq() > seq || ev.GetTsUnixMs() > ts
	}
}

// handle updates the session from one event and passes it to the ingestor.
func (r *runner) handle(ctx context.Context, s store.Session, ev *pb.EventEnvelope) (store.Session, error) {
	var err error
	switch e := ev.GetEvent().(type) {
	case *pb.EventEnvelope_Connection:
		if s, err = r.still(ctx); err != nil {
			return s, err
		}
		if stale(ev, s) {
			break
		}
		var st *pb.AccountStatus
		if e.Connection.GetState() == pb.ConnectionState_CONNECTED {
			cctx, cancel := context.WithTimeout(ctx, coreCallTimeout)
			st, _ = r.m.Core.Accounts.GetAccountStatus(cctx, Ref(s))
			cancel()
		}
		if s, err = r.applyState(ctx, s, e.Connection.GetState(), st); err != nil {
			return s, err
		}
	case *pb.EventEnvelope_Pairing:
		if s, err = r.applyPairing(ctx, e.Pairing, ev); err != nil {
			return s, err
		}
	case *pb.EventEnvelope_SelfPushName:
		if s, err = r.setMe(ctx, s, "", e.SelfPushName.GetNewName()); err != nil {
			return s, err
		}
	case *pb.EventEnvelope_Raw:
		if e.Raw.GetKind() == "gap" {
			cctx, cancel := context.WithTimeout(ctx, coreCallTimeout)
			st, gerr := r.m.Core.Accounts.GetAccountStatus(cctx, Ref(s))
			cancel()
			if gerr == nil {
				if s, err = r.applyState(ctx, s, st.GetState(), st); err != nil {
					return s, err
				}
			}
		}
	}
	if r.m.Ingest != nil {
		if err := r.m.Ingest.Handle(ctx, s, ev); err != nil {
			r.m.Log.Error("ingest", "session", r.name, "err", err)
		}
	}
	return s, nil
}

// stale reports a connection or pairing event from before the session's
// last start, replayed from the ring: history, not the state now. It is
// judged against the session as just re-read, since the start may have
// landed after the runner's copy was taken.
func stale(ev *pb.EventEnvelope, s store.Session) bool {
	return ev.GetTsUnixMs() < s.StartedAt
}

// applyState maps the core's connection state onto the WAHA status. A
// terminal disconnect ends the stream so run() reconnects with backoff;
// LOGGED_OUT and BANNED are final until someone starts the session again.
func (r *runner) applyState(ctx context.Context, s store.Session, state pb.ConnectionState, st *pb.AccountStatus) (store.Session, error) {
	var err error
	switch state {
	case pb.ConnectionState_CONNECTED:
		r.backoff = 0
		if st != nil {
			if s, err = r.setMe(ctx, s, st.GetJid().GetValue(), st.GetPushName()); err != nil {
				return s, err
			}
		}
		noQR := ""
		if s, err = r.m.Store.UpdateSession(ctx, s.Name, store.SessionPatch{QR: &noQR}); err != nil {
			return s, err
		}
		if s.Status != StatusWorking {
			return r.m.SetStatus(ctx, s, StatusWorking, nil)
		}
	case pb.ConnectionState_CONNECTING:
		if s.Status != StatusStarting && s.Status != StatusScanQR {
			return r.m.SetStatus(ctx, s, StatusStarting, nil)
		}
	case pb.ConnectionState_DISCONNECTED:
		if s.Status != StatusStarting {
			if s, err = r.m.SetStatus(ctx, s, StatusStarting, nil); err != nil {
				return s, err
			}
		}
		return s, errors.New("account disconnected")
	case pb.ConnectionState_LOGGED_OUT, pb.ConnectionState_BANNED:
		noQR := ""
		if s, err = r.m.Store.UpdateSession(ctx, s.Name, store.SessionPatch{QR: &noQR}); err != nil {
			return s, err
		}
		if s.Status != StatusFailed {
			if s, err = r.m.SetStatus(ctx, s, StatusFailed, nil); err != nil {
				return s, err
			}
		}
		// Hand the session back: nothing reconnects a logged-out account.
		stopped := false
		_, err = r.m.Store.UpdateSession(ctx, s.Name, store.SessionPatch{Started: &stopped})
		if err != nil {
			return s, err
		}
		return s, errReleased
	}
	return s, nil
}

func (r *runner) applyPairing(ctx context.Context, u *pb.PairingUpdate, env *pb.EventEnvelope) (store.Session, error) {
	// A pairing event still queued when the session was stopped must not
	// write a QR or a status over it, nor one from before its last start.
	s, err := r.still(ctx)
	if err != nil || stale(env, s) {
		return s, err
	}
	switch ev := u.GetEvent().(type) {
	case *pb.PairingUpdate_QrCode:
		qr := ev.QrCode
		s, err = r.m.Store.UpdateSession(ctx, s.Name, store.SessionPatch{QR: &qr})
		if err != nil {
			return s, err
		}
		// WAHA publishes SCAN_QR_CODE again on every new QR.
		return r.m.SetStatus(ctx, s, StatusScanQR, nil)
	case *pb.PairingUpdate_Paired:
		r.lid = ev.Paired.GetLid().GetValue()
		return r.setMe(ctx, s, ev.Paired.GetJid().GetValue(), "")
	case *pb.PairingUpdate_Error, *pb.PairingUpdate_CodeError:
		return r.m.SetStatus(ctx, s, StatusFailed, nil)
	case *pb.PairingUpdate_QrCodesExhausted:
		if ev.QrCodesExhausted.GetDisconnected() {
			return s, errors.New("QR codes exhausted")
		}
	}
	return s, nil
}

// Me is WAHA's MeInfo.
type Me struct {
	ID               string `json:"id"`
	PushName         string `json:"pushName"`
	LID              string `json:"lid,omitempty"`
	JID              string `json:"jid,omitempty"`
	ReachoutTimelock any    `json:"reachoutTimelock"`
	MessageCapping   any    `json:"messageCapping"`
}

// setMe merges what is known about the account into the session's me.
func (r *runner) setMe(ctx context.Context, s store.Session, jid, pushName string) (store.Session, error) {
	var me Me
	_ = json.Unmarshal(s.Me, &me)
	if jid != "" {
		me.ID, me.JID = waha.ChatIDFromJID(jid), jid
	}
	if pushName != "" {
		me.PushName = pushName
	}
	if r.lid != "" {
		me.LID = waha.ChatIDFromJID(r.lid)
	}
	if me.ID == "" {
		return s, nil
	}
	raw, err := json.Marshal(me)
	if err != nil {
		return s, err
	}
	rm := json.RawMessage(raw)
	return r.m.Store.UpdateSession(ctx, s.Name, store.SessionPatch{Me: &rm})
}
