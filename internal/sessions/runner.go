package sessions

import (
	"context"
	"encoding/json"
	"errors"
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
	backoff time.Duration
	lid     string // the account's LID, learned at pairing
}

// errReleased ends a runner whose session is no longer ours to run.
var errReleased = errors.New("session released")

func (r *runner) run(ctx context.Context) {
	log := r.m.Log.With("session", r.name)
	lease := time.NewTicker(r.m.LeaseTTL / 3)
	defer lease.Stop()
	for ctx.Err() == nil {
		err := r.connectAndFollow(ctx, lease.C)
		if errors.Is(err, errReleased) || ctx.Err() != nil {
			return
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

func (r *runner) connectAndFollow(ctx context.Context, lease <-chan time.Time) error {
	s, err := r.still(ctx)
	if err != nil {
		return err
	}
	if s, err = r.ensureAccount(ctx, s); err != nil {
		return err
	}
	// Subscribe before connecting so the first QR is never missed. The core
	// registers a subscriber only after it answers, so an event can fall in
	// between; it reaches the replay ring before the broadcast, so asking for
	// the ring and dropping what predates this subscription loses nothing
	// and replays nothing stale.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	since := time.Now().UnixMilli()
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
			if ev.GetTsUnixMs() < since {
				continue
			}
			select {
			case evs <- ev:
			case <-sctx.Done():
				return
			}
		}
	}()
	st, err := r.m.Core.Accounts.ConnectAccount(ctx, &pb.ConnectAccountRequest{Account: Ref(s), BackfillHistory: r.m.Backfill})
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
		case <-lease:
			if s, err = r.still(ctx); err != nil {
				return err
			}
		case ev := <-evs:
			if s, err = r.handle(ctx, s, ev); err != nil {
				return err
			}
		}
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
		var st *pb.AccountStatus
		if e.Connection.GetState() == pb.ConnectionState_CONNECTED {
			st, _ = r.m.Core.Accounts.GetAccountStatus(ctx, Ref(s))
		}
		if s, err = r.applyState(ctx, s, e.Connection.GetState(), st); err != nil {
			return s, err
		}
	case *pb.EventEnvelope_Pairing:
		if s, err = r.applyPairing(ctx, s, e.Pairing); err != nil {
			return s, err
		}
	case *pb.EventEnvelope_SelfPushName:
		if s, err = r.setMe(ctx, s, "", e.SelfPushName.GetNewName()); err != nil {
			return s, err
		}
	case *pb.EventEnvelope_Raw:
		if e.Raw.GetKind() == "gap" {
			if st, gerr := r.m.Core.Accounts.GetAccountStatus(ctx, Ref(s)); gerr == nil {
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

func (r *runner) applyPairing(ctx context.Context, s store.Session, u *pb.PairingUpdate) (store.Session, error) {
	switch ev := u.GetEvent().(type) {
	case *pb.PairingUpdate_QrCode:
		qr := ev.QrCode
		s, err := r.m.Store.UpdateSession(ctx, s.Name, store.SessionPatch{QR: &qr})
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
