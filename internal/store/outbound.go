package store

import "context"

// Poll is what voting in a poll needs and only its sender keeps: the secret
// the votes are keyed by, the chat, the creator's JID and the options as
// offered.
type Poll struct {
	WAID    string
	Chat    string // core JID
	Creator string // core JID
	Options []string
	Secret  []byte
}

// OutboundStore keeps what the core hands back on a send and needs again
// later: a poll's secret (to open its votes) and the recipients a status was
// posted to (to revoke it).
type OutboundStore interface {
	// PutPollSecret records the secret of a poll by its WhatsApp message id.
	PutPollSecret(ctx context.Context, session, waID string, secret []byte) error
	PollSecret(ctx context.Context, session, waID string) ([]byte, error)
	// PutPoll records a sent poll whole; GetPoll returns it (a poll stored
	// with PutPollSecret alone has no chat, creator or options).
	PutPoll(ctx context.Context, session string, p Poll) error
	GetPoll(ctx context.Context, session, waID string) (Poll, error)
	// PutStatusRecipients records who a status was posted to.
	PutStatusRecipients(ctx context.Context, session, waID string, recipients []string) error
	StatusRecipients(ctx context.Context, session, waID string) ([]string, error)
}
