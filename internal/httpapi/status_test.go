package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/testenv"
)

func withContacts(t *testing.T, e *testenv.Env, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := e.App.Store.UpsertContact(context.Background(), store.Contact{Session: "default", ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
}

func values(js []*pb.Jid) string {
	out := make([]string, len(js))
	for i, j := range js {
		out[i] = j.GetValue()
	}
	return strings.Join(out, ",")
}

func TestTextStatusAndDelete(t *testing.T) {
	e := working(t)
	withContacts(t, e, "5511111111111@c.us", "5511222222222@c.us", "123456789@lid", group)

	r := e.Do("POST", "/api/default/status/text", map[string]any{"text": "hello all", "backgroundColor": "#38b42f", "font": 2})
	if r.Status != 201 {
		t.Fatalf("text status: %d %s", r.Status, r.Body)
	}
	id := r.Map(t)["id"].(string)
	if !strings.HasPrefix(id, "true_status@broadcast_3EB0") || !strings.HasSuffix(id, "_"+meID) {
		t.Fatalf("status id: %s", id)
	}
	st := last[*pb.PostStatusTextRequest](t, e)
	if st.GetText() != "hello all" || st.GetBackgroundArgb() != 0xFF38B42F || st.GetFont() != 2 ||
		values(st.GetRecipients()) != "5511111111111@s.whatsapp.net,5511222222222@s.whatsapp.net" {
		t.Fatalf("every phone contact by default: %v", st)
	}

	e.Do("POST", "/api/default/status/text", map[string]any{"text": "just you", "contacts": []string{"5511333333333@c.us"}, "backgroundColor": "#11223380"})
	if st = last[*pb.PostStatusTextRequest](t, e); values(st.GetRecipients()) != "5511333333333@s.whatsapp.net" || st.GetBackgroundArgb() != 0x80112233 {
		t.Fatalf("explicit contacts: %v", st)
	}

	// Delete goes to the devices the status was posted to, not to today's
	// contacts.
	withContacts(t, e, "5511444444444@c.us")
	if r := e.Do("POST", "/api/default/status/delete", map[string]any{"id": id}); r.Status != 201 {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	rv := last[*pb.RevokeStatusRequest](t, e)
	if rv.GetMessageId() != id[len("true_status@broadcast_"):strings.LastIndex(id, "_")] ||
		values(rv.GetRecipients()) != "5511111111111@s.whatsapp.net,5511222222222@s.whatsapp.net" {
		t.Fatalf("revoke: %v", rv)
	}
	e.Do("POST", "/api/default/status/delete", map[string]any{"id": "UNKNOWNSTATUS", "contacts": []string{"5511555555555@c.us"}})
	if rv = last[*pb.RevokeStatusRequest](t, e); rv.GetMessageId() != "UNKNOWNSTATUS" || values(rv.GetRecipients()) != "5511555555555@s.whatsapp.net" {
		t.Fatalf("revoke without a stored list: %v", rv)
	}

	for name, body := range map[string]map[string]any{
		"no text":   {"backgroundColor": "#000000"},
		"bad color": {"text": "x", "backgroundColor": "green"},
	} {
		if r := e.Do("POST", "/api/default/status/text", body); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	if r := e.Do("POST", "/api/default/status/delete", map[string]any{}); r.Status != 400 {
		t.Fatalf("delete without id: %d", r.Status)
	}
}

func TestMediaStatus(t *testing.T) {
	e := working(t)
	withContacts(t, e, "5511111111111@c.us")
	var img bytes.Buffer
	_ = png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 640, 480)))
	r := e.Do("POST", "/api/default/status/image", map[string]any{"caption": "look",
		"file": map[string]any{"mimetype": "image/png", "data": base64.StdEncoding.EncodeToString(img.Bytes())}})
	if r.Status != 201 {
		t.Fatalf("image status: %d %s", r.Status, r.Body)
	}
	hdr := last[*pb.PostStatusMediaHeader](t, e)
	if hdr.GetMediaType() != pb.MediaType_MEDIA_TYPE_IMAGE || hdr.GetMimeType() != "image/png" || hdr.GetCaption() != "look" ||
		!bytes.HasPrefix(hdr.GetThumbnail(), []byte{0xFF, 0xD8}) || values(hdr.GetRecipients()) != "5511111111111@s.whatsapp.net" {
		t.Fatalf("image status header: %v", hdr)
	}
	if meta := last[*pb.MediaMeta](t, e); int(meta.GetFileLength()) != img.Len() {
		t.Fatalf("streamed %d of %d bytes", meta.GetFileLength(), img.Len())
	}
	if r := e.Do("POST", "/api/default/status/image", map[string]any{}); r.Status != 400 {
		t.Fatalf("no file: %d", r.Status)
	}

	needFFmpeg(t)
	src := filepath.Join(t.TempDir(), "in.avi")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-f", "lavfi",
		"-i", "testsrc=size=320x240:rate=10:duration=2", "-c:v", "mpeg4", src).CombinedOutput(); err != nil {
		t.Fatalf("make video: %v %s", err, out)
	}
	video, _ := os.ReadFile(src)
	r = e.Do("POST", "/api/default/status/video", map[string]any{"convert": true, "contacts": []string{peer},
		"file": map[string]any{"data": base64.StdEncoding.EncodeToString(video)}})
	if r.Status != 201 {
		t.Fatalf("video status: %d %s", r.Status, r.Body)
	}
	hdr = last[*pb.PostStatusMediaHeader](t, e)
	if hdr.GetMediaType() != pb.MediaType_MEDIA_TYPE_VIDEO || hdr.GetMimeType() != "video/mp4" || hdr.GetSeconds() != 2 ||
		!bytes.HasPrefix(hdr.GetThumbnail(), []byte{0xFF, 0xD8}) || values(hdr.GetRecipients()) != peerJ {
		t.Fatalf("video status header: %v", hdr)
	}
	if n := len(fakecore.Requests[*pb.PostStatusMediaHeader](e.Core)); n != 2 {
		t.Fatalf("%d media statuses", n)
	}
}
