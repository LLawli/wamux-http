-- A session key may cover several sessions. WAHA's ApiKeyDTO has one
-- `session`; the set lives in its own table and the column goes away.
CREATE TABLE api_key_sessions (
    key_id  TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    session TEXT NOT NULL,
    PRIMARY KEY (key_id, session)
);
INSERT INTO api_key_sessions (key_id, session) SELECT id, session FROM api_keys WHERE session <> '';
ALTER TABLE api_keys DROP COLUMN session;
