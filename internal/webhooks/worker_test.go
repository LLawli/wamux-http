package webhooks

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	if d := Backoff("constant", 2*time.Second, 5, 0); d != 2*time.Second {
		t.Errorf("constant: %v", d)
	}
	if d := Backoff("linear", 2*time.Second, 3, 0); d != 6*time.Second {
		t.Errorf("linear: %v", d)
	}
	if d := Backoff("exponential", time.Second, 3, 0); d < 8*time.Second || d > 9600*time.Millisecond {
		t.Errorf("exponential: %v", d)
	}
	if d := Backoff("constant", time.Second, 1, 30*time.Second); d != 30*time.Second {
		t.Errorf("Retry-After wins: %v", d)
	}
	// Far past where 2^n overflows a Duration, the wait stays the ceiling.
	for _, n := range []int{33, 40, 64, 1000} {
		if d := Backoff("exponential", 2*time.Second, n, 0); d != maxBackoff {
			t.Errorf("exponential n=%d: %v", n, d)
		}
	}
}
