package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/testenv"
	"github.com/LLawli/wamux-http/internal/waha"
)

const (
	peer  = "5511888888888@c.us"
	peerJ = "5511888888888@s.whatsapp.net"
	group = "120363000000000001@g.us"
)

func working(t *testing.T) *testenv.Env {
	t.Helper()
	e := testenv.Start(t, testenv.Options{})
	e.WorkingSession("default", meJID, nil)
	return e
}

// putMessage stores a message as the ingest side would.
func putMessage(t *testing.T, e *testenv.Env, id waha.MessageID, in *pb.InboundMessage, ack int, ts int64) {
	t.Helper()
	if in.Key == nil {
		key, err := id.Key()
		if err != nil {
			t.Fatal(err)
		}
		in.Key = key
		in.Chat = key.GetChat()
	}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.App.Store.UpsertMessages(context.Background(), []store.Message{{
		Session: "default", ID: id.String(), WAID: id.ID, ChatID: id.Chat, Timestamp: ts,
		FromMe: id.FromMe, Ack: ack, Body: []byte(`{}`), Raw: raw,
	}}); err != nil {
		t.Fatal(err)
	}
}

func last[T proto.Message](t *testing.T, e *testenv.Env) T {
	t.Helper()
	all := fakecore.Requests[T](e.Core)
	if len(all) == 0 {
		var zero T
		t.Fatalf("the core received no %T", zero)
	}
	return all[len(all)-1]
}

func TestSendText(t *testing.T) {
	e := working(t)
	r := e.Do("POST", "/api/sendText", map[string]any{
		"session": "default", "chatId": peer, "text": "hello", "mentions": []string{"5511777777777@c.us"},
	})
	if r.Status != 201 {
		t.Fatalf("sendText: %d %s", r.Status, r.Body)
	}
	got := r.Map(t)
	req := last[*pb.SendTextRequest](t, e)
	if got["id"] != "true_"+peer+"_"+lastKeyID(t, got) || !strings.HasPrefix(got["id"].(string), "true_"+peer+"_3EB0") {
		t.Fatalf("id: %v", got["id"])
	}
	if data, ok := got["_data"].(map[string]any); !ok || data["key"] == nil {
		t.Fatalf("_data: %v", got["_data"])
	}
	if req.GetTo().GetValue() != peerJ || req.GetText() != "hello" || len(req.GetMentions()) != 1 ||
		req.GetMentions()[0].GetJid().GetValue() != "5511777777777@s.whatsapp.net" || req.GetQuote() != nil || req.GetLinkPreview() != nil {
		t.Fatalf("core got %v", req)
	}

	// A bare number is a person; a group answer names me as participant.
	r = e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": group, "text": "all"})
	if id := r.Map(t)["id"].(string); !strings.HasPrefix(id, "true_"+group+"_") || !strings.HasSuffix(id, "_"+meID) {
		t.Fatalf("group id: %s", id)
	}
	e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": "+55 11 88888-8888", "text": "x"})
	if to := last[*pb.SendTextRequest](t, e).GetTo().GetValue(); to != peerJ {
		t.Fatalf("bare number went to %s", to)
	}

	for name, body := range map[string]map[string]any{
		"no chatId":  {"session": "default", "text": "x"},
		"no text":    {"session": "default", "chatId": peer},
		"bad chatId": {"session": "default", "chatId": "@", "text": "x"},
	} {
		if r := e.Do("POST", "/api/sendText", body); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	if r := e.Do("POST", "/api/sendText", map[string]any{"chatId": peer, "text": "x"}); r.Status != 400 {
		t.Fatalf("no session: %d", r.Status)
	}
	if r := e.Do("POST", "/api/sendText", map[string]any{"session": "nope", "chatId": peer, "text": "x"}); r.Status != 422 {
		t.Fatalf("unknown session: %d %s", r.Status, r.Body)
	}

	// The deprecated forms.
	if r := e.Do("GET", "/api/sendText?session=default&phone=5511888888888&text=via+get", nil); r.Status != 200 {
		t.Fatalf("GET sendText: %d %s", r.Status, r.Body)
	}
	if req := last[*pb.SendTextRequest](t, e); req.GetText() != "via get" || req.GetTo().GetValue() != peerJ {
		t.Fatalf("GET sendText sent %v", req)
	}
	if r := e.Do("POST", "/api/reply", map[string]any{"session": "default", "chatId": peer, "text": "re", "reply_to": "ABCDEF"}); r.Status != 201 {
		t.Fatalf("reply: %d %s", r.Status, r.Body)
	}
	q := last[*pb.SendTextRequest](t, e).GetQuote()
	if q.GetQuoted().GetId() != "ABCDEF" || q.GetQuoted().GetChat().GetValue() != peerJ || q.GetParticipant().GetValue() != peerJ {
		t.Fatalf("an unknown quoted DM message is the peer's: %v", q)
	}
}

func lastKeyID(t *testing.T, got map[string]any) string {
	t.Helper()
	key := got["_data"].(map[string]any)["key"].(map[string]any)
	return key["id"].(string)
}

func TestReplyToStoredMessage(t *testing.T) {
	e := working(t)
	quoted := waha.MessageID{Chat: group, ID: "3EB0QUOTED", Participant: "5511777777777@c.us"}
	putMessage(t, e, quoted, &pb.InboundMessage{Sender: &pb.Jid{Value: "5511777777777:3@s.whatsapp.net"}, Text: "original"}, 2, time.Now().UnixMilli())
	mine := waha.MessageID{FromMe: true, Chat: peer, ID: "3EB0MINE"}
	putMessage(t, e, mine, &pb.InboundMessage{Text: "mine"}, 1, time.Now().UnixMilli())

	// The serialized id is cut to its raw id, then completed from the store.
	e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": group, "text": "re", "reply_to": quoted.String()})
	q := last[*pb.SendTextRequest](t, e).GetQuote()
	if q.GetQuoted().GetId() != "3EB0QUOTED" || q.GetQuoted().GetFromMe() || q.GetQuoted().GetChat().GetValue() != group ||
		q.GetQuoted().GetParticipant().GetValue() != "5511777777777@s.whatsapp.net" || q.GetParticipant().GetValue() != "5511777777777@s.whatsapp.net" {
		t.Fatalf("group quote: %v", q)
	}
	e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": "re", "reply_to": "3EB0MINE"})
	q = last[*pb.SendTextRequest](t, e).GetQuote()
	if !q.GetQuoted().GetFromMe() || q.GetParticipant().GetValue() != "5511999999999@s.whatsapp.net" {
		t.Fatalf("quote of my own message: %v", q)
	}
	if r := e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": "re", "reply_to": "bad_id"}); r.Status != 400 {
		t.Fatalf("malformed reply_to: %d %s", r.Status, r.Body)
	}
}

// A reply_to whose message is in another chat quotes it there: the core
// sends a private reply to a group message, or a reply to a status.
func TestReplyToAnotherChat(t *testing.T) {
	e := working(t)
	member := "5511777777777@s.whatsapp.net"
	inGroup := waha.MessageID{Chat: group, ID: "3EB0INGROUP", Participant: "5511777777777@c.us"}
	putMessage(t, e, inGroup, &pb.InboundMessage{Sender: &pb.Jid{Value: "5511777777777:3@s.whatsapp.net"}, Text: "in the group"}, 2, time.Now().UnixMilli())

	// Stored: the bare id is enough, the store knows the chat.
	e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": "5511777777777@c.us", "text": "privately", "reply_to": "3EB0INGROUP"})
	q := last[*pb.SendTextRequest](t, e).GetQuote()
	if q.GetQuoted().GetChat().GetValue() != group || q.GetQuoted().GetParticipant().GetValue() != member || q.GetParticipant().GetValue() != member {
		t.Fatalf("private reply to a stored group message: %v", q)
	}

	// Not stored: a full WAHA id names the chat and the author.
	e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": "nice",
		"reply_to": "false_status@broadcast_3EB0STATUS_5511888888888@c.us"})
	q = last[*pb.SendTextRequest](t, e).GetQuote()
	if q.GetQuoted().GetChat().GetValue() != "status@broadcast" || q.GetQuoted().GetId() != "3EB0STATUS" ||
		q.GetQuoted().GetParticipant().GetValue() != peerJ || q.GetParticipant().GetValue() != peerJ {
		t.Fatalf("reply to a status: %v", q)
	}
	e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": "privately",
		"reply_to": "false_" + group + "_3EB0NOTSTORED_5511777777777@c.us"})
	q = last[*pb.SendTextRequest](t, e).GetQuote()
	if q.GetQuoted().GetChat().GetValue() != group || q.GetQuoted().GetParticipant().GetValue() != member {
		t.Fatalf("private reply to a group message not stored: %v", q)
	}

	// The request's chat stays the quoted one otherwise.
	e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": "re", "reply_to": "3EB0UNKNOWN"})
	if q := last[*pb.SendTextRequest](t, e).GetQuote(); q.GetQuoted().GetChat().GetValue() != peerJ || q.GetQuoted().GetParticipant() != nil {
		t.Fatalf("same-chat reply: %v", q)
	}

	// A DM keyed by the peer's LID is the same conversation as its phone
	// chat: replying there is no reply from another chat (measured live: the
	// quote went out with the LID as remoteJid).
	peerLID := "123456789012345@lid"
	inLID := waha.MessageID{Chat: peerLID, ID: "3EB0INLID"}
	putMessage(t, e, inLID, &pb.InboundMessage{Sender: &pb.Jid{Value: "123456789012345:2@lid"}, Text: "by lid"}, 2, time.Now().UnixMilli())
	for _, replyTo := range []string{"3EB0INLID", "false_" + peerLID + "_3EB0LIDNOTSTORED"} {
		e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": "re", "reply_to": replyTo})
		if q := last[*pb.SendTextRequest](t, e).GetQuote(); q.GetQuoted().GetChat().GetValue() != peerJ {
			t.Fatalf("reply to %s in the peer's LID chat: %v", replyTo, q)
		}
	}
}

func TestSendTextLinkPreview(t *testing.T) {
	t.Setenv("WAMUX_HTTP_MEDIA_ALLOW_PRIVATE", "true")
	var card bytes.Buffer
	_ = png.Encode(&card, image.NewRGBA(image.Rect(0, 0, 400, 200)))
	mux := http.NewServeMux()
	mux.HandleFunc("/article", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<head><meta property="og:title" content="Article"><meta property="og:description" content="About it"><meta property="og:image" content="/card.png"></head>`))
	})
	mux.HandleFunc("/card.png", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(card.Bytes()) })
	site := httptest.NewServer(mux)
	defer site.Close()
	e := working(t)

	text := "read this: " + site.URL + "/article"
	if r := e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": text}); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	lp := last[*pb.SendTextRequest](t, e).GetLinkPreview()
	if lp.GetTitle() != "Article" || lp.GetDescription() != "About it" || lp.GetMatchedText() != site.URL+"/article" || len(lp.GetJpegThumbnail()) < 100 {
		t.Fatalf("preview: %v", lp)
	}
	e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": text, "linkPreview": false})
	if lp := last[*pb.SendTextRequest](t, e).GetLinkPreview(); lp != nil {
		t.Fatalf("linkPreview false still previewed: %v", lp)
	}
	// A page that cannot be fetched sends the text without a preview.
	e.Do("POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": site.URL + "/missing"})
	if lp := last[*pb.SendTextRequest](t, e).GetLinkPreview(); lp != nil {
		t.Fatalf("a 404 page previewed: %v", lp)
	}

	// The deprecated sendLinkPreview: the URL is the text, the title wins.
	if r := e.Do("POST", "/api/sendLinkPreview", map[string]any{"session": "default", "chatId": peer, "url": site.URL + "/article", "title": "Mine"}); r.Status != 201 {
		t.Fatalf("sendLinkPreview: %d %s", r.Status, r.Body)
	}
	req := last[*pb.SendTextRequest](t, e)
	if req.GetText() != site.URL+"/article" || req.GetLinkPreview().GetTitle() != "Mine" || req.GetLinkPreview().GetDescription() != "About it" {
		t.Fatalf("sendLinkPreview sent %v", req)
	}

	// A custom preview with the caller's image.
	body := map[string]any{"session": "default", "chatId": peer, "text": "go https://example.com/x", "preview": map[string]any{
		"url": "https://example.com/x", "title": "T", "description": "D",
		"image": map[string]any{"data": base64.StdEncoding.EncodeToString(card.Bytes())},
	}}
	if r := e.Do("POST", "/api/send/link-custom-preview", body); r.Status != 201 {
		t.Fatalf("custom preview: %d %s", r.Status, r.Body)
	}
	lp = last[*pb.SendTextRequest](t, e).GetLinkPreview()
	if lp.GetTitle() != "T" || lp.GetDescription() != "D" || lp.GetMatchedText() != "https://example.com/x" || !bytes.HasPrefix(lp.GetJpegThumbnail(), []byte{0xFF, 0xD8}) {
		t.Fatalf("custom preview: %v", lp)
	}
	body["text"] = "no url in here"
	if r := e.Do("POST", "/api/send/link-custom-preview", body); r.Status != 400 {
		t.Fatalf("text without the preview URL: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/send/link-custom-preview", map[string]any{"session": "default", "chatId": peer, "text": "x"}); r.Status != 400 {
		t.Fatalf("no preview: %d %s", r.Status, r.Body)
	}
}

// wav is a mono 16-bit PCM WAV of a tone.
func wav(seconds float64) []byte {
	const rate = 8000
	n := int(seconds * rate)
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("RIFF")
	_ = binary.Write(&b, le, uint32(36+2*n))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(2 * rate), uint16(2), uint16(16)} {
		_ = binary.Write(&b, le, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, le, uint32(2*n))
	for i := range n {
		_ = binary.Write(&b, le, int16(10000*math.Sin(2*math.Pi*440*float64(i)/rate)))
	}
	return b.Bytes()
}

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
}

func TestSendMedia(t *testing.T) {
	t.Setenv("WAMUX_HTTP_MEDIA_ALLOW_PRIVATE", "true")
	pdf := []byte("%PDF-1.4\n% a tiny document\n")
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/docs/report.pdf" {
			_, _ = w.Write(pdf)
			return
		}
		http.NotFound(w, r)
	}))
	defer files.Close()
	e := working(t)
	var img bytes.Buffer
	_ = png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 8, 8)))

	r := e.Do("POST", "/api/sendImage", map[string]any{"session": "default", "chatId": peer, "caption": "pic",
		"file": map[string]any{"data": base64.StdEncoding.EncodeToString(img.Bytes())}})
	if r.Status != 201 || !strings.HasPrefix(r.Map(t)["id"].(string), "true_"+peer+"_") {
		t.Fatalf("sendImage: %d %s", r.Status, r.Body)
	}
	hdr := last[*pb.SendMediaHeader](t, e)
	meta := last[*pb.MediaMeta](t, e)
	if hdr.GetMediaType() != pb.MediaType_MEDIA_TYPE_IMAGE || hdr.GetMimeType() != "image/png" || hdr.GetCaption() != "pic" ||
		hdr.GetTo().GetValue() != peerJ || int(meta.GetFileLength()) != img.Len() {
		t.Fatalf("image header %v, %d bytes", hdr, meta.GetFileLength())
	}

	r = e.Do("POST", "/api/sendFile", map[string]any{"session": "default", "chatId": peer, "file": map[string]any{"url": files.URL + "/docs/report.pdf"}})
	if r.Status != 201 {
		t.Fatalf("sendFile: %d %s", r.Status, r.Body)
	}
	hdr, meta = last[*pb.SendMediaHeader](t, e), last[*pb.MediaMeta](t, e)
	if hdr.GetMediaType() != pb.MediaType_MEDIA_TYPE_DOCUMENT || hdr.GetMimeType() != "application/pdf" ||
		hdr.GetFilename() != "report.pdf" || int(meta.GetFileLength()) != len(pdf) {
		t.Fatalf("document header %v", hdr)
	}

	e.Do("POST", "/api/sendSticker", map[string]any{"session": "default", "chatId": peer, "file": map[string]any{"data": "UklGRgAAAABXRUJQ"}})
	if hdr = last[*pb.SendMediaHeader](t, e); hdr.GetMediaType() != pb.MediaType_MEDIA_TYPE_STICKER || hdr.GetMimeType() != "image/webp" {
		t.Fatalf("sticker header %v", hdr)
	}

	n := len(fakecore.Requests[*pb.SendMediaHeader](e.Core))
	for name, body := range map[string]map[string]any{
		"no file":    {"session": "default", "chatId": peer},
		"empty file": {"session": "default", "chatId": peer, "file": map[string]any{}},
		"bad base64": {"session": "default", "chatId": peer, "file": map[string]any{"data": "###"}},
		"ftp url":    {"session": "default", "chatId": peer, "file": map[string]any{"url": "ftp://x/y"}},
	} {
		if r := e.Do("POST", "/api/sendImage", body); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	if r := e.Do("POST", "/api/sendFile", map[string]any{"session": "default", "chatId": peer, "file": map[string]any{"url": files.URL + "/missing"}}); r.Status != 422 {
		t.Fatalf("404 file: %d %s", r.Status, r.Body)
	}
	if got := len(fakecore.Requests[*pb.SendMediaHeader](e.Core)); got != n {
		t.Fatalf("a failed load still sent %d media", got-n)
	}
}

func TestSendMediaURLIsGuarded(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer internal.Close()
	e := working(t)
	r := e.Do("POST", "/api/sendFile", map[string]any{"session": "default", "chatId": peer, "file": map[string]any{"url": internal.URL + "/x"}})
	if r.Status != 422 || !strings.Contains(string(r.Body), "refused") {
		t.Fatalf("a loopback URL: %d %s", r.Status, r.Body)
	}
	if n := len(fakecore.Requests[*pb.SendMediaHeader](e.Core)); n != 0 {
		t.Fatalf("the core got %d media", n)
	}
}

func TestSendVoiceAndVideo(t *testing.T) {
	needFFmpeg(t)
	e := working(t)
	r := e.Do("POST", "/api/sendVoice", map[string]any{"session": "default", "chatId": peer, "convert": true,
		"file": map[string]any{"data": base64.StdEncoding.EncodeToString(wav(2))}})
	if r.Status != 201 {
		t.Fatalf("sendVoice: %d %s", r.Status, r.Body)
	}
	hdr := last[*pb.SendMediaHeader](t, e)
	if hdr.GetMediaType() != pb.MediaType_MEDIA_TYPE_AUDIO || !hdr.GetPtt() || hdr.GetMimeType() != "audio/ogg; codecs=opus" ||
		hdr.GetSeconds() != 2 || len(hdr.GetWaveform()) != 64 || hdr.GetWaveform()[10] < 50 {
		t.Fatalf("voice header %v", hdr)
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "in.avi")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-f", "lavfi",
		"-i", "testsrc=size=161x121:rate=10:duration=2", "-c:v", "mpeg4", src).CombinedOutput(); err != nil {
		t.Fatalf("make video: %v %s", err, out)
	}
	video, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	r = e.Do("POST", "/api/sendVideo", map[string]any{"session": "default", "chatId": peer, "convert": true, "asNote": true,
		"caption": "clip", "file": map[string]any{"data": base64.StdEncoding.EncodeToString(video)}})
	if r.Status != 201 {
		t.Fatalf("sendVideo: %d %s", r.Status, r.Body)
	}
	hdr = last[*pb.SendMediaHeader](t, e)
	if hdr.GetMediaType() != pb.MediaType_MEDIA_TYPE_VIDEO || !hdr.GetPtv() || hdr.GetMimeType() != "video/mp4" || hdr.GetSeconds() != 2 || hdr.GetCaption() != "clip" {
		t.Fatalf("video header %v", hdr)
	}
	// Without convert the bytes go as they are, under WAHA's default type.
	e.Do("POST", "/api/sendVoice", map[string]any{"session": "default", "chatId": peer, "file": map[string]any{"data": "AAAA"}})
	if hdr = last[*pb.SendMediaHeader](t, e); hdr.GetMimeType() != "audio/ogg; codecs=opus" || !hdr.GetPtt() || hdr.GetSeconds() != 0 {
		t.Fatalf("unconverted voice %v", hdr)
	}
}

func TestSendContactVcard(t *testing.T) {
	e := working(t)
	r := e.Do("POST", "/api/sendContactVcard", map[string]any{"session": "default", "chatId": peer, "contacts": []any{
		map[string]any{"fullName": "Ana Lima", "organization": "ACME", "phoneNumber": "+55 11 97777-7777"},
		map[string]any{"vcard": "BEGIN:VCARD\nVERSION:3.0\nFN:Bia\nTEL;waid=5511966666666:+55 11 96666-6666\nEND:VCARD"},
	}})
	if r.Status != 201 {
		t.Fatalf("sendContactVcard: %d %s", r.Status, r.Body)
	}
	sent := fakecore.Requests[*pb.SendContactRequest](e.Core)
	if len(sent) != 2 {
		t.Fatalf("%d contacts sent", len(sent))
	}
	if sent[0].GetDisplayName() != "Ana Lima" || !strings.Contains(sent[0].GetVcard(), "waid=5511977777777:+55 11 97777-7777") ||
		!strings.Contains(sent[0].GetVcard(), "ORG:ACME;") || sent[1].GetDisplayName() != "Bia" {
		t.Fatalf("contacts: %v", sent)
	}
	if r := e.Do("POST", "/api/sendContactVcard", map[string]any{"session": "default", "chatId": peer}); r.Status != 400 {
		t.Fatalf("no contacts: %d", r.Status)
	}
}

func TestPollAndVote(t *testing.T) {
	e := working(t)
	r := e.Do("POST", "/api/sendPoll", map[string]any{"session": "default", "chatId": peer,
		"poll": map[string]any{"name": "Lunch?", "options": []string{"Pizza", "Sushi", "Salad"}, "multipleAnswers": true}})
	if r.Status != 201 {
		t.Fatalf("sendPoll: %d %s", r.Status, r.Body)
	}
	pollID := r.Map(t)["id"].(string)
	if p := last[*pb.SendPollRequest](t, e); p.GetName() != "Lunch?" || len(p.GetOptions()) != 3 || p.GetSelectableCount() != 3 {
		t.Fatalf("poll: %v", p)
	}
	e.Do("POST", "/api/sendPoll", map[string]any{"session": "default", "chatId": peer, "poll": map[string]any{"name": "One", "options": []string{"a", "b"}}})
	if p := last[*pb.SendPollRequest](t, e); p.GetSelectableCount() != 1 {
		t.Fatalf("single answer: %v", p)
	}
	// A channel takes 0 for several answers and 1 for one, as WA Web does.
	channel := "123456789012345678@newsletter"
	e.Do("POST", "/api/sendPoll", map[string]any{"session": "default", "chatId": channel, "poll": map[string]any{"name": "Many", "options": []string{"a", "b", "c"}, "multipleAnswers": true}})
	if p := last[*pb.SendPollRequest](t, e); p.GetTo().GetValue() != channel || p.GetSelectableCount() != 0 {
		t.Fatalf("channel, several answers: %v", p)
	}
	e.Do("POST", "/api/sendPoll", map[string]any{"session": "default", "chatId": channel, "poll": map[string]any{"name": "One", "options": []string{"a", "b"}}})
	if p := last[*pb.SendPollRequest](t, e); p.GetSelectableCount() != 1 {
		t.Fatalf("channel, single answer: %v", p)
	}
	if r := e.Do("POST", "/api/sendPoll", map[string]any{"session": "default", "chatId": peer}); r.Status != 400 {
		t.Fatalf("no poll: %d", r.Status)
	}

	r = e.Do("POST", "/api/sendPollVote", map[string]any{"session": "default", "chatId": peer, "pollMessageId": pollID, "votes": []string{"Sushi"}})
	if r.Status != 201 {
		t.Fatalf("vote: %d %s", r.Status, r.Body)
	}
	v := last[*pb.SendPollVoteRequest](t, e)
	if v.GetPollId() != pollID[strings.LastIndex(pollID, "_")+1:] || v.GetPollCreator().GetValue() != "5511999999999@s.whatsapp.net" ||
		len(v.GetMessageSecret()) != 32 || len(v.GetOptions()) != 1 || v.GetOptions()[0] != "Sushi" || v.GetChat().GetValue() != peerJ {
		t.Fatalf("vote: %v", v)
	}
	if r := e.Do("POST", "/api/sendPollVote", map[string]any{"session": "default", "chatId": peer, "pollMessageId": pollID, "votes": []string{"Tacos"}}); r.Status != 400 {
		t.Fatalf("an option the poll does not have: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/sendPollVote", map[string]any{"session": "default", "chatId": peer, "pollMessageId": "UNKNOWN", "votes": []string{"x"}}); r.Status != 422 {
		t.Fatalf("unknown poll: %d %s", r.Status, r.Body)
	}

	// A poll someone else sent: secret and options come from its message.
	secret := bytes.Repeat([]byte{7}, 32)
	raw, _ := proto.Marshal(&waE2E.Message{
		PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Q"), Options: []*waE2E.PollCreationMessage_Option{
			{OptionName: proto.String("Yes")}, {OptionName: proto.String("No")}}},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: secret},
	})
	theirs := waha.MessageID{Chat: group, ID: "3EB0THEIRPOLL", Participant: "5511777777777@c.us"}
	putMessage(t, e, theirs, &pb.InboundMessage{Sender: &pb.Jid{Value: "5511777777777:2@s.whatsapp.net"}, RawMessage: raw}, 2, time.Now().UnixMilli())
	r = e.Do("POST", "/api/sendPollVote", map[string]any{"session": "default", "chatId": group, "pollMessageId": theirs.String(), "votes": [][]string{{"No"}}})
	if r.Status != 201 {
		t.Fatalf("vote on a received poll: %d %s", r.Status, r.Body)
	}
	v = last[*pb.SendPollVoteRequest](t, e)
	if v.GetPollCreator().GetValue() != "5511777777777@s.whatsapp.net" || !bytes.Equal(v.GetMessageSecret(), secret) || v.GetOptions()[0] != "No" {
		t.Fatalf("received poll vote: %v", v)
	}
}

func TestReactionAndStar(t *testing.T) {
	e := working(t)
	r := e.Do("PUT", "/api/reaction", map[string]any{"session": "default", "messageId": "false_" + peer + "_3EB0TARGET", "reaction": "👍"})
	if r.Status != 200 || !strings.HasPrefix(r.Map(t)["id"].(string), "true_"+peer+"_") {
		t.Fatalf("reaction: %d %s", r.Status, r.Body)
	}
	rr := last[*pb.SendReactionRequest](t, e)
	if rr.GetEmoji() != "👍" || rr.GetTarget().GetId() != "3EB0TARGET" || rr.GetTarget().GetFromMe() || rr.GetTarget().GetChat().GetValue() != peerJ {
		t.Fatalf("reaction: %v", rr)
	}
	e.Do("PUT", "/api/reaction", map[string]any{"session": "default", "messageId": "true_" + group + "_3EB0MINE", "reaction": ""})
	if rr = last[*pb.SendReactionRequest](t, e); rr.GetTarget().GetParticipant().GetValue() != "5511999999999@s.whatsapp.net" || rr.GetEmoji() != "" {
		t.Fatalf("my group message: %v", rr)
	}
	if r := e.Do("PUT", "/api/reaction", map[string]any{"session": "default", "messageId": "3EB0BARE", "reaction": "x"}); r.Status != 400 {
		t.Fatalf("reaction wants a serialized id: %d %s", r.Status, r.Body)
	}

	r = e.Do("PUT", "/api/star", map[string]any{"session": "default", "messageId": "false_" + group + "_3EB0S_5511777777777@c.us", "chatId": group, "star": true})
	if r.Status != 200 || len(r.Body) != 0 {
		t.Fatalf("star: %d %s", r.Status, r.Body)
	}
	st := last[*pb.StarMessageRequest](t, e)
	if !st.GetStarred() || st.GetTarget().GetParticipant().GetValue() != "5511777777777@s.whatsapp.net" || st.GetTarget().GetId() != "3EB0S" {
		t.Fatalf("star: %v", st)
	}
	putMessage(t, e, waha.MessageID{FromMe: true, Chat: peer, ID: "3EB0STORED"}, &pb.InboundMessage{}, 1, time.Now().UnixMilli())
	e.Do("PUT", "/api/star", map[string]any{"session": "default", "messageId": "3EB0STORED", "chatId": peer, "star": false})
	if st = last[*pb.StarMessageRequest](t, e); st.GetStarred() || !st.GetTarget().GetFromMe() || st.GetTarget().GetChat().GetValue() != peerJ {
		t.Fatalf("unstar a bare id: %v", st)
	}
}

func TestSendSeenAndTyping(t *testing.T) {
	e := working(t)
	r := e.Do("POST", "/api/sendSeen", map[string]any{"session": "default", "chatId": peer, "messageIds": []string{
		"false_" + peer + "_A1", "true_" + peer + "_MINE", "false_" + group + "_G1_5511777777777@c.us",
	}, "messageId": "false_" + peer + "_A2"})
	if r.Status != 201 || len(r.Body) != 0 {
		t.Fatalf("sendSeen: %d %s", r.Status, r.Body)
	}
	reads := fakecore.Requests[*pb.MarkReadRequest](e.Core)
	if len(reads) != 2 || strings.Join(reads[0].GetMessageIds(), ",") != "A1,A2" || reads[0].GetSender() != nil ||
		reads[1].GetChat().GetValue() != group || reads[1].GetSender().GetValue() != "5511777777777@s.whatsapp.net" {
		t.Fatalf("receipts: %v", reads)
	}
	if r := e.Do("POST", "/api/sendSeen", map[string]any{"session": "default", "chatId": peer, "messageId": "A1"}); r.Status != 400 {
		t.Fatalf("sendSeen wants serialized ids: %d", r.Status)
	}

	// Without ids: the stored unread incoming messages of the last 7 days.
	now := time.Now().UnixMilli()
	unread := waha.MessageID{Chat: peer, ID: "U1"}
	putMessage(t, e, unread, &pb.InboundMessage{}, 2, now)
	putMessage(t, e, waha.MessageID{Chat: peer, ID: "READ"}, &pb.InboundMessage{}, 3, now)
	putMessage(t, e, waha.MessageID{Chat: peer, ID: "OLD"}, &pb.InboundMessage{}, 2, now-8*24*3600*1000)
	putMessage(t, e, waha.MessageID{FromMe: true, Chat: peer, ID: "MINE"}, &pb.InboundMessage{}, 2, now)
	r = e.Do("POST", "/api/sendSeen", map[string]any{"session": "default", "chatId": peer})
	if r.Status != 201 {
		t.Fatalf("sendSeen without ids: %d %s", r.Status, r.Body)
	}
	if ids := r.Map(t)["ids"].([]any); len(ids) != 1 || ids[0] != unread.String() {
		t.Fatalf("ids: %v", ids)
	}
	// MarkRead and MarkChatRead share a request type: the receipt for U1,
	// then the chat-wide mark, which names no message.
	reads = fakecore.Requests[*pb.MarkReadRequest](e.Core)
	if len(reads) != 4 || strings.Join(reads[2].GetMessageIds(), ",") != "U1" ||
		len(reads[3].GetMessageIds()) != 0 || reads[3].GetChat().GetValue() != peerJ {
		t.Fatalf("receipt and chat mark: %v", reads)
	}
	r = e.Do("POST", "/api/sendSeen", map[string]any{"session": "default", "chatId": group})
	if r.Status != 201 || string(bytes.TrimSpace(r.Body)) != `{"ids":[]}` {
		t.Fatalf("nothing unread: %d %s", r.Status, r.Body)
	}

	for path, state := range map[string]pb.PresenceState{
		"/api/startTyping": pb.PresenceState_PRESENCE_STATE_COMPOSING,
		"/api/stopTyping":  pb.PresenceState_PRESENCE_STATE_PAUSED,
	} {
		r := e.Do("POST", path, map[string]any{"session": "default", "chatId": peer})
		if r.Status != 201 || r.Map(t)["result"] != true {
			t.Fatalf("%s: %d %s", path, r.Status, r.Body)
		}
		if p := last[*pb.SendPresenceRequest](t, e); p.GetState() != state || p.GetChat().GetValue() != peerJ {
			t.Fatalf("%s: %v", path, p)
		}
	}
}

func TestButtonsReply(t *testing.T) {
	e := working(t)
	offer, _ := proto.Marshal(&waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{ContentText: proto.String("Pick")}})
	putMessage(t, e, waha.MessageID{Chat: peer, ID: "3EB0OFFER"}, &pb.InboundMessage{Sender: &pb.Jid{Value: peerJ}, RawMessage: offer}, 2, time.Now().UnixMilli())
	r := e.Do("POST", "/api/send/buttons/reply", map[string]any{"session": "default", "chatId": peer,
		"replyTo": "false_" + peer + "_3EB0OFFER", "selectedDisplayText": "Yes", "selectedButtonID": "btn-yes"})
	if r.Status != 201 {
		t.Fatalf("buttons reply: %d %s", r.Status, r.Body)
	}
	ir := last[*pb.SendInteractiveReplyRequest](t, e)
	if ir.GetButton().GetSelectedId() != "btn-yes" || ir.GetButton().GetDisplayText() != "Yes" ||
		ir.GetQuote().GetQuoted().GetId() != "3EB0OFFER" || !bytes.Equal(ir.GetQuotedMessage(), offer) {
		t.Fatalf("interactive reply: %v", ir)
	}
	if r := e.Do("POST", "/api/send/buttons/reply", map[string]any{"session": "default", "chatId": peer}); r.Status != 400 {
		t.Fatalf("missing fields: %d", r.Status)
	}
}

func TestChattingUnimplemented(t *testing.T) {
	e := working(t)
	for _, op := range []struct{ method, path string }{
		{"POST", "/api/sendLocation"}, {"POST", "/api/sendButtons"}, {"POST", "/api/sendList"},
		{"POST", "/api/forwardMessage"}, {"GET", "/api/default/new-message-id"}, {"POST", "/api/default/events"},
		{"POST", "/api/default/status/voice"}, {"GET", "/api/default/status/new-message-id"},
	} {
		if r := e.Do(op.method, op.path, map[string]any{"session": "default"}); r.Status != 501 {
			t.Errorf("%s %s: %d", op.method, op.path, r.Status)
		}
	}
}

func TestSendNeedsSendAction(t *testing.T) {
	e := working(t)
	r := e.Do("POST", "/api/keys", map[string]any{"isAdmin": false, "session": "default", "actions": map[string]bool{"read": true}})
	if r.Status != 201 {
		t.Fatalf("create key: %d %s", r.Status, r.Body)
	}
	key := r.Map(t)["key"].(string)
	if r := e.DoKey(key, "POST", "/api/sendText", map[string]any{"session": "default", "chatId": peer, "text": "x"}); r.Status != 403 {
		t.Fatalf("a read-only key sent: %d", r.Status)
	}
}
