package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"
)

// ErrRefused is a fetch the SSRF guard stopped: the URL, or a redirect, led
// to an address that is not public, or to a scheme other than http(s).
var ErrRefused = errors.New("refused by the media fetch guard")

// ErrTooLarge is a fetched body over the size cap.
var ErrTooLarge = errors.New("file is larger than the media size limit")

// FetchError is a fetch that failed for another reason: the network, a
// non-2xx answer, a timeout.
type FetchError struct {
	URL    string
	Status int // HTTP status when the server answered
	Err    error
}

func (e *FetchError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("fetch %s: HTTP %d", e.URL, e.Status)
	}
	return fmt.Sprintf("fetch %s: %v", e.URL, e.Err)
}

func (e *FetchError) Unwrap() error { return e.Err }

// FetchOptions configure a Fetcher.
type FetchOptions struct {
	AllowPrivate bool
	MaxBytes     int64
	Timeout      time.Duration

	// allow overrides the address policy; tests use it to let one loopback
	// server through while keeping the guard on for everything else.
	allow func(netip.AddrPort) bool
	// rewrite maps a dialed host:port to another before the guard sees it,
	// standing in for a DNS answer in tests.
	rewrite func(string) string
}

// Fetcher downloads what clients point at. The address check runs when the
// socket is about to connect, after DNS resolution, so a hostname resolving
// to a private address is refused, and so is every redirect hop: each one
// dials again through the same check. Proxies from the environment are
// ignored, since a proxy would make the dialed address meaningless.
type Fetcher struct {
	client *http.Client
	max    int64
}

const maxRedirects = 5

// userAgent is a browser's: some hosts refuse anything else, which is why
// WAHA sends one too.
const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36"

// NewFetcher builds a Fetcher.
func NewFetcher(o FetchOptions) *Fetcher {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultFetchTimeout
	}
	allow := o.allow
	if allow == nil {
		allow = func(ap netip.AddrPort) bool { return o.AllowPrivate || PublicAddr(ap.Addr()) }
	}
	d := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrRefused, address)
			}
			if !allow(ap) {
				return fmt.Errorf("%w: %s is not a public address", ErrRefused, ap.Addr())
			}
			return nil
		},
	}
	dial := d.DialContext
	if o.rewrite != nil {
		dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.DialContext(ctx, network, o.rewrite(addr))
		}
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: o.Timeout,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Fetcher{max: o.MaxBytes, client: &http.Client{
		Transport: tr,
		Timeout:   o.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("%w: redirect to %s", ErrRefused, req.URL.Scheme)
			}
			return nil
		},
	}}
}

// reserved are ranges netip has no predicate for that are never a public
// file server: "this network", carrier-grade NAT, IETF protocol
// assignments, benchmarking, and the old class E.
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// PublicAddr reports whether an address may be fetched from: not loopback,
// private, link-local, multicast, unspecified or reserved. An IPv4 address
// carried in IPv6 (mapped, or behind the NAT64 prefix) is judged as IPv4.
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if a.Is6() && nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range reserved {
		if p.Contains(a) {
			return false
		}
	}
	return a != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// Fetched is a downloaded body.
type Fetched struct {
	Data        []byte
	ContentType string   // the server's, parameters dropped; "" when absent
	URL         *url.URL // after redirects
}

// Get downloads a URL whole, up to the size cap.
func (f *Fetcher) Get(ctx context.Context, rawURL string) (Fetched, error) {
	return f.get(ctx, rawURL, f.max, false)
}

// GetHead downloads at most limit bytes and keeps what it got when the body
// is longer (a page whose <head> is all a link preview needs).
func (f *Fetcher) GetHead(ctx context.Context, rawURL string, limit int64) (Fetched, error) {
	return f.get(ctx, rawURL, min(limit, f.max), true)
}

func (f *Fetcher) get(ctx context.Context, rawURL string, limit int64, truncate bool) (Fetched, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Fetched{}, badInput("file.url must be an http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Fetched{}, badInput("file.url: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrRefused) {
			return Fetched{}, fmt.Errorf("fetch refused: %w", err)
		}
		return Fetched{}, &FetchError{URL: u.Redacted(), Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Fetched{}, &FetchError{URL: u.Redacted(), Status: resp.StatusCode}
	}
	if !truncate && resp.ContentLength > limit {
		return Fetched{}, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return Fetched{}, &FetchError{URL: u.Redacted(), Err: err}
	}
	if int64(len(data)) > limit {
		if !truncate {
			return Fetched{}, ErrTooLarge
		}
		data = data[:limit]
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return Fetched{Data: data, ContentType: ct, URL: resp.Request.URL}, nil
}

// File is WAHA's RemoteFile or BinaryFile: a URL to fetch or base64 data,
// with an optional mimetype and filename.
type File struct {
	URL      string `json:"url,omitempty"`
	Data     string `json:"data,omitempty"`
	Mimetype string `json:"mimetype,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// Blob is a file ready to send.
type Blob struct {
	Data     []byte
	Mimetype string
	Filename string
}

// Kind is what a file is sent as; it decides the default mimetype.
type Kind int

// The kinds WAHA sends.
const (
	KindDocument Kind = iota
	KindImage
	KindVoice
	KindVideo
	KindSticker
)

// WAHA's mimetypes for a file sent without one (WAMimeType).
const (
	MimeImage   = "image/jpeg"
	MimeVoice   = "audio/ogg; codecs=opus"
	MimeVideo   = "video/mp4"
	MimeSticker = "image/webp"
)

// Load turns a WAHA file into bytes. The mimetype is the caller's when
// given; otherwise it is sniffed for a document, kept when it matches the
// kind for an image or a video, and WAHA's default for the kind otherwise.
func (s *Service) Load(ctx context.Context, f *File, kind Kind) (Blob, error) {
	if f == nil || (f.URL == "" && f.Data == "") {
		return Blob{}, badInput(`file must have "url" or "data"`)
	}
	var b Blob
	var served string
	if f.URL != "" {
		got, err := s.Fetcher().Get(ctx, f.URL)
		if err != nil {
			return Blob{}, err
		}
		b.Data, served = got.Data, got.ContentType
		if kind == KindDocument && f.Filename == "" {
			if name := path.Base(got.URL.Path); name != "/" && name != "." {
				b.Filename = name
			}
		}
	} else {
		data, err := decodeBase64(f.Data)
		if err != nil {
			return Blob{}, err
		}
		b.Data = data
	}
	if f.Filename != "" {
		b.Filename = f.Filename
	}
	b.Mimetype = f.Mimetype
	if b.Mimetype == "" {
		b.Mimetype = sniff(b.Data, served, kind)
	}
	return b, nil
}

func sniff(data []byte, served string, kind Kind) string {
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(data))
	switch kind {
	case KindImage:
		if strings.HasPrefix(detected, "image/") {
			return detected
		}
		return MimeImage
	case KindVideo:
		if strings.HasPrefix(detected, "video/") {
			return detected
		}
		return MimeVideo
	case KindVoice:
		return MimeVoice
	case KindSticker:
		return MimeSticker
	}
	if detected != "" && detected != "application/octet-stream" && detected != "text/plain" {
		return detected
	}
	if served != "" {
		return served
	}
	return detected
}
