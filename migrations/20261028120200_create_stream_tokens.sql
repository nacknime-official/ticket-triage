-- +goose Up
CREATE TABLE stream_tokens (
    token_hash bytea       PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    ticket_id  uuid        NOT NULL REFERENCES tickets (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT stream_tokens_expires_after_created CHECK (expires_at > created_at)
);

-- The primary key serves the consume lookup by token_hash; this one backs the
-- foreign key.
CREATE INDEX stream_tokens_ticket_id_idx ON stream_tokens (ticket_id);

COMMENT ON TABLE stream_tokens IS
    'Short-lived single-use tokens that open the SSE stream of one ticket; consumed atomically by setting used_at.';
COMMENT ON COLUMN stream_tokens.token_hash IS 'sha256 of the stream token; the token itself is never stored.';
COMMENT ON COLUMN stream_tokens.used_at IS 'When the token was consumed; NULL while unused.';

-- +goose Down
DROP TABLE stream_tokens;
