package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LLawli/wamux-http/internal/store"
)

// runIngest holds the conformance cases for IngestStore added after the
// split; each area owns its file.
func runIngest(t *testing.T, open Opener) {
	t.Run("Acks", func(t *testing.T) { acks(t, open(t, "ia")) })
	t.Run("AllChatsMessages", func(t *testing.T) { allChatsMessages(t, open(t, "im")) })
	t.Run("TouchAndPatchChat", func(t *testing.T) { touchPatchChat(t, open(t, "ic")) })
	t.Run("GetLabel", func(t *testing.T) { getLabel(t, open(t, "il")) })
	t.Run("Presences", func(t *testing.T) { presences(t, open(t, "ip")) })
}

func acks(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.UpsertMessages(ctx, []store.Message{{Session: "s", ID: "x_A", WAID: "A", ChatID: "1@c.us", Ack: 1,
		Body: json.RawMessage(`{"id":"x_A","ack":1,"ackName":"SERVER"}`)}}))
	must(t, s.UpdateAck(ctx, "s", []string{"A"}, 3))
	m, _ := s.GetMessage(ctx, "s", "x_A")
	var body map[string]any
	_ = json.Unmarshal(m.Body, &body)
	if m.Ack != 3 || body["ack"] != float64(3) || body["ackName"] != "READ" {
		t.Fatalf("UpdateAck kept the body's ackName behind: %+v %s", m, m.Body)
	}
	// SetAck lowers, for a send the server refused.
	must(t, s.SetAck(ctx, "s", []string{"A"}, -1))
	m, _ = s.GetMessage(ctx, "s", "x_A")
	_ = json.Unmarshal(m.Body, &body)
	if m.Ack != -1 || body["ack"] != float64(-1) || body["ackName"] != "ERROR" {
		t.Fatalf("SetAck: %+v %s", m, m.Body)
	}
}

func allChatsMessages(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.UpsertMessages(ctx, []store.Message{
		{Session: "s", ID: "a", WAID: "a", ChatID: "1@c.us", Timestamp: 1, Body: json.RawMessage(`{}`)},
		{Session: "s", ID: "b", WAID: "b", ChatID: "2@c.us", Timestamp: 2, Body: json.RawMessage(`{}`)},
		{Session: "o", ID: "c", WAID: "c", ChatID: "2@c.us", Timestamp: 3, Body: json.RawMessage(`{}`)},
	}))
	all, err := s.ListMessages(ctx, "s", "", store.MessageFilter{})
	must(t, err)
	if len(all) != 2 || all[0].ID != "b" || all[1].ID != "a" {
		t.Fatalf("every chat of one session, newest first: %+v", all)
	}
}

func touchPatchChat(t *testing.T, s store.Store) {
	ctx := context.Background()
	yes := true
	must(t, s.PatchChat(ctx, "s", "1@c.us", store.ChatPatch{Archived: &yes}))
	must(t, s.TouchChat(ctx, "s", "1@c.us", "Ana", 2000, "m2", 1))
	must(t, s.TouchChat(ctx, "s", "1@c.us", "", 3000, "m3", 1))
	c, err := s.GetChat(ctx, "s", "1@c.us")
	must(t, err)
	if !c.Archived || c.UnreadCount != 2 || c.Name != "Ana" || c.LastMessageID != "m3" || c.ConversationTS != 3000 {
		t.Fatalf("touch lost the archive flag or miscounted: %+v", c)
	}
	// An older message does not rewind the chat; -1 resets the count.
	must(t, s.TouchChat(ctx, "s", "1@c.us", "", 1000, "m1", -1))
	c, _ = s.GetChat(ctx, "s", "1@c.us")
	if c.LastMessageID != "m3" || c.ConversationTS != 3000 || c.UnreadCount != 0 {
		t.Fatalf("rewound or unread kept: %+v", c)
	}
	no, five, name := false, 5, "Bia"
	must(t, s.PatchChat(ctx, "s", "1@c.us", store.ChatPatch{Archived: &no, Pinned: &yes, Unread: &five, Name: &name}))
	c, _ = s.GetChat(ctx, "s", "1@c.us")
	if c.Archived || !c.Pinned || c.UnreadCount != 5 || c.Name != "Bia" || c.LastMessageID != "m3" {
		t.Fatalf("patch: %+v", c)
	}
	// A new chat created by a touch starts with the given count.
	must(t, s.TouchChat(ctx, "s", "2@c.us", "", 10, "x", 1))
	if c, _ := s.GetChat(ctx, "s", "2@c.us"); c.UnreadCount != 1 {
		t.Fatalf("new chat unread: %+v", c)
	}
}

func getLabel(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.UpsertLabel(ctx, store.Label{Session: "s", ID: "7", Name: "Gone", Color: 3, Deleted: true}))
	l, err := s.GetLabel(ctx, "s", "7")
	must(t, err)
	if l.Name != "Gone" || !l.Deleted || l.Color != 3 {
		t.Fatalf("label: %+v", l)
	}
	if _, err := s.GetLabel(ctx, "s", "8"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing label: %v", err)
	}
}

func presences(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.PutPresence(ctx, store.Presence{Session: "s", ChatID: "g@g.us", Participant: "1@c.us", State: "typing", UpdatedAt: 1}))
	must(t, s.PutPresence(ctx, store.Presence{Session: "s", ChatID: "g@g.us", Participant: "2@c.us", State: "recording", UpdatedAt: 2}))
	must(t, s.PutPresence(ctx, store.Presence{Session: "s", ChatID: "g@g.us", Participant: "1@c.us", State: "paused", UpdatedAt: 3}))
	must(t, s.PutPresence(ctx, store.Presence{Session: "s", ChatID: "1@c.us", Participant: "1@c.us", State: "offline", LastSeen: 99, UpdatedAt: 4}))
	g, err := s.ListPresences(ctx, "s", "g@g.us")
	must(t, err)
	if len(g) != 2 || g[0].Participant != "2@c.us" || g[1].State != "paused" {
		t.Fatalf("one chat, latest per participant: %+v", g)
	}
	all, _ := s.ListPresences(ctx, "s", "")
	if len(all) != 3 || all[0].ChatID != "1@c.us" || all[0].LastSeen != 99 {
		t.Fatalf("all chats: %+v", all)
	}
	must(t, s.CreateSession(ctx, store.Session{Name: "s", Status: "STOPPED"}))
	must(t, s.PurgeSession(ctx, "s"))
	if left, _ := s.ListPresences(ctx, "s", ""); len(left) != 0 {
		t.Fatalf("purge kept presences: %+v", left)
	}
}
