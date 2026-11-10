package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/events"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/store/sqlite"
)

// A runner reads its session as started, then writes what an event says. A
// stop or a new start made in between wins: the logout's own LOGGED_OUT,
// handled while a delete stopped the session, must not come out as FAILED
// after STOPPED, nor stop a session started again.
func TestRunnerWriteLosesToAStopOrStartInBetween(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := &Manager{Store: st, Pub: &events.Publisher{Store: st}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const firstStart = int64(1000)
	if err := st.CreateSession(ctx, store.Session{Name: "s", Started: true, StartedAt: firstStart, Status: StatusWorking}); err != nil {
		t.Fatal(err)
	}
	seen, err := st.GetSession(ctx, "s") // what the runner read
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{m: m, name: "s", followFor: firstStart}

	if _, err := m.Stop(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.applyState(ctx, seen, pb.ConnectionState_LOGGED_OUT, nil); !errors.Is(err, errStale) {
		t.Fatalf("applyState after a stop: %v, want errStale", err)
	}
	if got := lastStatus(t, st); got != StatusStopped {
		t.Fatalf("last session.status %q, want STOPPED", got)
	}

	started, newStart := true, firstStart+1
	if _, err := st.UpdateSession(ctx, "s", store.SessionPatch{Started: &started, StartedAt: &newStart}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.applyState(ctx, seen, pb.ConnectionState_LOGGED_OUT, nil); !errors.Is(err, errStale) {
		t.Fatalf("applyState after a new start: %v, want errStale", err)
	}
	if s, _ := st.GetSession(ctx, "s"); !s.Started || s.Status == StatusFailed {
		t.Fatalf("the old start's event changed the new one: started=%v status=%s", s.Started, s.Status)
	}
}

func lastStatus(t *testing.T, st store.Store) string {
	t.Helper()
	evs, err := st.EventsAfter(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := ""
	for _, ev := range evs {
		if ev.Type == "session.status" {
			var env struct{ Payload struct{ Status string } }
			_ = json.Unmarshal(ev.Body, &env)
			last = env.Payload.Status
		}
	}
	return last
}
