package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// sse writes Datastar's server-sent events (protocol of Datastar v1). It is
// small enough to keep here rather than take the Go SDK and its
// dependencies; testdata/sse holds the SDK's own cases it must match.
type sse struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) *sse {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // a proxy in front must not hold the stream
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	return &sse{w: w, f: f}
}

// patchOpts are patch-elements' options; zero values are Datastar's defaults
// and are left out of the event.
type patchOpts struct {
	Selector          string
	Mode              string // "outer" (default), "inner", "append", ...
	UseViewTransition bool
}

// event writes one event: its type, then each data line as "key value".
func (s *sse) event(typ string, lines [][2]string) error {
	var b bytes.Buffer
	b.WriteString("event: " + typ + "\n")
	for _, l := range lines {
		for _, v := range strings.Split(l[1], "\n") {
			b.WriteString("data: " + l[0] + " " + v + "\n")
		}
	}
	b.WriteString("\n")
	if _, err := s.w.Write(b.Bytes()); err != nil {
		return err
	}
	if s.f != nil {
		s.f.Flush()
	}
	return nil
}

// patchElements morphs the given HTML into the page; by default each
// top-level element replaces the element with its id.
func (s *sse) patchElements(html string, o patchOpts) error {
	var lines [][2]string
	if o.Selector != "" {
		lines = append(lines, [2]string{"selector", o.Selector})
	}
	if o.Mode != "" && o.Mode != "outer" {
		lines = append(lines, [2]string{"mode", o.Mode})
	}
	if o.UseViewTransition {
		lines = append(lines, [2]string{"useViewTransition", "true"})
	}
	lines = append(lines, [2]string{"elements", html})
	return s.event("datastar-patch-elements", lines)
}

// patchSignals merges the values into the page's signals.
func (s *sse) patchSignals(signals any, onlyIfMissing bool) error {
	raw, err := json.Marshal(signals)
	if err != nil {
		return fmt.Errorf("signals: %w", err)
	}
	var lines [][2]string
	if onlyIfMissing {
		lines = append(lines, [2]string{"onlyIfMissing", "true"})
	}
	lines = append(lines, [2]string{"signals", string(raw)})
	return s.event("datastar-patch-signals", lines)
}
