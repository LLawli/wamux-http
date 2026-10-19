package media

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":                  true,
		"2606:4700:4700::1111":     true,
		"127.0.0.1":                false,
		"127.8.9.10":               false,
		"::1":                      false,
		"10.1.2.3":                 false,
		"172.16.0.1":               false,
		"192.168.1.1":              false,
		"fd00::1":                  false,
		"169.254.169.254":          false, // cloud metadata
		"fe80::1":                  false,
		"224.0.0.1":                false,
		"ff02::1":                  false,
		"0.0.0.0":                  false,
		"::":                       false,
		"100.64.0.1":               false,
		"255.255.255.255":          false,
		"::ffff:127.0.0.1":         false, // IPv4-mapped loopback
		"::ffff:8.8.8.8":           true,
		"64:ff9b::7f00:1":          false, // NAT64 of 127.0.0.1
		"64:ff9b::808:808":         true,  // NAT64 of 8.8.8.8
		"fe80::1%eth0":             false,
		"2001:4860:4860::8888":     true,
		"198.51.100.1":             true, // documentation, but routable as far as the guard cares
		"203.0.113.1":              true,
		"192.0.0.8":                false,
		"240.0.0.1":                false,
		"198.18.0.1":               false,
		"[::1]":                    false,
		"not an address":           false,
		"1.1.1.1":                  true,
		"172.32.0.1":               true,
		"11.0.0.1":                 true,
		"100.128.0.1":              true,
		"169.255.0.1":              true,
		"192.169.0.1":              true,
		"64:ff9b::a00:1":           false,
		"::ffff:10.0.0.1":          false,
		"::ffff:169.254.169.254":   false,
		"64:ff9b::a9fe:a9fe":       false,
		"2001:db8::1":              true,
		"fc00::1":                  false,
		"ff05::1":                  false,
		"0.1.2.3":                  false,
		"100.127.255.255":          false,
		"224.0.0.251":              false,
		"239.255.255.250":          false,
		"192.0.0.255":              false,
		"198.19.255.255":           false,
		"255.255.255.254":          false,
		"9.255.255.255":            true,
		"126.255.255.255":          true,
		"128.0.0.1":                true,
		"191.255.255.255":          true,
		"223.255.255.255":          true,
		"2001:4860:4860:0:0:0:0:1": true,
	} {
		a, err := netip.ParseAddr(strings.Trim(addr, "[]"))
		if err != nil {
			a = netip.Addr{}
		}
		if got := PublicAddr(a); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestFetchRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer srv.Close()
	f := NewFetcher(FetchOptions{})
	if _, err := f.Get(context.Background(), srv.URL); !errors.Is(err, ErrRefused) {
		t.Fatalf("a loopback URL went through: %v", err)
	}
	// A name that resolves to loopback is judged after resolution.
	u, _ := url.Parse(srv.URL)
	if _, err := f.Get(context.Background(), "http://localhost:"+u.Port()+"/"); !errors.Is(err, ErrRefused) {
		t.Fatalf("localhost went through: %v", err)
	}
	// With the opt-out the same URL is fetched.
	open := NewFetcher(FetchOptions{AllowPrivate: true})
	got, err := open.Get(context.Background(), srv.URL)
	if err != nil || string(got.Data) != "secret" {
		t.Fatalf("allow private: %q %v", got.Data, err)
	}
}

func TestFetchRefusesNameResolvingToPrivate(t *testing.T) {
	// rewrite plays the DNS answer: the name resolves to a private address,
	// and the guard sees the address, not the name.
	var dialed []string
	f := NewFetcher(FetchOptions{rewrite: func(addr string) string {
		dialed = append(dialed, addr)
		if strings.HasPrefix(addr, "files.example.com:") {
			return "10.20.30.40:80"
		}
		return addr
	}})
	_, err := f.Get(context.Background(), "http://files.example.com/a.jpg")
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "10.20.30.40") {
		t.Fatalf("a name resolving to 10.20.30.40 went through: %v", err)
	}
	if len(dialed) == 0 {
		t.Fatal("the fetch never dialed")
	}
}

func TestFetchRefusesRedirectToLoopback(t *testing.T) {
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("internal")) }))
	defer inner.Close()
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL+"/admin", http.StatusFound)
	}))
	defer outer.Close()
	ou, _ := url.Parse(outer.URL)
	outerAddr := netip.MustParseAddrPort(ou.Host)
	// Only the first hop is let through, as if it were a public host.
	f := NewFetcher(FetchOptions{allow: func(ap netip.AddrPort) bool { return ap == outerAddr }})
	_, err := f.Get(context.Background(), outer.URL)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("a redirect to 127.0.0.1 went through: %v", err)
	}
	// The same policy without the redirect fetches: the refusal above is the
	// redirect's.
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer direct.Close()
	du, _ := url.Parse(direct.URL)
	directAddr := netip.MustParseAddrPort(du.Host)
	g := NewFetcher(FetchOptions{allow: func(ap netip.AddrPort) bool { return ap == directAddr }})
	if got, err := g.Get(context.Background(), direct.URL); err != nil || string(got.Data) != "ok" {
		t.Fatalf("allowed host: %q %v", got.Data, err)
	}
}

func TestFetchRefusesRedirectToOtherScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	defer srv.Close()
	f := NewFetcher(FetchOptions{AllowPrivate: true})
	if _, err := f.Get(context.Background(), srv.URL); !errors.Is(err, ErrRefused) {
		t.Fatalf("redirect to file:// went through: %v", err)
	}
}

func TestFetchBadSchemeSizeAndStatus(t *testing.T) {
	f := NewFetcher(FetchOptions{AllowPrivate: true, MaxBytes: 10})
	for _, u := range []string{"file:///etc/passwd", "gopher://x", "ftp://example.com/a", "/relative", ""} {
		if _, err := f.Get(context.Background(), u); !errors.Is(err, ErrBadInput) {
			t.Errorf("%q: %v", u, err)
		}
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "11")
		_, _ = w.Write([]byte("12345678901"))
	}))
	defer big.Close()
	if _, err := f.Get(context.Background(), big.URL); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
	chunked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range 3 {
			_, _ = w.Write([]byte("12345"))
			w.(http.Flusher).Flush()
		}
	}))
	defer chunked.Close()
	if _, err := f.Get(context.Background(), chunked.URL); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the cap without a length: %v", err)
	}
	head, err := f.GetHead(context.Background(), chunked.URL, 7)
	if err != nil || string(head.Data) != "1234512" {
		t.Fatalf("GetHead keeps the head: %q %v", head.Data, err)
	}
	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	var fe *FetchError
	if _, err := f.Get(context.Background(), missing.URL); !errors.As(err, &fe) || fe.Status != 404 {
		t.Fatalf("404: %v", err)
	}
}

func TestLoadDefaultsAndSniffing(t *testing.T) {
	s := &Service{}
	png := []byte("\x89PNG\r\n\x1a\n0000")
	cases := []struct {
		file File
		kind Kind
		want string
	}{
		{File{Data: "aGVsbG8=", Mimetype: "text/x-custom"}, KindDocument, "text/x-custom"},
		{File{Data: b64(png)}, KindImage, "image/png"},
		{File{Data: "aGVsbG8="}, KindImage, MimeImage},
		{File{Data: "aGVsbG8="}, KindVoice, MimeVoice},
		{File{Data: "aGVsbG8="}, KindVideo, MimeVideo},
		{File{Data: "aGVsbG8="}, KindSticker, MimeSticker},
		{File{Data: b64([]byte("%PDF-1.4 ..."))}, KindDocument, "application/pdf"},
		{File{Data: "aGVsbG8"}, KindDocument, "text/plain"}, // unpadded base64
	}
	for _, tc := range cases {
		b, err := s.Load(context.Background(), &tc.file, tc.kind)
		if err != nil || b.Mimetype != tc.want {
			t.Errorf("%+v kind %d: %q %v, want %q", tc.file, tc.kind, b.Mimetype, err, tc.want)
		}
	}
	if _, err := s.Load(context.Background(), &File{}, KindImage); !errors.Is(err, ErrBadInput) {
		t.Fatalf("empty file: %v", err)
	}
	if _, err := s.Load(context.Background(), &File{Data: "!!not base64!!"}, KindImage); !errors.Is(err, ErrBadInput) {
		t.Fatalf("bad base64: %v", err)
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
