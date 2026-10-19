// Package ingest turns the core's events for a session into WAHA: stored
// messages, chats and contacts, and published WAHA events.
package ingest

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/core"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/events"
	"github.com/LLawli/wamux-http/internal/media"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// dedupeWindow is how long a published message or ack id is remembered, so
// a replayed event does not go out twice (WAHA's DistinctMessages and
// DistinctAck use the same 60 s).
const dedupeWindow = 60 * time.Second

// dedupeSweepAt is the remembered-id count past which expired ids are swept.
const dedupeSweepAt = 4096

// Processor handles one session's events. The session manager calls it from
// the session owner only, in event order.
type Processor struct {
	Config *config.Config
	Store  store.Store
	Core   *core.Client
	Pub    *events.Publisher
	Media  *media.Service
	Log    *slog.Logger

	mu   sync.Mutex
	seen map[string]time.Time
}

// Handle processes one event. An event WAHA has no event for goes out as
// engine.event.
func (p *Processor) Handle(ctx context.Context, sess store.Session, ev *pb.EventEnvelope) error {
	var handled bool
	var err error
	switch e := ev.GetEvent().(type) {
	case *pb.EventEnvelope_Message:
		handled, err = p.onMessage(ctx, sess, e.Message)
	case *pb.EventEnvelope_Receipt:
		handled, err = p.onReceipt(ctx, sess, e.Receipt)
	case *pb.EventEnvelope_ServerAck:
		handled, err = p.onServerAck(ctx, sess, e.ServerAck)
	case *pb.EventEnvelope_Presence:
		handled, err = p.onPresence(ctx, sess, e.Presence)
	case *pb.EventEnvelope_Group:
		handled, err = p.onGroup(ctx, sess, e.Group)
	case *pb.EventEnvelope_Call:
		handled, err = p.onCall(ctx, sess, e.Call)
	case *pb.EventEnvelope_Label:
		handled, err = p.onLabel(ctx, sess, e.Label)
	case *pb.EventEnvelope_AppState:
		handled, err = p.onAppState(ctx, sess, e.AppState)
	case *pb.EventEnvelope_HistorySync:
		handled, err = p.onHistorySync(ctx, sess, e.HistorySync)
	case *pb.EventEnvelope_PushName:
		handled, err = true, p.contact(ctx, sess.Name, waha.FromPB(e.PushName.GetJid()), "", e.PushName.GetPushName())
	case *pb.EventEnvelope_Contact:
		handled, err = p.onContact(ctx, sess, e.Contact)
	case *pb.EventEnvelope_SelfPushName:
		handled, err = true, p.contact(ctx, sess.Name, waha.ParseMe(sess.Me).ID, "", e.SelfPushName.GetNewName())
	}
	if err != nil || handled {
		return err
	}
	return p.engineEvent(ctx, sess, ev)
}

func (p *Processor) engineEvent(ctx context.Context, sess store.Session, ev *pb.EventEnvelope) error {
	payload, ok := waha.NewEngineEvent(ev)
	if !ok {
		return nil
	}
	return p.publish(ctx, sess, "engine.event", payload)
}

func (p *Processor) publish(ctx context.Context, sess store.Session, event string, payload any) error {
	_, err := p.Pub.Publish(ctx, sess, event, payload)
	return err
}

// messageContext is what building a WAMessage for this session needs.
func (p *Processor) messageContext(sess store.Session) waha.MessageContext {
	return waha.MessageContext{
		Me: waha.ParseMe(sess.Me),
		Media: waha.MediaOptions{
			BaseURL:   p.Config.BaseURL,
			Session:   sess.Name,
			Download:  p.Config.DownloadMedia,
			Mimetypes: p.Config.MediaMimetypes,
		},
	}
}

// ignored applies the session's config.ignore, OR'ed with the server-wide
// defaults: events from these chats are stored but not published.
func (p *Processor) ignored(sess store.Session, chat string) bool {
	var ig waha.IgnoreConfig
	if c := waha.ParseSessionConfig(sess.Config).Ignore; c != nil {
		ig = *c
	}
	switch {
	case chat == "status@broadcast":
		return ig.Status || p.Config.IgnoreStatus
	case waha.IsGroup(chat):
		return ig.Groups || p.Config.IgnoreGroups
	case strings.HasSuffix(chat, "@newsletter"):
		return ig.Channels || p.Config.IgnoreChannels
	case strings.HasSuffix(chat, "@broadcast"):
		return ig.Broadcast || p.Config.IgnoreBroadcast
	}
	return false
}

// firstSeen reports whether key was not seen within the dedupe window, and
// remembers it.
func (p *Processor) firstSeen(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.seen == nil {
		p.seen = map[string]time.Time{}
	}
	if at, ok := p.seen[key]; ok && now.Sub(at) < dedupeWindow {
		return false
	}
	if len(p.seen) > dedupeSweepAt {
		for k, at := range p.seen {
			if now.Sub(at) >= dedupeWindow {
				delete(p.seen, k)
			}
		}
	}
	p.seen[key] = now
	return true
}

func (p *Processor) logger() *slog.Logger {
	if p.Log == nil {
		return slog.Default()
	}
	return p.Log
}

// contact records a contact's saved name or push name.
func (p *Processor) contact(ctx context.Context, session, id, name, pushName string) error {
	if id == "" || (name == "" && pushName == "") {
		return nil
	}
	return p.Store.UpsertContact(ctx, store.Contact{Session: session, ID: id, Name: name, PushName: pushName})
}

func (p *Processor) onContact(ctx context.Context, sess store.Session, c *pb.ContactUpdate) (bool, error) {
	if c.GetRemoved() {
		return false, nil // the store has nothing to forget it with; engine.event carries it
	}
	a := c.GetAction()
	name := a.GetFullName()
	if name == "" {
		name = a.GetFirstName()
	}
	for _, j := range []*pb.Jid{c.GetJid(), a.GetPn(), a.GetLid()} {
		if err := p.contact(ctx, sess.Name, waha.FromPB(j), name, ""); err != nil {
			return true, err
		}
	}
	return true, nil
}
