-- +goose Up
CREATE TABLE shadow_classifications (
    run_id              text          NOT NULL CHECK (run_id ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    ticket_id           uuid          NOT NULL REFERENCES tickets (id) ON DELETE CASCADE,
    provider            text          NOT NULL,
    model               text,
    attempts            integer       NOT NULL CHECK (attempts >= 1),
    outcome             text          NOT NULL
        CHECK (outcome IN ('ok', 'transient_error', 'permanent_error', 'invalid_answer')),
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
    status              text          CHECK (status IN ('classified', 'needs_human')),
    status_reason       text          CHECK (status_reason IN ('low_confidence', 'category_other')),
    complaint_uncertain boolean       NOT NULL DEFAULT false,
    last_error          text,
    raw_response        jsonb,
    created_at          timestamptz   NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, ticket_id),
    CONSTRAINT shadow_classifications_cost_source_matches_cost
        CHECK ((cost_usd IS NULL) = (cost_source IS NULL)),
    -- Only a valid answer has a decision.
    CONSTRAINT shadow_classifications_status_matches_outcome
        CHECK ((outcome = 'ok') = (status IS NOT NULL)),
    CONSTRAINT shadow_classifications_status_reason_matches_status
        CHECK ((status = 'needs_human') = (status_reason IS NOT NULL))
);

-- Daily budget guard: shadow calls count toward the same daily spend.
CREATE INDEX shadow_classifications_created_at_idx ON shadow_classifications (created_at) INCLUDE (cost_usd);
-- Comparing runs for one ticket; also backs the foreign key.
CREATE INDEX shadow_classifications_ticket_id_idx ON shadow_classifications (ticket_id);

COMMENT ON TABLE shadow_classifications IS
    'Shadow-mode replay results (cmd/worker shadow), one row per run and ticket. Never read by the live flow; tickets are not changed by a replay.';
COMMENT ON COLUMN shadow_classifications.run_id IS 'Replay run; also names its Kafka consumer group, triage-shadow-<run_id>.';
COMMENT ON COLUMN shadow_classifications.model IS 'Model as served by the last call, taken from the response; NULL when there was no response.';
COMMENT ON COLUMN shadow_classifications.attempts IS 'Classifier calls made for the ticket in this run.';
COMMENT ON COLUMN shadow_classifications.cost_usd IS 'Total cost of the calls made for the ticket in this run.';
COMMENT ON COLUMN shadow_classifications.status IS 'Status the run''s thresholds would give; NULL when the classifier gave no valid answer.';
COMMENT ON COLUMN shadow_classifications.raw_response IS 'Raw response of the last call; holds no ticket text.';

-- +goose Down
DROP TABLE shadow_classifications;
