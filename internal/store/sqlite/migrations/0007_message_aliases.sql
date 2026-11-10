-- The id each of my own edits, revokes, reactions and poll votes went out
-- under, by its WhatsApp id: none is stored as a message, and its receipts
-- name the peer by LID even when the event named the phone chat.
CREATE TABLE message_aliases (
    session TEXT NOT NULL,
    wa_id   TEXT NOT NULL,
    id      TEXT NOT NULL,
    PRIMARY KEY (session, wa_id)
);
