package postgres

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/shadow"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

var _ shadow.Store = (*ShadowStore)(nil)

// ShadowStore is the storage of shadow.Processor: ticket reads, the daily
// spend, and the shadow_classifications rows. It never writes tickets or
// classifications.
type ShadowStore struct {
	q Querier
}

// NewShadowStore returns a shadow store that runs its statements on q.
func NewShadowStore(q Querier) *ShadowStore {
	return &ShadowStore{q: q}
}

// Ticket implements shadow.Store.
func (s *ShadowStore) Ticket(ctx context.Context, id uuid.UUID) (ticket.Ticket, error) {
	// Only Get is used, so no submitted topic is needed.
	return NewTickets(s.q, "").Get(ctx, id)
}

// SpentSince implements shadow.Store.
func (s *ShadowStore) SpentSince(ctx context.Context, since time.Time) (float64, error) {
	return spentSince(ctx, s.q, since)
}

// spentSince sums the cost of the live and shadow classifier calls recorded
// at or after since: both draw on the one daily budget. Calls without a cost
// count as zero.
func spentSince(ctx context.Context, q Querier, since time.Time) (float64, error) {
	var spent float64
	err := q.QueryRow(ctx, `
SELECT coalesce((SELECT sum(cost_usd) FROM classifications WHERE created_at >= $1), 0)::float8
     + coalesce((SELECT sum(cost_usd) FROM shadow_classifications WHERE created_at >= $1), 0)::float8`,
		since).Scan(&spent)
	if err != nil {
		return 0, fmt.Errorf("postgres: sum classifier cost since %s: %w", since.Format(time.RFC3339), err)
	}
	return spent, nil
}

// Done implements shadow.Store.
func (s *ShadowStore) Done(ctx context.Context, runID string, id uuid.UUID) (bool, error) {
	var done bool
	err := s.q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM shadow_classifications WHERE run_id = $1 AND ticket_id = $2 AND outcome = 'ok')`,
		runID, id).Scan(&done)
	if err != nil {
		return false, fmt.Errorf("postgres: check shadow row of run %s, ticket %s: %w", runID, id, err)
	}
	return done, nil
}

// upsertShadow stores the latest result of a run for a ticket. A replaced row
// (a failed classification retried by a resumed run) keeps adding up its
// attempts and cost, and its created_at moves to the last calls, so the
// budget counts the whole cost on that day.
const upsertShadow = `
INSERT INTO shadow_classifications (
    run_id, ticket_id, provider, model, attempts, outcome, http_status, latency_ms,
    input_tokens, output_tokens, cost_usd, cost_source,
    complaint_p, category, category_confidence, urgency, urgency_confidence,
    status, status_reason, complaint_uncertain, last_error, raw_response, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
ON CONFLICT (run_id, ticket_id) DO UPDATE SET
    provider = excluded.provider, model = excluded.model,
    attempts = shadow_classifications.attempts + excluded.attempts,
    outcome = excluded.outcome, http_status = excluded.http_status, latency_ms = excluded.latency_ms,
    input_tokens = excluded.input_tokens, output_tokens = excluded.output_tokens,
    cost_usd = CASE WHEN shadow_classifications.cost_usd IS NULL AND excluded.cost_usd IS NULL THEN NULL
        ELSE coalesce(shadow_classifications.cost_usd, 0) + coalesce(excluded.cost_usd, 0) END,
    cost_source = coalesce(excluded.cost_source, shadow_classifications.cost_source),
    complaint_p = excluded.complaint_p, category = excluded.category,
    category_confidence = excluded.category_confidence, urgency = excluded.urgency,
    urgency_confidence = excluded.urgency_confidence, status = excluded.status,
    status_reason = excluded.status_reason, complaint_uncertain = excluded.complaint_uncertain,
    last_error = excluded.last_error, raw_response = excluded.raw_response, created_at = excluded.created_at`

// Save implements shadow.Store.
func (s *ShadowStore) Save(ctx context.Context, r shadow.Result) error {
	m := r.Answer.Call
	var (
		cost                                       *float64
		complaintP, categoryConf, urgency, urgConf *float64
		category                                   *string
	)
	if r.CostSource != "" {
		cost = &r.CostUSD
	}
	if r.Outcome == shadow.OutcomeOK {
		a := r.Answer
		complaintP, categoryConf = &a.ComplaintP, &a.CategoryConfidence
		urgency, urgConf = &a.Urgency, &a.UrgencyConfidence
		category = &a.Category
	}
	_, err := s.q.Exec(ctx, upsertShadow,
		r.RunID, r.TicketID, m.Provider, nullString(m.Model), r.Attempts, string(r.Outcome), nullInt(m.HTTPStatus),
		m.Latency.Milliseconds(), m.InputTokens, m.OutputTokens, cost, nullString(string(r.CostSource)),
		complaintP, category, categoryConf, urgency, urgConf,
		nullString(string(r.Status)), nullString(string(r.Reason)), r.ComplaintUncertain,
		nullString(r.LastError), rawJSON(m.RawResponse), r.At)
	if err != nil {
		return fmt.Errorf("postgres: save shadow row of run %s, ticket %s: %w", r.RunID, r.TicketID, err)
	}
	return nil
}
