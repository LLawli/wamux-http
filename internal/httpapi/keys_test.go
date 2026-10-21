package httpapi_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/LLawli/wamux-http/internal/testenv"
)

func sessionsOf(t *testing.T, m map[string]any) []string {
	t.Helper()
	raw, ok := m["sessions"].([]any)
	if !ok {
		t.Fatalf("sessions is not a list: %v", m)
	}
	out := []string{}
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

func TestKeyWithSeveralSessions(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	for _, name := range []string{"a", "b", "c"} {
		e.Do("POST", "/api/sessions", map[string]any{"name": name})
	}

	created := e.Do("POST", "/api/keys", map[string]any{"sessions": []string{"b", "a", "b"}, "isActive": true, "actions": map[string]bool{"read": true}})
	if created.Status != 201 {
		t.Fatalf("create: %d %s", created.Status, created.Body)
	}
	m := created.Map(t)
	if got := sessionsOf(t, m); !slices.Equal(got, []string{"a", "b"}) || m["session"] != nil {
		t.Fatalf("several sessions: session=%v sessions=%v", m["session"], got)
	}
	key, id := m["key"].(string), m["id"].(string)
	for name, want := range map[string]int{"a": 200, "b": 200, "c": 403} {
		if r := e.DoKey(key, "GET", "/api/sessions/"+name, nil); r.Status != want {
			t.Fatalf("session %s: %d, want %d", name, r.Status, want)
		}
	}
	if r := e.DoKey(key, "GET", "/api/sessions?all=true", nil); r.Status != 200 ||
		!strings.Contains(string(r.Body), `"a"`) || !strings.Contains(string(r.Body), `"b"`) || strings.Contains(string(r.Body), `"c"`) {
		t.Fatalf("listing: %s", r.Body)
	}

	// WAHA's single session still works, alone or together with sessions.
	one := e.Do("POST", "/api/keys", map[string]any{"session": "a", "isActive": true}).Map(t)
	if one["session"] != "a" || !slices.Equal(sessionsOf(t, one), []string{"a"}) {
		t.Fatalf("one session: %v", one)
	}
	both := e.Do("POST", "/api/keys", map[string]any{"session": "c", "sessions": []string{"a"}, "isActive": true}).Map(t)
	if !slices.Equal(sessionsOf(t, both), []string{"a", "c"}) {
		t.Fatalf("session plus sessions: %v", both)
	}
	admin := e.Do("POST", "/api/keys", map[string]any{"isAdmin": true, "isActive": true}).Map(t)
	if admin["session"] != nil || len(sessionsOf(t, admin)) != 0 {
		t.Fatalf("admin key: %v", admin)
	}

	for _, c := range []struct {
		body   map[string]any
		status int
		msg    string
	}{
		{map[string]any{"sessions": []string{"a", "zz"}}, 422, "Session 'zz' does not exist"},
		{map[string]any{"isAdmin": true, "sessions": []string{"a"}}, 422, "Admin API key cannot be assigned to a session"},
		{map[string]any{"sessions": []string{}}, 422, "Session is required for non-admin API key"},
		{map[string]any{"sessions": []string{"a", ""}}, 400, ""},
	} {
		r := e.Do("POST", "/api/keys", c.body)
		if r.Status != c.status || (c.msg != "" && r.Map(t)["message"] != c.msg) {
			t.Fatalf("%v: %d %s", c.body, r.Status, r.Body)
		}
	}

	// PUT replaces the set, as it replaces the whole key in WAHA.
	if r := e.Do("PUT", "/api/keys/"+id, map[string]any{"sessions": []string{"c"}, "isActive": true, "actions": map[string]bool{"read": true}}); r.Status != 200 {
		t.Fatalf("update: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey(key, "GET", "/api/sessions/c", nil); r.Status != 200 {
		t.Fatalf("new session: %d", r.Status)
	}
	if r := e.DoKey(key, "GET", "/api/sessions/a", nil); r.Status != 403 {
		t.Fatalf("dropped session: %d", r.Status)
	}
	list := e.Do("GET", "/api/keys", nil)
	if !strings.Contains(string(list.Body), `"sessions":["c"]`) {
		t.Fatalf("listing: %s", list.Body)
	}
}

func TestWebSocketKeyWithSeveralSessions(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	for _, name := range []string{"a", "b", "c"} {
		e.Do("POST", "/api/sessions", map[string]any{"name": name})
	}
	key := e.Do("POST", "/api/keys", map[string]any{"sessions": []string{"a", "b"}, "isActive": true}).Map(t)["key"].(string)
	conn := dialWS(t, e, "x-api-key="+key)
	time.Sleep(100 * time.Millisecond)
	for _, name := range []string{"c", "b", "a"} {
		e.Do("POST", "/api/sessions/"+name+"/start", nil)
	}
	seen := map[string]bool{}
	for !seen["a"] || !seen["b"] {
		s := readEvent(t, conn)["session"].(string)
		if s == "c" {
			t.Fatal("a key for a and b saw an event of c")
		}
		seen[s] = true
	}

	// Without read on any of its sessions, every session is refused.
	blind := e.Do("POST", "/api/keys", map[string]any{"sessions": []string{"a", "b"}, "isActive": true, "actions": map[string]bool{"send": true}}).Map(t)["key"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := dialWS(t, e, "x-api-key="+blind).Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Reason != "Forbidden" {
		t.Fatalf("key without read: %v", err)
	}
}
