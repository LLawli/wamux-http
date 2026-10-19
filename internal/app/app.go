// Package app wires the edge together. main and the integration tests build
// the same graph through Wire.
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/core"
	"github.com/LLawli/wamux-http/internal/events"
	"github.com/LLawli/wamux-http/internal/httpapi"
	"github.com/LLawli/wamux-http/internal/ingest"
	"github.com/LLawli/wamux-http/internal/media"
	"github.com/LLawli/wamux-http/internal/sessions"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/webhooks"
)

// App is every long-lived part of one edge process.
type App struct {
	Config   *config.Config
	Store    store.Store
	Server   *httpapi.Server
	Sessions *sessions.Manager
	Tail     *events.Tail
	Webhooks *webhooks.Worker
	Pub      *events.Publisher
	Media    *media.Service
	Ingest   *ingest.Processor
}

// Wire builds the graph without starting anything.
func Wire(cfg *config.Config, st store.Store, cc *core.Client, log *slog.Logger) *App {
	pub := &events.Publisher{Store: st, Global: cfg.GlobalWebhook, Environment: httpapi.Environment(cfg.WorkerID)}
	med := &media.Service{Config: cfg, Store: st, Core: cc}
	ing := &ingest.Processor{Config: cfg, Store: st, Core: cc, Pub: pub, Media: med, Log: log}
	mgr := &sessions.Manager{Store: st, Core: cc, Pub: pub, Ingest: ing, Worker: cfg.WorkerID, LeaseTTL: cfg.LeaseTTL, Backfill: true, Log: log}
	tail := &events.Tail{Store: st, Log: log}
	return &App{
		Config: cfg, Store: st, Sessions: mgr, Tail: tail, Pub: pub, Media: med, Ingest: ing,
		Webhooks: &webhooks.Worker{Store: st, Worker: cfg.WorkerID, Timeout: cfg.WebhookTimeout, Log: log},
		Server: &httpapi.Server{
			Config: cfg, Store: st, Core: cc, Sessions: mgr, Pub: pub, Media: med, Tail: tail,
			Log: log, Started: time.Now(),
		},
	}
}

// Start runs the background loops until ctx ends.
func (a *App) Start(ctx context.Context) {
	go a.Sessions.Run(ctx)
	go a.Tail.Run(ctx)
	go a.Webhooks.Run(ctx)
	go a.retention(ctx)
	for _, name := range a.Config.StartSessions {
		go a.startAtBoot(ctx, name)
	}
}

// retention drops events older than the configured retention.
func (a *App) retention(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-a.Config.EventRetention).UnixMilli()
			if err := a.Store.PruneEvents(ctx, cutoff); err != nil {
				slog.Error("prune events", "err", err)
			}
		}
	}
}

// startAtBoot is WHATSAPP_START_SESSION: create the session if needed and
// start it.
func (a *App) startAtBoot(ctx context.Context, name string) {
	if _, err := a.Sessions.Create(ctx, name, nil, true); err == nil {
		return
	}
	if _, err := a.Sessions.Start(ctx, name); err != nil {
		slog.Error("start session at boot", "session", name, "err", err)
	}
}
