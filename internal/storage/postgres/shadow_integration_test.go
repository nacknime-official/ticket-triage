//go:build integration

package postgres_test

import (
	"errors"
	"math"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/shadow"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

func shadowResult(run string, id uuid.UUID, outcome shadow.Outcome, cost float64) shadow.Result {
	r := shadow.Result{
		RunID: run, TicketID: id, Outcome: outcome, Attempts: 1,
		Answer: classifier.Result{Call: classifier.Call{
			Provider: "openrouter", Model: "typesafe/jev-1.13-20260917", HTTPStatus: 200,
			Latency: 120 * time.Millisecond, InputTokens: 600, RawResponse: []byte(`{"answers":{}}`),
		}},
		CostUSD: cost, CostSource: classifier.CostReported,
		At: time.Now().UTC().Truncate(time.Microsecond),
	}
	if outcome == shadow.OutcomeOK {
		r.Answer.ComplaintP, r.Answer.Category, r.Answer.CategoryConfidence = 0.2, "billing", 0.9
		r.Answer.Urgency, r.Answer.UrgencyConfidence = 1, 0.7
		r.Status = ticket.StatusClassified
	} else {
		r.LastError = "classifier: transient error: 529"
	}
	return r
}

func TestShadowStore(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	ctx := t.Context()
	tickets := postgres.NewTickets(pool, submittedTopic)
	tk := createTicket(t, tickets, newTicket(t))
	store := postgres.NewShadowStore(pool)

	got, err := store.Ticket(ctx, tk.ID)
	if err != nil || got.ID != tk.ID {
		t.Fatalf("Ticket = %+v, %v", got, err)
	}
	if _, err := store.Ticket(ctx, uuid.NewV7()); !errors.Is(err, ticket.ErrNotFound) {
		t.Errorf("Ticket(unknown) error = %v, want ErrNotFound", err)
	}

	done := func(run string) bool {
		t.Helper()
		ok, err := store.Done(ctx, run, tk.ID)
		if err != nil {
			t.Fatalf("Done: %v", err)
		}
		return ok
	}
	if done("r1") {
		t.Fatal("Done before any row")
	}

	// A failed row does not count as done; the retry replaces it and adds up
	// the attempts and the cost.
	if err := store.Save(ctx, shadowResult("r1", tk.ID, shadow.OutcomeTransientError, 0.00001)); err != nil {
		t.Fatalf("Save failed row: %v", err)
	}
	if done("r1") {
		t.Error("Done after a failed row")
	}
	ok := shadowResult("r1", tk.ID, shadow.OutcomeOK, 0.00002)
	if err := store.Save(ctx, ok); err != nil {
		t.Fatalf("Save ok row: %v", err)
	}
	if !done("r1") || done("r2") {
		t.Error("Done is not per run")
	}
	var (
		attempts         int
		cost             float64
		status, category string
		lastErr          *string
	)
	err = pool.QueryRow(ctx,
		`SELECT attempts, cost_usd::float8, status, category, last_error FROM shadow_classifications
		 WHERE run_id = 'r1' AND ticket_id = $1`, tk.ID).Scan(&attempts, &cost, &status, &category, &lastErr)
	if err != nil {
		t.Fatalf("read shadow row: %v", err)
	}
	if attempts != 2 || math.Abs(cost-0.00003) > 1e-9 || status != "classified" || category != "billing" || lastErr != nil {
		t.Errorf("row = %d attempts, $%v, %s, %s, %v; want 2, $0.00003, classified, billing, NULL",
			attempts, cost, status, category, lastErr)
	}

	// A call without a known cost stays NULL, and a later one with a cost
	// starts the sum.
	other := createTicket(t, tickets, newTicket(t))
	noCost := shadowResult("r1", other.ID, shadow.OutcomeTransientError, 0)
	noCost.CostSource = ""
	for _, r := range []shadow.Result{noCost, noCost, shadowResult("r1", other.ID, shadow.OutcomeOK, 0.00001)} {
		if err := store.Save(ctx, r); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	// The schema rejects a decision without a valid answer.
	bad := shadowResult("r3", tk.ID, shadow.OutcomeTransientError, 0)
	bad.Status = ticket.StatusClassified
	var pgErr *pgconn.PgError
	if err := store.Save(ctx, bad); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Errorf("Save of a decided failure = %v, want a check violation", err)
	}

	// Live and shadow calls share the daily budget, for both stores.
	_, err = postgres.NewTriageStore(pool, "").Apply(ctx, triage.Update{
		TicketID: tk.ID, Status: ticket.StatusPending, At: time.Now().UTC(),
		Calls: []triage.Call{{Attempt: 1, Outcome: triage.CallTransientError, Err: errors.New("529"),
			Result: classifier.Result{Call: classifier.Call{Provider: "openrouter", CostUSD: 0.0001,
				CostSource: classifier.CostReported}}}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	since := time.Now().UTC().Add(-time.Hour)
	for name, spent := range map[string]func() (float64, error){
		"shadow": func() (float64, error) { return store.SpentSince(ctx, since) },
		"triage": func() (float64, error) { return postgres.NewTriageStore(pool, "").SpentSince(ctx, since) },
	} {
		got, err := spent()
		if err != nil {
			t.Fatalf("%s SpentSince: %v", name, err)
		}
		if want := 0.0001 + 0.00003 + 0.00001; math.Abs(got-want) > 1e-9 {
			t.Errorf("%s SpentSince = %v, want %v", name, got, want)
		}
	}
	if got, err := store.SpentSince(ctx, time.Now().Add(time.Hour)); err != nil || got != 0 {
		t.Errorf("SpentSince(future) = %v, %v; want 0", got, err)
	}

	// A replay never changes the ticket.
	after, err := tickets.Get(ctx, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.Status != ticket.StatusPending || after.Classification != nil {
		t.Errorf("ticket after shadow saves = %+v, want it pending and unclassified", after)
	}
}
