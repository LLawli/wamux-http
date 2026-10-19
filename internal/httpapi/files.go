package httpapi

import (
	"errors"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"github.com/LLawli/wamux-http/internal/media"
)

// registerFiles serves received media at the links `media.url` carries,
// `/api/files/<session>/<message id>.<ext>`. WAHA serves its files folder to
// any valid key; here a key needs `read` on the session.
func (s *Server) registerFiles(r chi.Router) {
	s.route(r, http.MethodGet, "/api/files/{session}/{filename}", Route{Action: ActRead, From: FromPath}, s.serveFile)
}

// errFileNotFound is WAHA's answer for a missing file (AllExceptionsFilter,
// ENOENT).
func errFileNotFound() *Error {
	return &Error{Status: http.StatusNotFound, Body: map[string]any{"error": map[string]any{
		"code": 404, "key": "FILE_NOT_FOUND", "message": "File not found",
		"details": "File not found or no longer available",
	}}}
}

func (s *Server) serveFile(c *Ctx) error {
	f, err := s.Media.File(c.R.Context(), c.Session, c.Param("filename"))
	if errors.Is(err, media.ErrFileNotFound) {
		return errFileNotFound()
	}
	if err != nil {
		return err
	}
	file, err := os.Open(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return errFileNotFound() // expired between the check and the open
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	st, err := file.Stat()
	if err != nil {
		return err
	}
	if f.Mimetype != "" {
		c.W.Header().Set("Content-Type", f.Mimetype)
	}
	http.ServeContent(c.W, c.R, st.Name(), st.ModTime(), file)
	return nil
}
