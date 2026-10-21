package dashboard_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LLawli/wamux-http/internal/testenv"
)

// stream opens /dashboard/ui with the signals given and reads it until want
// returns true or the time is up; it returns what was read.
func stream(t *testing.T, e *testenv.Env, key string, signals map[string]string, want func(string) bool) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	raw, _ := json.Marshal(signals)
	req, _ := http.NewRequestWithContext(ctx, "GET", e.URL+"/dashboard/ui?datastar="+url.QueryEscape(string(raw)), nil)
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	req.Header.Set("Datastar-Request", "true")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	var b strings.Builder
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		b.WriteString(sc.Text() + "\n")
		if sc.Text() == "" && want(b.String()) {
			return b.String()
		}
	}
	return b.String()
}

// post sends a form (or, with signals, JSON) to a panel action and returns
// the whole answer.
func post(t *testing.T, e *testenv.Env, path string, form url.Values, signals map[string]string) string {
	t.Helper()
	var body io.Reader
	ct := "application/x-www-form-urlencoded"
	if signals != nil {
		raw, _ := json.Marshal(signals)
		body, ct = strings.NewReader(string(raw)), "application/json"
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest("POST", e.URL+"/dashboard"+path, body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Api-Key", testenv.AdminKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("%s: %d %s", path, res.StatusCode, out)
	}
	return string(out)
}

func TestShellAndAssets(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	for path, want := range map[string]string{
		"/dashboard":                                  "text/html",
		"/dashboard/":                                 "text/html",
		"/dashboard/static/datastar.js":               "text/javascript",
		"/dashboard/static/app.css":                   "text/css",
		"/dashboard/static/fonts/plex-mono-400.woff2": "font/woff2",
	} {
		r := e.DoKey("", "GET", path, nil)
		if r.Status != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), want) || len(r.Body) < 100 {
			t.Fatalf("%s: %d %q %d bytes", path, r.Status, r.Header.Get("Content-Type"), len(r.Body))
		}
	}
	shell := string(e.DoKey("", "GET", "/dashboard/", nil).Body)
	if !strings.Contains(shell, `src="/dashboard/static/datastar.js"`) || strings.Contains(shell, testenv.AdminKey) {
		t.Fatalf("shell: %s", shell)
	}
	// The API is still served next to the panel.
	if r := e.Do("GET", "/api/sessions", nil); r.Status != 200 {
		t.Fatalf("api behind the panel: %d", r.Status)
	}
}

func TestLogin(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	none := stream(t, e, "", map[string]string{}, func(string) bool { return true })
	if !strings.Contains(none, `class="login"`) || strings.Contains(none, "datastar-patch-signals") {
		t.Fatalf("no key: %s", none)
	}
	wrong := stream(t, e, "wrong", map[string]string{}, func(s string) bool { return strings.Contains(s, "datastar-patch-signals") })
	if !strings.Contains(wrong, `class="login"`) || !strings.Contains(wrong, `data: signals {"_err":"invalid key","_key":""}`) {
		t.Fatalf("wrong key: %s", wrong)
	}
	// A session key is not enough: the panel manages keys.
	e.Do("POST", "/api/sessions", map[string]any{"name": "a"})
	k := e.Do("POST", "/api/keys", map[string]any{"session": "a", "isActive": true}).Map(t)["key"].(string)
	if s := stream(t, e, k, map[string]string{}, func(string) bool { return true }); !strings.Contains(s, `class="login"`) {
		t.Fatalf("session key: %s", s)
	}
}

func TestSessionsViewIsLive(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.Do("POST", "/api/sessions", map[string]any{"name": "alpha", "config": map[string]any{"metadata": map[string]string{"team": "ops"}}})
	e.Do("POST", "/api/sessions", map[string]any{"name": "beta"})

	got := stream(t, e, testenv.AdminKey, map[string]string{"view": "sessions", "sel": "alpha", "tab": "metadata"}, func(s string) bool {
		return strings.Contains(s, `id="app"`)
	})
	for _, want := range []string{`<span class="name">alpha</span>`, `<span class="name">beta</span>`, "STOPPED", `name="mk" value="team"`, `name="mv" value="ops"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}

	// Starting the session reaches an open stream: the list follows the
	// status, and the pair tab gets the QR.
	done := make(chan string, 1)
	go func() {
		done <- stream(t, e, testenv.AdminKey, map[string]string{"view": "sessions", "sel": "alpha"}, func(s string) bool {
			return strings.Contains(s, `id="qr"><img src="data:image/png;base64,`)
		})
	}()
	time.Sleep(300 * time.Millisecond)
	e.Do("POST", "/api/sessions/alpha/start", nil)
	live := <-done
	if !strings.Contains(live, `id="qr"><img src="data:image/png;base64,`) || !strings.Contains(live, "SCAN_QR_CODE") {
		t.Fatalf("live updates:\n%s", live)
	}
}

func TestCreateSessionAndConfig(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	form := url.Values{"name": {"gamma"}, "mk": {"user.id"}, "mv": {"42"}, "wn": {"1"},
		"w0_url": {"https://example.com/hook"}, "w0_ev": {"message", "session.status"},
		"w0_attempts": {"3"}, "w0_delay": {"1.5"}, "w0_policy": {"exponential"},
		"w0_hmac": {"secret"}, "w0_hn": {"X-Tenant", ""}, "w0_hv": {"t1", "ignored"}}

	// A row button answers the form again, with the row and what was typed.
	again := post(t, e, "/sessions?op=addm", form, nil)
	if strings.Count(again, `name="mk"`) != 2 || !strings.Contains(again, `value="https://example.com/hook"`) {
		t.Fatalf("add row:\n%s", again)
	}

	created := post(t, e, "/sessions?op=create", form, nil)
	if !strings.Contains(created, `"sel":"gamma"`) {
		t.Fatalf("create: %s", created)
	}
	var info struct {
		Config struct {
			Metadata map[string]string `json:"metadata"`
			Webhooks []map[string]any  `json:"webhooks"`
		} `json:"config"`
	}
	e.Do("GET", "/api/sessions/gamma", nil).JSON(t, &info)
	wh := info.Config.Webhooks
	if info.Config.Metadata["user.id"] != "42" || len(wh) != 1 || wh[0]["url"] != "https://example.com/hook" {
		t.Fatalf("stored config: %+v", info.Config)
	}
	raw, _ := json.Marshal(wh[0])
	for _, want := range []string{`"events":["session.status","message"]`, `"attempts":3`, `"delaySeconds":1.5`, `"policy":"exponential"`, `"hmac":{"key":"secret"}`, `"customHeaders":[{"name":"X-Tenant","value":"t1"}]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("webhook %s lacks %s", raw, want)
		}
	}

	// Saving the webhooks tab keeps the rest of the config.
	save := url.Values{"wn": {"1"}, "w0_url": {"https://example.com/other"}, "w0_ev": {"*"}}
	post(t, e, "/sessions/gamma/webhooks?op=save", save, nil)
	e.Do("GET", "/api/sessions/gamma", nil).JSON(t, &info)
	if info.Config.Metadata["user.id"] != "42" || len(info.Config.Webhooks) != 1 || info.Config.Webhooks[0]["url"] != "https://example.com/other" {
		t.Fatalf("after save: %+v", info.Config)
	}

	// The API's validation reaches the panel as a toast.
	bad := post(t, e, "/sessions/gamma/webhooks?op=save", url.Values{"wn": {"1"}, "w0_url": {"ftp://x"}}, nil)
	if !strings.Contains(bad, `class="toast bad"`) || !strings.Contains(bad, "must be a URL address") {
		t.Fatalf("invalid webhook: %s", bad)
	}

	// Typed text comes back escaped.
	evil := post(t, e, "/sessions/gamma/metadata?op=addm", url.Values{"mk": {"k"}, "mv": {`"><img src=x onerror=alert(1)>`}}, nil)
	if strings.Contains(evil, "<img src=x") {
		t.Fatalf("unescaped value: %s", evil)
	}

	// Delete, through the session's actions.
	out := post(t, e, "/sessions/gamma/delete", nil, map[string]string{"view": "sessions", "sel": "gamma"})
	if !strings.Contains(out, `"sel":""`) {
		t.Fatalf("delete: %s", out)
	}
	if r := e.Do("GET", "/api/sessions/gamma", nil); r.Status != 404 {
		t.Fatalf("deleted session: %d", r.Status)
	}
}

func TestKeys(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.Do("POST", "/api/sessions", map[string]any{"name": "a"})
	e.Do("POST", "/api/sessions", map[string]any{"name": "b"})

	view := stream(t, e, testenv.AdminKey, map[string]string{"view": "keys"}, func(string) bool { return false })
	if !strings.Contains(view, `id="key-table"`) || !strings.Contains(view, `name="s" value="b"`) {
		t.Fatalf("keys view: %s", view)
	}

	out := post(t, e, "/keys", url.Values{"scope": {"session"}, "s": {"a", "b"}, "a": {"read", "send"}}, nil)
	if !strings.Contains(out, `<output aria-label="New API key">key_`) {
		t.Fatalf("create key: %s", out)
	}
	var keys []struct {
		ID       string          `json:"id"`
		Sessions []string        `json:"sessions"`
		Actions  map[string]bool `json:"actions"`
		IsActive bool            `json:"isActive"`
	}
	e.Do("GET", "/api/keys", nil).JSON(t, &keys)
	if len(keys) != 1 || strings.Join(keys[0].Sessions, ",") != "a,b" || !keys[0].Actions["send"] || keys[0].Actions["control"] {
		t.Fatalf("stored key: %+v", keys)
	}
	id := keys[0].ID

	post(t, e, "/keys/"+id+"/toggle", nil, map[string]string{"view": "keys"})
	e.Do("GET", "/api/keys", nil).JSON(t, &keys)
	if keys[0].IsActive || strings.Join(keys[0].Sessions, ",") != "a,b" {
		t.Fatalf("toggled key: %+v", keys)
	}

	post(t, e, "/keys/"+id+"/revoke", nil, map[string]string{"view": "keys"})
	e.Do("GET", "/api/keys", nil).JSON(t, &keys)
	if len(keys) != 0 {
		t.Fatalf("revoked key still listed: %+v", keys)
	}

	// A session key without sessions is refused by the API, and the form
	// comes back as it was.
	bad := post(t, e, "/keys", url.Values{"scope": {"session"}, "a": {"read"}}, nil)
	if !strings.Contains(bad, "Session is required for non-admin API key") || !strings.Contains(bad, `name="a" value="read" checked`) {
		t.Fatalf("invalid key: %s", bad)
	}
}

// Every action needs the admin key, like the stream.
func TestActionsNeedTheAdminKey(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.Do("POST", "/api/sessions", map[string]any{"name": "a"})
	req, _ := http.NewRequest("POST", e.URL+"/dashboard/sessions/a/delete", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !strings.Contains(string(body), `class="login"`) {
		t.Fatalf("no key: %s", body)
	}
	if r := e.Do("GET", "/api/sessions/a", nil); r.Status != 200 {
		t.Fatalf("session deleted without a key: %d", r.Status)
	}
}
