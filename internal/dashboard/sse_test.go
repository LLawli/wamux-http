package dashboard

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The writer must produce, byte for byte, what Datastar's SDK test suite
// expects for the same input.
func TestSSEMatchesDatastarCases(t *testing.T) {
	inputs, err := filepath.Glob("testdata/sse/*/input.json")
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, in := range inputs {
		dirs = append(dirs, filepath.Dir(in))
	}
	if len(dirs) < 7 {
		t.Fatalf("expected the SDK cases, found %d", len(dirs))
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, "input.json"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(dir, "output.txt"))
			if err != nil {
				t.Fatal(err)
			}
			var in struct {
				Events []struct {
					Type              string         `json:"type"`
					Elements          string         `json:"elements"`
					Selector          string         `json:"selector"`
					Mode              string         `json:"mode"`
					UseViewTransition bool           `json:"useViewTransition"`
					Signals           map[string]any `json:"signals"`
					OnlyIfMissing     bool           `json:"onlyIfMissing"`
				} `json:"events"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			s := newSSE(rec)
			for _, ev := range in.Events {
				switch ev.Type {
				case "patchElements":
					err = s.patchElements(ev.Elements, patchOpts{Selector: ev.Selector, Mode: ev.Mode, UseViewTransition: ev.UseViewTransition})
				case "patchSignals":
					err = s.patchSignals(ev.Signals, ev.OnlyIfMissing)
				default:
					t.Fatalf("unexpected event type %q", ev.Type)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := rec.Body.String(); got != string(want) {
				t.Fatalf("got:\n%q\nwant:\n%q", got, want)
			}
		})
	}
}
