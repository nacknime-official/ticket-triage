//go:build integration

package postgres_test

import (
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/migrations"
)

// postgresImage matches the Postgres service in compose.yaml.
const postgresImage = "pgvector/pgvector:0.8.7-pg16"

// newPool starts a fresh Postgres container, applies every migration and
// returns a pool connected to it.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()
	ctr, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("triage"),
		tcpostgres.WithUsername("triage"),
		tcpostgres.WithPassword("triage"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newTicket returns a pending ticket with a fresh id and access token hash.
func newTicket(t *testing.T) ticket.Ticket {
	t.Helper()
	_, hash := ticket.NewToken()
	now := time.Now().UTC().Truncate(time.Microsecond)
	return ticket.Ticket{
		ID:              uuid.NewV7(),
		Subject:         "Refund",
		Body:            "Charged twice, please refund.",
		Status:          ticket.StatusPending,
		AccessTokenHash: hash,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func createTicket(t *testing.T, repo *postgres.Tickets, tk ticket.Ticket) ticket.Ticket {
	t.Helper()
	if err := repo.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return tk
}

// asReal rounds v the way a Postgres real column stores it.
func asReal(v float64) float64 { return float64(float32(v)) }

func TestTicketRoundTrip(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	repo := postgres.NewTickets(pool)

	classified := newTicket(t)
	classified.Status = ticket.StatusClassified
	classified.Classification = &ticket.Classification{
		Category:           "billing",
		CategoryConfidence: 0.91,
		ComplaintP:         0.82,
		IsComplaint:        true,
		Urgency:            2.2,
		UrgencyConfidence:  0.7,
	}
	classified.Attempts = 1
	classified.UpdatedAt = classified.CreatedAt.Add(3 * time.Second)
	classified.ClassifiedAt = classified.UpdatedAt

	lowConfidence := newTicket(t)
	lowConfidence.Status = ticket.StatusNeedsHuman
	lowConfidence.StatusReason = ticket.ReasonLowConfidence
	lowConfidence.Classification = &ticket.Classification{
		Category:           "technical",
		CategoryConfidence: 0.4,
		ComplaintP:         0.5,
		ComplaintUncertain: true,
		Urgency:            0.3,
		UrgencyConfidence:  0.55,
	}
	lowConfidence.ClassifiedAt = lowConfidence.CreatedAt.Add(time.Second)

	unavailable := newTicket(t)
	unavailable.Status = ticket.StatusNeedsHuman
	unavailable.StatusReason = ticket.ReasonClassifierUnavailable
	unavailable.Attempts = 5
	unavailable.LastError = "jev: 529 overloaded"
	unavailable.ClassifiedAt = unavailable.CreatedAt.Add(time.Minute)

	tests := []struct {
		name         string
		in           ticket.Ticket
		wantPriority *string
	}{
		{"pending", newTicket(t), nil},
		{"classified", classified, new("high")},
		{"needs_human with classification", lowConfidence, new("low")},
		{"needs_human without classification", unavailable, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			createTicket(t, repo, tt.in)

			got, err := repo.Get(ctx, tt.in.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			want := tt.in
			if c := want.Classification; c != nil {
				rounded := *c
				rounded.CategoryConfidence = asReal(c.CategoryConfidence)
				rounded.ComplaintP = asReal(c.ComplaintP)
				rounded.Urgency = asReal(c.Urgency)
				rounded.UrgencyConfidence = asReal(c.UrgencyConfidence)
				want.Classification = &rounded
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Get mismatch\n got: %#v\nwant: %#v", got, want)
			}
			if got.CreatedAt.Location() != time.UTC {
				t.Errorf("CreatedAt location = %v, want UTC", got.CreatedAt.Location())
			}

			var priority *string
			if err := pool.QueryRow(ctx, `SELECT priority FROM tickets WHERE id = $1`, tt.in.ID).Scan(&priority); err != nil {
				t.Fatalf("read priority: %v", err)
			}
			if !reflect.DeepEqual(priority, tt.wantPriority) {
				t.Errorf("priority column = %v, want %v", deref(priority), deref(tt.wantPriority))
			}
		})
	}
}

func deref(p *string) string {
	if p == nil {
		return "NULL"
	}
	return *p
}

func TestTicketCreateDuplicate(t *testing.T) {
	t.Parallel()
	repo := postgres.NewTickets(newPool(t))
	tk := createTicket(t, repo, newTicket(t))
	if err := repo.Create(t.Context(), tk); err == nil {
		t.Fatal("second Create with the same id succeeded, want an error")
	}
}

func TestTicketGetNotFound(t *testing.T) {
	t.Parallel()
	repo := postgres.NewTickets(newPool(t))
	createTicket(t, repo, newTicket(t))

	_, err := repo.Get(t.Context(), uuid.NewV7())
	if !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("Get of unknown id error = %v, want ticket.ErrNotFound", err)
	}
}

func TestCountPending(t *testing.T) {
	t.Parallel()
	repo := postgres.NewTickets(newPool(t))
	ctx := t.Context()

	count := func() int {
		t.Helper()
		n, err := repo.CountPending(ctx)
		if err != nil {
			t.Fatalf("CountPending: %v", err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("CountPending on empty table = %d, want 0", n)
	}

	for range 3 {
		createTicket(t, repo, newTicket(t))
	}
	done := newTicket(t)
	done.Status = ticket.StatusNeedsHuman
	done.StatusReason = ticket.ReasonBudgetExhausted
	createTicket(t, repo, done)

	if n := count(); n != 3 {
		t.Fatalf("CountPending = %d, want 3", n)
	}
}

// TestServiceRoundTrip checks that the ticket Submit returns is the ticket Get
// reads back from Postgres.
func TestServiceRoundTrip(t *testing.T) {
	t.Parallel()
	svc := ticket.NewService(postgres.NewTickets(newPool(t)), ticket.Config{})
	ctx := t.Context()

	submitted, token, err := svc.Submit(ctx, "Login", "I cannot log in since this morning.")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got, err := svc.Get(ctx, submitted.ID.String(), token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, submitted) {
		t.Fatalf("Get mismatch\n got: %#v\nwant: %#v", got, submitted)
	}
	if _, err := svc.Get(ctx, submitted.ID.String(), ticket.TokenFromString("wrong")); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("Get with a wrong token error = %v, want ticket.ErrNotFound", err)
	}
}

func TestWithTx(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	ctx := t.Context()
	errFail := errors.New("fail")

	tests := []struct {
		name    string
		fnErr   error
		wantErr error
		wantRow bool
	}{
		{"commit", nil, nil, true},
		{"rollback", errFail, errFail, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk := newTicket(t)
			err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
				if err := postgres.NewTickets(tx).Create(ctx, tk); err != nil {
					return err
				}
				return tt.fnErr
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("WithTx error = %v, want %v", err, tt.wantErr)
			}
			_, err = postgres.NewTickets(pool).Get(ctx, tk.ID)
			if gotRow := err == nil; gotRow != tt.wantRow {
				t.Fatalf("ticket visible after WithTx = %v (Get error %v), want %v", gotRow, err, tt.wantRow)
			}
		})
	}

	t.Run("panic", func(t *testing.T) {
		tk := newTicket(t)
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("WithTx swallowed the panic")
				}
			}()
			_ = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
				if err := postgres.NewTickets(tx).Create(ctx, tk); err != nil {
					return err
				}
				panic("boom")
			})
		}()
		if _, err := postgres.NewTickets(pool).Get(ctx, tk.ID); !errors.Is(err, ticket.ErrNotFound) {
			t.Fatalf("Get after panicking tx error = %v, want ticket.ErrNotFound", err)
		}
		// The pool must not have lost the connection to an open transaction.
		if stat := pool.Stat(); stat.AcquiredConns() != 0 {
			t.Fatalf("acquired connections after WithTx = %d, want 0", stat.AcquiredConns())
		}
	})
}

func TestStreamTokenConsume(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	tickets := postgres.NewTickets(pool)
	tokens := postgres.NewStreamTokens(pool)
	ctx := t.Context()
	owner := createTicket(t, tickets, newTicket(t)).ID
	other := createTicket(t, tickets, newTicket(t)).ID

	_, hash := ticket.NewToken()
	if err := tokens.Create(ctx, owner, hash, time.Minute); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, unknown := ticket.NewToken()

	tests := []struct {
		name     string
		ticketID uuid.UUID
		hash     ticket.TokenHash
		wantErr  error
	}{
		{"bound to another ticket", other, hash, ticket.ErrNotFound},
		{"first use", owner, hash, nil},
		{"second use", owner, hash, ticket.ErrNotFound},
		{"unknown token", owner, unknown, ticket.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tokens.Consume(ctx, tt.ticketID, tt.hash)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("Consume error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestStreamTokenConcurrentConsume(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	tokens := postgres.NewStreamTokens(pool)
	ctx := t.Context()
	id := createTicket(t, postgres.NewTickets(pool), newTicket(t)).ID

	const consumers = 2
	for round := range 20 {
		_, hash := ticket.NewToken()
		if err := tokens.Create(ctx, id, hash, time.Minute); err != nil {
			t.Fatalf("round %d: Create: %v", round, err)
		}

		start := make(chan struct{})
		errs := make([]error, consumers)
		var wg sync.WaitGroup
		for i := range consumers {
			wg.Go(func() {
				<-start
				errs[i] = tokens.Consume(ctx, id, hash)
			})
		}
		close(start)
		wg.Wait()

		wins := 0
		for _, err := range errs {
			switch {
			case err == nil:
				wins++
			case !errors.Is(err, ticket.ErrNotFound):
				t.Fatalf("round %d: Consume error = %v, want nil or ticket.ErrNotFound", round, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d of %d concurrent consumers won, want exactly 1", round, wins, consumers)
		}
	}
}

func TestStreamTokenExpiry(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	tokens := postgres.NewStreamTokens(pool)
	ctx := t.Context()
	id := createTicket(t, postgres.NewTickets(pool), newTicket(t)).ID

	_, hash := ticket.NewToken()
	const ttl = 50 * time.Millisecond
	if err := tokens.Create(ctx, id, hash, ttl); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var left time.Duration
	err := pool.QueryRow(ctx, `SELECT expires_at - created_at FROM stream_tokens WHERE ticket_id = $1`, id).Scan(&left)
	if err != nil {
		t.Fatalf("read expiry: %v", err)
	}
	if left != ttl {
		t.Fatalf("stored lifetime = %v, want %v", left, ttl)
	}

	time.Sleep(2 * ttl)
	if err := tokens.Consume(ctx, id, hash); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("Consume of an expired token error = %v, want ticket.ErrNotFound", err)
	}

	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := tokens.Create(ctx, id, hash, ttl); err == nil {
			t.Errorf("Create with ttl %v succeeded, want an error", ttl)
		}
	}
}
