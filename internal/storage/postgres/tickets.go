package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

var _ ticket.Repository = (*Tickets)(nil)

// Tickets stores tickets in the tickets table.
type Tickets struct {
	q Querier
}

// NewTickets returns a ticket repository that runs its statements on q.
func NewTickets(q Querier) *Tickets {
	return &Tickets{q: q}
}

const insertTicket = `
INSERT INTO tickets (
    id, subject, body, status, status_reason, access_token_hash,
    category, category_confidence, complaint_p, is_complaint, complaint_uncertain,
    urgency, urgency_confidence, priority,
    attempts, last_error, created_at, updated_at, classified_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`

// Create inserts t with every column it maps to. Timestamps are stored with
// microsecond precision.
func (r *Tickets) Create(ctx context.Context, t ticket.Ticket) error {
	var (
		category                                   *string
		categoryConf, complaintP, urgency, urgConf *float64
		isComplaint                                *bool
		complaintUncertain                         bool
		priority                                   *string
	)
	if c := t.Classification; c != nil {
		category = &c.Category
		categoryConf, complaintP = &c.CategoryConfidence, &c.ComplaintP
		urgency, urgConf = &c.Urgency, &c.UrgencyConfidence
		isComplaint = &c.IsComplaint
		complaintUncertain = c.ComplaintUncertain
		p := c.Priority().String()
		priority = &p
	}
	_, err := r.q.Exec(ctx, insertTicket,
		t.ID, t.Subject, t.Body, string(t.Status), nullString(string(t.StatusReason)), t.AccessTokenHash[:],
		category, categoryConf, complaintP, isComplaint, complaintUncertain,
		urgency, urgConf, priority,
		t.Attempts, nullString(t.LastError), t.CreatedAt, t.UpdatedAt, nullTime(t.ClassifiedAt))
	if err != nil {
		return fmt.Errorf("postgres: insert ticket: %w", err)
	}
	return nil
}

// selectTicket reads every column but priority, which the domain derives from
// urgency (ticket.Classification.Priority).
const selectTicket = `
SELECT id, subject, body, status, status_reason, access_token_hash,
       category, category_confidence, complaint_p, is_complaint, complaint_uncertain,
       urgency, urgency_confidence,
       attempts, last_error, created_at, updated_at, classified_at
FROM tickets
WHERE id = $1`

// Get returns the ticket with the id, or an error wrapping ticket.ErrNotFound.
// The Classification is set when the row has a category; a missing score in
// such a row reads as zero.
func (r *Tickets) Get(ctx context.Context, id uuid.UUID) (ticket.Ticket, error) {
	var (
		t                                          ticket.Ticket
		status                                     string
		reason, lastError, category                *string
		hash                                       []byte
		categoryConf, complaintP, urgency, urgConf *float64
		isComplaint                                *bool
		complaintUncertain                         bool
		classifiedAt                               *time.Time
	)
	err := r.q.QueryRow(ctx, selectTicket, id).Scan(
		&t.ID, &t.Subject, &t.Body, &status, &reason, &hash,
		&category, &categoryConf, &complaintP, &isComplaint, &complaintUncertain,
		&urgency, &urgConf,
		&t.Attempts, &lastError, &t.CreatedAt, &t.UpdatedAt, &classifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ticket.Ticket{}, fmt.Errorf("postgres: ticket %s: %w", id, ticket.ErrNotFound)
	}
	if err != nil {
		return ticket.Ticket{}, fmt.Errorf("postgres: select ticket %s: %w", id, err)
	}
	if len(hash) != len(t.AccessTokenHash) {
		return ticket.Ticket{}, fmt.Errorf("postgres: ticket %s: access token hash has %d bytes, want %d",
			id, len(hash), len(t.AccessTokenHash))
	}
	copy(t.AccessTokenHash[:], hash)

	t.Status = ticket.Status(status)
	t.StatusReason = ticket.Reason(deref(reason))
	t.LastError = deref(lastError)
	t.CreatedAt = t.CreatedAt.UTC()
	t.UpdatedAt = t.UpdatedAt.UTC()
	if classifiedAt != nil {
		t.ClassifiedAt = classifiedAt.UTC()
	}
	if category != nil {
		t.Classification = &ticket.Classification{
			Category:           *category,
			CategoryConfidence: deref(categoryConf),
			ComplaintP:         deref(complaintP),
			IsComplaint:        deref(isComplaint),
			ComplaintUncertain: complaintUncertain,
			Urgency:            deref(urgency),
			UrgencyConfidence:  deref(urgConf),
		}
	}
	return t, nil
}

// CountPending returns the number of tickets in ticket.StatusPending.
func (r *Tickets) CountPending(ctx context.Context) (int, error) {
	var n int
	err := r.q.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE status = $1`, string(ticket.StatusPending)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("postgres: count pending tickets: %w", err)
	}
	return n, nil
}

// nullString maps "" to NULL.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullTime maps the zero time to NULL.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// deref returns *p, or the zero value for a NULL column.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
