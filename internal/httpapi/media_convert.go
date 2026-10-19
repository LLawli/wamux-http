package httpapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/LLawli/wamux-http/internal/media"
)

// The media tag: convert a voice note or a video to the format WhatsApp
// plays, with the same ffmpeg pipelines the sends use.
func (s *Server) registerMedia(r chi.Router) {
	rt := Route{Action: ActRead, From: FromPath, Working: true}
	s.route(r, http.MethodPost, "/api/{session}/media/convert/voice", rt,
		s.convert(media.KindVoice, media.MimeVoice, "output.opus", (*media.Converter).Voice))
	s.route(r, http.MethodPost, "/api/{session}/media/convert/video", rt,
		s.convert(media.KindVideo, media.MimeVideo, "output.mp4", (*media.Converter).Video))
}

// convert answers the converted file: the binary with a download name, or,
// for `Accept: application/json`, WAHA's Base64File (BufferResponseInterceptor).
func (s *Server) convert(kind media.Kind, mimetype, filename string, fn func(*media.Converter, context.Context, []byte) ([]byte, error)) Handler {
	return func(c *Ctx) error {
		var req struct {
			URL  string `json:"url"`
			Data string `json:"data"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		if req.URL == "" && req.Data == "" {
			return ErrUnprocessable(`Either "url" or "data" must be specified.`)
		}
		blob, err := s.Media.Load(c.R.Context(), &media.File{URL: req.URL, Data: req.Data}, kind)
		if err != nil {
			return mediaError(err)
		}
		out, err := fn(s.Media.Converter(), c.R.Context(), blob.Data)
		if err != nil {
			return err
		}
		if c.R.Header.Get("Accept") == "application/json" {
			return c.JSON(http.StatusCreated, map[string]string{"mimetype": mimetype, "data": base64.StdEncoding.EncodeToString(out)})
		}
		h := c.W.Header()
		h.Set("Content-Type", mimetype)
		h.Set("Content-Length", strconv.Itoa(len(out)))
		h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		c.W.WriteHeader(http.StatusCreated)
		_, _ = c.W.Write(out)
		return nil
	}
}
