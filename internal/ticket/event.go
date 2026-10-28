package ticket

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"
	"uuid"
)

// Submitted is the event that announces a new ticket to the classifier
// workers (README §5). It carries the id only, never ticket text: the worker
// reads the ticket from storage.
type Submitted struct {
	TicketID    uuid.UUID `json:"ticket_id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// SubmittedOf returns the Submitted event of t.
func SubmittedOf(t Ticket) Submitted {
	return Submitted{TicketID: t.ID, SubmittedAt: t.CreatedAt}
}

// Marshal encodes the event as its JSON payload.
func (e Submitted) Marshal() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("ticket: marshal submitted event %s: %w", e.TicketID, err)
	}
	return b, nil
}

// UnmarshalSubmitted decodes a Submitted payload. Unknown fields are
// rejected and a missing ticket id is an error, so a malformed record is
// treated as poison rather than processed with a zero id.
func UnmarshalSubmitted(b []byte) (Submitted, error) {
	var e Submitted
	if err := json.Unmarshal(b, &e, json.RejectUnknownMembers(true)); err != nil {
		return Submitted{}, fmt.Errorf("ticket: unmarshal submitted event: %w", err)
	}
	if e.TicketID == uuid.Nil() {
		return Submitted{}, errors.New("ticket: submitted event without ticket_id")
	}
	return e, nil
}

// StatusChangedType names the StatusChanged event; the outbox message carries
// it in its "type" header.
const StatusChangedType = "ticket.status_changed"

// StatusChanged is the event that announces a ticket leaving pending to the
// API instances (README §2, step 4; §3.4). It carries ids and status fields
// only, never ticket text; its fields match GET /tickets/{id}, with null for
// what is not set.
type StatusChanged struct {
	TicketID     uuid.UUID `json:"ticket_id"`
	Status       Status    `json:"status"`
	StatusReason *Reason   `json:"status_reason"`
	Category     *string   `json:"category"`
	Priority     *string   `json:"priority"`
	IsComplaint  *bool     `json:"is_complaint"`
	Message      string    `json:"message"`
	At           time.Time `json:"at"`
}

// StatusChangedOf returns the StatusChanged event of t, at t.UpdatedAt.
func StatusChangedOf(t Ticket) StatusChanged {
	e := StatusChanged{TicketID: t.ID, Status: t.Status, Message: t.Message(), At: t.UpdatedAt}
	if t.StatusReason != "" {
		reason := t.StatusReason
		e.StatusReason = &reason
	}
	if c := t.Classification; c != nil {
		category, priority, complaint := c.Category, c.Priority().String(), c.IsComplaint
		e.Category, e.Priority, e.IsComplaint = &category, &priority, &complaint
	}
	return e
}

// Marshal encodes the event as its JSON payload.
func (e StatusChanged) Marshal() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("ticket: marshal status event %s: %w", e.TicketID, err)
	}
	return b, nil
}

// UnmarshalStatusChanged decodes a StatusChanged payload. Unknown fields are
// rejected, and a missing ticket id or an unknown status is an error.
func UnmarshalStatusChanged(b []byte) (StatusChanged, error) {
	var e StatusChanged
	if err := json.Unmarshal(b, &e, json.RejectUnknownMembers(true)); err != nil {
		return StatusChanged{}, fmt.Errorf("ticket: unmarshal status event: %w", err)
	}
	if e.TicketID == uuid.Nil() {
		return StatusChanged{}, errors.New("ticket: status event without ticket_id")
	}
	if !e.Status.Valid() {
		return StatusChanged{}, fmt.Errorf("ticket: status event %s with unknown status %q", e.TicketID, e.Status)
	}
	return e, nil
}
