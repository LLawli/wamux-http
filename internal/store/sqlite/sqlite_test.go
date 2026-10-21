package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
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

// A database made before keys could cover several sessions keeps each
// key's session through migration 0006.
func TestKeySessionsMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0001_init", "0002_outbound", "0003_ingest", "0004_send", "0005_checkpoint"} {
		body, err := migrations.ReadFile("migrations/" + v + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO api_keys (id, hash, hint, is_admin, session, is_active, created_at)
		VALUES ('k_session', 'h1', 'abcd', 0, 'default', 1, 1), ('k_admin', 'h2', 'efgh', 1, '', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	keys, err := s.ListAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !slices.Equal(keys[0].Sessions, []string{"default"}) || keys[1].Sessions != nil {
		t.Fatalf("migrated keys: %+v", keys)
	}
}
