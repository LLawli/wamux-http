// Package storetest is the conformance suite every store engine must pass.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/LLawli/wamux-http/internal/store"
)

// Opener opens a fresh, empty store. Calling it twice with the same key must
// open the same database (two processes sharing it).
type Opener func(t *testing.T, key string) store.Store

// Run runs the whole suite.
func Run(t *testing.T, open Opener) {
	t.Run("Sessions", func(t *testing.T) { sessions(t, open(t, "s")) })
	t.Run("APIKeys", func(t *testing.T) { apiKeys(t, open(t, "k")) })
	t.Run("Leases", func(t *testing.T) { leases(t, open(t, "l"), open(t, "l")) })
	t.Run("EventsAndDeliveries", func(t *testing.T) { events(t, open(t, "e")) })
	t.Run("ConcurrentClaims", func(t *testing.T) { concurrentClaims(t, open(t, "c"), open(t, "c")) })
	t.Run("Messages", func(t *testing.T) { messages(t, open(t, "m")) })
	t.Run("Chats", func(t *testing.T) { chats(t, open(t, "ch")) })
	t.Run("ContactsAndLabels", func(t *testing.T) { contactsLabels(t, open(t, "cl")) })
	t.Run("Purge", func(t *testing.T) { purge(t, open(t, "p")) })
	t.Run("Outbound", func(t *testing.T) { outbound(t, open(t, "o")) })
	runIngest(t, open)
	runOutbound(t, open)
	runDirectory(t, open)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func sessions(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.CreateSession(ctx, store.Session{Name: "default", Status: "STOPPED", Config: json.RawMessage(`{"metadata":{"a":"1"}}`), CreatedAt: 1, UpdatedAt: 1}))
	if err := s.CreateSession(ctx, store.Session{Name: "default", Status: "STOPPED"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate session: %v", err)
	}
	if _, err := s.GetSession(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing session: %v", err)
	}
	started, status, uuid := true, "STARTING", "u-1"
	got, err := s.UpdateSession(ctx, "default", store.SessionPatch{Started: &started, Status: &status, AccountUUID: &uuid})
	must(t, err)
	if !got.Started || got.Status != "STARTING" || got.AccountUUID != "u-1" || string(got.Config) != `{"metadata":{"a":"1"}}` {
		t.Fatalf("patch: %+v", got)
	}
	null := json.RawMessage("null")
	got, err = s.UpdateSession(ctx, "default", store.SessionPatch{Config: &null})
	must(t, err)
	if got.Config != nil {
		t.Fatalf("null config stays null, got %s", got.Config)
	}
	if _, err := s.UpdateSession(ctx, "nope", store.SessionPatch{Started: &started}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("patch missing: %v", err)
	}
	must(t, s.SetEventCheckpoint(ctx, "default", 42, 1700))
	if got, _ := s.GetSession(ctx, "default"); got.EventSeq != 42 || got.EventTS != 1700 {
		t.Fatalf("checkpoint: %d %d", got.EventSeq, got.EventTS)
	}
	must(t, s.CreateSession(ctx, store.Session{Name: "a", Status: "STOPPED"}))
	all, err := s.ListSessions(ctx)
	must(t, err)
	if len(all) != 2 || all[0].Name != "a" {
		t.Fatalf("list: %+v", all)
	}
	must(t, s.DeleteSession(ctx, "a"))
	if err := s.DeleteSession(ctx, "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func apiKeys(t *testing.T, s store.Store) {
	ctx := context.Background()
	k := store.APIKey{ID: "key_1", Hash: "h1", Hint: "abcd", Sessions: []string{"default", "vendas"}, IsActive: true, Actions: json.RawMessage(`{"read":true}`), CreatedAt: 1}
	must(t, s.CreateAPIKey(ctx, k))
	if err := s.CreateAPIKey(ctx, store.APIKey{ID: "key_2", Hash: "h1"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate hash: %v", err)
	}
	got, err := s.GetAPIKeyByHash(ctx, "h1")
	must(t, err)
	if got.ID != "key_1" || !slices.Equal(got.Sessions, []string{"default", "vendas"}) || string(got.Actions) != `{"read":true}` {
		t.Fatalf("by hash: %+v", got)
	}
	got.IsActive, got.Sessions = false, []string{"suporte"}
	must(t, s.UpdateAPIKey(ctx, got))
	got, err = s.GetAPIKey(ctx, "key_1")
	must(t, err)
	if got.IsActive || got.Hash != "h1" || !slices.Equal(got.Sessions, []string{"suporte"}) {
		t.Fatalf("update: %+v", got)
	}
	if err := s.UpdateAPIKey(ctx, store.APIKey{ID: "nope", Sessions: []string{"x"}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update a missing key: %v", err)
	}
	must(t, s.CreateAPIKey(ctx, store.APIKey{ID: "key_3", Hash: "h3", IsAdmin: true, IsActive: true, CreatedAt: 2}))
	list, err := s.ListAPIKeys(ctx)
	must(t, err)
	if len(list) != 2 || !slices.Equal(list[0].Sessions, []string{"suporte"}) || list[1].Sessions != nil {
		t.Fatalf("list: %+v", list)
	}
	// A key deleted and created again with the same id (a rotated scoped
	// key) must not inherit the old sessions.
	must(t, s.DeleteAPIKey(ctx, "key_1"))
	if _, err := s.GetAPIKey(ctx, "key_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted key: %v", err)
	}
	must(t, s.CreateAPIKey(ctx, store.APIKey{ID: "key_1", Hash: "h4", Sessions: []string{"default"}, IsActive: true, CreatedAt: 3}))
	got, err = s.GetAPIKey(ctx, "key_1")
	must(t, err)
	if !slices.Equal(got.Sessions, []string{"default"}) {
		t.Fatalf("recreated key: %+v", got)
	}
}

func leases(t *testing.T, a, b store.Store) {
	ctx := context.Background()
	ok, err := a.AcquireLease(ctx, "session:default", "A", 1000, 500)
	must(t, err)
	if !ok {
		t.Fatal("A could not take a free lease")
	}
	if ok, _ := b.AcquireLease(ctx, "session:default", "B", 1200, 500); ok {
		t.Fatal("B took a live lease held by A")
	}
	if ok, _ := a.AcquireLease(ctx, "session:default", "A", 1400, 500); !ok {
		t.Fatal("A could not renew its own lease")
	}
	if h, _ := b.LeaseHolder(ctx, "session:default", 1500); h != "A" {
		t.Fatalf("holder = %q", h)
	}
	if ok, _ := b.AcquireLease(ctx, "session:default", "B", 1899, 500); ok {
		t.Fatal("B took the lease before the renewal expired")
	}
	if ok, _ := b.AcquireLease(ctx, "session:default", "B", 1900, 500); !ok {
		t.Fatal("B could not take an expired lease")
	}
	must(t, a.ReleaseLease(ctx, "session:default", "A")) // not A's any more: no-op
	if h, _ := a.LeaseHolder(ctx, "session:default", 2000); h != "B" {
		t.Fatalf("A released B's lease: holder %q", h)
	}
	must(t, b.ReleaseLease(ctx, "session:default", "B"))
	if h, _ := a.LeaseHolder(ctx, "session:default", 2000); h != "" {
		t.Fatalf("released lease still held by %q", h)
	}
}

func events(t *testing.T, s store.Store) {
	ctx := context.Background()
	if seq, _ := s.LastEventSeq(ctx); seq != 0 {
		t.Fatalf("empty store last seq %d", seq)
	}
	d := store.Delivery{URL: "http://x", Body: json.RawMessage(`{"a":1}`), Webhook: json.RawMessage(`{"url":"http://x"}`), NextAttemptAt: 100}
	s1, err := s.AppendEvent(ctx, store.Event{ID: "evt_1", Session: "default", Type: "message", Body: json.RawMessage(`{"n":1}`), CreatedAt: 10}, []store.Delivery{d, d})
	must(t, err)
	s2, err := s.AppendEvent(ctx, store.Event{ID: "evt_2", Session: "default", Type: "message", Body: json.RawMessage(`{"n":2}`), CreatedAt: 20}, nil)
	must(t, err)
	if s2 <= s1 {
		t.Fatalf("seq not increasing: %d then %d", s1, s2)
	}
	evs, err := s.EventsAfter(ctx, s1, 10)
	must(t, err)
	if len(evs) != 1 || evs[0].ID != "evt_2" || string(evs[0].Body) != `{"n":2}` {
		t.Fatalf("after: %+v", evs)
	}
	if got, _ := s.ClaimDeliveries(ctx, "A", 99, 1000, 10); len(got) != 0 {
		t.Fatalf("claimed a delivery before it was due: %+v", got)
	}
	got, err := s.ClaimDeliveries(ctx, "A", 100, 1000, 10)
	must(t, err)
	if len(got) != 2 || got[0].EventID != "evt_1" || string(got[0].Webhook) != `{"url":"http://x"}` {
		t.Fatalf("claim: %+v", got)
	}
	if again, _ := s.ClaimDeliveries(ctx, "B", 500, 1000, 10); len(again) != 0 {
		t.Fatalf("B claimed A's locked deliveries: %+v", again)
	}
	must(t, s.CompleteDelivery(ctx, got[0].ID))
	must(t, s.RetryDelivery(ctx, got[1].ID, "boom", 3000))
	if again, _ := s.ClaimDeliveries(ctx, "B", 2000, 1000, 10); len(again) != 0 {
		t.Fatalf("retry claimed before next attempt: %+v", again)
	}
	again, err := s.ClaimDeliveries(ctx, "B", 3000, 1000, 10)
	must(t, err)
	if len(again) != 1 || again[0].Attempts != 1 || again[0].LastError != "boom" {
		t.Fatalf("retry: %+v", again)
	}
	must(t, s.PostponeDelivery(ctx, again[0].ID, 3500))
	if c, _ := s.ClaimDeliveries(ctx, "C", 3400, 1000, 10); len(c) != 0 {
		t.Fatalf("postponed delivery claimed early: %+v", c)
	}
	again, err = s.ClaimDeliveries(ctx, "C", 3500, 1000, 10)
	must(t, err)
	if len(again) != 1 || again[0].Attempts != 1 {
		t.Fatalf("a postpone must not count an attempt: %+v", again)
	}
	must(t, s.RetryDelivery(ctx, again[0].ID, "final", 0))
	if left, _ := s.ClaimDeliveries(ctx, "B", 99999, 1000, 10); len(left) != 0 {
		t.Fatalf("given-up delivery still there: %+v", left)
	}
	// A crashed holder's lock expires.
	_, err = s.AppendEvent(ctx, store.Event{ID: "evt_3", Session: "default", Type: "message", Body: json.RawMessage(`{}`), CreatedAt: 30}, []store.Delivery{d})
	must(t, err)
	if c, _ := s.ClaimDeliveries(ctx, "A", 5000, 1000, 10); len(c) != 1 {
		t.Fatalf("claim evt_3: %+v", c)
	}
	if c, _ := s.ClaimDeliveries(ctx, "B", 6000, 1000, 10); len(c) != 1 {
		t.Fatalf("expired lock not reclaimable: %+v", c)
	}
	must(t, s.PruneEvents(ctx, 25))
	evs, _ = s.EventsAfter(ctx, 0, 10)
	if len(evs) != 1 || evs[0].ID != "evt_3" {
		t.Fatalf("prune: %+v", evs)
	}
}

func concurrentClaims(t *testing.T, a, b store.Store) {
	ctx := context.Background()
	const n = 200
	for i := 0; i < n; i++ {
		_, err := a.AppendEvent(ctx, store.Event{ID: fmt.Sprint("evt_", i), Session: "s", Type: "message", Body: json.RawMessage(`{}`), CreatedAt: 1},
			[]store.Delivery{{URL: "http://x", Body: json.RawMessage(`{}`), Webhook: json.RawMessage(`{}`), NextAttemptAt: 0}})
		must(t, err)
	}
	var mu sync.Mutex
	seen := map[int64]string{}
	var wg sync.WaitGroup
	for _, w := range []struct {
		s    store.Store
		name string
	}{{a, "A"}, {b, "B"}, {a, "C"}, {b, "D"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < n; round++ { // bounded: a store that re-hands claimed rows must fail, not hang
				ds, err := w.s.ClaimDeliveries(ctx, w.name, 10, 60000, 7)
				if err != nil {
					t.Error(err)
					return
				}
				if len(ds) == 0 {
					return
				}
				mu.Lock()
				for _, d := range ds {
					if prev, dup := seen[d.ID]; dup {
						t.Errorf("delivery %d claimed by %s and %s", d.ID, prev, w.name)
					}
					seen[d.ID] = w.name
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d of %d deliveries", len(seen), n)
	}
}

func messages(t *testing.T, s store.Store) {
	ctx := context.Background()
	mk := func(id string, ts int64, fromMe bool, ack int) store.Message {
		return store.Message{Session: "s", ID: "x_" + id, WAID: id, ChatID: "1@c.us", Timestamp: ts, FromMe: fromMe, Ack: ack,
			Body: json.RawMessage(fmt.Sprintf(`{"id":"x_%s","ack":%d}`, id, ack)), Raw: []byte{1}}
	}
	must(t, s.UpsertMessages(ctx, []store.Message{mk("A", 1000, true, 1), mk("B", 2000, false, 0), mk("C", 3000, true, 2)}))
	// Replays never lower the ack, and keep the raw payload.
	again := mk("C", 3000, true, 1)
	again.Raw = nil
	must(t, s.UpsertMessages(ctx, []store.Message{again}))
	c, err := s.GetMessage(ctx, "s", "x_C")
	must(t, err)
	if c.Ack != 2 || len(c.Raw) != 1 {
		t.Fatalf("replay lowered ack or dropped raw: %+v", c)
	}
	must(t, s.UpdateAck(ctx, "s", []string{"A", "C"}, 3))
	a, _ := s.GetMessage(ctx, "s", "x_A")
	var body map[string]any
	_ = json.Unmarshal(a.Body, &body)
	if a.Ack != 3 || body["ack"] != float64(3) {
		t.Fatalf("UpdateAck: %+v %s", a, a.Body)
	}
	must(t, s.UpdateAck(ctx, "s", []string{"A"}, 1))
	if a, _ := s.GetMessage(ctx, "s", "x_A"); a.Ack != 3 {
		t.Fatalf("UpdateAck lowered: %d", a.Ack)
	}
	// A replay with a lower ack keeps the higher one, in the column and in
	// the body the routes serve.
	must(t, s.UpsertMessages(ctx, []store.Message{mk("A", 1000, true, 1)}))
	a, _ = s.GetMessage(ctx, "s", "x_A")
	body = map[string]any{}
	_ = json.Unmarshal(a.Body, &body)
	if a.Ack != 3 || body["ack"] != float64(3) || body["ackName"] != "READ" {
		t.Fatalf("a replay lowered the body's ack: %d %s", a.Ack, a.Body)
	}
	if m, err := s.GetMessageByWAID(ctx, "s", "B"); err != nil || m.ID != "x_B" {
		t.Fatalf("by wa id: %+v %v", m, err)
	}
	list, err := s.ListMessages(ctx, "s", "1@c.us", store.MessageFilter{Limit: 2})
	must(t, err)
	if len(list) != 2 || list[0].WAID != "C" || list[1].WAID != "B" {
		t.Fatalf("newest first: %+v", list)
	}
	yes := true
	gte := int64(1500)
	list, _ = s.ListMessages(ctx, "s", "1@c.us", store.MessageFilter{FromMe: &yes, TimestampGTE: &gte, Ascending: true})
	if len(list) != 1 || list[0].WAID != "C" {
		t.Fatalf("filter: %+v", list)
	}
	list, _ = s.ListMessages(ctx, "s", "1@c.us", store.MessageFilter{Limit: 1, Offset: 2})
	if len(list) != 1 || list[0].WAID != "A" {
		t.Fatalf("offset: %+v", list)
	}
	must(t, s.DeleteMessage(ctx, "s", "x_A"))
	if _, err := s.GetMessage(ctx, "s", "x_A"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted message: %v", err)
	}
	must(t, s.DeleteChatMessages(ctx, "s", "1@c.us"))
	if list, _ := s.ListMessages(ctx, "s", "1@c.us", store.MessageFilter{}); len(list) != 0 {
		t.Fatalf("chat messages left: %d", len(list))
	}
}

func chats(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.UpsertChat(ctx, store.Chat{Session: "s", ID: "1@c.us", Name: "Ana", ConversationTS: 2000, LastMessageID: "m2"}))
	// An older history chunk does not rewind the chat, nor erase its name.
	must(t, s.UpsertChat(ctx, store.Chat{Session: "s", ID: "1@c.us", ConversationTS: 1000, LastMessageID: "m1"}))
	c, err := s.GetChat(ctx, "s", "1@c.us")
	must(t, err)
	if c.ConversationTS != 2000 || c.LastMessageID != "m2" || c.Name != "Ana" {
		t.Fatalf("rewound: %+v", c)
	}
	must(t, s.UpsertChat(ctx, store.Chat{Session: "s", ID: "2@c.us", Name: "Bia", ConversationTS: 3000}))
	must(t, s.UpsertChat(ctx, store.Chat{Session: "s", ID: "3@g.us", Name: "Grupo", ConversationTS: 500}))
	list, err := s.ListChats(ctx, "s", store.ChatFilter{})
	must(t, err)
	if len(list) != 3 || list[0].ID != "2@c.us" || list[2].ID != "3@g.us" {
		t.Fatalf("by ts desc: %+v", list)
	}
	list, _ = s.ListChats(ctx, "s", store.ChatFilter{SortBy: "name", Ascending: true, Limit: 2})
	if len(list) != 2 || list[0].Name != "Ana" || list[1].Name != "Bia" {
		t.Fatalf("by name: %+v", list)
	}
	list, _ = s.ListChats(ctx, "s", store.ChatFilter{IDs: []string{"3@g.us", "1@c.us"}, SortBy: "id", Ascending: true})
	if len(list) != 2 || list[0].ID != "1@c.us" {
		t.Fatalf("by ids: %+v", list)
	}
	must(t, s.UpsertMessages(ctx, []store.Message{{Session: "s", ID: "m", WAID: "m", ChatID: "1@c.us", Body: json.RawMessage(`{}`)}}))
	must(t, s.DeleteChat(ctx, "s", "1@c.us"))
	if _, err := s.GetChat(ctx, "s", "1@c.us"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted chat: %v", err)
	}
	if _, err := s.GetMessage(ctx, "s", "m"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("chat delete left its messages: %v", err)
	}
}

func contactsLabels(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.UpsertContact(ctx, store.Contact{Session: "s", ID: "1@c.us", Name: "Ana"}))
	must(t, s.UpsertContact(ctx, store.Contact{Session: "s", ID: "1@c.us", PushName: "Aninha"}))
	c, err := s.GetContact(ctx, "s", "1@c.us")
	must(t, err)
	if c.Name != "Ana" || c.PushName != "Aninha" {
		t.Fatalf("merge: %+v", c)
	}
	must(t, s.UpsertContact(ctx, store.Contact{Session: "s", ID: "2@c.us"}))
	if list, _ := s.ListContacts(ctx, "s", 1, 1); len(list) != 1 || list[0].ID != "2@c.us" {
		t.Fatalf("page: %+v", list)
	}
	must(t, s.UpsertLabel(ctx, store.Label{Session: "s", ID: "1", Name: "New", Color: 2}))
	must(t, s.UpsertLabel(ctx, store.Label{Session: "s", ID: "2", Name: "Gone", Deleted: true}))
	must(t, s.SetChatLabel(ctx, "s", "1", "1@c.us", true))
	must(t, s.SetChatLabel(ctx, "s", "1", "1@c.us", true))
	if ls, _ := s.ListLabels(ctx, "s"); len(ls) != 1 || ls[0].Name != "New" {
		t.Fatalf("labels: %+v", ls)
	}
	if ls, _ := s.ChatLabels(ctx, "s", "1@c.us"); len(ls) != 1 {
		t.Fatalf("chat labels: %+v", ls)
	}
	if ids, _ := s.LabelChats(ctx, "s", "1"); len(ids) != 1 || ids[0] != "1@c.us" {
		t.Fatalf("label chats: %+v", ids)
	}
	must(t, s.SetChatLabel(ctx, "s", "1", "1@c.us", false))
	if ids, _ := s.LabelChats(ctx, "s", "1"); len(ids) != 0 {
		t.Fatalf("label not removed: %+v", ids)
	}
}

func purge(t *testing.T, s store.Store) {
	ctx := context.Background()
	must(t, s.CreateSession(ctx, store.Session{Name: "p", Status: "STOPPED"}))
	must(t, s.CreateSession(ctx, store.Session{Name: "q", Status: "STOPPED"}))
	for _, name := range []string{"p", "q"} {
		must(t, s.UpsertMessages(ctx, []store.Message{{Session: name, ID: "m", WAID: "m", ChatID: "1@c.us", Body: json.RawMessage(`{}`)}}))
		must(t, s.UpsertChat(ctx, store.Chat{Session: name, ID: "1@c.us"}))
	}
	must(t, s.PurgeSession(ctx, "p"))
	if _, err := s.GetSession(ctx, "p"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purged session: %v", err)
	}
	if _, err := s.GetMessage(ctx, "p", "m"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purged message: %v", err)
	}
	if _, err := s.GetMessage(ctx, "q", "m"); err != nil {
		t.Fatalf("purge touched another session: %v", err)
	}
}

func outbound(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.PollSecret(ctx, "s", "P1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing secret: %v", err)
	}
	must(t, s.PutPollSecret(ctx, "s", "P1", []byte{1, 2, 3}))
	if b, err := s.PollSecret(ctx, "s", "P1"); err != nil || len(b) != 3 {
		t.Fatalf("secret: %v %v", b, err)
	}
	must(t, s.PutStatusRecipients(ctx, "s", "S1", []string{"1@s.whatsapp.net", "2@s.whatsapp.net"}))
	if r, err := s.StatusRecipients(ctx, "s", "S1"); err != nil || len(r) != 2 {
		t.Fatalf("recipients: %v %v", r, err)
	}
	must(t, s.CreateSession(ctx, store.Session{Name: "s", Status: "STOPPED"}))
	must(t, s.PurgeSession(ctx, "s"))
	if _, err := s.PollSecret(ctx, "s", "P1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purge kept the poll secret: %v", err)
	}
}
