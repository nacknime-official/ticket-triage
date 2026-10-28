-- +goose Up
CREATE TABLE outbox (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic        text        NOT NULL,
    key          bytea       NOT NULL,
    payload      bytea       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    attempts     integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error   text
);

-- The relay claims unpublished rows in id order; the partial index stays small
-- because published rows leave it.
CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;
-- Cleanup deletes published rows older than the retention window.
CREATE INDEX outbox_published_at_idx ON outbox (published_at) WHERE published_at IS NOT NULL;

COMMENT ON TABLE outbox IS
    'Transactional outbox: rows written in the same transaction as the state change, published to Kafka by the relay. Payloads carry ids and status fields only, never ticket text.';
COMMENT ON COLUMN outbox.key IS 'Kafka record key; the ticket id.';
COMMENT ON COLUMN outbox.headers IS 'Kafka record headers as a JSON object of string values.';
COMMENT ON COLUMN outbox.published_at IS 'When the relay published the row; NULL while unpublished.';
COMMENT ON COLUMN outbox.attempts IS 'Failed publish attempts so far, for diagnostics.';
COMMENT ON COLUMN outbox.last_error IS 'Last publish error, for diagnostics.';

-- +goose Down
DROP TABLE outbox;
