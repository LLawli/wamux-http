-- What a vote needs besides the secret: the poll's chat, its creator and its
-- options, kept for the polls this edge sent.
ALTER TABLE poll_secrets ADD COLUMN chat TEXT NOT NULL DEFAULT '';
ALTER TABLE poll_secrets ADD COLUMN creator TEXT NOT NULL DEFAULT '';
ALTER TABLE poll_secrets ADD COLUMN options TEXT NOT NULL DEFAULT '[]';
