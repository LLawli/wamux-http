package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T, key string) store.Store {
		s, err := Open(context.Background(), filepath.Join(sharedDir(t), key+".db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

var dirs = map[*testing.T]string{}

// sharedDir gives every opener call inside one subtest the same directory,
// so two openers with one key share a database file.
func sharedDir(t *testing.T) string {
	if d, ok := dirs[t]; ok {
		return d
	}
	d := t.TempDir()
	dirs[t] = d
	return d
}
