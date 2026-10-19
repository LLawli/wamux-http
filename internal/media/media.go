// Package media moves files in and out of WhatsApp for the edge: it fetches
// the URLs clients send (behind an SSRF guard), converts voice and video with
// ffmpeg, and serves received media under /api/files, downloading it from the
// core on first request.
package media

import (
	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/core"
	"github.com/LLawli/wamux-http/internal/store"
)

// Service is the media layer.
type Service struct {
	Config *config.Config
	Store  store.Store
	Core   *core.Client
}
