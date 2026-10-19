package sqlite

import (
	"context"
	"encoding/json"
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
