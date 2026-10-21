package dashboard

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/LLawli/wamux-http/internal/waha"
)

// The panel's forms post as application/x-www-form-urlencoded and come back
// rendered again: adding or removing a row is a round trip that keeps what
// was typed, so the browser holds no form state of its own.

// retryPolicies are WAHA's webhook retry policies.
var retryPolicies = []string{"constant", "linear", "exponential"}

// pair is one custom header, or one metadata entry.
type pair struct{ K, V string }

// webhookForm is one webhook as the editor shows it.
type webhookForm struct {
	URL      string
	Events   map[string]bool
	Attempts string
	Delay    string
	Policy   string
	HMAC     string
	Headers  []pair
	Open     bool
}

// EventList is every event and whether the webhook takes it, "*" first.
func (w webhookForm) EventList() []grant {
	out := []grant{{Name: "*", Label: "*", On: w.Events["*"]}}
	for _, e := range waha.AllEvents {
		out = append(out, grant{Name: e, Label: e, On: w.Events[e]})
	}
	return out
}

// Summary is the collapsed webhook's event list.
func (w webhookForm) Summary() string {
	var on []string
	for _, e := range w.EventList() {
		if e.On {
			on = append(on, e.Name)
		}
	}
	if len(on) > 3 {
		return strings.Join(on[:3], " · ") + fmt.Sprintf(" +%d", len(on)-3)
	}
	return strings.Join(on, " · ")
}

// Policies are the retry policies and whether this webhook uses each.
func (w webhookForm) Policies() []grant {
	var out []grant
	for _, p := range retryPolicies {
		out = append(out, grant{Name: p, Label: p, On: w.Policy == p})
	}
	return out
}

func webhooksFromConfig(cfg waha.SessionConfig) []webhookForm {
	var out []webhookForm
	for _, wh := range cfg.Webhooks {
		f := webhookForm{URL: wh.URL, Events: map[string]bool{}}
		for _, e := range wh.Events {
			f.Events[e] = true
		}
		if r := wh.Retries; r != nil {
			if r.Attempts != nil {
				f.Attempts = strconv.Itoa(*r.Attempts)
			}
			if r.DelaySeconds != nil {
				f.Delay = strconv.FormatFloat(*r.DelaySeconds, 'f', -1, 64)
			}
			f.Policy = r.Policy
		}
		if wh.HMAC != nil {
			f.HMAC = wh.HMAC.Key
		}
		for _, h := range wh.CustomHeaders {
			f.Headers = append(f.Headers, pair{h.Name, h.Value})
		}
		out = append(out, f)
	}
	return out
}

// webhooksFromForm reads the editor's fields, w<i>_<field>.
func webhooksFromForm(v url.Values) []webhookForm {
	n, _ := strconv.Atoi(v.Get("wn"))
	var out []webhookForm
	for i := range min(n, 50) {
		p := fmt.Sprintf("w%d_", i)
		f := webhookForm{
			URL:      strings.TrimSpace(v.Get(p + "url")),
			Events:   map[string]bool{},
			Attempts: strings.TrimSpace(v.Get(p + "attempts")),
			Delay:    strings.TrimSpace(v.Get(p + "delay")),
			Policy:   v.Get(p + "policy"),
			HMAC:     v.Get(p + "hmac"),
			Open:     v.Get(p+"open") != "",
		}
		for _, e := range v[p+"ev"] {
			f.Events[e] = true
		}
		names, values := v[p+"hn"], v[p+"hv"]
		for j := range names {
			val := ""
			if j < len(values) {
				val = values[j]
			}
			f.Headers = append(f.Headers, pair{names[j], val})
		}
		out = append(out, f)
	}
	return out
}

// webhookOp applies an editor button: add, del, addh or delh.
func webhookOp(list []webhookForm, op string, i, j int) []webhookForm {
	switch op {
	case "add":
		for k := range list {
			list[k].Open = false
		}
		list = append(list, webhookForm{Events: map[string]bool{"message": true, "session.status": true}, Open: true})
	case "del":
		if i >= 0 && i < len(list) {
			list = append(list[:i], list[i+1:]...)
		}
	case "addh":
		if i >= 0 && i < len(list) {
			list[i].Headers = append(list[i].Headers, pair{})
			list[i].Open = true
		}
	case "delh":
		if i >= 0 && i < len(list) && j >= 0 && j < len(list[i].Headers) {
			list[i].Headers = append(list[i].Headers[:j], list[i].Headers[j+1:]...)
			list[i].Open = true
		}
	}
	return list
}

// webhookJSON is a webhook as SessionConfig.webhooks holds it; blank fields
// are left out so WAHA's defaults apply.
func webhookJSON(f webhookForm) (map[string]any, error) {
	wh := map[string]any{"url": f.URL}
	events := []string{}
	for _, e := range f.EventList() {
		if e.On {
			events = append(events, e.Name)
		}
	}
	wh["events"] = events
	retries := map[string]any{}
	if f.Attempts != "" {
		n, err := strconv.Atoi(f.Attempts)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("attempts must be a whole number")
		}
		retries["attempts"] = n
	}
	if f.Delay != "" {
		d, err := strconv.ParseFloat(f.Delay, 64)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("delay must be a number of seconds")
		}
		retries["delaySeconds"] = d
	}
	if f.Policy != "" {
		retries["policy"] = f.Policy
	}
	if len(retries) > 0 {
		wh["retries"] = retries
	}
	if f.HMAC != "" {
		wh["hmac"] = map[string]string{"key": f.HMAC}
	}
	var headers []map[string]string
	for _, h := range f.Headers {
		if strings.TrimSpace(h.K) != "" {
			headers = append(headers, map[string]string{"name": strings.TrimSpace(h.K), "value": h.V})
		}
	}
	if len(headers) > 0 {
		wh["customHeaders"] = headers
	}
	return wh, nil
}

func pairsFromForm(v url.Values, kName, vName string) []pair {
	keys, vals := v[kName], v[vName]
	var out []pair
	for i := range keys {
		val := ""
		if i < len(vals) {
			val = vals[i]
		}
		out = append(out, pair{keys[i], val})
	}
	return out
}

func pairsOp(list []pair, op string, i int) []pair {
	switch op {
	case "addm":
		list = append(list, pair{})
	case "delm":
		if i >= 0 && i < len(list) {
			list = append(list[:i], list[i+1:]...)
		}
	}
	return list
}

func metadataJSON(list []pair) map[string]string {
	out := map[string]string{}
	for _, p := range list {
		if k := strings.TrimSpace(p.K); k != "" {
			out[k] = p.V
		}
	}
	return out
}

func metadataFromConfig(cfg waha.SessionConfig) []pair {
	var out []pair
	for k, v := range cfg.Metadata {
		out = append(out, pair{k, v})
	}
	slices.SortFunc(out, func(a, b pair) int { return strings.Compare(a.K, b.K) })
	return out
}

// configWith returns the session's stored config with one key replaced,
// keeping every other key as it was.
func configWith(raw json.RawMessage, field string, value any) map[string]any {
	cfg := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &cfg)
	}
	if value == nil {
		delete(cfg, field)
	} else {
		cfg[field] = value
	}
	return cfg
}

func parseConfig(raw json.RawMessage) waha.SessionConfig {
	var cfg waha.SessionConfig
	if len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &cfg)
	}
	return cfg
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}
