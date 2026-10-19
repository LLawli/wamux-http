package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

func (s *Server) registerObservability(r chi.Router) {
	// /ping is the one route with no key at all.
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"message": "pong"})
	})
	s.registered["GET /ping"] = true

	s.route(r, "GET", "/health", Route{Action: ActManage}, func(c *Ctx) error {
		ctx, cancel := context.WithTimeout(c.R.Context(), 3*time.Second)
		defer cancel()
		info, errs := map[string]any{}, map[string]any{}
		if _, err := s.Store.ListSessions(ctx); err != nil {
			errs["store"] = map[string]string{"status": "down", "message": err.Error()}
		} else {
			info["store"] = map[string]string{"status": "up"}
		}
		if h, err := s.Core.Admin.Check(ctx, &pb.Empty{}); err != nil || !h.GetReady() {
			msg := "not ready"
			if err != nil {
				msg = err.Error()
			}
			errs["core"] = map[string]string{"status": "down", "message": msg}
		} else {
			info["core"] = map[string]string{"status": "up", "version": h.GetVersion()}
		}
		details := map[string]any{}
		for k, v := range info {
			details[k] = v
		}
		for k, v := range errs {
			details[k] = v
		}
		if len(errs) > 0 {
			return c.JSON(http.StatusServiceUnavailable, map[string]any{"status": "error", "info": info, "error": errs, "details": details})
		}
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "info": info, "error": errs, "details": details})
	})

	version := func(c *Ctx) error { return c.JSON(http.StatusOK, s.Pub.Environment) }
	s.route(r, "GET", "/api/version", Route{Action: ActRetrieve}, version)
	s.route(r, "GET", "/api/server/version", Route{Action: ActRetrieve}, version)

	s.route(r, "GET", "/api/server/status", Route{Action: ActRetrieve}, func(c *Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{
			"startTimestamp": s.Started.UnixMilli(),
			"uptime":         time.Since(s.Started).Milliseconds(),
			"worker":         map[string]string{"id": s.Config.WorkerID},
		})
	})

	s.route(r, "GET", "/api/server/environment", Route{Action: ActManage}, func(c *Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{
			"WAHA_BASE_URL":           s.Config.BaseURL,
			"WAHA_WORKER_ID":          s.Config.WorkerID,
			"WHATSAPP_DEFAULT_ENGINE": s.Pub.Environment.Engine,
			"WHATSAPP_FILES_FOLDER":   s.Config.FilesFolder,
			"WHATSAPP_DOWNLOAD_MEDIA": s.Config.DownloadMedia,
		})
	})

	s.route(r, "POST", "/api/server/stop", Route{Action: ActManage}, func(c *Ctx) error {
		if s.Shutdown == nil {
			return ErrNotImplemented("")
		}
		go s.Shutdown()
		return c.JSON(http.StatusCreated, map[string]bool{"stopping": true})
	})
}
