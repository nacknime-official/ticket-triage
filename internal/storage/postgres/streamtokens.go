package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

var _ ticket.StreamTokenStore = (*StreamTokens)(nil)

// StreamTokens stores the single-use SSE stream tokens (PRD §3.1, §4.1),
// only as sha256 hashes.
type StreamTokens struct {
	q Querier
}

// NewStreamTokens returns a stream-token store that runs its statements on q.
func NewStreamTokens(q Querier) *StreamTokens {
	return &StreamTokens{q: q}
}

// Create stores hash as a stream token for the ticket, valid for ttl from now
// by the database clock.
func (s *StreamTokens) Create(ctx context.Context, ticketID uuid.UUID, hash ticket.TokenHash, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("postgres: stream token ttl must be positive")
	}
	_, err := s.q.Exec(ctx,
		`INSERT INTO stream_tokens (token_hash, ticket_id, expires_at) VALUES ($1, $2, now() + $3::interval)`,
		hash[:], ticketID, ttl)
	if err != nil {
		return fmt.Errorf("postgres: insert stream token for ticket %s: %w", ticketID, err)
	}
	return nil
}

// Consume atomically marks the token used. It succeeds at most once per
// token, and only for the ticket the token was minted for. An unknown, used,
// expired, or other ticket's token returns an error wrapping ticket.ErrNotFound,
// the same for every case.
func (s *StreamTokens) Consume(ctx context.Context, ticketID uuid.UUID, hash ticket.TokenHash) error {
	tag, err := s.q.Exec(ctx,
		`UPDATE stream_tokens SET used_at = now()
		 WHERE token_hash = $1 AND ticket_id = $2 AND used_at IS NULL AND expires_at > now()`,
		hash[:], ticketID)
	if err != nil {
		return fmt.Errorf("postgres: consume stream token for ticket %s: %w", ticketID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: stream token for ticket %s: %w", ticketID, ticket.ErrNotFound)
	}
	return nil
}
