package fakecore

import (
	"sync"

	"google.golang.org/protobuf/proto"
)

// RequestLog records the requests a fake service received, for the fakes a
// test passes in Options (which cannot reach Core's own record).
type RequestLog struct {
	mu   sync.Mutex
	reqs []proto.Message
}

func (l *RequestLog) log(m proto.Message) {
	l.mu.Lock()
	l.reqs = append(l.reqs, proto.Clone(m))
	l.mu.Unlock()
}

// Logged returns every request l recorded whose type is T, in order.
func Logged[T proto.Message](l *RequestLog) []T {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []T
	for _, r := range l.reqs {
		if v, ok := r.(T); ok {
			out = append(out, v)
		}
	}
	return out
}
