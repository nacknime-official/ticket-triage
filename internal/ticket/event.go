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
