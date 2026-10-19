package store

import "context"

// OutboundStore keeps what the core hands back on a send and needs again
// later: a poll's secret (to open its votes) and the recipients a status was
// posted to (to revoke it).
type OutboundStore interface {
	// PutPollSecret records the secret of a poll by its WhatsApp message id.
	PutPollSecret(ctx context.Context, session, waID string, secret []byte) error
	PollSecret(ctx context.Context, session, waID string) ([]byte, error)
	// PutStatusRecipients records who a status was posted to.
	PutStatusRecipients(ctx context.Context, session, waID string, recipients []string) error
	StatusRecipients(ctx context.Context, session, waID string) ([]string, error)
}
