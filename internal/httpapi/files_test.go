package httpapi_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLawli/wamux-http/internal/config"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/testenv"
	"github.com/LLawli/wamux-http/internal/waha"
)

func jsonBody(v any) ([]byte, error) { return json.Marshal(v) }

func filesEnv(t *testing.T, lifetime time.Duration) (*testenv.Env, *fakecore.MediaFiles) {
	t.Helper()
	mf := fakecore.NewMediaFiles()
	e := testenv.Start(t, testenv.Options{
		Core:   fakecore.Options{Media: mf},
		Config: func(c *config.Config) { c.FilesLifetime = lifetime },
	})
	e.WorkingSession("default", meJID, nil)
	return e, mf
}

func TestServeReceivedMedia(t *testing.T) {
	e, mf := filesEnv(t, 0)
	photo := bytes.Repeat([]byte{0xFF, 0xD8, 0x42}, 20000) // several download chunks
	mf.Put("/v/photo", photo)
	putMessage(t, e, waha.MessageID{Chat: peer, ID: "3EB0PHOTO"}, &pb.InboundMessage{
		Media: &pb.MediaDescriptor{DirectPath: "/v/photo", MimeType: "image/jpeg", MediaType: pb.MediaType_MEDIA_TYPE_IMAGE},
	}, 2, time.Now().UnixMilli())

	for range 2 {
		r := e.Do("GET", "/api/files/default/3EB0PHOTO.jpeg", nil)
		if r.Status != 200 || r.Header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(r.Body, photo) {
			t.Fatalf("file: %d %v %d bytes", r.Status, r.Header.Get("Content-Type"), len(r.Body))
		}
	}
	if n := len(mf.Requests()); n != 1 {
		t.Fatalf("downloaded %d times; the second request is served from the cache", n)
	}
	if req := mf.Requests()[0]; req.GetDescriptor_().GetDirectPath() != "/v/photo" || req.GetAccount().GetUuid() == "" {
		t.Fatalf("download request: %v", req)
	}
	cached, err := os.ReadFile(filepath.Join(e.App.Config.FilesFolder, "default", "3EB0PHOTO.jpeg"))
	if err != nil || !bytes.Equal(cached, photo) {
		t.Fatalf("cache: %v", err)
	}

	for _, path := range []string{
		"/api/files/default/3EB0PHOTO.png",    // the message's media is a JPEG
		"/api/files/default/3EB0MISSING.jpeg", // no such message
		"/api/files/default/..%2Fedge.db",
		"/api/files/other/3EB0PHOTO.jpeg",
	} {
		r := e.Do("GET", path, nil)
		if r.Status != 404 || r.Map(t)["error"].(map[string]any)["key"] != "FILE_NOT_FOUND" {
			t.Errorf("%s: %d %s", path, r.Status, r.Body)
		}
	}

	// The media key (read on the session) works in the query, as mediaUrl
	// links carry it; another session's key does not.
	key := e.Do("POST", "/api/keys/media", map[string]any{"session": "default"}).Map(t)["key"].(string)
	if r := e.DoKey("", "GET", "/api/files/default/3EB0PHOTO.jpeg?x-api-key="+key, nil); r.Status != 200 {
		t.Fatalf("media key: %d", r.Status)
	}
	e.Do("POST", "/api/sessions", map[string]any{"name": "other"})
	other := e.Do("POST", "/api/keys/media", map[string]any{"session": "other"}).Map(t)["key"].(string)
	if r := e.DoKey(other, "GET", "/api/files/default/3EB0PHOTO.jpeg", nil); r.Status != 403 {
		t.Fatalf("another session's key: %d", r.Status)
	}
}

func TestServedMediaExpires(t *testing.T) {
	e, mf := filesEnv(t, 300*time.Millisecond)
	mf.Put("/v/voice", []byte("OggS fake voice"))
	putMessage(t, e, waha.MessageID{Chat: peer, ID: "3EB0VOICE"}, &pb.InboundMessage{
		Media: &pb.MediaDescriptor{DirectPath: "/v/voice", MimeType: "audio/ogg; codecs=opus", MediaType: pb.MediaType_MEDIA_TYPE_AUDIO},
	}, 2, time.Now().UnixMilli())
	if r := e.Do("GET", "/api/files/default/3EB0VOICE.oga", nil); r.Status != 200 || r.Header.Get("Content-Type") != "audio/ogg; codecs=opus" {
		t.Fatalf("voice: %d %v", r.Status, r.Header)
	}
	path := filepath.Join(e.App.Config.FilesFolder, "default", "3EB0VOICE.oga")
	e.Eventually("the cached file expires", 3*time.Second, func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	})
	if r := e.Do("GET", "/api/files/default/3EB0VOICE.oga", nil); r.Status != 200 {
		t.Fatalf("after expiry: %d", r.Status)
	}
	if n := len(mf.Requests()); n != 2 {
		t.Fatalf("an expired file is downloaded again: %d downloads", n)
	}
}
