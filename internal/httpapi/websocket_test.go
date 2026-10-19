package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/LLawli/wamux-http/internal/testenv"
)

func dialWS(t *testing.T, e *testenv.Env, query string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.URL, "http")+"/ws?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func readEvent(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWebSocketEvents(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	all := dialWS(t, e, "x-api-key="+testenv.AdminKey)
	onlyB := dialWS(t, e, "X-Api-Key="+testenv.AdminKey+"&session=b&events=session.status")
	time.Sleep(100 * time.Millisecond) // let both subscriptions attach

	e.Do("POST", "/api/sessions", map[string]any{"name": "a", "start": true})
	ev := readEvent(t, all)
	if ev["event"] != "session.status" || ev["session"] != "a" || !strings.HasPrefix(ev["id"].(string), "evt_") {
		t.Fatalf("all sessions: %v", ev)
	}
	e.Do("POST", "/api/sessions", map[string]any{"name": "b", "start": true})
	ev = readEvent(t, onlyB)
	if ev["session"] != "b" {
		t.Fatalf("filtered client got session %v", ev["session"])
	}
}

func TestWebSocketAuth(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	conn := dialWS(t, e, "x-api-key=wrong")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusPolicyViolation || ce.Reason != "Unauthorized" {
		t.Fatalf("wrong key: %v", err)
	}

	e.Do("POST", "/api/sessions", map[string]any{"name": "a"})
	e.Do("POST", "/api/sessions", map[string]any{"name": "b"})
	key := e.Do("POST", "/api/keys", map[string]any{"session": "a", "isActive": true}).Map(t)["key"].(string)
	other := dialWS(t, e, "x-api-key="+key+"&session=b")
	_, _, err = other.Read(ctx)
	if !errors.As(err, &ce) || ce.Reason != "Forbidden" {
		t.Fatalf("other session: %v", err)
	}
	// A session key asking for every session gets its own only.
	own := dialWS(t, e, "x-api-key="+key)
	time.Sleep(100 * time.Millisecond)
	e.Do("POST", "/api/sessions/b/start", nil)
	e.Do("POST", "/api/sessions/a/start", nil)
	if ev := readEvent(t, own); ev["session"] != "a" {
		t.Fatalf("session key saw %v", ev["session"])
	}
}
