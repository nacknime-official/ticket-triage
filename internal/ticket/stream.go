package ticket

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"
)

// DefaultStreamTokenTTL is the default lifetime of a stream token (PRD §4.1).
const DefaultStreamTokenTTL = 60 * time.Second

// StreamTokenStore stores stream tokens by their hash.
type StreamTokenStore interface {
	// Create stores hash as a stream token for the ticket, valid for ttl.
	Create(ctx context.Context, ticketID uuid.UUID, hash TokenHash, ttl time.Duration) error
	// Consume atomically marks the token used. It succeeds at most once per
	// token, only for the ticket it was created for and only before it
	// expires; every other case returns an error wrapping ErrNotFound.
	Consume(ctx context.Context, ticketID uuid.UUID, hash TokenHash) error
}

// Streams issues and redeems the single-use stream tokens that open the SSE
// stream of a ticket (PRD §3.4). EventSource cannot send headers, so the
// page trades its access token for a short-lived stream token it can put in
// the URL.
type Streams struct {
	svc   *Service
	store StreamTokenStore
	ttl   time.Duration
}

// NewStreams returns Streams that check access tokens with svc and keep
// stream tokens in store for ttl; ttl <= 0 means DefaultStreamTokenTTL.
func NewStreams(svc *Service, store StreamTokenStore, ttl time.Duration) *Streams {
	if ttl <= 0 {
		ttl = DefaultStreamTokenTTL
	}
	return &Streams{svc: svc, store: store, ttl: ttl}
}

// Issue returns a new stream token for the ticket and its lifetime, if access
// is the ticket's access token. It fails exactly like Service.Get, with
// ErrNotFound for every not-found or unauthorized case. Only the token's hash
// is stored.
func (s *Streams) Issue(ctx context.Context, id string, access Token) (Token, time.Duration, error) {
	t, err := s.svc.Get(ctx, id, access)
	if err != nil {
		return Token{}, 0, err
	}
	token, hash := NewToken()
	if err := s.store.Create(ctx, t.ID, hash, s.ttl); err != nil {
		return Token{}, 0, fmt.Errorf("ticket: issue stream token for %s: %w", t.ID, err)
	}
	return token, s.ttl, nil
}

// Open consumes the stream token of the ticket and returns the ticket id. An
// unknown, used, expired or other ticket's token and a malformed id all
// return ErrNotFound. The token is spent even if the caller then fails.
func (s *Streams) Open(ctx context.Context, id string, token Token) (uuid.UUID, error) {
	tid, err := uuid.Parse(id)
	if err != nil {
		return uuid.UUID{}, ErrNotFound
	}
	err = s.store.Consume(ctx, tid, token.Hash())
	if errors.Is(err, ErrNotFound) {
		return uuid.UUID{}, ErrNotFound
	}
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("ticket: open stream of %s: %w", tid, err)
	}
	return tid, nil
}

// Current returns the ticket without checking a token. Call it only for an id
// that Open returned: the consumed stream token is the proof of access.
func (s *Streams) Current(ctx context.Context, id uuid.UUID) (Ticket, error) {
	t, err := s.svc.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Ticket{}, ErrNotFound
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("ticket: get %s: %w", id, err)
	}
	return t, nil
}
