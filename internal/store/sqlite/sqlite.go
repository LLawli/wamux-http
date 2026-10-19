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

const sessionCols = `name, account_uuid, config, started, status, qr, me, statuses, created_at, updated_at`

func scanSession(row interface{ Scan(...any) error }) (store.Session, error) {
	var s store.Session
	var cfg, me, statuses sql.NullString
	err := row.Scan(&s.Name, &s.AccountUUID, &cfg, &s.Started, &s.Status, &s.QR, &me, &statuses, &s.CreatedAt, &s.UpdatedAt)
	s.Config, s.Me, s.Statuses = rawJSON(cfg), rawJSON(me), rawJSON(statuses)
	return s, err
}

// CreateSession inserts a session; ErrConflict when the name is taken.
func (s *Store) CreateSession(ctx context.Context, x store.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (`+sessionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		x.Name, x.AccountUUID, nullJSON(x.Config), b2i(x.Started), x.Status, x.QR, nullJSON(x.Me), nullJSON(x.Statuses), x.CreatedAt, x.UpdatedAt)
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
		for _, t := range []string{"messages", "chats", "contacts", "labels", "chat_labels", "deliveries", "events"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE session = ?`, name); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE name = ?`, name)
		return err
	})
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

// --- messages ---

const msgCols = `session, id, wa_id, chat_id, ts, from_me, ack, body, raw`

func scanMessage(row interface{ Scan(...any) error }) (store.Message, error) {
	var m store.Message
	var body string
	err := row.Scan(&m.Session, &m.ID, &m.WAID, &m.ChatID, &m.Timestamp, &m.FromMe, &m.Ack, &body, &m.Raw)
	m.Body = json.RawMessage(body)
	return m, err
}

// UpsertMessages inserts or replaces messages. A replayed message keeps the
// higher of the stored and the new ack.
func (s *Store) UpsertMessages(ctx context.Context, ms []store.Message) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO messages (`+msgCols+`) VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT (session, id) DO UPDATE SET chat_id = excluded.chat_id, ts = excluded.ts,
			ack = max(messages.ack, excluded.ack), body = excluded.body, raw = coalesce(excluded.raw, messages.raw)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, m := range ms {
			if _, err := st.ExecContext(ctx, m.Session, m.ID, m.WAID, m.ChatID, m.Timestamp, b2i(m.FromMe), m.Ack, string(m.Body), m.Raw); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetMessage returns one message by its WAHA id.
func (s *Store) GetMessage(ctx context.Context, session, id string) (store.Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE session = ? AND id = ?`, session, id))
	return m, notFound(err)
}

// GetMessageByWAID returns the newest message with WhatsApp id waID.
func (s *Store) GetMessageByWAID(ctx context.Context, session, waID string) (store.Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE session = ? AND wa_id = ? ORDER BY ts DESC LIMIT 1`, session, waID))
	return m, notFound(err)
}

// ListMessages pages one chat's messages, newest first unless Ascending.
func (s *Store) ListMessages(ctx context.Context, session, chatID string, f store.MessageFilter) ([]store.Message, error) {
	q := `SELECT ` + msgCols + ` FROM messages WHERE session = ? AND chat_id = ?`
	args := []any{session, chatID}
	if f.FromMe != nil {
		q += ` AND from_me = ?`
		args = append(args, b2i(*f.FromMe))
	}
	if f.TimestampGTE != nil {
		q += ` AND ts >= ?`
		args = append(args, *f.TimestampGTE)
	}
	if f.TimestampLTE != nil {
		q += ` AND ts <= ?`
		args = append(args, *f.TimestampLTE)
	}
	if f.Ack != nil {
		q += ` AND ack = ?`
		args = append(args, *f.Ack)
	}
	if f.Ascending {
		q += ` ORDER BY ts ASC, id ASC`
	} else {
		q += ` ORDER BY ts DESC, id DESC`
	}
	q += ` LIMIT ? OFFSET ?`
	args = append(args, limitOr(f.Limit), f.Offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func limitOr(n int) int {
	if n <= 0 {
		return -1 // SQLite: no limit
	}
	return n
}

// UpdateAck raises the ack of the given messages, never lowering it.
func (s *Store) UpdateAck(ctx context.Context, session string, waIDs []string, ack int) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range waIDs {
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET ack = ?, body = json_set(body, '$.ack', ?)
				WHERE session = ? AND wa_id = ? AND ack < ?`, ack, ack, session, id, ack); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteMessage removes one message.
func (s *Store) DeleteMessage(ctx context.Context, session, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE session = ? AND id = ?`, session, id)
	return err
}

// DeleteChatMessages removes every message of a chat.
func (s *Store) DeleteChatMessages(ctx context.Context, session, chatID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE session = ? AND chat_id = ?`, session, chatID)
	return err
}

// --- chats ---

const chatCols = `session, id, name, conversation_ts, last_message_id, archived, pinned, unread_count, body`

func scanChat(row interface{ Scan(...any) error }) (store.Chat, error) {
	var c store.Chat
	var body sql.NullString
	err := row.Scan(&c.Session, &c.ID, &c.Name, &c.ConversationTS, &c.LastMessageID, &c.Archived, &c.Pinned, &c.UnreadCount, &body)
	c.Body = rawJSON(body)
	return c, err
}

// UpsertChat inserts or replaces a chat. The conversation timestamp and last
// message only move forward, so an old history chunk cannot rewind them.
func (s *Store) UpsertChat(ctx context.Context, c store.Chat) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chats (`+chatCols+`) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT (session, id) DO UPDATE SET
		name = CASE WHEN excluded.name != '' THEN excluded.name ELSE chats.name END,
		last_message_id = CASE WHEN excluded.conversation_ts >= chats.conversation_ts AND excluded.last_message_id != '' THEN excluded.last_message_id ELSE chats.last_message_id END,
		conversation_ts = max(chats.conversation_ts, excluded.conversation_ts),
		archived = excluded.archived, pinned = excluded.pinned, unread_count = excluded.unread_count,
		body = coalesce(excluded.body, chats.body)`,
		c.Session, c.ID, c.Name, c.ConversationTS, c.LastMessageID, b2i(c.Archived), b2i(c.Pinned), c.UnreadCount, nullJSON(c.Body))
	return err
}

// GetChat returns one chat.
func (s *Store) GetChat(ctx context.Context, session, id string) (store.Chat, error) {
	c, err := scanChat(s.db.QueryRowContext(ctx, `SELECT `+chatCols+` FROM chats WHERE session = ? AND id = ?`, session, id))
	return c, notFound(err)
}

// ListChats pages chats.
func (s *Store) ListChats(ctx context.Context, session string, f store.ChatFilter) ([]store.Chat, error) {
	q := `SELECT ` + chatCols + ` FROM chats WHERE session = ?`
	args := []any{session}
	if len(f.IDs) > 0 {
		q += ` AND id IN (?` + strings.Repeat(",?", len(f.IDs)-1) + `)`
		for _, id := range f.IDs {
			args = append(args, id)
		}
	}
	col := map[string]string{"id": "id", "name": "name"}[f.SortBy]
	if col == "" {
		col = "conversation_ts"
	}
	dir := "DESC"
	if f.Ascending {
		dir = "ASC"
	}
	q += ` ORDER BY ` + col + ` ` + dir + `, id ` + dir + ` LIMIT ? OFFSET ?`
	args = append(args, limitOr(f.Limit), f.Offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteChat removes a chat and its messages.
func (s *Store) DeleteChat(ctx context.Context, session, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session = ? AND chat_id = ?`, session, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM chats WHERE session = ? AND id = ?`, session, id)
		return err
	})
}

// --- contacts ---

// UpsertContact inserts or merges a contact; empty names do not erase.
func (s *Store) UpsertContact(ctx context.Context, c store.Contact) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO contacts (session, id, name, push_name, body) VALUES (?,?,?,?,?)
		ON CONFLICT (session, id) DO UPDATE SET
		name = CASE WHEN excluded.name != '' THEN excluded.name ELSE contacts.name END,
		push_name = CASE WHEN excluded.push_name != '' THEN excluded.push_name ELSE contacts.push_name END,
		body = coalesce(excluded.body, contacts.body)`,
		c.Session, c.ID, c.Name, c.PushName, nullJSON(c.Body))
	return err
}

// GetContact returns one contact.
func (s *Store) GetContact(ctx context.Context, session, id string) (store.Contact, error) {
	var c store.Contact
	var body sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT session, id, name, push_name, body FROM contacts WHERE session = ? AND id = ?`, session, id).
		Scan(&c.Session, &c.ID, &c.Name, &c.PushName, &body)
	c.Body = rawJSON(body)
	return c, notFound(err)
}

// ListContacts pages contacts by id.
func (s *Store) ListContacts(ctx context.Context, session string, limit, offset int) ([]store.Contact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session, id, name, push_name, body FROM contacts WHERE session = ? ORDER BY id LIMIT ? OFFSET ?`,
		session, limitOr(limit), offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Contact
	for rows.Next() {
		var c store.Contact
		var body sql.NullString
		if err := rows.Scan(&c.Session, &c.ID, &c.Name, &c.PushName, &body); err != nil {
			return nil, err
		}
		c.Body = rawJSON(body)
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- labels ---

// UpsertLabel inserts or replaces a label.
func (s *Store) UpsertLabel(ctx context.Context, l store.Label) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO labels (session, id, name, color, deleted) VALUES (?,?,?,?,?)
		ON CONFLICT (session, id) DO UPDATE SET name = excluded.name, color = excluded.color, deleted = excluded.deleted`,
		l.Session, l.ID, l.Name, l.Color, b2i(l.Deleted))
	return err
}

func (s *Store) queryLabels(ctx context.Context, q string, args ...any) ([]store.Label, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Label
	for rows.Next() {
		var l store.Label
		if err := rows.Scan(&l.Session, &l.ID, &l.Name, &l.Color, &l.Deleted); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListLabels returns the labels that are not deleted.
func (s *Store) ListLabels(ctx context.Context, session string) ([]store.Label, error) {
	return s.queryLabels(ctx, `SELECT session, id, name, color, deleted FROM labels WHERE session = ? AND deleted = 0 ORDER BY id`, session)
}

// SetChatLabel adds or removes a chat from a label.
func (s *Store) SetChatLabel(ctx context.Context, session, labelID, chatID string, on bool) error {
	q := `DELETE FROM chat_labels WHERE session = ? AND label_id = ? AND chat_id = ?`
	if on {
		q = `INSERT OR IGNORE INTO chat_labels (session, label_id, chat_id) VALUES (?,?,?)`
	}
	_, err := s.db.ExecContext(ctx, q, session, labelID, chatID)
	return err
}

// ChatLabels returns the labels a chat has.
func (s *Store) ChatLabels(ctx context.Context, session, chatID string) ([]store.Label, error) {
	return s.queryLabels(ctx, `SELECT l.session, l.id, l.name, l.color, l.deleted FROM labels l
		JOIN chat_labels c ON c.session = l.session AND c.label_id = l.id
		WHERE l.session = ? AND c.chat_id = ? AND l.deleted = 0 ORDER BY l.id`, session, chatID)
}

// LabelChats returns the chat ids under a label.
func (s *Store) LabelChats(ctx context.Context, session, labelID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id FROM chat_labels WHERE session = ? AND label_id = ? ORDER BY chat_id`, session, labelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func nowMs() int64 { return time.Now().UnixMilli() }
