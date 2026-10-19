package events

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/LLawli/wamux-http/internal/store"
)

// Tail follows the store's event log and fans each new event out to this
// process's subscribers (WebSocket clients). Every process tails the same
// log, so a subscriber sees every session's events whichever process owns
// the session.
type Tail struct {
	Store store.Store
	Every time.Duration
	Log   *slog.Logger

	mu   sync.Mutex
	subs map[chan store.Event]struct{}
}

// Subscribe returns a channel of new events and a function that ends the
// subscription. A subscriber that does not keep up loses events rather than
// slowing the others.
func (t *Tail) Subscribe(buffer int) (<-chan store.Event, func()) {
	ch := make(chan store.Event, buffer)
	t.mu.Lock()
	if t.subs == nil {
		t.subs = map[chan store.Event]struct{}{}
	}
	t.subs[ch] = struct{}{}
	t.mu.Unlock()
	return ch, func() {
		t.mu.Lock()
		if _, ok := t.subs[ch]; ok {
			delete(t.subs, ch)
			close(ch)
		}
		t.mu.Unlock()
	}
}

// Run polls the log from its current end until ctx ends.
func (t *Tail) Run(ctx context.Context) {
	every := t.Every
	if every == 0 {
		every = 200 * time.Millisecond
	}
	log := t.Log
	if log == nil {
		log = slog.Default()
	}
	last, err := t.Store.LastEventSeq(ctx)
	if err != nil {
		log.Error("event tail start", "err", err)
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		for {
			evs, err := t.Store.EventsAfter(ctx, last, 500)
			if err != nil {
				log.Error("event tail", "err", err)
				break
			}
			for _, e := range evs {
				last = e.Seq
				t.fanout(e)
			}
			if len(evs) < 500 {
				break
			}
		}
	}
}

func (t *Tail) fanout(e store.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for ch := range t.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
