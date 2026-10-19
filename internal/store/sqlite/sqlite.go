// Package sqlite implements store.Store on SQLite (modernc.org/sqlite, no
// cgo). Several edge processes on one host can share the file: WAL mode,
// immediate transactions and a busy timeout serialize the writers.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // driver "sqlite"

	"github.com/LLawli/wamux-http/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the SQLite engine.
type Store struct{ db *sql.DB }

var _ store.Store = (*Store)(nil)

// Open opens (creating if needed) the database at path and migrates it.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT count(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", version, err)
		}
	}
	return nil
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// nullJSON stores an empty raw message as NULL.
func nullJSON(r json.RawMessage) any {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	return string(r)
}

func rawJSON(ns sql.NullString) json.RawMessage {
	if !ns.Valid {
		return nil
	}
	return json.RawMessage(ns.String)
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- sessions ---

const sessionCols = `name, account_uuid, config, started, status, qr, me, statuses, event_seq, event_ts, started_at, created_at, updated_at`

func scanSession(row interface{ Scan(...any) error }) (store.Session, error) {
	var s store.Session
	var cfg, me, statuses sql.NullString
	err := row.Scan(&s.Name, &s.AccountUUID, &cfg, &s.Started, &s.Status, &s.QR, &me, &statuses, &s.EventSeq, &s.EventTS, &s.StartedAt, &s.CreatedAt, &s.UpdatedAt)
	s.Config, s.Me, s.Statuses = rawJSON(cfg), rawJSON(me), rawJSON(statuses)
	return s, err
}

// CreateSession inserts a session; ErrConflict when the name is taken.
func (s *Store) CreateSession(ctx context.Context, x store.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (`+sessionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		x.Name, x.AccountUUID, nullJSON(x.Config), b2i(x.Started), x.Status, x.QR, nullJSON(x.Me), nullJSON(x.Statuses), x.EventSeq, x.EventTS, x.StartedAt, x.CreatedAt, x.UpdatedAt)
	if isUnique(err) {
		return store.ErrConflict
	}
	return err
}

// GetSession returns one session.
func (s *Store) GetSession(ctx context.Context, name string) (store.Session, error) {
	x, err := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE name = ?`, name))
	return x, notFound(err)
}

// ListSessions returns every session, by name.
func (s *Store) ListSessions(ctx context.Context) ([]store.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionCols+` FROM sessions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// UpdateSession applies a patch and returns the updated session.
func (s *Store) UpdateSession(ctx context.Context, name string, p store.SessionPatch) (store.Session, error) {
	var sets []string
	var args []any
	add := func(col string, v any) { sets = append(sets, col+" = ?"); args = append(args, v) }
	if p.AccountUUID != nil {
		add("account_uuid", *p.AccountUUID)
	}
	if p.Config != nil {
		add("config", nullJSON(*p.Config))
	}
	if p.Started != nil {
		add("started", b2i(*p.Started))
	}
	if p.Status != nil {
		add("status", *p.Status)
	}
	if p.QR != nil {
		add("qr", *p.QR)
	}
	if p.Me != nil {
		add("me", nullJSON(*p.Me))
	}
	if p.Statuses != nil {
		add("statuses", nullJSON(*p.Statuses))
	}
	if p.StartedAt != nil {
		add("started_at", *p.StartedAt)
	}
	add("updated_at", nowMs())
	args = append(args, name)
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET `+strings.Join(sets, ", ")+` WHERE name = ?`, args...)
	if err != nil {
		return store.Session{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.Session{}, store.ErrNotFound
	}
	return s.GetSession(ctx, name)
}

// DeleteSession removes the session row only; see PurgeSession.
func (s *Store) DeleteSession(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// PurgeSession removes the session and everything stored for it.
func (s *Store) PurgeSession(ctx context.Context, name string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, t := range purgeTables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE session = ?`, name); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE name = ?`, name)
		return err
	})
}

// SetEventCheckpoint records the last core event processed for a session.
func (s *Store) SetEventCheckpoint(ctx context.Context, session string, seq, ts int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET event_seq = ?, event_ts = ? WHERE name = ?`, seq, ts, session)
	return err
}

// --- api keys ---

const keyCols = `id, hash, hint, is_admin, session, is_active, actions, created_at`

func scanKey(row interface{ Scan(...any) error }) (store.APIKey, error) {
	var k store.APIKey
	var actions sql.NullString
	err := row.Scan(&k.ID, &k.Hash, &k.Hint, &k.IsAdmin, &k.Session, &k.IsActive, &actions, &k.CreatedAt)
	k.Actions = rawJSON(actions)
	return k, err
}

// CreateAPIKey inserts a key.
func (s *Store) CreateAPIKey(ctx context.Context, k store.APIKey) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys (`+keyCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		k.ID, k.Hash, k.Hint, b2i(k.IsAdmin), k.Session, b2i(k.IsActive), nullJSON(k.Actions), k.CreatedAt)
	if isUnique(err) {
		return store.ErrConflict
	}
	return err
}

// GetAPIKeyByHash looks a key up by the hash of its value.
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (store.APIKey, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx, `SELECT `+keyCols+` FROM api_keys WHERE hash = ?`, hash))
	return k, notFound(err)
}

// GetAPIKey looks a key up by id.
func (s *Store) GetAPIKey(ctx context.Context, id string) (store.APIKey, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx, `SELECT `+keyCols+` FROM api_keys WHERE id = ?`, id))
	return k, notFound(err)
}

// ListAPIKeys returns every key, oldest first.
func (s *Store) ListAPIKeys(ctx context.Context) ([]store.APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyCols+` FROM api_keys ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// UpdateAPIKey rewrites a key's flags (never its hash).
func (s *Store) UpdateAPIKey(ctx context.Context, k store.APIKey) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET is_admin = ?, session = ?, is_active = ?, actions = ? WHERE id = ?`,
		b2i(k.IsAdmin), k.Session, b2i(k.IsActive), nullJSON(k.Actions), k.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DeleteAPIKey removes a key.
func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- leases ---

// AcquireLease takes the lease when free, expired or already ours.
func (s *Store) AcquireLease(ctx context.Context, name, holder string, now, ttlMs int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO leases (name, holder, expires_at) VALUES (?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET holder = excluded.holder, expires_at = excluded.expires_at
		WHERE leases.holder = excluded.holder OR leases.expires_at <= ?`,
		name, holder, now+ttlMs, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseLease drops the lease if holder has it.
func (s *Store) ReleaseLease(ctx context.Context, name, holder string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM leases WHERE name = ? AND holder = ?`, name, holder)
	return err
}

// LeaseHolder is who holds a live lease, "" when nobody does.
func (s *Store) LeaseHolder(ctx context.Context, name string, now int64) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT holder FROM leases WHERE name = ? AND expires_at > ?`, name, now).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

// --- events and deliveries ---

// AppendEvent stores the event and its deliveries atomically.
func (s *Store) AppendEvent(ctx context.Context, e store.Event, ds []store.Delivery) (int64, error) {
	var seq int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO events (id, session, type, body, created_at) VALUES (?,?,?,?,?)`,
			e.ID, e.Session, e.Type, string(e.Body), e.CreatedAt)
		if err != nil {
			return err
		}
		if seq, err = res.LastInsertId(); err != nil {
			return err
		}
		for _, d := range ds {
			if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries (event_id, session, url, body, webhook, next_attempt_at) VALUES (?,?,?,?,?,?)`,
				e.ID, e.Session, d.URL, string(d.Body), string(d.Webhook), d.NextAttemptAt); err != nil {
				return err
			}
		}
		return nil
	})
	return seq, err
}

// EventsAfter returns events with seq > after, oldest first.
func (s *Store) EventsAfter(ctx context.Context, after int64, limit int) ([]store.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, id, session, type, body, created_at FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Event
	for rows.Next() {
		var e store.Event
		var body string
		if err := rows.Scan(&e.Seq, &e.ID, &e.Session, &e.Type, &body, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Body = json.RawMessage(body)
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastEventSeq is the newest event's seq, 0 when there is none.
func (s *Store) LastEventSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT max(seq) FROM events`).Scan(&seq)
	return seq.Int64, err
}

// PruneEvents drops events created before the given time.
func (s *Store) PruneEvents(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, before)
	return err
}

// ClaimDeliveries locks due deliveries for holder.
func (s *Store) ClaimDeliveries(ctx context.Context, holder string, now, lockMs int64, limit int) ([]store.Delivery, error) {
	var out []store.Delivery
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `UPDATE deliveries SET locked_until = ?, locked_by = ?
			WHERE id IN (SELECT id FROM deliveries WHERE next_attempt_at <= ? AND locked_until <= ? ORDER BY next_attempt_at, id LIMIT ?)
			RETURNING id, event_id, session, url, body, webhook, attempts, next_attempt_at, last_error`,
			now+lockMs, holder, now, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d store.Delivery
			var body, wh string
			if err := rows.Scan(&d.ID, &d.EventID, &d.Session, &d.URL, &body, &wh, &d.Attempts, &d.NextAttemptAt, &d.LastError); err != nil {
				return err
			}
			d.Body, d.Webhook = json.RawMessage(body), json.RawMessage(wh)
			out = append(out, d)
		}
		return rows.Err()
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

// CompleteDelivery removes a delivered request.
func (s *Store) CompleteDelivery(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deliveries WHERE id = ?`, id)
	return err
}

// RetryDelivery reschedules a failed attempt, or drops it when nextAt is 0.
func (s *Store) RetryDelivery(ctx context.Context, id int64, lastErr string, nextAt int64) error {
	if nextAt == 0 {
		return s.CompleteDelivery(ctx, id)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE deliveries SET attempts = attempts + 1, last_error = ?, next_attempt_at = ?, locked_until = 0, locked_by = '' WHERE id = ?`,
		lastErr, nextAt, id)
	return err
}

func nowMs() int64 { return time.Now().UnixMilli() }

// purgeTables are the per-session tables PurgeSession empties. A migration
// that adds one adds it here.
var purgeTables = []string{"messages", "chats", "contacts", "labels", "chat_labels", "deliveries", "events", "poll_secrets", "status_posts", "presences"}
