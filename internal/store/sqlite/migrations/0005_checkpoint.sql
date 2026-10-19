-- The last core event the session's owner processed: a new owner replays
-- the core's ring from there instead of from the moment it subscribed.
ALTER TABLE sessions ADD COLUMN event_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN event_ts INTEGER NOT NULL DEFAULT 0;
-- When the session was last started (ms): a core event older than that, such
-- as the LOGGED_OUT of a logout-and-restart, no longer decides its state.
ALTER TABLE sessions ADD COLUMN started_at INTEGER NOT NULL DEFAULT 0;
