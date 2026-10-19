package httpapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LLawli/wamux-http/internal/config"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/testenv"
)

// A media message the ingest side receives carries a media.url that the
// files route serves: the two halves name the file the same way.
func TestReceivedMediaURLIsServed(t *testing.T) {
	mf := fakecore.NewMediaFiles()
	e := testenv.Start(t, testenv.Options{Core: fakecore.Options{Media: mf}})
	hook := e.NewHook(nil)
	uuid := e.WorkingSession("default", meJID, map[string]any{
		"webhooks": []map[string]any{{"url": hook.URL, "events": []string{"message"}}},
	})
	clip := []byte("not really a quicktime movie, but bytes all the same")
	mf.Put("/v/clip", clip)
	e.Core.Emit(uuid, &pb.EventEnvelope{Event: &pb.EventEnvelope_Message{Message: &pb.InboundMessage{
		Key:       &pb.MessageKey{Chat: &pb.Jid{Value: "5511888888888@s.whatsapp.net"}, Id: "3EB0CLIP"},
		Chat:      &pb.Jid{Value: "5511888888888@s.whatsapp.net"},
		Sender:    &pb.Jid{Value: "5511888888888@s.whatsapp.net"},
		Timestamp: time.Now().UnixMilli(),
		Media: &pb.MediaDescriptor{
			DirectPath: "/v/clip", MimeType: "video/quicktime", MediaType: pb.MediaType_MEDIA_TYPE_VIDEO,
			FileLength: uint64(len(clip)),
		},
	}}})
	e.Eventually("message webhook", 5*time.Second, func() bool { return len(hook.Events("message")) > 0 })
	media := hook.Events("message")[0]["payload"].(map[string]any)["media"].(map[string]any)
	u, _ := media["url"].(string)
	if !strings.HasPrefix(u, e.URL+"/api/files/default/3EB0CLIP.") {
		t.Fatalf("media.url = %q", u)
	}
	r := e.Do("GET", strings.TrimPrefix(u, e.URL), nil)
	if r.Status != 200 || string(r.Body) != string(clip) || r.Header.Get("Content-Type") != "video/quicktime" {
		t.Fatalf("GET %s: %d %q %s", u, r.Status, r.Header.Get("Content-Type"), r.Body)
	}
}

// A group picture given by URL is fetched through the media layer.
func TestGroupPictureFromURL(t *testing.T) {
	t.Setenv("WAMUX_HTTP_MEDIA_ALLOW_PRIVATE", "true")
	gid := "120363000000000009@g.us"
	groups := fakecore.NewGroups(&pb.GroupMetadata{Id: &pb.Jid{Value: gid}})
	e := testenv.Start(t, testenv.Options{Core: fakecore.Options{Groups: groups}})
	e.WorkingSession("default", meJID, nil)
	img := []byte("\xff\xd8\xff\xe0 a jpeg, as far as the sniffer cares")
	src := e.NewFileServer(img, "image/jpeg")
	if r := e.Do("PUT", "/api/default/groups/"+gid+"/picture", map[string]any{"file": map[string]string{"url": src}}); r.Status != 200 {
		t.Fatalf("url picture: %d %s", r.Status, r.Body)
	}
	if string(groups.Photo(gid)) != string(img) {
		t.Fatalf("core got %q", groups.Photo(gid))
	}
}

// Strict mode refuses unknown fields, never the session a body route is
// named by.
func TestStrictModeKeepsBodySession(t *testing.T) {
	e := testenv.Start(t, testenv.Options{Config: func(c *config.Config) { c.HTTPStrictMode = true }})
	e.WorkingSession("default", meJID, nil)
	if r := e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": "5511888888888@c.us", "text": "hi"}); r.Status != 201 {
		t.Fatalf("sendText in strict mode: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": "5511888888888@c.us", "text": "hi", "bogus": 1}); r.Status != 400 {
		t.Fatalf("an unknown field in strict mode: %d %s", r.Status, r.Body)
	}
}

// A webhook URL that never answers ties up only its own share of the
// delivery pool: another session's webhooks still go out at once.
func TestHangingWebhookDoesNotStarveOthers(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	hang := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-hang }))
	t.Cleanup(func() { close(hang); slow.Close() })
	fast := e.NewHook(nil)
	for i := range 6 {
		e.Do("POST", "/api/sessions", map[string]any{"name": fmt.Sprintf("hang%d", i), "start": true, "config": map[string]any{
			"webhooks": []map[string]any{{"url": slow.URL, "events": []string{"session.status"}}},
		}})
	}
	time.Sleep(200 * time.Millisecond) // the hanging deliveries are in flight
	start := time.Now()
	e.Do("POST", "/api/sessions", map[string]any{"name": "ok", "start": true, "config": map[string]any{
		"webhooks": []map[string]any{{"url": fast.URL, "events": []string{"session.status"}}},
	}})
	e.Eventually("the other session's webhook", 2*time.Second, func() bool { return len(fast.Requests()) > 0 })
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("delivered after %v", d)
	}
}

// sendSeen without ids reads the chat once: the messages it read are stored
// as READ, so a second call has nothing left to acknowledge.
func TestSendSeenDoesNotReacknowledge(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	uuid := e.WorkingSession("default", meJID, nil)
	e.Core.Emit(uuid, inbound("UNREAD1", "hello"))
	e.Eventually("stored", 5*time.Second, func() bool {
		r := e.Do("GET", "/api/default/chats/5511888888888@c.us/messages?limit=5", nil)
		return strings.Contains(string(r.Body), "UNREAD1")
	})
	seen := func() []any {
		r := e.Do("POST", "/api/sendSeen", map[string]any{"session": "default", "chatId": "5511888888888@c.us"})
		if r.Status != 201 {
			t.Fatalf("sendSeen: %d %s", r.Status, r.Body)
		}
		return r.Map(t)["ids"].([]any)
	}
	if ids := seen(); len(ids) != 1 {
		t.Fatalf("first sendSeen read %v", ids)
	}
	receipts := len(fakecore.Requests[*pb.MarkReadRequest](e.Core))
	if ids := seen(); len(ids) != 0 {
		t.Fatalf("second sendSeen read again: %v", ids)
	}
	var withIDs int
	for _, r := range fakecore.Requests[*pb.MarkReadRequest](e.Core)[receipts:] {
		withIDs += len(r.GetMessageIds())
	}
	if withIDs != 0 {
		t.Fatalf("the second sendSeen sent %d receipts", withIDs)
	}
}

// Deleting a session still delivers its last session.status.
func TestDeleteDeliversTheLastStatus(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	hook := e.NewHook(nil)
	e.WorkingSession("default", meJID, map[string]any{
		"webhooks": []map[string]any{{"url": hook.URL, "events": []string{"session.status"}}},
	})
	if r := e.Do("DELETE", "/api/sessions/default", nil); r.Status != 200 {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	e.Eventually("STOPPED webhook", 5*time.Second, func() bool {
		evs := hook.Events("session.status")
		return len(evs) > 0 && evs[len(evs)-1]["payload"].(map[string]any)["status"] == "STOPPED"
	})
}

// An excluded path opens that path only, not the routes under it.
func TestExcludedPathIsExact(t *testing.T) {
	e := testenv.Start(t, testenv.Options{Config: func(c *config.Config) { c.APIKeyExcludeDir = []string{"/api/sessions"} }})
	e.Do("POST", "/api/sessions", map[string]any{"name": "s1"})
	if r := e.DoKey("", "GET", "/api/sessions?all=true", nil); r.Status != 200 {
		t.Fatalf("excluded path: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey("", "DELETE", "/api/sessions/s1", nil); r.Status != 401 {
		t.Fatalf("a route under the excluded path opened without a key: %d", r.Status)
	}
}
