package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/LLawli/wamux-http/internal/testenv"
)

func TestConvertVoiceAndVideo(t *testing.T) {
	needFFmpeg(t)
	e := working(t)
	voice := map[string]any{"data": base64.StdEncoding.EncodeToString(wav(1))}
	r := e.Do("POST", "/api/default/media/convert/voice", voice)
	if r.Status != 201 || r.Header.Get("Content-Type") != "audio/ogg; codecs=opus" || !bytes.HasPrefix(r.Body, []byte("OggS")) ||
		r.Header.Get("Content-Disposition") != `attachment; filename="output.opus"` {
		t.Fatalf("voice: %d %v %d bytes", r.Status, r.Header, len(r.Body))
	}

	// Accept: application/json answers WAHA's Base64File.
	raw, _ := jsonBody(voice)
	req, _ := http.NewRequest("POST", e.URL+"/api/default/media/convert/voice", bytes.NewReader(raw))
	req.Header.Set("X-Api-Key", testenv.AdminKey)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	got := testenv.Response{Status: resp.StatusCode, Body: body}.Map(t)
	data, _ := base64.StdEncoding.DecodeString(got["data"].(string))
	if resp.StatusCode != 201 || got["mimetype"] != "audio/ogg; codecs=opus" || !bytes.HasPrefix(data, []byte("OggS")) {
		t.Fatalf("json voice: %d %v", resp.StatusCode, got["mimetype"])
	}

	src := filepath.Join(t.TempDir(), "in.avi")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-f", "lavfi",
		"-i", "testsrc=size=64x48:rate=10:duration=1", "-c:v", "mpeg4", src).CombinedOutput(); err != nil {
		t.Fatalf("make video: %v %s", err, out)
	}
	video, _ := os.ReadFile(src)
	r = e.Do("POST", "/api/default/media/convert/video", map[string]any{"data": base64.StdEncoding.EncodeToString(video)})
	if r.Status != 201 || r.Header.Get("Content-Type") != "video/mp4" || !bytes.Contains(r.Body[:32], []byte("ftyp")) {
		t.Fatalf("video: %d %v", r.Status, r.Header)
	}

	if r := e.Do("POST", "/api/default/media/convert/voice", map[string]any{}); r.Status != 422 {
		t.Fatalf("neither url nor data: %d %s", r.Status, r.Body)
	}
	if r := e.Do("POST", "/api/default/media/convert/voice", map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("noise"))}); r.Status != 500 {
		t.Fatalf("garbage: %d %s", r.Status, r.Body)
	}

	// Converting is a read: a read-only key may.
	k := e.Do("POST", "/api/keys", map[string]any{"isAdmin": false, "session": "default", "actions": map[string]bool{"read": true}})
	if r := e.DoKey(k.Map(t)["key"].(string), "POST", "/api/default/media/convert/voice", voice); r.Status != 201 {
		t.Fatalf("read key: %d", r.Status)
	}
}
