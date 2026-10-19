// Package events publishes WAHA events: it builds the envelope, picks the
// webhooks that want the event and appends both to the store in one
// transaction. Delivery is the webhooks package's job; WebSocket fan-out
// tails the store's event log.
package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// Publisher appends events.
type Publisher struct {
	Store       store.Store
	Global      *config.GlobalWebhook
	Environment waha.Environment
}

// Publish records one event for a session. payload is marshalled as-is.
func (p *Publisher) Publish(ctx context.Context, sess store.Session, event string, payload any) (waha.Envelope, error) {
	cfg := waha.ParseSessionConfig(sess.Config)
	me := sess.Me
	if len(me) == 0 {
		me = json.RawMessage("null")
	}
	env := waha.Envelope{
		ID:          waha.NewEventID(),
		Timestamp:   waha.NowMs(),
		Event:       event,
		Session:     sess.Name,
		Metadata:    cfg.Metadata,
		Me:          me,
		Payload:     payload,
		Engine:      config.Engine,
		Environment: p.Environment,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return env, fmt.Errorf("marshal %s event: %w", event, err)
	}
	var ds []store.Delivery
	for _, wh := range p.webhooks(cfg) {
		if wh.URL == "" || !waha.EventMatches(wh.Events, event) {
			continue
		}
		whJSON, err := json.Marshal(wh)
		if err != nil {
			return env, err
		}
		ds = append(ds, store.Delivery{URL: wh.URL, Body: body, Webhook: whJSON, NextAttemptAt: env.Timestamp})
	}
	_, err = p.Store.AppendEvent(ctx, store.Event{
		ID: env.ID, Session: sess.Name, Type: event, Body: body, CreatedAt: env.Timestamp,
	}, ds)
	return env, err
}

func (p *Publisher) webhooks(cfg waha.SessionConfig) []waha.WebhookConfig {
	out := append([]waha.WebhookConfig(nil), cfg.Webhooks...)
	if g := p.Global; g != nil {
		wh := waha.WebhookConfig{URL: g.URL, Events: g.Events}
		if g.HMACKey != "" {
			wh.HMAC = &waha.HMACConfig{Key: g.HMACKey}
		}
		if g.RetriesPolicy != "" || g.RetriesDelay != nil || g.RetriesAttempt != nil {
			wh.Retries = &waha.RetriesConfig{Policy: g.RetriesPolicy, DelaySeconds: g.RetriesDelay, Attempts: g.RetriesAttempt}
		}
		for _, h := range g.CustomHeaders {
			wh.CustomHeaders = append(wh.CustomHeaders, waha.CustomHeader{Name: h[0], Value: h[1]})
		}
		out = append(out, wh)
	}
	return out
}
