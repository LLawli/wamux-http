package httpapi_test

import (
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/LLawli/wamux-http/internal/config"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/testenv"
	"github.com/LLawli/wamux-http/internal/webhooks"
)

func TestSessionLifecycle(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	hook := e.NewHook(nil)

	r := e.Do("POST", "/api/sessions", map[string]any{
		"name": "default", "start": true,
		"config": map[string]any{
			"metadata": map[string]string{"user.id": "42"},
			"webhooks": []map[string]any{{"url": hook.URL, "events": []string{"session.status", "message"}, "hmac": map[string]string{"key": "s3cret"}}},
		},
	})
	if r.Status != 201 {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	if got := r.Map(t)["name"]; got != "default" {
		t.Fatalf("create answered name %v", got)
	}

	e.Eventually("SCAN_QR_CODE", 5*time.Second, func() bool { return e.Status("default") == "SCAN_QR_CODE" })
	if qr := e.Do("GET", "/api/default/auth/qr?format=raw", nil); qr.Status != 200 || !strings.HasPrefix(qr.Map(t)["value"].(string), "2@fake-qr-") {
		t.Fatalf("raw QR: %d %s", qr.Status, qr.Body)
	}
	png := e.Do("GET", "/api/default/auth/qr", nil)
	if png.Status != 200 || png.Header.Get("Content-Type") != "image/png" || len(png.Body) < 100 {
		t.Fatalf("PNG QR: %d %s %d bytes", png.Status, png.Header.Get("Content-Type"), len(png.Body))
	}

	if err := e.Core.ScanQR("default", "5511999999999:7@s.whatsapp.net", "Ana"); err != nil {
		t.Fatal(err)
	}
	e.Eventually("WORKING", 5*time.Second, func() bool { return e.Status("default") == "WORKING" })

	me := e.Do("GET", "/api/sessions/default/me", nil).Map(t)
	if me["id"] != "5511999999999@c.us" || me["jid"] != "5511999999999:7@s.whatsapp.net" {
		t.Fatalf("me: %v", me)
	}
	info := e.Do("GET", "/api/sessions/default", nil).Map(t)
	if info["assignedWorker"] != "test-worker" || info["status"] != "WORKING" {
		t.Fatalf("info: %v", info)
	}

	// The QR is gone once paired: the QR route wants SCAN_QR_CODE.
	if r := e.Do("GET", "/api/default/auth/qr", nil); r.Status != 422 || r.Map(t)["status"] != "WORKING" {
		t.Fatalf("QR after pairing: %d %s", r.Status, r.Body)
	}

	// session.status went out for STARTING, SCAN_QR_CODE and WORKING, signed.
	e.Eventually("WORKING webhook", 5*time.Second, func() bool {
		for _, ev := range hook.Events("session.status") {
			if ev["payload"].(map[string]any)["status"] == "WORKING" {
				return true
			}
		}
		return false
	})
	var seen []string
	for _, req := range hook.Requests() {
		ev := req.Event()
		if ev["event"] != "session.status" {
			t.Fatalf("hook got an event it did not ask for: %v", ev["event"])
		}
		seen = append(seen, ev["payload"].(map[string]any)["status"].(string))
		if req.Header.Get("X-Webhook-Hmac") != webhooks.Sign("s3cret", req.Body) || req.Header.Get("X-Webhook-Hmac-Algorithm") != "sha512" {
			t.Fatalf("bad signature headers: %v", req.Header)
		}
		if ev["metadata"].(map[string]any)["user.id"] != "42" || ev["engine"] != "GOWS" || !strings.HasPrefix(ev["id"].(string), "evt_") {
			t.Fatalf("envelope: %v", ev)
		}
	}
	if strings.Join(seen, ",") != "STARTING,SCAN_QR_CODE,WORKING" {
		t.Fatalf("status sequence %v", seen)
	}
	last := hook.Events("session.status")[2]
	if pts := last["payload"].(map[string]any)["statuses"].([]any); len(pts) != 3 {
		t.Fatalf("statuses history: %v", pts)
	}

	// Stop keeps the pairing; start reconnects without a QR.
	if r := e.Do("POST", "/api/sessions/default/stop", nil); r.Status != 201 || r.Map(t)["status"] != "STOPPED" {
		t.Fatalf("stop: %d %s", r.Status, r.Body)
	}
	if acc, _ := e.Core.Account("default"); acc.State != pb.ConnectionState_DISCONNECTED {
		t.Fatalf("core still %v after stop", acc.State)
	}
	if list := e.Do("GET", "/api/sessions", nil); string(list.Body) != "[]\n" {
		t.Fatalf("a stopped session is listed only with all=true: %s", list.Body)
	}
	if list := e.Do("GET", "/api/sessions?all=true", nil); !strings.Contains(string(list.Body), `"STOPPED"`) {
		t.Fatalf("all=true: %s", list.Body)
	}
	e.Do("POST", "/api/sessions/default/start", nil)
	e.Eventually("WORKING again", 5*time.Second, func() bool { return e.Status("default") == "WORKING" })

	// Logout unpairs and, the session being started, comes back for a QR.
	if r := e.Do("POST", "/api/sessions/default/logout", nil); r.Status != 201 {
		t.Fatalf("logout: %d %s", r.Status, r.Body)
	}
	e.Eventually("SCAN_QR_CODE after logout", 5*time.Second, func() bool { return e.Status("default") == "SCAN_QR_CODE" })
	if acc, _ := e.Core.Account("default"); acc.Paired {
		t.Fatal("logout left the account paired")
	}

	if r := e.Do("DELETE", "/api/sessions/default", nil); r.Status != 200 || len(r.Body) != 0 {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	if _, ok := e.Core.Account("default"); ok {
		t.Fatal("delete left the account in the core")
	}
	if r := e.Do("GET", "/api/sessions/default", nil); r.Status != 404 || r.Map(t)["message"] != "Session not found" {
		t.Fatalf("get deleted: %d %s", r.Status, r.Body)
	}
	if r := e.Do("DELETE", "/api/sessions/default", nil); r.Status != 200 {
		t.Fatalf("delete is idempotent in WAHA: %d", r.Status)
	}
}

// Logout and delete drop the device from the phone's linked devices even
// when the session is stopped: the core logs out only a connected account
// (measured live), so the edge connects it for that.
func TestUnpairStoppedSession(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	one := e.WorkingSession("one", meJID, nil)
	two := e.WorkingSession("two", "5511999990002@s.whatsapp.net", nil)
	three := e.WorkingSession("three", "5511999990003@s.whatsapp.net", nil)
	for _, n := range []string{"one", "two"} {
		if r := e.Do("POST", "/api/sessions/"+n+"/stop", nil); r.Status != 201 {
			t.Fatalf("stop %s: %d %s", n, r.Status, r.Body)
		}
	}
	if r := e.Do("POST", "/api/sessions/one/logout", nil); r.Status != 201 || r.Map(t)["status"] != "STOPPED" {
		t.Fatalf("logout stopped: %d %s", r.Status, r.Body)
	}
	if acc, _ := e.Core.Account(one); acc.Paired || acc.State == pb.ConnectionState_CONNECTED {
		t.Fatalf("stopped session after logout: paired=%v state=%v", acc.Paired, acc.State)
	}
	for _, n := range []string{"two", "three"} {
		if r := e.Do("DELETE", "/api/sessions/"+n, nil); r.Status != 200 {
			t.Fatalf("delete %s: %d %s", n, r.Status, r.Body)
		}
	}
	if got := e.Core.Unpaired(); !slices.Equal(got, []string{one, two, three}) {
		t.Fatalf("unpaired %v, want one, two (stopped) and three (working)", got)
	}
	// Logging out again has nothing left to drop.
	if r := e.Do("POST", "/api/sessions/one/logout", nil); r.Status != 201 {
		t.Fatalf("second logout: %d %s", r.Status, r.Body)
	}
}

func TestLoggedOutByPhoneFails(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.WorkingSession("s1", "5511888888888@s.whatsapp.net", nil)
	e.Core.Disconnect("s1", pb.ConnectionState_LOGGED_OUT)
	e.Eventually("FAILED", 5*time.Second, func() bool { return e.Status("s1") == "FAILED" })
	time.Sleep(300 * time.Millisecond)
	if n := len(fakecore.Requests[*pb.ConnectAccountRequest](e.Core)); n != 1 {
		t.Fatalf("a logged-out session was reconnected (%d connects)", n)
	}
}

func TestDisconnectReconnects(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.WorkingSession("s1", "5511888888888@s.whatsapp.net", nil)
	e.Core.Disconnect("s1", pb.ConnectionState_DISCONNECTED)
	e.Eventually("reconnected", 5*time.Second, func() bool {
		return len(fakecore.Requests[*pb.ConnectAccountRequest](e.Core)) >= 2 && e.Status("s1") == "WORKING"
	})
}

func TestSessionValidation(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	if r := e.Do("POST", "/api/sessions", map[string]any{"name": "bad name!"}); r.Status != 400 {
		t.Fatalf("bad name: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/sessions", map[string]any{"config": map[string]any{"webhooks": []map[string]any{{"url": "ftp://x", "events": []string{"message"}}}}}); r.Status != 400 {
		t.Fatalf("bad webhook url: %d %s", r.Status, r.Body)
	}
	r := e.Do("POST", "/api/sessions", map[string]any{})
	name, _ := r.Map(t)["name"].(string)
	if r.Status != 201 || !strings.HasPrefix(name, "session_") || r.Map(t)["status"] != "STOPPED" {
		t.Fatalf("unnamed, unstarted create: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/sessions", map[string]any{"name": name}); r.Status != 422 || !strings.Contains(r.Map(t)["message"].(string), "already exists") {
		t.Fatalf("duplicate: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/sessions/nope/start", nil); r.Status != 404 {
		t.Fatalf("start unknown: %d", r.Status)
	}
	if r := e.Do("POST", "/api/sessions/nope/stop", nil); r.Status != 201 || len(r.Body) != 0 {
		t.Fatalf("stop unknown answers an empty 201 in WAHA: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/sessions/nope/me", nil); r.Status != 404 || !strings.Contains(r.Map(t)["message"].(string), "We didn't find a session") {
		t.Fatalf("me unknown: %d %s", r.Status, r.Body)
	}
}

func TestAuth(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	if r := e.DoKey("", "GET", "/ping", nil); r.Status != 200 || r.Map(t)["message"] != "pong" {
		t.Fatalf("ping: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey("", "GET", "/api/sessions", nil); r.Status != 401 || string(r.Body) != "{\"message\":\"Unauthorized\",\"statusCode\":401}\n" {
		t.Fatalf("no key: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey("wrong", "GET", "/api/sessions", nil); r.Status != 401 {
		t.Fatalf("wrong key: %d", r.Status)
	}
	if r := e.DoKey("", "GET", "/api/sessions?x-api-key="+testenv.AdminKey, nil); r.Status != 200 {
		t.Fatalf("query key: %d %s", r.Status, r.Body)
	}

	e.Do("POST", "/api/sessions", map[string]any{"name": "a"})
	e.Do("POST", "/api/sessions", map[string]any{"name": "b"})
	created := e.Do("POST", "/api/keys", map[string]any{"isAdmin": false, "session": "a", "isActive": true, "actions": map[string]bool{"read": true}})
	if created.Status != 201 {
		t.Fatalf("create key: %d %s", created.Status, created.Body)
	}
	key := created.Map(t)["key"].(string)
	id := created.Map(t)["id"].(string)
	if !strings.HasPrefix(key, "key_") || len(key) != 36 || !strings.HasPrefix(id, "key_id_") {
		t.Fatalf("key format: %s %s", key, id)
	}
	if r := e.DoKey(key, "GET", "/api/sessions/a", nil); r.Status != 200 {
		t.Fatalf("read own session: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey(key, "GET", "/api/sessions/b", nil); r.Status != 403 || string(r.Body) != "{\"message\":\"Forbidden\",\"statusCode\":403}\n" {
		t.Fatalf("read other session: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey(key, "POST", "/api/sessions/a/start", nil); r.Status != 403 {
		t.Fatalf("control without the action: %d", r.Status)
	}
	if r := e.DoKey(key, "GET", "/api/keys", nil); r.Status != 403 {
		t.Fatalf("session key listing keys: %d", r.Status)
	}
	if r := e.DoKey(key, "GET", "/api/sessions?all=true", nil); r.Status != 200 || strings.Contains(string(r.Body), `"b"`) {
		t.Fatalf("session key sees other sessions: %s", r.Body)
	}
	list := e.Do("GET", "/api/keys", nil)
	if strings.Contains(string(list.Body), key) || !strings.Contains(string(list.Body), "key_****"+key[len(key)-4:]) {
		t.Fatalf("listing must mask the key: %s", list.Body)
	}
	if r := e.Do("PUT", "/api/keys/"+id, map[string]any{"isAdmin": false, "session": "a", "isActive": false}); r.Status != 200 {
		t.Fatalf("deactivate: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey(key, "GET", "/api/sessions/a", nil); r.Status != 401 {
		t.Fatalf("inactive key: %d", r.Status)
	}
	if r := e.Do("POST", "/api/keys", map[string]any{"isAdmin": true, "session": "a"}); r.Status != 422 {
		t.Fatalf("admin key with a session: %d", r.Status)
	}
	if r := e.Do("DELETE", "/api/keys/"+id, nil); r.Status != 200 || r.Map(t)["result"] != true {
		t.Fatalf("delete key: %d %s", r.Status, r.Body)
	}
	// Body-session routes need the name, even for the admin.
	if r := e.Do("POST", "/api/keys/media", map[string]any{}); r.Status != 400 || r.Map(t)["message"] != "Session name is required" {
		t.Fatalf("missing body session: %d %s", r.Status, r.Body)
	}
}

func TestSha512AdminKey(t *testing.T) {
	sum := sha512.Sum512([]byte("plain-secret"))
	e := testenv.Start(t, testenv.Options{Config: func(c *config.Config) { c.APIKey = "sha512:" + hex.EncodeToString(sum[:]) }})
	if r := e.DoKey("plain-secret", "GET", "/api/sessions", nil); r.Status != 200 {
		t.Fatalf("sha512 key: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey("sha512:"+hex.EncodeToString(sum[:]), "GET", "/api/sessions", nil); r.Status != 401 {
		t.Fatalf("the hash itself must not authenticate: %d", r.Status)
	}
}

func TestWorkingRoutesWait(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	r := e.Do("GET", "/api/missing/auth/qr", nil)
	if r.Status != 422 || r.Map(t)["error"] != `Session "missing" does not exist` {
		t.Fatalf("missing session: %d %s", r.Status, r.Body)
	}
	e.Do("POST", "/api/sessions", map[string]any{"name": "idle"})
	r = e.Do("GET", "/api/idle/auth/qr", nil)
	m := r.Map(t)
	if r.Status != 422 || m["status"] != "STOPPED" || m["expected"].([]any)[0] != "SCAN_QR_CODE" {
		t.Fatalf("stopped session: %d %s", r.Status, r.Body)
	}
}

func TestUnimplementedAnswers501(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	r := e.Do("GET", "/api/server/debug/heapsnapshot", nil)
	if r.Status != 501 || !strings.Contains(r.Map(t)["message"].(string), "not implemented by 'GOWS' engine") {
		t.Fatalf("501: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey("", "GET", "/api/server/debug/heapsnapshot", nil); r.Status != 401 {
		t.Fatalf("a 501 route still needs a key: %d", r.Status)
	}
	if r := e.Do("GET", "/nowhere", nil); r.Status != 404 || r.Map(t)["message"] != "Cannot GET /nowhere" {
		t.Fatalf("unknown route: %d %s", r.Status, r.Body)
	}
}

func TestWebhookRetries(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	var mu sync.Mutex
	tries := map[string]int{}
	hook := e.NewHook(func(_ int, r *http.Request) int {
		mu.Lock()
		defer mu.Unlock()
		id := r.Header.Get("X-Webhook-Request-Id")
		tries[id]++
		if tries[id] <= 2 {
			return 500
		}
		return 200
	})
	e.Do("POST", "/api/sessions", map[string]any{"name": "r", "start": true, "config": map[string]any{
		"webhooks": []map[string]any{{"url": hook.URL, "events": []string{"session.status"}, "retries": map[string]any{"delaySeconds": 0.05, "attempts": 3, "policy": "constant"}}},
	}})
	// The first event's request fails twice, then succeeds: three requests
	// carrying the same request id.
	sameEvent := func() []testenv.HookRequest {
		reqs := hook.Requests()
		if len(reqs) == 0 {
			return nil
		}
		var out []testenv.HookRequest
		for _, r := range reqs {
			if r.Header.Get("X-Webhook-Request-Id") == reqs[0].Header.Get("X-Webhook-Request-Id") {
				out = append(out, r)
			}
		}
		return out
	}
	e.Eventually("redelivery", 5*time.Second, func() bool { return len(sameEvent()) >= 3 })
	reqs := sameEvent()
	if string(reqs[0].Body) != string(reqs[2].Body) {
		t.Fatal("a retry must resend the same body")
	}
	if reqs[0].Header.Get("User-Agent") != "WAHA/"+config.Version || reqs[0].Header.Get("X-Webhook-Timestamp") == "" {
		t.Fatalf("headers: %v", reqs[0].Header)
	}
}

// The reachout timelock the core reports lands in me, and a change while
// WORKING is a new WORKING whose data carries it while it is active.
func TestReachoutTimelock(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	hook := e.NewHook(nil)
	uuid := e.WorkingSession("default", "5511999999999@s.whatsapp.net",
		map[string]any{"webhooks": []map[string]any{{"url": hook.URL, "events": []string{"session.status"}}}})
	working := func() []map[string]any {
		var out []map[string]any
		for _, ev := range hook.Events("session.status") {
			if p, _ := ev["payload"].(map[string]any); p["status"] == "WORKING" {
				out = append(out, p)
			}
		}
		return out
	}
	e.Eventually("first WORKING", 5*time.Second, func() bool { return len(working()) == 1 })
	timelock := func(u *pb.ReachoutTimelockUpdate) {
		e.Core.Emit(uuid, &pb.EventEnvelope{Event: &pb.EventEnvelope_ReachoutTimelock{ReachoutTimelock: u}})
	}
	me := func() map[string]any {
		var m map[string]any
		e.Do("GET", "/api/sessions/default/me", nil).JSON(t, &m)
		tl, _ := m["reachoutTimelock"].(map[string]any)
		return tl
	}

	timelock(&pb.ReachoutTimelockUpdate{EnforcementType: proto.String("DEFAULT"), IsActive: proto.Bool(true), TimeEnforcementEnds: proto.String("1793100000000")})
	e.Eventually("restricted WORKING", 5*time.Second, func() bool { return len(working()) == 2 })
	data, _ := working()[1]["data"].(map[string]any)
	if tl, _ := data["reachoutTimelock"].(map[string]any); tl["isActive"] != true || tl["enforcementType"] != "DEFAULT" || tl["timeEnforcementEnds"] != float64(1793100000) {
		t.Fatalf("WORKING data: %v", working()[1])
	}
	if tl := me(); tl["isActive"] != true || tl["timeEnforcementEnds"] != float64(1793100000) {
		t.Fatalf("me: %v", tl)
	}

	// Lifted, with the other fields left out: they keep what was known.
	timelock(&pb.ReachoutTimelockUpdate{IsActive: proto.Bool(false)})
	e.Eventually("lifted WORKING", 5*time.Second, func() bool { return len(working()) == 3 })
	if working()[2]["data"] != nil {
		t.Fatalf("lifted WORKING carries data: %v", working()[2])
	}
	if tl := me(); tl["isActive"] != false || tl["enforcementType"] != "DEFAULT" || tl["timeEnforcementEnds"] != float64(1793100000) {
		t.Fatalf("me after lift: %v", tl)
	}

	// The same state again is not news.
	timelock(&pb.ReachoutTimelockUpdate{IsActive: proto.Bool(false)})
	time.Sleep(300 * time.Millisecond)
	if n := len(working()); n != 3 {
		t.Fatalf("a repeated state published a WORKING: %d", n)
	}
}
