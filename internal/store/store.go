// Package store is the edge's persistence, behind the Store interface. Every
// method speaks domain records; SQL lives in the engine packages (sqlite/).
// Every time is int64 milliseconds since the epoch.
package store

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrNotFound is returned when a looked-up record does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a record that must be unique already exists.
var ErrConflict = errors.New("already exists")

// Session is a WAHA session: a named wamux account plus what the core does
// not keep (config, desired state, last QR, last status).
type Session struct {
	Name        string
	AccountUUID string          // the core's account uuid; empty until created there
	Config      json.RawMessage // WAHA SessionConfig, verbatim; null when unset
	Started     bool            // desired state: the edge keeps it connected
	Status      string          // last WAHA status written by the owner
	QR          string          // last QR payload while pairing; empty otherwise
	Me          json.RawMessage // WAHA MeInfo; null until paired
	Statuses    json.RawMessage // the last WAHA SessionStatusPoint values, oldest first
	CreatedAt   int64
	UpdatedAt   int64
}

// SessionPatch updates the fields that are non-nil.
type SessionPatch struct {
	AccountUUID *string
	Config      *json.RawMessage
	Started     *bool
	Status      *string
	QR          *string
	Me          *json.RawMessage
	Statuses    *json.RawMessage
}

// APIKey is a key that is not the admin key from the environment. Only the
// SHA-256 of the key is stored.
type APIKey struct {
	ID        string
	Hash      string // hex sha256 of the key
	Hint      string // last characters of the key, for listings
	IsAdmin   bool
	Session   string // empty for an admin key
	IsActive  bool
	Actions   json.RawMessage // WAHA SessionActionsDTO; null means all
	CreatedAt int64
}

// Event is one WAHA event, as appended by the session's owner. Seq is
// assigned by the store and grows monotonically.
type Event struct {
	Seq       int64
	ID        string // WAHA event id (evt_<ulid>)
	Session   string
	Type      string // WAHA event name, e.g. "message"
	Body      json.RawMessage
	CreatedAt int64
}

// Delivery is one webhook request waiting in the outbox.
type Delivery struct {
	ID            int64
	EventID       string
	Session       string
	URL           string
	Body          json.RawMessage
	Webhook       json.RawMessage // the WebhookConfig it came from (hmac, retries, headers)
	Attempts      int
	NextAttemptAt int64
	LastError     string
}

// Message is a stored WAHA message.
type Message struct {
	Session   string
	ID        string // WAHA serialized id
	WAID      string // WhatsApp's own id (the last part of ID)
	ChatID    string // WAHA chat id
	Timestamp int64  // ms
	FromMe    bool
	Ack       int
	Body      json.RawMessage // WAMessage JSON
	Raw       []byte          // the core's InboundMessage, protobuf-encoded
}

// MessageFilter selects messages of one chat.
type MessageFilter struct {
	Limit, Offset int
	Ascending     bool
	FromMe        *bool
	TimestampGTE  *int64 // ms
	TimestampLTE  *int64 // ms
	Ack           *int
}

// Chat is a stored chat overview.
type Chat struct {
	Session        string
	ID             string
	Name           string
	ConversationTS int64 // ms of the last message
	LastMessageID  string
	Archived       bool
	Pinned         bool
	UnreadCount    int
	Body           json.RawMessage // extra WAHA fields, verbatim
}

// ChatFilter orders and pages chats.
type ChatFilter struct {
	Limit, Offset int
	SortBy        string // conversationTimestamp | id | name
	Ascending     bool
	IDs           []string
}

// Contact is a stored contact.
type Contact struct {
	Session  string
	ID       string // WAHA id (@c.us or @lid)
	Name     string // saved name, from a contact sync
	PushName string
	Body     json.RawMessage
}

// Label is a WhatsApp label (chat list), as the phone synced it.
type Label struct {
	Session string
	ID      string
	Name    string
	Color   int
	Deleted bool
}

// Store is every persistence operation the edge performs.
type Store interface {
	Close() error

	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, name string) (Session, error)
	ListSessions(ctx context.Context) ([]Session, error)
	UpdateSession(ctx context.Context, name string, p SessionPatch) (Session, error)
	DeleteSession(ctx context.Context, name string) error

	CreateAPIKey(ctx context.Context, k APIKey) error
	GetAPIKeyByHash(ctx context.Context, hash string) (APIKey, error)
	GetAPIKey(ctx context.Context, id string) (APIKey, error)
	ListAPIKeys(ctx context.Context) ([]APIKey, error)
	UpdateAPIKey(ctx context.Context, k APIKey) error
	DeleteAPIKey(ctx context.Context, id string) error

	// AcquireLease takes or renews a named lease for holder until now+ttlMs.
	// It succeeds when the lease is free, expired, or already held by holder.
	AcquireLease(ctx context.Context, name, holder string, now, ttlMs int64) (bool, error)
	ReleaseLease(ctx context.Context, name, holder string) error
	LeaseHolder(ctx context.Context, name string, now int64) (string, error)

	// AppendEvent stores an event and its webhook deliveries in one
	// transaction, and returns the event's sequence number.
	AppendEvent(ctx context.Context, e Event, deliveries []Delivery) (int64, error)
	EventsAfter(ctx context.Context, seq int64, limit int) ([]Event, error)
	LastEventSeq(ctx context.Context) (int64, error)
	PruneEvents(ctx context.Context, before int64) error

	// ClaimDeliveries locks up to limit due deliveries for holder until
	// now+lockMs. A crashed holder's deliveries become claimable again.
	ClaimDeliveries(ctx context.Context, holder string, now, lockMs int64, limit int) ([]Delivery, error)
	CompleteDelivery(ctx context.Context, id int64) error
	// RetryDelivery records a failed attempt; nextAt 0 gives up for good.
	RetryDelivery(ctx context.Context, id int64, lastErr string, nextAt int64) error

	UpsertMessages(ctx context.Context, ms []Message) error
	GetMessage(ctx context.Context, session, id string) (Message, error)
	GetMessageByWAID(ctx context.Context, session, waID string) (Message, error)
	ListMessages(ctx context.Context, session, chatID string, f MessageFilter) ([]Message, error)
	// UpdateAck raises the ack of the given WhatsApp ids; it never lowers one.
	UpdateAck(ctx context.Context, session string, waIDs []string, ack int) error
	DeleteMessage(ctx context.Context, session, id string) error
	DeleteChatMessages(ctx context.Context, session, chatID string) error

	UpsertChat(ctx context.Context, c Chat) error
	GetChat(ctx context.Context, session, id string) (Chat, error)
	ListChats(ctx context.Context, session string, f ChatFilter) ([]Chat, error)
	DeleteChat(ctx context.Context, session, id string) error

	UpsertContact(ctx context.Context, c Contact) error
	GetContact(ctx context.Context, session, id string) (Contact, error)
	ListContacts(ctx context.Context, session string, limit, offset int) ([]Contact, error)

	UpsertLabel(ctx context.Context, l Label) error
	ListLabels(ctx context.Context, session string) ([]Label, error)
	SetChatLabel(ctx context.Context, session, labelID, chatID string, on bool) error
	ChatLabels(ctx context.Context, session, chatID string) ([]Label, error)
	LabelChats(ctx context.Context, session, labelID string) ([]string, error)

	// PurgeSession removes every record a session owns, the session included.
	PurgeSession(ctx context.Context, name string) error
}
