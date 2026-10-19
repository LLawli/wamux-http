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
var Divergences = map[string]string{}

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

// specFixes correct WAHA's OpenAPI where the running WAHA disagrees with it
// (docs/waha-behavior.md): the server sends null for these, and a webhook's
// events are strings.
var specFixes = []struct {
	schema string
	path   []string
	why    string
	apply  func(map[string]any)
}{
	{"WebhookConfig", []string{"properties", "events", "items"}, "events are event names", func(m map[string]any) { m["type"] = "string" }},
	{"SessionInfo", []string{"properties", "me"}, "null before the first login", nullable},
	{"SessionInfo", []string{"properties", "presence"}, "null unless online/offline is known", nullable},
	{"SessionInfo", []string{"properties", "config"}, "null for a session created without one", nullable},
	{"SessionInfo", []string{"properties", "timestamps", "properties", "activity"}, "null while not WORKING", nullable},
	{"SessionDTO", []string{"properties", "config"}, "null for a session created without one", nullable},
	{"MeInfo", []string{"properties", "reachoutTimelock"}, "null without a restriction", nullable},
	{"MeInfo", []string{"properties", "messageCapping"}, "null without a restriction", nullable},
	{"ApiKeyDTO", []string{"properties", "actions"}, "null means the default actions", nullable},
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
