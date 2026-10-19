CREATE TABLE presences (
    session     TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    participant TEXT NOT NULL,
    state       TEXT NOT NULL,
    last_seen   INTEGER NOT NULL DEFAULT 0,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (session, chat_id, participant)
);
