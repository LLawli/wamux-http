package dashboard

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"

	"github.com/LLawli/wamux-http/internal/webhooks"
)

// The templates build Datastar expressions with these. html/template treats
// a data-on:* attribute as JavaScript (it drops the data- prefix and sees
// on...), so a plain string placed there would be quoted; these return
// template.JS built from jsString, which quotes every dynamic part itself.

// jsString is s as a JavaScript string literal. json.Marshal escapes <, >
// and &, so the literal cannot end a script or an attribute early.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

const keyHeader = `{'X-Api-Key': $_key}`

func funcs() template.FuncMap {
	return template.FuncMap{
		// post sends the signals to a panel path.
		"post": func(path string) template.JS {
			return template.JS(fmt.Sprintf("@post(%s, {headers: %s})", jsString(Base+path), keyHeader))
		},
		// form sends the closest form to a panel path.
		"form": func(path string) template.JS {
			return template.JS(fmt.Sprintf("@post(%s, {headers: %s, contentType: 'form'})", jsString(Base+path), keyHeader))
		},
		// nav moves to a view; the stream reopens through _go.
		"nav": func(view, sel, tab string) template.JS {
			return template.JS(fmt.Sprintf("$view = %s; $sel = %s; $tab = %s; $_go = Date.now()", jsString(view), jsString(sel), jsString(tab)))
		},
		// arm runs a destructive action on a second click within 3 s; the
		// first click arms the button (see the .armed style).
		"arm": func(id string, action template.JS) template.JS {
			q := jsString(id)
			return template.JS(fmt.Sprintf("$_arm === %s ? ($_arm = '', %s) : ($_arm = %s, setTimeout(() => $_arm === %s && ($_arm = ''), 3000))", q, action, q, q))
		},
		// armed is the data-class expression of an armable button.
		"armed": func(id string) string { return "$_arm === " + jsString(id) },
		"path": func(parts ...string) string {
			return strings.Join(parts, "")
		},
		"esc":      url.PathEscape,
		"initials": initials,
		// dict builds a template argument from name, value pairs.
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of arguments")
			}
			m := map[string]any{}
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		// The retry defaults the webhook worker applies, as placeholders.
		"attemptsDefault": func() string { return strconv.Itoa(webhooks.DefaultAttempts) },
		"delayDefault":    func() string { return strconv.Itoa(webhooks.DefaultDelaySeconds) },
	}
}

// initials are up to two letters of a name, for an avatar without picture.
func initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		for _, r := range w {
			out = append(out, r)
			break
		}
		if len(out) == 2 {
			break
		}
	}
	return strings.ToUpper(string(out))
}
