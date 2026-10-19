package waha

import "testing"

func TestEventMatches(t *testing.T) {
	cases := []struct {
		sel   []string
		event string
		want  bool
	}{
		{[]string{"*"}, "message", true},
		{[]string{"*"}, "state.change", false},
		{[]string{"*"}, "engine.event", false},
		{[]string{"message.*"}, "message.ack", true},
		{[]string{"message.*"}, "message", false},
		{[]string{"message"}, "message.any", false},
		{[]string{"session.status", "message"}, "message", true},
		{[]string{"state.change"}, "state.change", true},
		{nil, "message", false},
	}
	for _, c := range cases {
		if got := EventMatches(c.sel, c.event); got != c.want {
			t.Errorf("EventMatches(%v, %q) = %v", c.sel, c.event, got)
		}
	}
}
