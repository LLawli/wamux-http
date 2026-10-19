package store

import (
	"context"
	"encoding/json"
)

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

// IngestStore is what the ingest side writes and the read routes (chats,
// messages, contacts, labels) read.
type IngestStore interface {
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
}
