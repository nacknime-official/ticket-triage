//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nacknime-official/ticket-triage/internal/outbox"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// outboxRow is the stored state of one outbox message.
type outboxRow struct {
	ID        int64
	Topic     string
	Key       string
	Payload   string
	Published bool
	Attempts  int
	LastError string
}

func outboxRows(t *testing.T, pool *pgxpool.Pool) []outboxRow {
	t.Helper()
	rows, err := pool.Query(t.Context(),
		`SELECT id, topic, key, payload, published_at IS NOT NULL, attempts, coalesce(last_error, '')
		 FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (outboxRow, error) {
		var r outboxRow
		var key, payload []byte
		err := row.Scan(&r.ID, &r.Topic, &key, &payload, &r.Published, &r.Attempts, &r.LastError)
		r.Key, r.Payload = string(key), string(payload)
		return r, err
	})
	if err != nil {
		t.Fatalf("collect outbox rows: %v", err)
	}
	return got
}

func TestSubmitWritesOutbox(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	svc := ticket.NewService(postgres.NewTickets(pool, submittedTopic), ticket.Config{})

	tk, _, err := svc.Submit(t.Context(), "Refund", "Charged twice, please refund.")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	rows := outboxRows(t, pool)
	if len(rows) != 1 {
		t.Fatalf("outbox has %d rows, want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Topic != submittedTopic || r.Key != tk.ID.String() || r.Published || r.Attempts != 0 {
		t.Errorf("outbox row = %+v, want unpublished %s keyed by %s", r, submittedTopic, tk.ID)
	}
	ev, err := ticket.UnmarshalSubmitted([]byte(r.Payload))
	if err != nil {
		t.Fatalf("payload %s: %v", r.Payload, err)
	}
	if want := ticket.SubmittedOf(tk); ev != want {
		t.Errorf("payload event = %+v, want %+v", ev, want)
	}
}

func TestSubmitAtomic(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	repo := postgres.NewTickets(pool, submittedTopic)
	ctx := t.Context()

	tk := newTicket(t)
	if err := repo.Submit(ctx, tk); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// A failing ticket insert must not leave an outbox row behind.
	if err := repo.Submit(ctx, tk); err == nil {
		t.Fatal("second Submit with the same id succeeded, want an error")
	}
	if n := len(outboxRows(t, pool)); n != 1 {
		t.Fatalf("outbox has %d rows after a failed Submit, want 1", n)
	}

	// Inside an outer transaction Submit joins it and rolls back with it.
	other := newTicket(t)
	errFail := errors.New("fail")
	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if err := postgres.NewTickets(tx, submittedTopic).Submit(ctx, other); err != nil {
			return err
		}
		return errFail
	})
	if !errors.Is(err, errFail) {
		t.Fatalf("WithTx error = %v, want %v", err, errFail)
	}
	if _, err := repo.Get(ctx, other.ID); !errors.Is(err, ticket.ErrNotFound) {
		t.Errorf("ticket of a rolled-back Submit: Get error = %v, want ticket.ErrNotFound", err)
	}
	if n := len(outboxRows(t, pool)); n != 1 {
		t.Errorf("outbox has %d rows after a rolled-back Submit, want 1", n)
	}

	if err := postgres.NewTickets(pool, "").Submit(ctx, newTicket(t)); err == nil {
		t.Error("Submit without a topic succeeded, want an error")
	}
}

// addMessages stores n messages on topic "t" keyed "k0".."k<n-1>".
func addMessages(t *testing.T, store *postgres.Outbox, n int) {
	t.Helper()
	for i := range n {
		m := outbox.Message{Topic: "t", Key: fmt.Appendf(nil, "k%d", i), Payload: []byte(`{}`)}
		if err := store.Add(t.Context(), m); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
}

func keys(msgs []outbox.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = string(m.Key)
	}
	return out
}

func TestOutboxPublishPending(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	store := postgres.NewOutbox(pool)
	ctx := t.Context()

	headers := map[string]string{"attempt": "2"}
	if err := store.Add(ctx, outbox.Message{Topic: "t", Key: []byte("k0"), Payload: []byte(`{"a":1}`), Headers: headers}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	addMessages(t, store, 3) // k0, k1, k2 after the first k0

	var seen []outbox.Message
	errBroker := errors.New("broker down")
	claimed, err := store.PublishPending(ctx, 3, func(_ context.Context, msgs []outbox.Message) []error {
		seen = msgs
		return []error{nil, errBroker, nil}
	})
	if err != nil || claimed != 3 {
		t.Fatalf("PublishPending = %d, %v; want 3, nil", claimed, err)
	}
	if got, want := keys(seen), []string{"k0", "k0", "k1"}; !slices.Equal(got, want) {
		t.Errorf("claimed keys %v, want %v (oldest first)", got, want)
	}
	if !reflect.DeepEqual(seen[0].Headers, headers) || seen[1].Headers != nil {
		t.Errorf("headers = %v, %v; want %v, nil", seen[0].Headers, seen[1].Headers, headers)
	}
	if string(seen[0].Payload) != `{"a":1}` || seen[0].CreatedAt.IsZero() {
		t.Errorf("first message = %+v, want its payload and created_at", seen[0])
	}

	rows := outboxRows(t, pool)
	wantState := []struct {
		published bool
		attempts  int
		lastError string
	}{{true, 0, ""}, {false, 1, "broker down"}, {true, 0, ""}, {false, 0, ""}}
	for i, w := range wantState {
		r := rows[i]
		if r.Published != w.published || r.Attempts != w.attempts || r.LastError != w.lastError {
			t.Errorf("row %d = %+v, want published=%v attempts=%d last_error=%q", i, r, w.published, w.attempts, w.lastError)
		}
	}

	// The failed message and the unclaimed one are claimed again.
	claimed, err = store.PublishPending(ctx, 10, func(_ context.Context, msgs []outbox.Message) []error {
		seen = msgs
		return make([]error, len(msgs))
	})
	if err != nil || claimed != 2 {
		t.Fatalf("second PublishPending = %d, %v; want 2, nil", claimed, err)
	}
	if got, want := keys(seen), []string{"k0", "k2"}; !slices.Equal(got, want) {
		t.Errorf("second claim keys %v, want %v", got, want)
	}

	claimed, err = store.PublishPending(ctx, 10, func(context.Context, []outbox.Message) []error {
		t.Error("publish called with nothing pending")
		return nil
	})
	if err != nil || claimed != 0 {
		t.Fatalf("PublishPending on a drained outbox = %d, %v; want 0, nil", claimed, err)
	}
}

func TestOutboxPublishPendingRollsBack(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	store := postgres.NewOutbox(pool)
	addMessages(t, store, 2)

	// A publish result of the wrong length must mark nothing.
	_, err := store.PublishPending(t.Context(), 10, func(context.Context, []outbox.Message) []error {
		return []error{nil}
	})
	if err == nil {
		t.Fatal("PublishPending with a short result succeeded, want an error")
	}
	for _, r := range outboxRows(t, pool) {
		if r.Published || r.Attempts != 0 {
			t.Errorf("row %+v changed by a rolled-back batch", r)
		}
	}
}

// TestOutboxSkipLocked checks that a second relay claims other rows instead
// of waiting for, or duplicating, the rows a first relay holds.
func TestOutboxSkipLocked(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	store := postgres.NewOutbox(pool)
	ctx := t.Context()
	addMessages(t, store, 4)

	holding := make(chan []string)
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := store.PublishPending(ctx, 2, func(_ context.Context, msgs []outbox.Message) []error {
			holding <- keys(msgs)
			<-release
			return make([]error, len(msgs))
		})
		firstDone <- err
	}()
	first := <-holding

	var second []string
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := store.PublishPending(claimCtx, 10, func(_ context.Context, msgs []outbox.Message) []error {
		second = keys(msgs)
		return make([]error, len(msgs))
	})
	close(release)
	if err != nil {
		t.Fatalf("second PublishPending while the first holds rows: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first PublishPending: %v", err)
	}
	if want := []string{"k0", "k1"}; !slices.Equal(first, want) {
		t.Errorf("first relay claimed %v, want %v", first, want)
	}
	if want := []string{"k2", "k3"}; !slices.Equal(second, want) {
		t.Errorf("second relay claimed %v, want %v", second, want)
	}
	for _, r := range outboxRows(t, pool) {
		if !r.Published {
			t.Errorf("row %+v unpublished after both relays", r)
		}
	}
}

func TestOutboxDeletePublished(t *testing.T) {
	t.Parallel()
	pool := newPool(t)
	store := postgres.NewOutbox(pool)
	ctx := t.Context()
	addMessages(t, store, 3)

	// k0 published long ago, k1 just now, k2 unpublished.
	if _, err := pool.Exec(ctx, `UPDATE outbox SET published_at = now() - interval '8 days' WHERE key = 'k0'`); err != nil {
		t.Fatalf("age k0: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE key = 'k1'`); err != nil {
		t.Fatalf("publish k1: %v", err)
	}
	n, err := store.DeletePublished(ctx, time.Now().Add(-7*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("DeletePublished = %d, %v; want 1, nil", n, err)
	}
	var left []string
	for _, r := range outboxRows(t, pool) {
		left = append(left, r.Key)
	}
	if want := []string{"k1", "k2"}; !slices.Equal(left, want) {
		t.Errorf("rows left %v, want %v", left, want)
	}
}
