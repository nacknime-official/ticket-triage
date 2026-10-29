package ticket_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// memStreamTokens is an in-memory ticket.StreamTokenStore. Tokens never
// expire on their own; expire marks one expired.
type memStreamTokens struct {
	mu     sync.Mutex
	tokens map[ticket.TokenHash]*memStreamToken
	ttls   []time.Duration
	// Errors injected per method.
	createErr, consumeErr error
}

type memStreamToken struct {
	ticketID      uuid.UUID
	used, expired bool
}

func newMemStreamTokens() *memStreamTokens {
	return &memStreamTokens{tokens: make(map[ticket.TokenHash]*memStreamToken)}
}

func (s *memStreamTokens) Create(_ context.Context, ticketID uuid.UUID, hash ticket.TokenHash, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	s.tokens[hash] = &memStreamToken{ticketID: ticketID}
	s.ttls = append(s.ttls, ttl)
	return nil
}

func (s *memStreamTokens) Consume(_ context.Context, ticketID uuid.UUID, hash ticket.TokenHash) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumeErr != nil {
		return s.consumeErr
	}
	tok, ok := s.tokens[hash]
	if !ok || tok.used || tok.expired || tok.ticketID != ticketID {
		return fmt.Errorf("stream token for %s: %w", ticketID, ticket.ErrNotFound)
	}
	tok.used = true
	return nil
}

func (s *memStreamTokens) expire(t ticket.Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[t.Hash()].expired = true
}

func TestStreamsIssueAndOpen(t *testing.T) {
	repo, store := newMemRepo(), newMemStreamTokens()
	svc := ticket.NewService(repo, ticket.Config{})
	streams := ticket.NewStreams(svc, store, 0)
	tk, access, err := svc.Submit(t.Context(), "s", "b")
	if err != nil {
		t.Fatal(err)
	}

	stream, ttl, err := streams.Issue(t.Context(), tk.ID.String(), access)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if ttl != ticket.DefaultStreamTokenTTL || store.ttls[0] != ttl {
		t.Errorf("ttl = %v, stored %v, want %v", ttl, store.ttls, ticket.DefaultStreamTokenTTL)
	}
	if len(stream.Reveal()) < 26 || stream.Reveal() == access.Reveal() {
		t.Errorf("stream token %q is short or equals the access token", stream.Reveal())
	}
	if _, ok := store.tokens[stream.Hash()]; !ok {
		t.Error("the stream token hash is not stored")
	}

	id, err := streams.Open(t.Context(), tk.ID.String(), stream)
	if err != nil || id != tk.ID {
		t.Fatalf("Open = %s, %v, want %s", id, err, tk.ID)
	}
	if _, err := streams.Open(t.Context(), tk.ID.String(), stream); !errors.Is(err, ticket.ErrNotFound) {
		t.Errorf("second Open error = %v, want ErrNotFound", err)
	}

	got, err := streams.Current(t.Context(), id)
	if err != nil || got.ID != tk.ID || got.Status != ticket.StatusPending {
		t.Errorf("Current = %v, %v", got, err)
	}
	if _, err := streams.Current(t.Context(), uuid.NewV7()); err != ticket.ErrNotFound { //nolint:errorlint // the very same error value.
		t.Errorf("Current(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestStreamsIssueNotFound(t *testing.T) {
	repo, store := newMemRepo(), newMemStreamTokens()
	svc := ticket.NewService(repo, ticket.Config{})
	streams := ticket.NewStreams(svc, store, time.Minute)
	tk, _, err := svc.Submit(t.Context(), "s", "b")
	if err != nil {
		t.Fatal(err)
	}
	_, otherAccess, err := svc.Submit(t.Context(), "s2", "b2")
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		id     string
		access ticket.Token
	}{
		"unknown id":             {uuid.NewV7().String(), otherAccess},
		"malformed id":           {"nope", otherAccess},
		"wrong token":            {tk.ID.String(), ticket.TokenFromString("WRONG")},
		"another ticket's token": {tk.ID.String(), otherAccess},
	} {
		t.Run(name, func(t *testing.T) {
			tok, ttl, err := streams.Issue(t.Context(), tc.id, tc.access)
			if err != ticket.ErrNotFound { //nolint:errorlint // the very same error value, not just a wrapper.
				t.Fatalf("Issue error = %v, want ErrNotFound", err)
			}
			if tok.Reveal() != "" || ttl != 0 {
				t.Errorf("Issue returned %q, %v with an error", tok.Reveal(), ttl)
			}
		})
	}
	if len(store.tokens) != 0 {
		t.Errorf("%d tokens stored for refused requests", len(store.tokens))
	}
}

func TestStreamsOpenNotFound(t *testing.T) {
	repo, store := newMemRepo(), newMemStreamTokens()
	svc := ticket.NewService(repo, ticket.Config{})
	streams := ticket.NewStreams(svc, store, time.Minute)
	issue := func() (ticket.Ticket, ticket.Token) {
		t.Helper()
		tk, access, err := svc.Submit(t.Context(), "s", "b")
		if err != nil {
			t.Fatal(err)
		}
		stream, _, err := streams.Issue(t.Context(), tk.ID.String(), access)
		if err != nil {
			t.Fatal(err)
		}
		return tk, stream
	}

	used, usedToken := issue()
	if _, err := streams.Open(t.Context(), used.ID.String(), usedToken); err != nil {
		t.Fatal(err)
	}
	expired, expiredToken := issue()
	store.expire(expiredToken)
	mine, myToken := issue()
	other, _ := issue()

	for name, tc := range map[string]struct {
		id    string
		token ticket.Token
	}{
		"used":              {used.ID.String(), usedToken},
		"expired":           {expired.ID.String(), expiredToken},
		"unknown token":     {mine.ID.String(), ticket.TokenFromString("UNKNOWN")},
		"other ticket's id": {other.ID.String(), myToken},
		"unknown id":        {uuid.NewV7().String(), myToken},
		"malformed id":      {"nope", myToken},
		"empty token":       {mine.ID.String(), ticket.TokenFromString("")},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := streams.Open(t.Context(), tc.id, tc.token)
			if err != ticket.ErrNotFound { //nolint:errorlint // the very same error value, not just a wrapper.
				t.Fatalf("Open error = %v, want ErrNotFound", err)
			}
			if id != (uuid.UUID{}) {
				t.Errorf("Open returned id %s with an error", id)
			}
		})
	}
	// None of the refused attempts spent the token of its own ticket.
	if _, err := streams.Open(t.Context(), mine.ID.String(), myToken); err != nil {
		t.Errorf("Open of the untouched token: %v", err)
	}
}

func TestStreamsStoreErrors(t *testing.T) {
	boom := errors.New("boom")
	repo, store := newMemRepo(), newMemStreamTokens()
	svc := ticket.NewService(repo, ticket.Config{})
	streams := ticket.NewStreams(svc, store, time.Minute)
	tk, access, err := svc.Submit(t.Context(), "s", "b")
	if err != nil {
		t.Fatal(err)
	}

	store.createErr = boom
	if _, _, err := streams.Issue(t.Context(), tk.ID.String(), access); !errors.Is(err, boom) {
		t.Errorf("Issue error = %v, want wrapped boom", err)
	}
	store.consumeErr = boom
	if _, err := streams.Open(t.Context(), tk.ID.String(), access); !errors.Is(err, boom) ||
		errors.Is(err, ticket.ErrNotFound) {
		t.Errorf("Open error = %v, want wrapped boom", err)
	}
	repo.getErr = boom
	if _, err := streams.Current(t.Context(), tk.ID); !errors.Is(err, boom) {
		t.Errorf("Current error = %v, want wrapped boom", err)
	}
}
