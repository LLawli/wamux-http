package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/go-chi/chi/v5"
)

// api calls the edge's own WAHA API in process, with the key the browser
// sent: the panel is a WAHA client, so every rule and check of the API
// applies to it as to any other.
type api struct {
	h   http.Handler
	key string
}

// reply is one API answer.
type reply struct {
	Status int
	Body   []byte
}

func (a api) do(ctx context.Context, method, path string, body any) reply {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return reply{Status: http.StatusInternalServerError, Body: []byte(`{"message":"` + err.Error() + `"}`)}
		}
		rd = bytes.NewReader(raw)
	}
	// The panel's own request carries chi's routing state for /dashboard;
	// left in place, the API's router would route by it instead of path.
	ctx = context.WithValue(ctx, chi.RouteCtxKey, nil)
	req := httptest.NewRequestWithContext(ctx, method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if a.key != "" {
		req.Header.Set("X-Api-Key", a.key)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return reply{Status: rec.Code, Body: rec.Body.Bytes()}
}

func (r reply) ok() bool { return r.Status >= 200 && r.Status < 300 }

func (r reply) decode(v any) error { return json.Unmarshal(r.Body, v) }

// message is the WAHA error message, a string or a list of them.
func (r reply) message() string {
	var e struct {
		Message any `json:"message"`
	}
	if json.Unmarshal(r.Body, &e) != nil {
		return http.StatusText(r.Status)
	}
	switch m := e.Message.(type) {
	case string:
		return m
	case []any:
		var parts []string
		for _, p := range m {
			if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "; ")
	}
	return http.StatusText(r.Status)
}
