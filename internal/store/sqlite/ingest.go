package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/LLawli/wamux-http/internal/store"
)

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
// higher of the stored and the new ack, in its column and in its body.
func (s *Store) UpsertMessages(ctx context.Context, ms []store.Message) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO messages (`+msgCols+`) VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT (session, id) DO UPDATE SET chat_id = excluded.chat_id, ts = excluded.ts,
			ack = max(messages.ack, excluded.ack),
			body = CASE WHEN excluded.ack >= messages.ack THEN excluded.body
				ELSE json_set(excluded.body, '$.ack', messages.ack, '$.ackName', json_extract(messages.body, '$.ackName')) END,
			raw = coalesce(excluded.raw, messages.raw)`)
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

// ListMessages pages one chat's messages (every chat's for ""), newest first
// unless Ascending.
func (s *Store) ListMessages(ctx context.Context, session, chatID string, f store.MessageFilter) ([]store.Message, error) {
	q := `SELECT ` + msgCols + ` FROM messages WHERE session = ?`
	args := []any{session}
	if chatID != "" {
		q += ` AND chat_id = ?`
		args = append(args, chatID)
	}
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
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET ack = ?, body = json_set(body, '$.ack', ?, '$.ackName', ?)
				WHERE session = ? AND wa_id = ? AND ack < ?`, ack, ack, ackName(ack), session, id, ack); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetAck sets the ack of the given messages, lowering it if need be.
func (s *Store) SetAck(ctx context.Context, session string, waIDs []string, ack int) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range waIDs {
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET ack = ?, body = json_set(body, '$.ack', ?, '$.ackName', ?)
				WHERE session = ? AND wa_id = ?`, ack, ack, ackName(ack), session, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ackName is WAHA's name for an ack, kept in step with the stored body.
func ackName(ack int) string {
	switch ack {
	case -1:
		return "ERROR"
	case 0:
		return "PENDING"
	case 1:
		return "SERVER"
	case 2:
		return "DEVICE"
	case 3:
		return "READ"
	case 4:
		return "PLAYED"
	}
	return "UNKNOWN"
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

// TouchChat records a message in a chat without touching its flags.
func (s *Store) TouchChat(ctx context.Context, session, id, name string, ts int64, lastMessageID string, unread int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chats (session, id, name, conversation_ts, last_message_id, unread_count)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT (session, id) DO UPDATE SET
		name = CASE WHEN excluded.name != '' THEN excluded.name ELSE chats.name END,
		last_message_id = CASE WHEN excluded.conversation_ts >= chats.conversation_ts AND excluded.last_message_id != '' THEN excluded.last_message_id ELSE chats.last_message_id END,
		conversation_ts = max(chats.conversation_ts, excluded.conversation_ts),
		unread_count = CASE ? WHEN 1 THEN chats.unread_count + 1 WHEN -1 THEN 0 ELSE chats.unread_count END`,
		session, id, name, ts, lastMessageID, max(unread, 0), unread)
	return err
}

// PatchChat sets the given flags, creating the chat when needed.
func (s *Store) PatchChat(ctx context.Context, session, id string, p store.ChatPatch) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO chats (session, id) VALUES (?,?)`, session, id); err != nil {
			return err
		}
		set := func(col string, v any) error {
			_, err := tx.ExecContext(ctx, `UPDATE chats SET `+col+` = ? WHERE session = ? AND id = ?`, v, session, id)
			return err
		}
		if p.Archived != nil {
			if err := set("archived", b2i(*p.Archived)); err != nil {
				return err
			}
		}
		if p.Pinned != nil {
			if err := set("pinned", b2i(*p.Pinned)); err != nil {
				return err
			}
		}
		if p.Unread != nil {
			if err := set("unread_count", *p.Unread); err != nil {
				return err
			}
		}
		if p.Name != nil {
			if err := set("name", *p.Name); err != nil {
				return err
			}
		}
		return nil
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

// GetLabel returns one label, deleted or not.
func (s *Store) GetLabel(ctx context.Context, session, id string) (store.Label, error) {
	ls, err := s.queryLabels(ctx, `SELECT session, id, name, color, deleted FROM labels WHERE session = ? AND id = ?`, session, id)
	if err != nil {
		return store.Label{}, err
	}
	if len(ls) == 0 {
		return store.Label{}, store.ErrNotFound
	}
	return ls[0], nil
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

// --- presences ---

// PutPresence replaces a participant's presence in a chat.
func (s *Store) PutPresence(ctx context.Context, p store.Presence) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO presences (session, chat_id, participant, state, last_seen, updated_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT (session, chat_id, participant) DO UPDATE SET state = excluded.state, last_seen = excluded.last_seen, updated_at = excluded.updated_at`,
		p.Session, p.ChatID, p.Participant, p.State, p.LastSeen, p.UpdatedAt)
	return err
}

// ListPresences returns the presences of one chat, or of all, by chat and
// update order.
func (s *Store) ListPresences(ctx context.Context, session, chatID string) ([]store.Presence, error) {
	q := `SELECT session, chat_id, participant, state, last_seen, updated_at FROM presences WHERE session = ?`
	args := []any{session}
	if chatID != "" {
		q += ` AND chat_id = ?`
		args = append(args, chatID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY chat_id, updated_at, participant`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Presence
	for rows.Next() {
		var p store.Presence
		if err := rows.Scan(&p.Session, &p.ChatID, &p.Participant, &p.State, &p.LastSeen, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
