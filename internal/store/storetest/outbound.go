package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/LLawli/wamux-http/internal/store"
)

// runOutbound holds the conformance cases for OutboundStore added after the
// split; each area owns its file.
func runOutbound(t *testing.T, open Opener) {
	t.Run("Polls", func(t *testing.T) { polls(t, open(t, "po")) })
}

func polls(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.GetPoll(ctx, "s", "P1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing poll: %v", err)
	}
	p := store.Poll{WAID: "P1", Chat: "1@s.whatsapp.net", Creator: "2@s.whatsapp.net", Options: []string{"a", "b"}, Secret: []byte{9, 9}}
	must(t, s.PutPoll(ctx, "s", p))
	got, err := s.GetPoll(ctx, "s", "P1")
	must(t, err)
	if got.Chat != p.Chat || got.Creator != p.Creator || len(got.Options) != 2 || got.Options[1] != "b" || len(got.Secret) != 2 {
		t.Fatalf("poll: %+v", got)
	}
	if b, err := s.PollSecret(ctx, "s", "P1"); err != nil || len(b) != 2 {
		t.Fatalf("PutPoll's secret through PollSecret: %v %v", b, err)
	}
	// A poll known only by its secret reads back with no options.
	must(t, s.PutPollSecret(ctx, "s", "P2", []byte{1}))
	got, err = s.GetPoll(ctx, "s", "P2")
	must(t, err)
	if len(got.Options) != 0 || got.Creator != "" || len(got.Secret) != 1 {
		t.Fatalf("secret-only poll: %+v", got)
	}
	if _, err := s.GetPoll(ctx, "other", "P1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a poll leaked across sessions: %v", err)
	}
}
