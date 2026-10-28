-- +goose Up
CREATE TABLE classifications (
    id                  bigint        GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ticket_id           uuid          NOT NULL REFERENCES tickets (id) ON DELETE CASCADE,
    provider            text          NOT NULL,
    model               text,
    attempt             integer       NOT NULL CHECK (attempt >= 1),
    outcome             text          NOT NULL
        CHECK (outcome IN ('ok', 'transient_error', 'permanent_error', 'auth_error', 'invalid_answer')),
    http_status         integer,
    latency_ms          integer       NOT NULL CHECK (latency_ms >= 0),
    input_tokens        integer       CHECK (input_tokens >= 0),
    output_tokens       integer       CHECK (output_tokens >= 0),
    cost_usd            numeric(12,8) CHECK (cost_usd >= 0),
    cost_source         text          CHECK (cost_source IN ('reported', 'estimated')),
    complaint_p         real,
    category            text,
    category_confidence real,
    urgency             real,
    urgency_confidence  real,
    raw_response        jsonb,
    created_at          timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT classifications_cost_source_matches_cost
        CHECK ((cost_usd IS NULL) = (cost_source IS NULL))
);

-- Audit history of one ticket; also backs the foreign key.
CREATE INDEX classifications_ticket_id_created_at_idx ON classifications (ticket_id, created_at);
-- Daily budget guard: sum(cost_usd) since the start of the UTC day.
CREATE INDEX classifications_created_at_idx ON classifications (created_at) INCLUDE (cost_usd);

COMMENT ON TABLE classifications IS
    'Audit log, one row per classifier call (including failed ones), so eval and debugging need no repeat calls.';
COMMENT ON COLUMN classifications.model IS 'Model as served, taken from the response; NULL when there was no response.';
COMMENT ON COLUMN classifications.attempt IS 'Attempt number of this call for the ticket, starting at 1.';
COMMENT ON COLUMN classifications.http_status IS 'HTTP status of the call; NULL on timeout or network error.';
COMMENT ON COLUMN classifications.cost_source IS 'reported by the provider, or estimated from input_tokens and the configured price.';
COMMENT ON COLUMN classifications.raw_response IS 'Raw classifier response with all probabilities and confidences; holds no ticket text.';

-- +goose Down
DROP TABLE classifications;
