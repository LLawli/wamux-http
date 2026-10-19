package sqlite

import (
	"context"
	"encoding/json"

	"github.com/LLawli/wamux-http/internal/store"
)

// PutPollSecret records a poll's secret.
func (s *Store) PutPollSecret(ctx context.Context, session, waID string, secret []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO poll_secrets (session, wa_id, secret) VALUES (?,?,?)
		ON CONFLICT (session, wa_id) DO UPDATE SET secret = excluded.secret`, session, waID, secret)
	return err
}

// PollSecret returns a poll's secret.
func (s *Store) PollSecret(ctx context.Context, session, waID string) ([]byte, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT secret FROM poll_secrets WHERE session = ? AND wa_id = ?`, session, waID).Scan(&b)
	return b, notFound(err)
}

// PutStatusRecipients records who a status went to.
func (s *Store) PutStatusRecipients(ctx context.Context, session, waID string, recipients []string) error {
	raw, err := json.Marshal(recipients)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO status_posts (session, wa_id, recipients) VALUES (?,?,?)
		ON CONFLICT (session, wa_id) DO UPDATE SET recipients = excluded.recipients`, session, waID, string(raw))
	return err
}

// StatusRecipients returns who a status went to.
func (s *Store) StatusRecipients(ctx context.Context, session, waID string) ([]string, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT recipients FROM status_posts WHERE session = ? AND wa_id = ?`, session, waID).Scan(&raw); err != nil {
		return nil, notFound(err)
	}
	var out []string
	return out, json.Unmarshal([]byte(raw), &out)
}

// PutPoll records a sent poll.
func (s *Store) PutPoll(ctx context.Context, session string, p store.Poll) error {
	opts, err := json.Marshal(p.Options)
	if err != nil {
		return err
	}
	if p.Options == nil {
		opts = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO poll_secrets (session, wa_id, secret, chat, creator, options) VALUES (?,?,?,?,?,?)
		ON CONFLICT (session, wa_id) DO UPDATE SET secret = excluded.secret, chat = excluded.chat,
		creator = excluded.creator, options = excluded.options`,
		session, p.WAID, p.Secret, p.Chat, p.Creator, string(opts))
	return err
}

// GetPoll returns a sent poll.
func (s *Store) GetPoll(ctx context.Context, session, waID string) (store.Poll, error) {
	p := store.Poll{WAID: waID}
	var opts string
	err := s.db.QueryRowContext(ctx, `SELECT secret, chat, creator, options FROM poll_secrets WHERE session = ? AND wa_id = ?`,
		session, waID).Scan(&p.Secret, &p.Chat, &p.Creator, &opts)
	if err != nil {
		return p, notFound(err)
	}
	return p, json.Unmarshal([]byte(opts), &p.Options)
}
