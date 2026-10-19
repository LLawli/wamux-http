package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// APIKeyDTO is WAHA's ApiKeyDTO. Key is the plain key only in the answer that
// creates it; listings show it masked, because the store keeps only a hash
// (a deliberate difference from WAHA, which returns stored keys in full).
type APIKeyDTO struct {
	ID       string          `json:"id"`
	Key      string          `json:"key"`
	IsActive bool            `json:"isActive"`
	IsAdmin  bool            `json:"isAdmin"`
	Session  *string         `json:"session"`
	Actions  json.RawMessage `json:"actions"`
}

func keyDTO(k store.APIKey, plain string) APIKeyDTO {
	d := APIKeyDTO{ID: k.ID, Key: plain, IsActive: k.IsActive, IsAdmin: k.IsAdmin, Actions: orNull(k.Actions)}
	if d.Key == "" {
		d.Key = "key_****" + k.Hint
	}
	if k.Session != "" {
		sess := k.Session
		d.Session = &sess
	}
	return d
}

const keyAlphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// newKey is WAHA's key format: "key_" + 32 characters of [0-9a-zA-Z].
func newKey() (string, error) {
	b := make([]byte, 32)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(keyAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = keyAlphabet[n.Int64()]
	}
	return "key_" + string(b), nil
}

type keyRequest struct {
	IsAdmin  bool            `json:"isAdmin"`
	Session  *string         `json:"session"`
	IsActive *bool           `json:"isActive"`
	Actions  json.RawMessage `json:"actions"`
}

// check applies WAHA's invariants on a key's shape.
func (s *Server) checkKey(ctx context.Context, req keyRequest) (store.APIKey, error) {
	k := store.APIKey{IsAdmin: req.IsAdmin, IsActive: req.IsActive == nil || *req.IsActive}
	if req.Session != nil {
		k.Session = *req.Session
	}
	if len(req.Actions) > 0 && string(req.Actions) != "null" {
		var a map[string]bool
		if err := json.Unmarshal(req.Actions, &a); err != nil {
			return k, ErrBadRequest([]string{"actions must be an object of booleans"})
		}
		k.Actions = req.Actions
	}
	switch {
	case k.IsAdmin && k.Session != "":
		return k, ErrUnprocessable("Admin API key cannot be assigned to a session")
	case !k.IsAdmin && k.Session == "":
		return k, ErrUnprocessable("Session is required for non-admin API key")
	case k.IsAdmin && len(k.Actions) > 0:
		return k, ErrUnprocessable("Actions can be set only for session API keys")
	}
	if k.Session != "" {
		if _, err := s.Store.GetSession(ctx, k.Session); errors.Is(err, store.ErrNotFound) {
			return k, ErrUnprocessable("Session '" + k.Session + "' does not exist")
		} else if err != nil {
			return k, err
		}
	}
	return k, nil
}

// mint stores a new key with the given id and returns its plain value.
func (s *Server) mint(ctx context.Context, k store.APIKey) (store.APIKey, string, error) {
	plain, err := newKey()
	if err != nil {
		return k, "", err
	}
	k.Hash, k.Hint, k.CreatedAt = HashKey(plain), plain[len(plain)-4:], waha.NowMs()
	return k, plain, s.Store.CreateAPIKey(ctx, k)
}

func (s *Server) registerKeys(r chi.Router) {
	s.route(r, "GET", "/api/keys", Route{Action: ActManage}, func(c *Ctx) error {
		keys, err := s.Store.ListAPIKeys(c.R.Context())
		if err != nil {
			return err
		}
		out := []APIKeyDTO{}
		for _, k := range keys {
			out = append(out, keyDTO(k, ""))
		}
		return c.JSON(http.StatusOK, out)
	})

	s.route(r, "POST", "/api/keys", Route{Action: ActManage}, func(c *Ctx) error {
		var req keyRequest
		if err := c.Bind(&req); err != nil {
			return err
		}
		k, err := s.checkKey(c.R.Context(), req)
		if err != nil {
			return err
		}
		k.ID = waha.NewPrefixedID("key_id_")
		k, plain, err := s.mint(c.R.Context(), k)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, keyDTO(k, plain))
	})

	s.route(r, "PUT", "/api/keys/{id}", Route{Action: ActManage}, func(c *Ctx) error {
		var req keyRequest
		if err := c.Bind(&req); err != nil {
			return err
		}
		old, err := s.Store.GetAPIKey(c.R.Context(), c.Param("id"))
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound("API key not found")
		}
		if err != nil {
			return err
		}
		k, err := s.checkKey(c.R.Context(), req)
		if err != nil {
			return err
		}
		k.ID, k.Hash, k.Hint, k.CreatedAt = old.ID, old.Hash, old.Hint, old.CreatedAt
		if err := s.Store.UpdateAPIKey(c.R.Context(), k); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, keyDTO(k, ""))
	})

	s.route(r, "DELETE", "/api/keys/{id}", Route{Action: ActManage}, func(c *Ctx) error {
		err := s.Store.DeleteAPIKey(c.R.Context(), c.Param("id"))
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound("API key not found")
		}
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]bool{"result": true})
	})

	// Scoped keys have a fixed id per session. WAHA hands the existing key
	// back; with only a hash stored, the key is rotated and the new one
	// returned instead.
	scoped := func(path string, act Action, kind string, actions string) {
		s.route(r, "POST", path, Route{Action: act, From: FromBody}, func(c *Ctx) error {
			ctx := c.R.Context()
			if _, err := s.Store.GetSession(ctx, c.Session); errors.Is(err, store.ErrNotFound) {
				return ErrUnprocessable("Session '" + c.Session + "' does not exist")
			} else if err != nil {
				return err
			}
			id := "key_id_" + kind + "_" + c.Session
			if err := s.Store.DeleteAPIKey(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			k, plain, err := s.mint(ctx, store.APIKey{ID: id, Session: c.Session, IsActive: true, Actions: json.RawMessage(actions)})
			if err != nil {
				return err
			}
			return c.JSON(http.StatusCreated, keyDTO(k, plain))
		})
	}
	scoped("/api/keys/media", ActRead, "media", `{"read":true}`)
	scoped("/api/keys/control", ActControl, "control", `{"control":true}`)
}
