package ticket

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"
)

// DefaultMaxPending is the default cap on pending tickets (README §4.1).
const DefaultMaxPending = 1000

var (
	// ErrNotFound means the ticket does not exist or the caller may not see it.
	// Service.Get returns it for an unknown id, a malformed id and a wrong
	// token alike, so callers cannot tell the cases apart.
	ErrNotFound = errors.New("ticket: not found")
	// ErrBacklogFull means the pending cap is reached; retry later.
	ErrBacklogFull = errors.New("ticket: too many pending tickets")
)

// Repository is the storage the Service needs.
type Repository interface {
	// Submit stores the new ticket t together with its Submitted event, in
	// one transaction, so the event is published if and only if the ticket
	// is stored (README §4.2: no dual writes).
	Submit(ctx context.Context, t Ticket) error
	// Get returns the ticket with the id, or an error wrapping ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (Ticket, error)
	// CountPending returns the number of tickets in StatusPending.
	CountPending(ctx context.Context) (int, error)
}

// Config holds the Service limits. Zero fields take the defaults.
type Config struct {
	Limits
	// MaxPending is the pending-ticket count at which Submit refuses new
	// tickets with ErrBacklogFull.
	MaxPending int
}

func (c Config) withDefaults() Config {
	if c.MaxSubjectChars <= 0 {
		c.MaxSubjectChars = DefaultMaxSubjectChars
	}
	if c.MaxBodyChars <= 0 {
		c.MaxBodyChars = DefaultMaxBodyChars
	}
	if c.MaxPending <= 0 {
		c.MaxPending = DefaultMaxPending
	}
	return c
}

// Service submits and reads tickets.
type Service struct {
	repo Repository
	cfg  Config
	now  func() time.Time
}

// NewService returns a Service backed by repo.
func NewService(repo Repository, cfg Config) *Service {
	return &Service{repo: repo, cfg: cfg.withDefaults(), now: time.Now}
}

// Submit validates the input, refuses it while the pending cap is reached,
// and stores a new pending ticket along with its Submitted event. The returned token is the only copy of
// the plaintext access token; only its hash is stored.
//
// The cap is a soft limit: concurrent submits that count before either
// inserts may overshoot it by the number in flight.
func (s *Service) Submit(ctx context.Context, subject, body string) (Ticket, Token, error) {
	if err := s.cfg.Validate(subject, body); err != nil {
		return Ticket{}, Token{}, err
	}
	pending, err := s.repo.CountPending(ctx)
	if err != nil {
		return Ticket{}, Token{}, fmt.Errorf("ticket: count pending: %w", err)
	}
	if pending >= s.cfg.MaxPending {
		return Ticket{}, Token{}, ErrBacklogFull
	}

	token, hash := NewToken()
	// Postgres keeps microseconds; truncating here makes the returned ticket
	// equal to the one read back later.
	now := s.now().UTC().Truncate(time.Microsecond)
	t := Ticket{
		ID:              uuid.NewV7(),
		Subject:         subject,
		Body:            body,
		Status:          StatusPending,
		AccessTokenHash: hash,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.repo.Submit(ctx, t); err != nil {
		return Ticket{}, Token{}, fmt.Errorf("ticket: submit %s: %w", t.ID, err)
	}
	return t, token, nil
}

// Get returns the ticket with the given id if token is its access token.
// A malformed id, an unknown id and a wrong or empty token all return
// ErrNotFound; a token is compared even when no ticket exists, so the cases
// cost the same.
func (s *Service) Get(ctx context.Context, id string, token Token) (Ticket, error) {
	tid, err := uuid.Parse(id)
	if err != nil {
		dummyHash.Matches(token)
		return Ticket{}, ErrNotFound
	}
	t, err := s.repo.Get(ctx, tid)
	if errors.Is(err, ErrNotFound) {
		dummyHash.Matches(token)
		return Ticket{}, ErrNotFound
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("ticket: get %s: %w", tid, err)
	}
	if !t.AccessTokenHash.Matches(token) {
		return Ticket{}, ErrNotFound
	}
	return t, nil
}
