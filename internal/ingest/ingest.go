// Package ingest turns the core's events for a session into WAHA: stored
// messages, chats and contacts, and published WAHA events.
package ingest

import (
	"context"
	"log/slog"

	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/core"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/events"
	"github.com/LLawli/wamux-http/internal/media"
	"github.com/LLawli/wamux-http/internal/store"
)

// Processor handles one session's events. The session manager calls it from
// the session owner only, in event order.
type Processor struct {
	Config *config.Config
	Store  store.Store
	Core   *core.Client
	Pub    *events.Publisher
	Media  *media.Service
	Log    *slog.Logger
}

// Handle processes one event.
func (p *Processor) Handle(ctx context.Context, sess store.Session, ev *pb.EventEnvelope) error {
	return nil
}
