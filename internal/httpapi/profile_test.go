package httpapi_test

import (
	"encoding/base64"
	"testing"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
)

func TestProfile(t *testing.T) {
	contacts := fakecore.NewContacts("Ana Core")
	e := directoryEnv(t, fakecore.Options{Contacts: contacts})

	// No picture yet: null, which MyProfile allows.
	if r := e.Do("GET", "/api/default/profile", nil); r.Status != 200 || string(r.Body) != `{"id":"5511999999999@c.us","name":"Ana Core","picture":null}`+"\n" {
		t.Fatalf("profile: %d %s", r.Status, r.Body)
	}
	pics := fakecore.Logged[*pb.JidRequest](&contacts.RequestLog)
	if len(pics) != 1 || pics[0].GetJid().GetValue() != "5511999999999@s.whatsapp.net" {
		t.Fatalf("own picture asked by phone jid, no device: %v", pics)
	}

	if r := e.Do("PUT", "/api/default/profile/name", map[string]string{"name": "Ana Nova"}); r.Status != 200 || string(r.Body) != `{"success":true}`+"\n" {
		t.Fatalf("set name: %d %s", r.Status, r.Body)
	}
	if contacts.PushName() != "Ana Nova" {
		t.Fatalf("core push name %q", contacts.PushName())
	}
	if r := e.Do("GET", "/api/default/profile", nil); r.Map(t)["name"] != "Ana Nova" {
		t.Fatalf("name read back: %s", r.Body)
	}
	if r := e.Do("PUT", "/api/default/profile/name", map[string]any{}); r.Status != 400 {
		t.Fatalf("no name: %d %s", r.Status, r.Body)
	} else if msgs := r.Map(t)["message"].([]any); len(msgs) != 2 || msgs[0] != "name should not be empty" || msgs[1] != "name must be a string" {
		t.Fatalf("validation messages: %v", msgs)
	}

	img := []byte("\x89PNG fake")
	if r := e.Do("PUT", "/api/default/profile/picture", map[string]any{"file": map[string]string{"mimetype": "image/png", "data": base64.StdEncoding.EncodeToString(img)}}); r.Status != 200 || string(r.Body) != `{"success":true}`+"\n" {
		t.Fatalf("set picture: %d %s", r.Status, r.Body)
	}
	if string(contacts.Picture()) != string(img) {
		t.Fatalf("core picture %q", contacts.Picture())
	}
	// A URL goes through the media layer's SSRF guard: a local address is refused.
	if r := e.Do("PUT", "/api/default/profile/picture", map[string]any{"file": map[string]string{"url": e.URL + "/ping"}}); r.Status != 422 {
		t.Fatalf("local url picture: %d %s", r.Status, r.Body)
	}
	if r := e.Do("PUT", "/api/default/profile/picture", map[string]any{}); r.Status != 400 {
		t.Fatalf("no file: %d %s", r.Status, r.Body)
	}
	if r := e.Do("DELETE", "/api/default/profile/picture", nil); r.Status != 200 || string(r.Body) != `{"success":true}`+"\n" || contacts.Picture() != nil {
		t.Fatalf("delete picture: %d %s", r.Status, r.Body)
	}
}

func TestProfilePicture(t *testing.T) {
	contacts := fakecore.NewContacts("Ana")
	contacts.Pictures["5511999999999@s.whatsapp.net"] = "https://pps.whatsapp.net/me.jpg"
	e := directoryEnv(t, fakecore.Options{Contacts: contacts})
	if r := e.Do("GET", "/api/default/profile", nil); r.Status != 200 || r.Map(t)["picture"] != "https://pps.whatsapp.net/me.jpg" {
		t.Fatalf("profile: %d %s", r.Status, r.Body)
	}
}
