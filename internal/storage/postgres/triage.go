package postgres

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

var _ triage.Store = (*TriageStore)(nil)

// TriageDB is what TriageStore runs on; *pgxpool.Pool satisfies it.
type TriageDB interface {
	Querier
	Beginner
}

// TriageStore is the storage of triage.Processor: ticket reads, the daily
// spend, and the result write with its classifications audit rows.
type TriageStore struct {
	db TriageDB
}

// NewTriageStore returns a triage store on db.
func NewTriageStore(db TriageDB) *TriageStore {
	return &TriageStore{db: db}
}

// Ticket implements triage.Store.
func (s *TriageStore) Ticket(ctx context.Context, id uuid.UUID) (ticket.Ticket, error) {
	return NewTickets(s.db).Get(ctx, id)
}

// SpentSince implements triage.Store. Calls without a cost count as zero.
func (s *TriageStore) SpentSince(ctx context.Context, since time.Time) (float64, error) {
	var spent float64
	err := s.db.QueryRow(ctx,
		`SELECT coalesce(sum(cost_usd), 0)::float8 FROM classifications WHERE created_at >= $1`, since).Scan(&spent)
	if err != nil {
		return 0, fmt.Errorf("postgres: sum classifier cost since %s: %w", since.Format(time.RFC3339), err)
	}
	return spent, nil
}

// finishTicket moves a pending ticket to a final status.
const finishTicket = `
UPDATE tickets SET
    status = $2, status_reason = $3,
    category = $4, category_confidence = $5, complaint_p = $6, is_complaint = $7, complaint_uncertain = $8,
    urgency = $9, urgency_confidence = $10, priority = $11,
    attempts = attempts + $12, last_error = coalesce($13, last_error),
    updated_at = $14, classified_at = $14
WHERE id = $1 AND status = 'pending'`

// recordAttempts keeps a ticket pending and records its failed calls.
const recordAttempts = `
UPDATE tickets SET attempts = attempts + $2, last_error = coalesce($3, last_error), updated_at = $4
WHERE id = $1 AND status = 'pending'`

const insertClassification = `
INSERT INTO classifications (
    ticket_id, provider, model, attempt, outcome, http_status, latency_ms,
    input_tokens, output_tokens, cost_usd, cost_source,
    complaint_p, category, category_confidence, urgency, urgency_confidence, raw_response
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`

// Apply implements triage.Store.
func (s *TriageStore) Apply(ctx context.Context, u triage.Update) (bool, error) {
	applied := false
	err := WithTx(ctx, s.db, func(tx pgx.Tx) error {
		n, err := updateTicket(ctx, tx, u)
		if err != nil || n == 0 {
			return err
		}
		if err := insertCalls(ctx, tx, u.TicketID, u.Calls); err != nil {
			return err
		}
		// T19: the status event of a final update goes to the outbox here, in
		// this transaction.
		applied = true
		return nil
	})
	return applied, err
}

func updateTicket(ctx context.Context, tx pgx.Tx, u triage.Update) (int64, error) {
	lastErr := nullString(u.LastError)
	if u.Status == ticket.StatusPending {
		tag, err := tx.Exec(ctx, recordAttempts, u.TicketID, len(u.Calls), lastErr, u.At)
		if err != nil {
			return 0, fmt.Errorf("postgres: record attempts of ticket %s: %w", u.TicketID, err)
		}
		return tag.RowsAffected(), nil
	}

	var (
		category                                   *string
		categoryConf, complaintP, urgency, urgConf *float64
		isComplaint                                *bool
		complaintUncertain                         bool
		priority                                   *string
	)
	if c := u.Classification; c != nil {
		category = &c.Category
		categoryConf, complaintP = &c.CategoryConfidence, &c.ComplaintP
		urgency, urgConf = &c.Urgency, &c.UrgencyConfidence
		isComplaint = &c.IsComplaint
		complaintUncertain = c.ComplaintUncertain
		p := c.Priority().String()
		priority = &p
	}
	tag, err := tx.Exec(ctx, finishTicket, u.TicketID, string(u.Status), nullString(string(u.Reason)),
		category, categoryConf, complaintP, isComplaint, complaintUncertain,
		urgency, urgConf, priority,
		len(u.Calls), lastErr, u.At)
	if err != nil {
		return 0, fmt.Errorf("postgres: update ticket %s: %w", u.TicketID, err)
	}
	return tag.RowsAffected(), nil
}

// insertCalls writes one audit row per call in a single round trip.
func insertCalls(ctx context.Context, tx pgx.Tx, id uuid.UUID, calls []triage.Call) error {
	if len(calls) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, c := range calls {
		m := c.Result.Call
		var (
			cost                                       *float64
			complaintP, categoryConf, urgency, urgConf *float64
			category                                   *string
		)
		if m.CostSource != "" {
			cost = &m.CostUSD
		}
		if c.Outcome == triage.CallOK {
			r := c.Result
			complaintP, categoryConf = &r.ComplaintP, &r.CategoryConfidence
			urgency, urgConf = &r.Urgency, &r.UrgencyConfidence
			category = &r.Category
		}
		b.Queue(insertClassification,
			id, m.Provider, nullString(m.Model), c.Attempt, string(c.Outcome), nullInt(m.HTTPStatus),
			m.Latency.Milliseconds(), m.InputTokens, m.OutputTokens, cost, nullString(string(m.CostSource)),
			complaintP, category, categoryConf, urgency, urgConf, rawJSON(m.RawResponse))
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return fmt.Errorf("postgres: insert classifications of ticket %s: %w", id, err)
	}
	return nil
}

// rawJSON returns raw for a jsonb column: NULL when empty, as is when it is
// valid JSON, and otherwise as a JSON string, so a non-JSON error body is
// still kept.
func rawJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	if jsontext.Value(raw).IsValid() {
		return raw
	}
	quoted, err := json.Marshal(string(raw))
	if err != nil {
		// Invalid UTF-8; the bytes are not worth failing the write for.
		return nil
	}
	return quoted
}

// nullInt maps 0 to NULL; an HTTP status of 0 means there was no response.
func nullInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}
