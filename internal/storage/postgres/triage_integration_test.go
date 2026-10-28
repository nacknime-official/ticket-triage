//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

// auditRow is the part of a classifications row the tests check.
type auditRow struct {
	attempt    int
	outcome    string
	model      *string
	httpStatus *int
	cost       *float64
	costSource *string
	category   *string
	raw        *string
}

func readAudit(t *testing.T, db postgres.Querier, id any) []auditRow {
	t.Helper()
	rows, err := db.Query(t.Context(), `
		SELECT attempt, outcome, model, http_status, cost_usd::float8, cost_source, category, raw_response::text
		FROM classifications WHERE ticket_id = $1 ORDER BY id`, id)
	if err != nil {
		t.Fatalf("query classifications: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.attempt, &r.outcome, &r.model, &r.httpStatus, &r.cost, &r.costSource, &r.category, &r.raw); err != nil {
			t.Fatalf("scan classification: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read classifications: %v", err)
	}
	return out
}

func okResult() classifier.Result {
	return classifier.Result{
		ComplaintP: 0.8, Category: "billing", CategoryConfidence: 0.9, Urgency: 2.2, UrgencyConfidence: 0.7,
		Call: classifier.Call{Provider: "jev", Model: "typesafe/jev-1.14", HTTPStatus: 200, Latency: 140 * time.Millisecond,
			InputTokens: 450, CostUSD: 0.0000189, CostSource: classifier.CostReported, RawResponse: []byte(`{"answers":{}}`)},
	}
}

func TestTriageApply(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	store := postgres.NewTriageStore(pool)
	tickets := postgres.NewTickets(pool, submittedTopic)
	ctx := t.Context()
	tk := createTicket(t, tickets, newTicket(t))
	at := tk.CreatedAt.Add(2 * time.Second)

	// A failed run keeps the ticket pending and records its calls.
	failed := triage.Update{
		TicketID: tk.ID, Status: ticket.StatusPending, LastError: "classifier: transient error: 503",
		Calls: []triage.Call{{
			Attempt: 1, Outcome: triage.CallTransientError,
			Result: classifier.Result{Call: classifier.Call{Provider: "jev", HTTPStatus: 503, RawResponse: []byte("<html>busy</html>")}},
		}, {
			Attempt: 2, Outcome: triage.CallTransientError,
			Result: classifier.Result{Call: classifier.Call{Provider: "jev", Latency: 3 * time.Second}},
		}},
		At: at,
	}
	if ok, err := store.Apply(ctx, failed); err != nil || !ok {
		t.Fatalf("Apply(pending) = %v, %v; want true", ok, err)
	}
	got, err := tickets.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ticket.StatusPending || got.Attempts != 2 || got.LastError != failed.LastError || !got.UpdatedAt.Equal(at) {
		t.Fatalf("after a failed run: status %q, attempts %d, last_error %q, updated_at %v",
			got.Status, got.Attempts, got.LastError, got.UpdatedAt)
	}

	// The next run classifies; last_error is kept when the run had none.
	res := okResult()
	c := res.Classification()
	c.IsComplaint = true
	done := triage.Update{
		TicketID: tk.ID, Status: ticket.StatusClassified, Classification: &c,
		Calls: []triage.Call{{Attempt: 3, Outcome: triage.CallOK, Result: res}},
		At:    at.Add(time.Minute),
	}
	if ok, err := store.Apply(ctx, done); err != nil || !ok {
		t.Fatalf("Apply(classified) = %v, %v; want true", ok, err)
	}
	got, err = tickets.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ticket.StatusClassified || got.Attempts != 3 || got.LastError != failed.LastError ||
		!got.ClassifiedAt.Equal(done.At) || got.Classification == nil || got.Classification.Category != "billing" ||
		!got.Classification.IsComplaint {
		t.Fatalf("after classifying: %+v (classification %+v)", got, got.Classification)
	}
	var priority string
	if err := pool.QueryRow(ctx, `SELECT priority FROM tickets WHERE id = $1`, tk.ID).Scan(&priority); err != nil {
		t.Fatal(err)
	}
	if priority != "high" {
		t.Errorf("priority = %q, want high", priority)
	}

	// Once final, nothing else is written.
	again := done
	again.Calls = []triage.Call{{Attempt: 4, Outcome: triage.CallOK, Result: res}}
	if ok, err := store.Apply(ctx, again); err != nil || ok {
		t.Fatalf("Apply on a final ticket = %v, %v; want false", ok, err)
	}

	audit := readAudit(t, pool, tk.ID)
	if len(audit) != 3 {
		t.Fatalf("audit rows = %d, want 3", len(audit))
	}
	for i, want := range []string{"transient_error", "transient_error", "ok"} {
		if audit[i].attempt != i+1 || audit[i].outcome != want {
			t.Errorf("audit row %d = attempt %d %q, want %d %q", i, audit[i].attempt, audit[i].outcome, i+1, want)
		}
	}
	first, last := audit[0], audit[2]
	if first.raw == nil || *first.raw != `"<html>busy</html>"` {
		t.Errorf("non-JSON raw response stored as %v, want a JSON string", first.raw)
	}
	if first.cost != nil || first.costSource != nil || first.model != nil || first.category != nil {
		t.Errorf("failed call row has cost %v, source %v, model %v, category %v; want NULLs",
			first.cost, first.costSource, first.model, first.category)
	}
	if audit[1].httpStatus != nil || audit[1].raw != nil {
		t.Errorf("timed-out call row has http_status %v, raw %v; want NULLs", audit[1].httpStatus, audit[1].raw)
	}
	if last.cost == nil || *last.cost != res.Call.CostUSD || last.costSource == nil || *last.costSource != "reported" ||
		last.category == nil || *last.category != "billing" || last.httpStatus == nil || *last.httpStatus != 200 {
		t.Errorf("ok call row = %+v", last)
	}
}

func TestTriageSpentSince(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	store := postgres.NewTriageStore(pool)
	ctx := t.Context()
	tk := createTicket(t, postgres.NewTickets(pool, submittedTopic), newTicket(t))

	midnight := time.Now().UTC().Truncate(24 * time.Hour)
	if spent, err := store.SpentSince(ctx, midnight); err != nil || spent != 0 {
		t.Fatalf("SpentSince on an empty table = %v, %v; want 0", spent, err)
	}

	unknown := classifier.Result{Call: classifier.Call{Provider: "jev"}}
	calls := []triage.Call{
		{Attempt: 1, Outcome: triage.CallOK, Result: okResult()},
		{Attempt: 2, Outcome: triage.CallOK, Result: okResult()},
		{Attempt: 3, Outcome: triage.CallTransientError, Result: unknown},
	}
	u := triage.Update{TicketID: tk.ID, Status: ticket.StatusPending, Calls: calls, At: time.Now().UTC()}
	if ok, err := store.Apply(ctx, u); err != nil || !ok {
		t.Fatalf("Apply = %v, %v", ok, err)
	}
	// Move the first call to yesterday.
	if _, err := pool.Exec(ctx, `UPDATE classifications SET created_at = $2 WHERE ticket_id = $1 AND attempt = 1`,
		tk.ID, midnight.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	spent, err := store.SpentSince(ctx, midnight)
	if err != nil {
		t.Fatalf("SpentSince: %v", err)
	}
	if want := okResult().Call.CostUSD; spent != want {
		t.Fatalf("SpentSince(today) = %v, want %v (one call today with a cost)", spent, want)
	}
}

// barrier holds every Classify call until n calls arrived, so concurrent
// Process calls all pass the pending read before any of them writes.
type barrier struct {
	classifier.Fake
	n       int
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func newBarrier(n int) *barrier { return &barrier{n: n, release: make(chan struct{})} }

func (b *barrier) Classify(ctx context.Context, in classifier.Input) (classifier.Result, error) {
	b.mu.Lock()
	b.arrived++
	if b.arrived == b.n {
		close(b.release)
	}
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-ctx.Done():
		return classifier.Result{}, ctx.Err()
	}
	return b.Fake.Classify(ctx, in)
}

// TestProcessConcurrent runs two Process calls on one ticket at once: exactly
// one wins the conditional update, and only the winner's audit row is kept.
func TestProcessConcurrent(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	tickets := postgres.NewTickets(pool, submittedTopic)
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := triage.Config{Thresholds: classifier.DefaultThresholds(), DailyBudgetUSD: 1}

	const workers = 2
	for round := range 20 {
		tk := createTicket(t, tickets, newTicket(t))
		p := triage.NewProcessor(postgres.NewTriageStore(pool), newBarrier(workers), cfg, log)

		outs := make([]triage.Outcome, workers)
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for i := range workers {
			wg.Go(func() { outs[i], errs[i] = p.Process(ctx, tk.ID.String()) })
		}
		wg.Wait()

		kinds := map[triage.Kind]int{}
		for i := range workers {
			if errs[i] != nil {
				t.Fatalf("round %d: Process error: %v", round, errs[i])
			}
			kinds[outs[i].Kind]++
		}
		if kinds[triage.Done] != 1 || kinds[triage.Skipped] != workers-1 {
			t.Fatalf("round %d: outcomes %v, want one done and the rest skipped", round, kinds)
		}

		got, err := tickets.Get(ctx, tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != ticket.StatusClassified || got.Attempts != 1 {
			t.Fatalf("round %d: ticket status %q, attempts %d; want classified, 1", round, got.Status, got.Attempts)
		}
		if audit := readAudit(t, pool, tk.ID); len(audit) != 1 || audit[0].outcome != "ok" {
			t.Fatalf("round %d: audit rows %+v, want exactly the winner's", round, audit)
		}
	}

	// A third Process on a finished ticket skips before calling the classifier.
	tk := createTicket(t, tickets, newTicket(t))
	fake := &classifier.Fake{}
	p := triage.NewProcessor(postgres.NewTriageStore(pool), fake, cfg, log)
	for i, want := range []triage.Kind{triage.Done, triage.Skipped} {
		out, err := p.Process(ctx, tk.ID.String())
		if err != nil || out.Kind != want {
			t.Fatalf("Process %d = %v, %v; want %v", i+1, out.Kind, err, want)
		}
	}
	if fake.Calls() != 1 {
		t.Fatalf("classifier calls = %d, want 1", fake.Calls())
	}
	if out, err := p.Process(ctx, "nope"); err != nil || out.Kind != triage.Poison {
		t.Fatalf("Process of a bad id = %v, %v; want Poison", out.Kind, err)
	}
	if out, _ := p.Process(ctx, newTicket(t).ID.String()); !errors.Is(out.Err, ticket.ErrNotFound) {
		t.Fatalf("Process of a missing ticket = %+v, want Poison with ticket.ErrNotFound", out)
	}
}
