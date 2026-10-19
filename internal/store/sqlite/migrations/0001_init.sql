CREATE TABLE sessions (
    name         TEXT PRIMARY KEY,
    account_uuid TEXT NOT NULL DEFAULT '',
    config       TEXT,
    started      INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL DEFAULT 'STOPPED',
    qr           TEXT NOT NULL DEFAULT '',
    me           TEXT,
    statuses     TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE api_keys (
    id         TEXT PRIMARY KEY,
    hash       TEXT NOT NULL UNIQUE,
    hint       TEXT NOT NULL,
    is_admin   INTEGER NOT NULL,
    session    TEXT NOT NULL DEFAULT '',
    is_active  INTEGER NOT NULL,
    actions    TEXT,
    created_at INTEGER NOT NULL
);

CREATE TABLE leases (
    name       TEXT PRIMARY KEY,
    holder     TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE TABLE events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    id         TEXT NOT NULL,
    session    TEXT NOT NULL,
    type       TEXT NOT NULL,
    body       TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX events_created ON events (created_at);

CREATE TABLE deliveries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        TEXT NOT NULL,
    session         TEXT NOT NULL,
    url             TEXT NOT NULL,
    body            TEXT NOT NULL,
    webhook         TEXT NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    locked_until    INTEGER NOT NULL DEFAULT 0,
    locked_by       TEXT NOT NULL DEFAULT '',
    last_error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX deliveries_due ON deliveries (next_attempt_at);

CREATE TABLE messages (
    session   TEXT NOT NULL,
    id        TEXT NOT NULL,
    wa_id     TEXT NOT NULL,
    chat_id   TEXT NOT NULL,
    ts        INTEGER NOT NULL,
    from_me   INTEGER NOT NULL,
    ack       INTEGER NOT NULL,
    body      TEXT NOT NULL,
    raw       BLOB,
    PRIMARY KEY (session, id)
);
CREATE INDEX messages_chat ON messages (session, chat_id, ts);
CREATE INDEX messages_wa_id ON messages (session, wa_id);

CREATE TABLE chats (
    session         TEXT NOT NULL,
    id              TEXT NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    conversation_ts INTEGER NOT NULL DEFAULT 0,
    last_message_id TEXT NOT NULL DEFAULT '',
    archived        INTEGER NOT NULL DEFAULT 0,
    pinned          INTEGER NOT NULL DEFAULT 0,
    unread_count    INTEGER NOT NULL DEFAULT 0,
    body            TEXT,
    PRIMARY KEY (session, id)
);
CREATE INDEX chats_ts ON chats (session, conversation_ts);

CREATE TABLE contacts (
    session   TEXT NOT NULL,
    id        TEXT NOT NULL,
    name      TEXT NOT NULL DEFAULT '',
    push_name TEXT NOT NULL DEFAULT '',
    body      TEXT,
    PRIMARY KEY (session, id)
);

CREATE TABLE labels (
    session TEXT NOT NULL,
    id      TEXT NOT NULL,
    name    TEXT NOT NULL DEFAULT '',
    color   INTEGER NOT NULL DEFAULT 0,
    deleted INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (session, id)
);

CREATE TABLE chat_labels (
    session  TEXT NOT NULL,
    label_id TEXT NOT NULL,
    chat_id  TEXT NOT NULL,
    PRIMARY KEY (session, label_id, chat_id)
);
