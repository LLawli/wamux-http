package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/go-chi/chi/v5"

	"github.com/LLawli/wamux-http/contract"
)

// Divergences are places where WAHA itself does not follow its OpenAPI
// document, and this edge follows WAHA. Key: "METHOD /path STATUS"; value:
// why. A response matching one of these is not checked against the schema.
var Divergences = map[string]string{
	"GET /api/{session}/groups 200": "GOWS answers the list as an array (PaginatorInMemory over the engine's list); the document says object",
}

var (
	specOnce sync.Once
	spec     *openapi3.T
	matcher  *chi.Mux
	specErr  error
)

func loadSpec() {
	raw, err := normalize(contract.OpenAPI)
	if err != nil {
		specErr = err
		return
	}
	loader := openapi3.NewLoader()
	spec, specErr = loader.LoadFromData(raw)
	if specErr != nil {
		return
	}
	matcher = chi.NewRouter()
	for _, op := range contract.Operations() {
		matcher.MethodFunc(op.Method, op.Path, func(http.ResponseWriter, *http.Request) {})
	}
}

// CheckContract validates one response against the operation's schema for
// its status. A status the operation does not document, or a body that is
// not JSON, is not checked: WAHA documents only its success answers.
func CheckContract(t testing.TB, req *http.Request, status int, header http.Header, body []byte) {
	t.Helper()
	specOnce.Do(loadSpec)
	if specErr != nil {
		t.Fatalf("contract: %v", specErr)
	}
	rctx := chi.NewRouteContext()
	if !matcher.Match(rctx, req.Method, req.URL.Path) {
		return // not a WAHA operation (/api/files, /ws, ...)
	}
	pattern := rctx.RoutePattern()
	if _, ok := Divergences[req.Method+" "+pattern+" "+itoa(status)]; ok {
		return
	}
	item := spec.Paths.Find(pattern)
	if item == nil {
		return
	}
	op := item.GetOperation(req.Method)
	if op == nil || op.Responses == nil || op.Responses.Status(status) == nil {
		return
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "application/json") || len(bytes.TrimSpace(body)) == 0 {
		return
	}
	params := map[string]string{}
	for i, k := range rctx.URLParams.Keys {
		params[k] = rctx.URLParams.Values[i]
	}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: params,
			Route:      &routers.Route{Spec: spec, Path: pattern, PathItem: item, Method: req.Method, Operation: op},
			Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		},
		Status: status,
		Header: header,
		Body:   io.NopCloser(bytes.NewReader(body)),
		Options: &openapi3filter.Options{
			IncludeResponseStatus: true,
			MultiError:            true,
		},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		t.Errorf("contract: %s %s answered %d with a body that breaks WAHA's schema:\n%v\nbody: %s", req.Method, pattern, status, err, truncate(body))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// normalize drops the boolean `required` WAHA's document puts on some schema
// properties (OpenAPI wants a list of names on the parent), which no strict
// loader accepts. Parameters keep theirs: there a boolean is correct.
func normalize(doc []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, err
	}
	var walk func(any, bool)
	walk = func(n any, inParams bool) {
		switch x := n.(type) {
		case map[string]any:
			if _, isBool := x["required"].(bool); isBool && !inParams {
				delete(x, "required")
			}
			for k, c := range x {
				walk(c, k == "parameters")
			}
		case []any:
			for _, c := range x {
				walk(c, inParams)
			}
		}
	}
	walk(v, false)
	// WAHA declares 3.1.0 but writes 3.0 (nullable, boolean required); a 3.1
	// validator ignores `nullable`, so the copy says what the document is.
	v.(map[string]any)["openapi"] = "3.0.3"
	schemas := v.(map[string]any)["components"].(map[string]any)["schemas"].(map[string]any)
	for _, f := range specFixes {
		node := schemas[f.schema]
		for _, step := range f.path {
			m, ok := node.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("spec fix %s %v: no %q", f.schema, f.path, step)
			}
			node = m[step]
		}
		m, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("spec fix %s %v: not an object", f.schema, f.path)
		}
		f.apply(m)
	}
	return json.Marshal(v)
}

// specFix changes one node of one schema.
type specFix struct {
	schema string
	path   []string
	why    string
	apply  func(map[string]any)
}

// specFixes correct WAHA's OpenAPI where the running WAHA disagrees with it
// (docs/waha-behavior.md): the server sends null for these, and a webhook's
// events are strings.
var specFixes = []specFix{
	{"WebhookConfig", []string{"properties", "events", "items"}, "events are event names", func(m map[string]any) { m["type"] = "string" }},
	{"SessionInfo", []string{"properties", "me"}, "null before the first login", nullable},
	{"SessionInfo", []string{"properties", "presence"}, "null unless online/offline is known", nullable},
	{"SessionInfo", []string{"properties", "config"}, "null for a session created without one", nullable},
	{"SessionInfo", []string{"properties", "timestamps", "properties", "activity"}, "null while not WORKING", nullable},
	{"SessionDTO", []string{"properties", "config"}, "null for a session created without one", nullable},
	{"MeInfo", []string{"properties", "reachoutTimelock"}, "null without a restriction", nullable},
	{"MeInfo", []string{"properties", "messageCapping"}, "null without a restriction", nullable},
	{"ApiKeyDTO", []string{"properties", "actions"}, "null means the default actions", nullable},
	{"LidToPhoneNumber", []string{"properties", "lid"}, "null for an unknown phone number", nullable},
	{"LidToPhoneNumber", []string{"properties", "pn"}, "null for an unknown lid", nullable},
}

// nullable marks a property nullable; a bare $ref gets wrapped in allOf, since
// siblings of $ref are ignored.
func nullable(m map[string]any) {
	if ref, ok := m["$ref"]; ok {
		delete(m, "$ref")
		m["allOf"] = []any{map[string]any{"$ref": ref}}
	}
	m["nullable"] = true
}

func truncate(b []byte) string {
	if len(b) > 600 {
		return string(b[:600]) + "..."
	}
	return string(b)
}

// The messages, acks and chat reads: GOWS sends null where WAHA's document
// says string or object (docs/waha-behavior.md, section 6), and leaves
// mediaUrl out.
func init() {
	null := func(schema, why string, props ...string) []specFix {
		var out []specFix
		for _, p := range props {
			out = append(out, specFix{schema, []string{"properties", p}, why, nullable})
		}
		return out
	}
	noMediaURL := func(schema string) specFix {
		return specFix{schema, nil, "GOWS never sets mediaUrl", func(m map[string]any) { m["required"] = without(m["required"], "mediaUrl") }}
	}
	fixes := [][]specFix{
		null("WAMessage", "null outside groups, or when the message has none", "to", "participant", "body", "media", "location", "vCards", "replyTo"),
		{noMediaURL("WAMessage")},
		null("WAMessageEditedBody", "null outside groups, or when the message has none", "to", "participant", "media", "location", "vCards", "replyTo"),
		{noMediaURL("WAMessageEditedBody")},
		null("WAMedia", "null when the file is not served, or has no name", "url", "filename"),
		null("ReplyToMessage", "null when the quote does not say", "participant", "body", "media"),
		null("WAMessageAckBody", "null outside groups", "to", "participant"),
		null("WAMessageReaction", "null outside groups", "to", "participant"),
		null("WAMessageRevokedBody", "null when the revoked message is unknown", "before"),
		null("PollVote", "null outside groups", "to", "participant"),
		null("WAHAPresenceData", "null for chat states and unknown last seen", "lastSeen"),
		null("ChatSummary", "null for a chat with no stored message", "lastMessage"),
		null("ChatPictureResponse", "null when the chat has no picture", "url"),
		null("GroupParticipant", "null when the phone number is unknown", "pn"),
	}
	for _, f := range fixes {
		specFixes = append(specFixes, f...)
	}
}

func without(list any, name string) []any {
	var out []any
	items, _ := list.([]any)
	for _, v := range items {
		if v != name {
			out = append(out, v)
		}
	}
	return out
}

// CheckWebhook validates a received envelope's payload against the schema
// WAHA documents for its event: the payload of the webhook schema whose
// event defaults to that name (WAHAWebhookMessage, WebhookGroupV2Join, ...).
func CheckWebhook(t testing.TB, ev map[string]any) {
	t.Helper()
	specOnce.Do(loadSpec)
	if specErr != nil {
		t.Fatalf("contract: %v", specErr)
	}
	name, _ := ev["event"].(string)
	var payload *openapi3.Schema
	for _, ref := range spec.Components.Schemas {
		if ref.Value == nil {
			continue
		}
		evProp := ref.Value.Properties["event"]
		if evProp == nil || evProp.Value == nil || evProp.Value.Default != name {
			continue
		}
		if p := ref.Value.Properties["payload"]; p != nil {
			payload = p.Value
		}
	}
	if payload == nil {
		t.Errorf("contract: no webhook schema documents event %q", name)
		return
	}
	if err := payload.VisitJSON(ev["payload"], openapi3.MultiErrors()); err != nil {
		t.Errorf("contract: %s payload breaks WAHA's schema:\n%v\npayload: %v", name, err, ev["payload"])
	}
}
