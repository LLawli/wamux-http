// Package webhooks delivers the outbox: every process claims due deliveries
// from the store, sends them the way WAHA does (headers, HMAC, retries) and
// records the outcome. Unlike WAHA, which fires and forgets, a delivery
// survives a restart.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// WAHA's retry defaults.
const (
	DefaultDelaySeconds = 2
	DefaultAttempts     = 15
	maxBackoff          = time.Hour
	defaultConcurrency  = 64
	defaultPerURL       = 4
	idlePoll            = 500 * time.Millisecond
)

// Worker delivers webhooks until its context ends.
type Worker struct {
	Store   store.Store
	Worker  string
	Timeout time.Duration
	Client  *http.Client
	Log     *slog.Logger
	Now     func() time.Time
	// Poll is how often an idle worker looks for due deliveries.
	Poll time.Duration
	// Concurrency bounds the deliveries in flight; PerURL bounds those to
	// one URL.
	Concurrency, PerURL int
}

func (w *Worker) defaults() {
	if w.Timeout == 0 {
		w.Timeout = 30 * time.Second
	}
	if w.Client == nil {
		w.Client = &http.Client{Timeout: w.Timeout}
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.Poll == 0 {
		w.Poll = idlePoll
	}
	if w.Concurrency == 0 {
		w.Concurrency = defaultConcurrency
	}
	if w.PerURL == 0 {
		w.PerURL = defaultPerURL
	}
}

// Run claims and delivers until ctx ends, then waits for the deliveries in
// flight. Deliveries run in a pool of Concurrency, refilled as each one ends,
// and at most PerURL of them go to one URL at a time: a URL that hangs ties
// up its own share, never the other sessions' webhooks.
func (w *Worker) Run(ctx context.Context) {
	w.defaults()
	slots := make(chan struct{}, w.Concurrency)
	freed := make(chan struct{}, 1)
	var wg sync.WaitGroup
	defer wg.Wait()
	var mu sync.Mutex
	perURL := map[string]int{}
	for ctx.Err() == nil {
		free := w.Concurrency - len(slots)
		if free == 0 {
			select {
			case <-ctx.Done():
				return
			case <-freed:
			}
			continue
		}
		lock := w.Timeout + 30*time.Second
		ds, err := w.Store.ClaimDeliveries(ctx, w.Worker, w.Now().UnixMilli(), lock.Milliseconds(), free)
		if err != nil {
			w.Log.Error("webhook claim", "err", err)
		}
		for _, d := range ds {
			mu.Lock()
			busy := perURL[d.URL] >= w.PerURL
			if !busy {
				perURL[d.URL]++
			}
			mu.Unlock()
			if busy {
				// Its URL is at its share: back in the queue, no attempt spent.
				if err := w.Store.PostponeDelivery(ctx, d.ID, w.Now().Add(w.Poll).UnixMilli()); err != nil {
					w.Log.Error("webhook postpone", "id", d.ID, "err", err)
				}
				continue
			}
			slots <- struct{}{}
			wg.Add(1)
			go func(d store.Delivery) {
				defer wg.Done()
				w.deliver(ctx, d)
				mu.Lock()
				perURL[d.URL]--
				if perURL[d.URL] == 0 {
					delete(perURL, d.URL)
				}
				mu.Unlock()
				<-slots
				select {
				case freed <- struct{}{}:
				default:
				}
			}(d)
		}
		if len(ds) == 0 || err != nil {
			select {
			case <-ctx.Done():
				return
			case <-freed:
			case <-time.After(w.Poll):
			}
		}
	}
}

func (w *Worker) deliver(ctx context.Context, d store.Delivery) {
	var wh waha.WebhookConfig
	_ = json.Unmarshal(d.Webhook, &wh)
	retryAfter, err := w.send(ctx, d, wh)
	if err == nil {
		if cerr := w.Store.CompleteDelivery(ctx, d.ID); cerr != nil {
			w.Log.Error("webhook complete", "id", d.ID, "err", cerr)
		}
		return
	}
	attempts, delay, policy := retryPolicy(wh.Retries)
	// d.Attempts counts the failures recorded so far; this one is the next.
	retry := d.Attempts + 1
	var next int64
	if retry <= attempts {
		next = w.Now().Add(Backoff(policy, delay, retry, retryAfter)).UnixMilli()
	} else {
		w.Log.Warn("webhook given up", "url", d.URL, "event", d.EventID, "attempts", retry, "err", err)
	}
	if rerr := w.Store.RetryDelivery(ctx, d.ID, err.Error(), next); rerr != nil {
		w.Log.Error("webhook retry", "id", d.ID, "err", rerr)
	}
}

// send makes one attempt. retryAfter is the server's Retry-After, if any.
func (w *Worker) send(ctx context.Context, d store.Delivery, wh waha.WebhookConfig) (time.Duration, error) {
	rctx, cancel := context.WithTimeout(ctx, w.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, d.URL, bytes.NewReader(d.Body))
	if err != nil {
		return 0, err
	}
	for k, v := range Headers(d, wh) {
		req.Header.Set(k, v)
	}
	resp, err := w.Client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return 0, nil
	}
	return parseRetryAfter(resp.Header.Get("Retry-After"), w.Now()), fmt.Errorf("HTTP %d", resp.StatusCode)
}

// Headers are the headers WAHA sends with a webhook body.
func Headers(d store.Delivery, wh waha.WebhookConfig) map[string]string {
	h := map[string]string{
		"Content-Type":         "application/json",
		"User-Agent":           "WAHA/" + config.Version,
		"X-Webhook-Request-Id": requestID(d.EventID),
	}
	var env struct {
		Timestamp int64 `json:"timestamp"`
	}
	if json.Unmarshal(d.Body, &env) == nil && env.Timestamp != 0 {
		h["X-Webhook-Timestamp"] = strconv.FormatInt(env.Timestamp, 10)
	}
	if wh.HMAC != nil && wh.HMAC.Key != "" {
		h["X-Webhook-Hmac"] = Sign(wh.HMAC.Key, d.Body)
		h["X-Webhook-Hmac-Algorithm"] = "sha512"
	}
	for _, c := range wh.CustomHeaders {
		if c.Name != "" {
			h[c.Name] = c.Value
		}
	}
	return h
}

// Sign is WAHA's signature: lowercase hex HMAC-SHA512 of the exact body.
func Sign(key string, body []byte) string {
	m := hmac.New(sha512.New, []byte(key))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// requestID is stable per event (and so across retries), like WAHA's.
func requestID(eventID string) string {
	id := strings.TrimPrefix(eventID, "evt_")
	if len(id) > 18 {
		id = id[len(id)-18:]
	}
	return id
}

func retryPolicy(r *waha.RetriesConfig) (attempts int, delay time.Duration, policy string) {
	attempts, delay, policy = DefaultAttempts, DefaultDelaySeconds*time.Second, "constant"
	if r == nil {
		return
	}
	if r.Attempts != nil && *r.Attempts >= 0 {
		attempts = *r.Attempts
	}
	if r.DelaySeconds != nil && *r.DelaySeconds >= 0 {
		delay = time.Duration(*r.DelaySeconds * float64(time.Second))
	}
	if r.Policy != "" {
		policy = r.Policy
	}
	return
}

// Backoff is the wait before retry number n (1-based), as axios-retry
// computes it for WAHA: constant, linear (n times the delay) or exponential
// (2^n times the delay plus up to 20% jitter), never less than Retry-After.
func Backoff(policy string, delay time.Duration, n int, retryAfter time.Duration) time.Duration {
	var d float64
	switch policy {
	case "linear":
		d = float64(n) * float64(delay)
	case "exponential":
		base := float64(delay) * math.Pow(2, float64(n))
		d = base + base*0.2*rand.Float64()
	default:
		d = float64(delay)
	}
	// Clamped as a float: 2^n overflows a Duration past n = 33, and the
	// conversion would turn the wait into nothing.
	return max(time.Duration(min(d, float64(maxBackoff))), retryAfter)
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
