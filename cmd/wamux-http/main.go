// Command wamux-http serves WAHA's HTTP API on top of a wamux daemon.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/LLawli/wamux-http/internal/app"
	"github.com/LLawli/wamux-http/internal/config"
	"github.com/LLawli/wamux-http/internal/core"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/store/sqlite"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wamux-http:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)
	if cfg.APIKeyGenerated {
		log.Warn("no API key configured; generated one for this run", "WAHA_API_KEY", cfg.APIKey)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cc, err := core.Dial(cfg.CoreSocket)
	if err != nil {
		return err
	}
	defer func() { _ = cc.Close() }()

	a := app.Wire(cfg, st, cc, log)
	a.Server.Shutdown = stop
	a.Start(ctx)

	srv := &http.Server{Addr: cfg.Listen, Handler: a.Server.Router(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Listen, "core", cfg.CoreSocket, "worker", cfg.WorkerID)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openStore picks the engine from the URL scheme.
func openStore(ctx context.Context, dsn string) (store.Store, error) {
	if path, ok := strings.CutPrefix(dsn, "sqlite://"); ok {
		return sqlite.Open(ctx, path)
	}
	return nil, fmt.Errorf("WAMUX_HTTP_DATABASE_URL: unsupported engine in %q (supported: sqlite://)", dsn)
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "trace", "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error", "fatal":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "JSON" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
