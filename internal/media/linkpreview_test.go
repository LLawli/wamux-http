package media

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestFirstURL(t *testing.T) {
	for text, want := range map[string]string{
		"look https://example.com/a?b=1.":      "https://example.com/a?b=1",
		"(see http://x.org/path), ok":          "http://x.org/path",
		"no link here":                         "",
		"two: https://a.io and https://b.io":   "https://a.io",
		"ftp://nope.example and nothing else":  "",
		`<a href="https://q.example/x">y</a>`:  "https://q.example/x",
		"https://wa.me/5511999999999!":         "https://wa.me/5511999999999",
		"trailing https://example.com/page/?!": "https://example.com/page/",
	} {
		if got := FirstURL(text); got != want {
			t.Errorf("FirstURL(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestParsePreview(t *testing.T) {
	base, _ := url.Parse("https://example.com/blog/post")
	page := `<!doctype html><html><head>
<title>Fallback &amp; title</title>
<meta name="description" content="plain description">
<meta property="og:title" content="OG title">
<meta content="/img/card.png" property="og:image">
</head><body><meta property="og:description" content="in the body, ignored"></body></html>`
	p := ParsePreview(base, []byte(page))
	if p.Title != "OG title" || p.Description != "plain description" || p.Image != "https://example.com/img/card.png" {
		t.Fatalf("preview: %+v", p)
	}
	p = ParsePreview(base, []byte(`<html><head><title> Only &amp; title </title><meta name="twitter:image" content="https://cdn.example/t.jpg"></head></html>`))
	if p.Title != "Only & title" || p.Description != "" || p.Image != "https://cdn.example/t.jpg" {
		t.Fatalf("fallbacks: %+v", p)
	}
	if p := ParsePreview(base, []byte("not html at all")); p != (Preview{}) {
		t.Fatalf("garbage: %+v", p)
	}
}

func TestLinkPreviewFetchesPageAndImage(t *testing.T) {
	t.Setenv("WAMUX_HTTP_MEDIA_ALLOW_PRIVATE", "true")
	var img bytes.Buffer
	_ = png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 600, 300)))
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<head><meta property="og:title" content="Hello"><meta property="og:description" content="World"><meta property="og:image" content="/card.png"></head>`))
	})
	mux.HandleFunc("/card.png", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(img.Bytes()) })
	mux.HandleFunc("/file.zip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &Service{}
	lp := s.LinkPreview(context.Background(), "read "+srv.URL+"/page.")
	if lp == nil || lp.MatchedText != srv.URL+"/page" || lp.Title != "Hello" || lp.Description != "World" {
		t.Fatalf("preview: %+v", lp)
	}
	if len(lp.JpegThumbnail) < 100 || lp.JpegThumbnail[0] != 0xFF || lp.JpegThumbnail[1] != 0xD8 {
		t.Fatalf("no JPEG thumbnail: %d bytes", len(lp.JpegThumbnail))
	}
	if lp := s.LinkPreview(context.Background(), "get "+srv.URL+"/file.zip"); lp != nil {
		t.Fatalf("a non-HTML link got a preview: %+v", lp)
	}
	if lp := s.LinkPreview(context.Background(), "nothing to see"); lp != nil {
		t.Fatalf("no URL, still a preview: %+v", lp)
	}
}

func TestLinkPreviewIsGuarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<head><title>internal</title></head>`))
	}))
	defer srv.Close()
	if lp := (&Service{}).LinkPreview(context.Background(), srv.URL); lp != nil {
		t.Fatalf("a loopback page was previewed: %+v", lp)
	}
}

func TestExtension(t *testing.T) {
	for mt, want := range map[string]string{
		"image/jpeg":             "jpeg",
		"audio/ogg; codecs=opus": "oga",
		"video/mp4":              "mp4",
		"application/was":        "zip",
		"application/pdf":        "pdf",
		"application/x-unknown":  "bin",
		"":                       "bin",
	} {
		if got := Extension(mt); got != want {
			t.Errorf("Extension(%q) = %q, want %q", mt, got, want)
		}
	}
	if got := FileName("3EB0ABC", "image/webp"); got != "3EB0ABC.webp" {
		t.Fatal(got)
	}
}
