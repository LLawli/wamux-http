CREATE TABLE poll_secrets (
    session TEXT NOT NULL,
    wa_id   TEXT NOT NULL,
    secret  BLOB NOT NULL,
    PRIMARY KEY (session, wa_id)
);

CREATE TABLE status_posts (
    session    TEXT NOT NULL,
    wa_id      TEXT NOT NULL,
    recipients TEXT NOT NULL,
    PRIMARY KEY (session, wa_id)
);
