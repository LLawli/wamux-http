package httpapi_test

import (
	"strings"
	"testing"
	"time"

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
