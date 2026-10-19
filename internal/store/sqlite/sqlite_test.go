package sqlite

import (
	"context"
	"path/filepath"
	"sync"
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

// Several edge processes starting together on a new database all open it:
// each migration runs once, whoever gets there first.
func TestConcurrentOpenMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(context.Background(), path)
			if err == nil {
				err = s.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent open: %v", err)
		}
	}
}
