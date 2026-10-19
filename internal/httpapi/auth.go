package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/LLawli/wamux-http/internal/store"
)

// Action is what a route needs from a session key (WAHA's SessionActions),
// plus the two server-level grants.
type Action string

// The actions a route can require.
const (
	ActRead     Action = "read"
	ActSend     Action = "send"
	ActControl  Action = "control"
	ActSetting  Action = "setting"
	ActDelete   Action = "delete"
	ActApp      Action = "app"
	ActManage   Action = "manage"   // admin only: keys, server control, /health
	ActRetrieve Action = "retrieve" // any key: server version and status
	ActList     Action = "list"     // any key: the session list, filtered
)

// Principal is who a request authenticated as.
type Principal struct {
	Admin   bool
	Session string          // the only session a non-admin key may touch
	Actions map[string]bool // nil means WAHA's defaults
}

var defaultActions = map[string]bool{"delete": false, "setting": true, "control": true, "app": true, "read": true, "send": true}

// Allows applies WAHA's policy for one action on one session.
func (p Principal) Allows(a Action, session string) bool {
	if p.Admin {
		return true
	}
	switch a {
	case ActManage:
		return false
	case ActRetrieve, ActList:
		return true
	}
	if session == "" || session != p.Session {
		return false
	}
	actions := p.Actions
	if actions == nil {
		actions = defaultActions
	}
	return actions[string(a)]
}

// keyFrom reads the key the way WAHA does: the header wins over the query.
func keyFrom(r *http.Request) string {
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	for name, v := range r.URL.Query() {
		if strings.EqualFold(name, "x-api-key") && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// HashKey is how a stored key is looked up.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// matchesAdmin compares a presented key with the configured admin key, which
// is plain or "sha512:<hex>", in constant time.
func matchesAdmin(configured, presented string) bool {
	if configured == "" || presented == "" {
		return false
	}
	if want, ok := strings.CutPrefix(configured, "sha512:"); ok {
		sum := sha512.Sum512([]byte(presented))
		return subtle.ConstantTimeCompare([]byte(strings.ToLower(want)), []byte(hex.EncodeToString(sum[:]))) == 1
	}
	return subtle.ConstantTimeCompare([]byte(configured), []byte(presented)) == 1
}

// authenticate resolves the request's key to a principal.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if s.Config.NoAPIKey {
		return Principal{Admin: true}, nil
	}
	for _, p := range s.Config.APIKeyExcludeDir {
		if strings.HasPrefix(r.URL.Path, p) {
			return Principal{Admin: true}, nil
		}
	}
	key := keyFrom(r)
	if key == "" {
		return Principal{}, ErrUnauthorized()
	}
	if matchesAdmin(s.Config.APIKey, key) {
		return Principal{Admin: true}, nil
	}
	k, err := s.Store.GetAPIKeyByHash(ctx, HashKey(key))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !k.IsActive) {
		return Principal{}, ErrUnauthorized()
	}
	if err != nil {
		return Principal{}, ErrUnauthorized()
	}
	p := Principal{Admin: k.IsAdmin, Session: k.Session}
	if len(k.Actions) > 0 {
		p.Actions = map[string]bool{}
		_ = json.Unmarshal(k.Actions, &p.Actions)
	}
	return p, nil
}
