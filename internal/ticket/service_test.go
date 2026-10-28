package ticket_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// memRepo is an in-memory ticket.Repository.
type memRepo struct {
	mu      sync.Mutex
	tickets map[uuid.UUID]ticket.Ticket
	gets    int
	// Errors injected per method.
	submitErr, getErr, countErr error
}

func newMemRepo() *memRepo {
	return &memRepo{tickets: make(map[uuid.UUID]ticket.Ticket)}
}

func (r *memRepo) Submit(_ context.Context, t ticket.Ticket) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.submitErr != nil {
		return r.submitErr
	}
	if _, ok := r.tickets[t.ID]; ok {
		return fmt.Errorf("duplicate id %s", t.ID)
	}
	r.tickets[t.ID] = t
	return nil
}

func (r *memRepo) Get(_ context.Context, id uuid.UUID) (ticket.Ticket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gets++
	if r.getErr != nil {
		return ticket.Ticket{}, r.getErr
	}
	t, ok := r.tickets[id]
	if !ok {
		return ticket.Ticket{}, fmt.Errorf("get %s: %w", id, ticket.ErrNotFound)
	}
	return t, nil
}

func (r *memRepo) CountPending(context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.countErr != nil {
		return 0, r.countErr
	}
	n := 0
	for _, t := range r.tickets {
		if t.Status == ticket.StatusPending {
			n++
		}
	}
	return n, nil
}

func (r *memRepo) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tickets)
}

func TestSubmit(t *testing.T) {
	repo := newMemRepo()
	svc := ticket.NewService(repo, ticket.Config{})
	before := time.Now().Truncate(time.Microsecond)

	tk, tok, err := svc.Submit(t.Context(), "Refund", "Charged twice, please refund.")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if v := tk.ID[6] >> 4; v != 7 {
		t.Errorf("id version = %d, want 7", v)
	}
	if tk.Status != ticket.StatusPending || tk.StatusReason != "" || tk.Classification != nil {
		t.Errorf("new ticket state = %q/%q/%v, want pending without reason or classification",
			tk.Status, tk.StatusReason, tk.Classification)
	}
	if tk.CreatedAt.Before(before) || tk.CreatedAt.After(time.Now()) || !tk.UpdatedAt.Equal(tk.CreatedAt) {
		t.Errorf("timestamps created=%v updated=%v not set to submit time", tk.CreatedAt, tk.UpdatedAt)
	}
	if tk.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", tk.CreatedAt.Location())
	}
	if !tk.CreatedAt.Equal(tk.CreatedAt.Truncate(time.Microsecond)) {
		t.Errorf("CreatedAt = %v, want microsecond precision as stored by Postgres", tk.CreatedAt)
	}

	stored, err := repo.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("stored ticket: %v", err)
	}
	if stored.Subject != "Refund" || stored.Body != "Charged twice, please refund." {
		t.Errorf("stored text = %q/%q", stored.Subject, stored.Body)
	}
	if stored.AccessTokenHash != tok.Hash() {
		t.Error("stored hash is not the hash of the returned token")
	}

	got, err := svc.Get(t.Context(), tk.ID.String(), ticket.TokenFromString(tok.Reveal()))
	if err != nil {
		t.Fatalf("Get() with the issued token: %v", err)
	}
	if got.ID != tk.ID {
		t.Errorf("Get() id = %s, want %s", got.ID, tk.ID)
	}
}

func TestSubmitDistinctIDsAndTokens(t *testing.T) {
	svc := ticket.NewService(newMemRepo(), ticket.Config{})
	a, ta, err := svc.Submit(t.Context(), "a", "a")
	if err != nil {
		t.Fatal(err)
	}
	b, tb, err := svc.Submit(t.Context(), "b", "b")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || ta.Reveal() == tb.Reveal() {
		t.Error("two submits share an id or a token")
	}
	if a.ID.Compare(b.ID) >= 0 {
		t.Errorf("UUIDv7 ids not time-ordered: %s then %s", a.ID, b.ID)
	}
}

func TestSubmitValidation(t *testing.T) {
	repo := newMemRepo()
	svc := ticket.NewService(repo, ticket.Config{Limits: ticket.Limits{MaxSubjectChars: 3}})

	_, _, err := svc.Submit(t.Context(), "four", "")
	var verr *ticket.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Submit() error = %v, want *ValidationError", err)
	}
	if len(verr.Problems) != 2 {
		t.Errorf("Problems = %+v, want subject too long and body required", verr.Problems)
	}
	if repo.len() != 0 {
		t.Error("invalid ticket was stored")
	}

	// Unset limits take the defaults.
	_, _, err = svc.Submit(t.Context(), "abc", strings.Repeat("ж", ticket.DefaultMaxBodyChars+1))
	want := []ticket.Problem{{Field: ticket.FieldBody, Code: ticket.ProblemTooLong, Max: ticket.DefaultMaxBodyChars}}
	if !errors.As(err, &verr) || !slices.Equal(verr.Problems, want) {
		t.Errorf("Submit() error = %v, want %+v", err, want)
	}
}

func TestSubmitPendingCap(t *testing.T) {
	tests := []struct {
		name       string
		maxPending int
		existing   []ticket.Status
		wantErr    error
	}{
		{"below cap", 2, []ticket.Status{ticket.StatusPending}, nil},
		{"at cap", 2, []ticket.Status{ticket.StatusPending, ticket.StatusPending}, ticket.ErrBacklogFull},
		{"non-pending not counted", 2, []ticket.Status{ticket.StatusPending, ticket.StatusClassified, ticket.StatusNeedsHuman}, nil},
		{"cap of one", 1, []ticket.Status{ticket.StatusPending}, ticket.ErrBacklogFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMemRepo()
			for _, s := range tt.existing {
				repo.tickets[uuid.NewV7()] = ticket.Ticket{Status: s}
			}
			svc := ticket.NewService(repo, ticket.Config{MaxPending: tt.maxPending})
			_, _, err := svc.Submit(t.Context(), "s", "b")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Submit() error = %v, want %v", err, tt.wantErr)
			}
			wantLen := len(tt.existing)
			if tt.wantErr == nil {
				wantLen++
			}
			if repo.len() != wantLen {
				t.Errorf("stored %d tickets, want %d", repo.len(), wantLen)
			}
		})
	}
}

func TestSubmitDefaultPendingCap(t *testing.T) {
	repo := newMemRepo()
	for range ticket.DefaultMaxPending {
		repo.tickets[uuid.NewV7()] = ticket.Ticket{Status: ticket.StatusPending}
	}
	svc := ticket.NewService(repo, ticket.Config{})
	if _, _, err := svc.Submit(t.Context(), "s", "b"); !errors.Is(err, ticket.ErrBacklogFull) {
		t.Fatalf("Submit() error = %v, want ErrBacklogFull", err)
	}
}

func TestSubmitRepositoryErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, setup := range map[string]func(*memRepo){
		"count":  func(r *memRepo) { r.countErr = boom },
		"submit": func(r *memRepo) { r.submitErr = boom },
	} {
		t.Run(name, func(t *testing.T) {
			repo := newMemRepo()
			setup(repo)
			_, tok, err := ticket.NewService(repo, ticket.Config{}).Submit(t.Context(), "s", "b")
			if !errors.Is(err, boom) {
				t.Fatalf("Submit() error = %v, want wrapped boom", err)
			}
			if tok.Reveal() != "" {
				t.Error("token returned with an error")
			}
		})
	}
}

func TestGetNotFoundEquivalence(t *testing.T) {
	repo := newMemRepo()
	svc := ticket.NewService(repo, ticket.Config{})
	tk, tok, err := svc.Submit(t.Context(), "s", "b")
	if err != nil {
		t.Fatal(err)
	}
	other, otherTok, err := svc.Submit(t.Context(), "s2", "b2")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		id    string
		token ticket.Token
	}{
		{"unknown id", uuid.NewV7().String(), tok},
		{"malformed id", "not-a-uuid", tok},
		{"empty id", "", tok},
		{"wrong token", tk.ID.String(), ticket.TokenFromString("WRONG")},
		{"empty token", tk.ID.String(), ticket.TokenFromString("")},
		{"zero token", tk.ID.String(), ticket.Token{}},
		{"another ticket's token", tk.ID.String(), otherTok},
		{"token for another id", other.ID.String(), tok},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.Get(t.Context(), tt.id, tt.token)
			if err != ticket.ErrNotFound { //nolint:errorlint // the very same error value, not just a wrapper.
				t.Fatalf("Get() error = %v, want exactly ErrNotFound", err)
			}
			if got != (ticket.Ticket{}) {
				t.Errorf("Get() returned ticket data with ErrNotFound: %v", got)
			}
		})
	}
}

func TestGetMalformedIDSkipsRepository(t *testing.T) {
	repo := newMemRepo()
	svc := ticket.NewService(repo, ticket.Config{})
	if _, err := svc.Get(t.Context(), "../etc/passwd", ticket.Token{}); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
	if repo.gets != 0 {
		t.Errorf("repository called %d times for a malformed id", repo.gets)
	}
}

func TestGetRepositoryError(t *testing.T) {
	boom := errors.New("connection refused")
	repo := newMemRepo()
	repo.getErr = boom
	_, err := ticket.NewService(repo, ticket.Config{}).Get(t.Context(), uuid.NewV7().String(), ticket.Token{})
	if !errors.Is(err, boom) {
		t.Fatalf("Get() error = %v, want wrapped repository error", err)
	}
	if errors.Is(err, ticket.ErrNotFound) {
		t.Error("an infrastructure error must not look like not-found")
	}
}
