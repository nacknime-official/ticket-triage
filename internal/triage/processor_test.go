package triage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

// fakeStore keeps tickets in memory and applies updates with the same
// pending-only condition as the Postgres store.
type fakeStore struct {
	mu      sync.Mutex
	tickets map[uuid.UUID]ticket.Ticket
	spent   float64
	since   []time.Time
	updates []triage.Update
	// lose makes Apply report that another worker won.
	lose bool
	err  error
}

func newFakeStore(tickets ...ticket.Ticket) *fakeStore {
	s := &fakeStore{tickets: make(map[uuid.UUID]ticket.Ticket)}
	for _, t := range tickets {
		s.tickets[t.ID] = t
	}
	return s
}

func (s *fakeStore) Ticket(_ context.Context, id uuid.UUID) (ticket.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return ticket.Ticket{}, s.err
	}
	t, ok := s.tickets[id]
	if !ok {
		return ticket.Ticket{}, ticket.ErrNotFound
	}
	return t, nil
}

func (s *fakeStore) SpentSince(_ context.Context, since time.Time) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.since = append(s.since, since)
	return s.spent, nil
}

func (s *fakeStore) Apply(_ context.Context, u triage.Update) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	t, ok := s.tickets[u.TicketID]
	if s.lose || !ok || t.Status != ticket.StatusPending {
		return false, nil
	}
	s.updates = append(s.updates, u)
	t.Status, t.StatusReason, t.Classification = u.Status, u.Reason, u.Classification
	t.Attempts += len(u.Calls)
	if u.LastError != "" {
		t.LastError = u.LastError
	}
	s.tickets[u.TicketID] = t
	return true, nil
}

func (s *fakeStore) ticket(id uuid.UUID) ticket.Ticket {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tickets[id]
}

// script is a classifier that answers call n with steps[n], repeating the
// last step after the end.
type script struct {
	mu    sync.Mutex
	steps []func(ctx context.Context) (classifier.Result, error)
	calls []time.Time
}

func (s *script) Classify(ctx context.Context, _ classifier.Input) (classifier.Result, error) {
	s.mu.Lock()
	n := len(s.calls)
	s.calls = append(s.calls, time.Now())
	step := s.steps[min(n, len(s.steps)-1)]
	s.mu.Unlock()
	return step(ctx)
}

func (s *script) times() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

func answer(r classifier.Result) func(context.Context) (classifier.Result, error) {
	return func(context.Context) (classifier.Result, error) { return r, nil }
}

func fail(err error) func(context.Context) (classifier.Result, error) {
	return func(context.Context) (classifier.Result, error) {
		return classifier.Result{Call: classifier.Call{Provider: "test", HTTPStatus: 503}}, err
	}
}

// retryAfterError is a transient error carrying a Retry-After, as an adapter
// returns for a 429.
type retryAfterError struct{ d time.Duration }

func (e retryAfterError) Error() string             { return "rate limited" }
func (e retryAfterError) Unwrap() error             { return classifier.ErrTransient }
func (e retryAfterError) RetryAfter() time.Duration { return e.d }

func confident() classifier.Result {
	return classifier.Result{
		ComplaintP: 0.8, Category: classifier.CategoryBilling, CategoryConfidence: 0.9,
		Urgency: 2.2, UrgencyConfidence: 0.8,
		Call: classifier.Call{Provider: "test", Model: "m-1", HTTPStatus: 200, Latency: 120 * time.Millisecond,
			InputTokens: 450, CostUSD: 0.00002, CostSource: classifier.CostReported, RawResponse: []byte(`{}`)},
	}
}

const (
	subject = "Charged twice"
	body    = "My card number 4111 was charged twice."
)

func pendingTicket() ticket.Ticket {
	return ticket.Ticket{ID: uuid.NewV7(), Subject: subject, Body: body, Status: ticket.StatusPending}
}

func testConfig() triage.Config {
	return triage.Config{Thresholds: classifier.DefaultThresholds(), DailyBudgetUSD: 1}
}

func newProcessor(store triage.Store, cls classifier.Classifier, cfg triage.Config) *triage.Processor {
	return triage.NewProcessor(store, cls, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestProcess(t *testing.T) {
	permanent := fmt.Errorf("%w: 422", classifier.ErrPermanent)
	badCategory := confident()
	badCategory.Category = "sales"
	lowConfidence := confident()
	lowConfidence.CategoryConfidence = 0.4
	other := confident()
	other.Category = classifier.CategoryOther

	tests := []struct {
		name  string
		steps []func(context.Context) (classifier.Result, error)
		// prepare adjusts the store and returns the id to process.
		prepare      func(s *fakeStore, tk *ticket.Ticket) string
		wantKind     triage.Kind
		wantErr      error // of Process
		wantCause    error // Outcome.Err
		wantStatus   ticket.Status
		wantReason   ticket.Reason
		wantCalls    []triage.CallOutcome
		wantClassify bool // the stored ticket has a classification
		wantAttempts int
		wantLastErr  bool
		wantRetry    time.Duration
	}{
		{
			name: "classified", steps: steps(answer(confident())),
			wantKind: triage.Done, wantStatus: ticket.StatusClassified,
			wantCalls: []triage.CallOutcome{triage.CallOK}, wantClassify: true, wantAttempts: 1,
		},
		{
			name: "low confidence", steps: steps(answer(lowConfidence)),
			wantKind: triage.Done, wantStatus: ticket.StatusNeedsHuman, wantReason: ticket.ReasonLowConfidence,
			wantCalls: []triage.CallOutcome{triage.CallOK}, wantClassify: true, wantAttempts: 1,
		},
		{
			name: "category other", steps: steps(answer(other)),
			wantKind: triage.Done, wantStatus: ticket.StatusNeedsHuman, wantReason: ticket.ReasonCategoryOther,
			wantCalls: []triage.CallOutcome{triage.CallOK}, wantClassify: true, wantAttempts: 1,
		},
		{
			name: "transient then classified", steps: steps(fail(classifier.ErrTransient), answer(confident())),
			wantKind: triage.Done, wantStatus: ticket.StatusClassified,
			wantCalls:    []triage.CallOutcome{triage.CallTransientError, triage.CallOK},
			wantClassify: true, wantAttempts: 2, wantLastErr: true,
		},
		{
			name: "transient exhausted", steps: steps(fail(classifier.ErrTransient)),
			wantKind: triage.Retryable, wantCause: classifier.ErrTransient, wantStatus: ticket.StatusPending,
			wantCalls: []triage.CallOutcome{
				triage.CallTransientError, triage.CallTransientError, triage.CallTransientError,
			},
			wantAttempts: 3, wantLastErr: true, wantRetry: triage.DefaultRetryAfter,
		},
		{
			name: "long retry-after goes back to the transport", steps: steps(fail(retryAfterError{time.Minute})),
			wantKind: triage.Retryable, wantCause: classifier.ErrTransient, wantStatus: ticket.StatusPending,
			wantCalls:    []triage.CallOutcome{triage.CallTransientError},
			wantAttempts: 1, wantLastErr: true, wantRetry: time.Minute,
		},
		{
			name: "permanent error fails open", steps: steps(fail(permanent)),
			wantKind: triage.Poison, wantCause: classifier.ErrPermanent,
			wantStatus: ticket.StatusNeedsHuman, wantReason: ticket.ReasonClassifierUnavailable,
			wantCalls: []triage.CallOutcome{triage.CallPermanentError}, wantAttempts: 1, wantLastErr: true,
		},
		{
			name: "invalid answer from the classifier", steps: steps(fail(classifier.ErrInvalidAnswer)),
			wantKind: triage.Poison, wantCause: classifier.ErrInvalidAnswer,
			wantStatus: ticket.StatusNeedsHuman, wantReason: ticket.ReasonInvalidAnswer,
			wantCalls: []triage.CallOutcome{triage.CallInvalidAnswer}, wantAttempts: 1, wantLastErr: true,
		},
		{
			name: "answer outside the taxonomy", steps: steps(answer(badCategory)),
			wantKind: triage.Poison, wantCause: classifier.ErrInvalidAnswer,
			wantStatus: ticket.StatusNeedsHuman, wantReason: ticket.ReasonInvalidAnswer,
			wantCalls: []triage.CallOutcome{triage.CallInvalidAnswer}, wantAttempts: 1, wantLastErr: true,
		},
		{
			name: "auth error stops", steps: steps(fail(classifier.ErrAuth)),
			wantErr: classifier.ErrAuth, wantStatus: ticket.StatusPending,
			wantCalls: []triage.CallOutcome{triage.CallAuthError}, wantAttempts: 1, wantLastErr: true,
		},
		{
			name: "budget exhausted", steps: steps(answer(confident())),
			prepare: func(s *fakeStore, tk *ticket.Ticket) string {
				s.spent = 1
				return tk.ID.String()
			},
			wantKind: triage.Done, wantStatus: ticket.StatusNeedsHuman, wantReason: ticket.ReasonBudgetExhausted,
		},
		{
			name: "not pending", steps: steps(answer(confident())),
			prepare: func(s *fakeStore, tk *ticket.Ticket) string {
				tk.Status, tk.StatusReason = ticket.StatusNeedsHuman, ticket.ReasonLowConfidence
				s.tickets[tk.ID] = *tk
				return tk.ID.String()
			},
			wantKind: triage.Skipped, wantStatus: ticket.StatusNeedsHuman, wantReason: ticket.ReasonLowConfidence,
		},
		{
			name: "another worker won", steps: steps(answer(confident())),
			prepare: func(s *fakeStore, tk *ticket.Ticket) string {
				s.lose = true
				return tk.ID.String()
			},
			wantKind: triage.Skipped, wantStatus: ticket.StatusPending,
		},
		{
			name: "missing ticket", steps: steps(answer(confident())),
			prepare:  func(*fakeStore, *ticket.Ticket) string { return uuid.NewV7().String() },
			wantKind: triage.Poison, wantCause: ticket.ErrNotFound, wantStatus: ticket.StatusPending,
		},
		{
			name: "bad id", steps: steps(answer(confident())),
			prepare:  func(*fakeStore, *ticket.Ticket) string { return "not-a-uuid" },
			wantKind: triage.Poison, wantCause: triage.ErrBadID, wantStatus: ticket.StatusPending,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tk := pendingTicket()
				store := newFakeStore(tk)
				id := tk.ID.String()
				if tt.prepare != nil {
					id = tt.prepare(store, &tk)
				}
				cls := &script{steps: tt.steps}

				out, err := newProcessor(store, cls, testConfig()).Process(t.Context(), id)
				if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
					t.Fatalf("Process error = %v, want %v", err, tt.wantErr)
				}
				if out.Kind != tt.wantKind {
					t.Errorf("Kind = %v, want %v", out.Kind, tt.wantKind)
				}
				if !errors.Is(out.Err, tt.wantCause) || (tt.wantCause == nil && out.Err != nil) {
					t.Errorf("Outcome.Err = %v, want %v", out.Err, tt.wantCause)
				}
				if out.RetryAfter != tt.wantRetry {
					t.Errorf("RetryAfter = %v, want %v", out.RetryAfter, tt.wantRetry)
				}
				if tt.wantKind == triage.Done || tt.wantKind == triage.Poison && tt.wantStatus != ticket.StatusPending {
					if out.Status != tt.wantStatus || out.Reason != tt.wantReason {
						t.Errorf("Outcome status = %q/%q, want %q/%q", out.Status, out.Reason, tt.wantStatus, tt.wantReason)
					}
				}

				got := store.ticket(tk.ID)
				if got.Status != tt.wantStatus || got.StatusReason != tt.wantReason {
					t.Errorf("stored status = %q/%q, want %q/%q", got.Status, got.StatusReason, tt.wantStatus, tt.wantReason)
				}
				if (got.Classification != nil) != tt.wantClassify {
					t.Errorf("stored classification = %+v, want present %v", got.Classification, tt.wantClassify)
				}
				if got.Attempts != tt.wantAttempts {
					t.Errorf("attempts = %d, want %d", got.Attempts, tt.wantAttempts)
				}
				if (got.LastError != "") != tt.wantLastErr {
					t.Errorf("last_error = %q, want set %v", got.LastError, tt.wantLastErr)
				}

				var calls []triage.CallOutcome
				for i, u := range store.updates {
					for j, c := range u.Calls {
						calls = append(calls, c.Outcome)
						if want := len(calls); c.Attempt != want {
							t.Errorf("update %d call %d attempt = %d, want %d", i, j, c.Attempt, want)
						}
					}
				}
				if !slices.Equal(calls, tt.wantCalls) {
					t.Errorf("recorded calls = %v, want %v", calls, tt.wantCalls)
				}
				if made := len(cls.times()); made != len(tt.wantCalls) && !store.lose {
					t.Errorf("classifier calls = %d, want %d", made, len(tt.wantCalls))
				}
			})
		})
	}
}

func steps(s ...func(context.Context) (classifier.Result, error)) []func(context.Context) (classifier.Result, error) {
	return s
}

func TestProcessClassification(t *testing.T) {
	tests := []struct {
		name          string
		complaintP    float64
		wantComplaint bool
		wantUncertain bool
	}{
		{"clear complaint", 0.9, true, false},
		{"uncertain yes", 0.5, true, true},
		{"uncertain no", 0.4, false, true},
		{"clear no", 0.1, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := confident()
			r.ComplaintP = tt.complaintP
			tk := pendingTicket()
			store := newFakeStore(tk)
			out, err := newProcessor(store, &script{steps: steps(answer(r))}, testConfig()).Process(t.Context(), tk.ID.String())
			if err != nil || out.Kind != triage.Done {
				t.Fatalf("Process = %+v, %v; want Done", out, err)
			}
			c := store.ticket(tk.ID).Classification
			want := ticket.Classification{
				Category: r.Category, CategoryConfidence: r.CategoryConfidence,
				ComplaintP: r.ComplaintP, IsComplaint: tt.wantComplaint, ComplaintUncertain: tt.wantUncertain,
				Urgency: r.Urgency, UrgencyConfidence: r.UrgencyConfidence,
			}
			if c == nil || *c != want {
				t.Fatalf("classification = %+v, want %+v", c, want)
			}
			u := store.updates[0]
			if u.At.IsZero() || u.At.Location() != time.UTC {
				t.Errorf("Update.At = %v, want a UTC time", u.At)
			}
		})
	}
}

// TestBackoff checks the waits between transient retries on the fake clock.
func TestBackoff(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		min, max []time.Duration
	}{
		{
			name: "exponential with jitter", err: classifier.ErrTransient,
			min: []time.Duration{100 * time.Millisecond, 200 * time.Millisecond},
			max: []time.Duration{200 * time.Millisecond, 400 * time.Millisecond},
		},
		{
			name: "retry-after honored", err: retryAfterError{time.Second},
			min: []time.Duration{time.Second, time.Second},
			max: []time.Duration{time.Second, time.Second},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				for range 20 { // the jitter is random; check its bounds repeatedly
					tk := pendingTicket()
					cls := &script{steps: steps(fail(tt.err))}
					out, err := newProcessor(newFakeStore(tk), cls, testConfig()).Process(t.Context(), tk.ID.String())
					if err != nil || out.Kind != triage.Retryable {
						t.Fatalf("Process = %+v, %v; want Retryable", out, err)
					}
					at := cls.times()
					if len(at) != triage.DefaultMaxAttempts {
						t.Fatalf("calls = %d, want %d", len(at), triage.DefaultMaxAttempts)
					}
					for i := 1; i < len(at); i++ {
						if gap := at[i].Sub(at[i-1]); gap < tt.min[i-1] || gap > tt.max[i-1] {
							t.Fatalf("wait before call %d = %v, want in [%v, %v]", i+1, gap, tt.min[i-1], tt.max[i-1])
						}
					}
				}
			})
		})
	}
}

// TestCallTimeout checks that a hanging call is cut off by the per-call
// timeout and counted as transient.
func TestCallTimeout(t *testing.T) {
	tests := []struct {
		name string
		// ret is what the hanging classifier returns once ctx is done.
		ret func(ctx context.Context) error
	}{
		{"classifier maps it to transient", func(ctx context.Context) error {
			return fmt.Errorf("%w: %w", classifier.ErrTransient, ctx.Err())
		}},
		{"classifier returns the bare ctx error", func(ctx context.Context) error { return ctx.Err() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hang := func(ctx context.Context) (classifier.Result, error) {
					<-ctx.Done()
					return classifier.Result{}, tt.ret(ctx)
				}
				tk := pendingTicket()
				store := newFakeStore(tk)
				cfg := testConfig()
				cfg.MaxAttempts = 1
				start := time.Now()
				out, err := newProcessor(store, &script{steps: steps(hang)}, cfg).Process(t.Context(), tk.ID.String())
				if err != nil || out.Kind != triage.Retryable {
					t.Fatalf("Process = %+v, %v; want Retryable", out, err)
				}
				if elapsed := time.Since(start); elapsed != triage.DefaultCallTimeout {
					t.Errorf("Process took %v, want the call timeout %v", elapsed, triage.DefaultCallTimeout)
				}
				c := store.updates[0].Calls[0]
				if c.Outcome != triage.CallTransientError || c.Result.Call.Latency != triage.DefaultCallTimeout {
					t.Errorf("recorded call = %v after %v, want transient after %v",
						c.Outcome, c.Result.Call.Latency, triage.DefaultCallTimeout)
				}
				if c.Result.Call.Provider == "" {
					t.Error("recorded call has no provider")
				}
			})
		})
	}
}

// TestCancelDuringRetry checks that a cancelled ctx stops the backoff wait at
// once and writes nothing, so the message is redelivered.
func TestCancelDuringRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		tk := pendingTicket()
		store := newFakeStore(tk)
		cls := &script{steps: steps(func(context.Context) (classifier.Result, error) {
			time.AfterFunc(50*time.Millisecond, cancel)
			return classifier.Result{}, classifier.ErrTransient
		})}

		start := time.Now()
		_, err := newProcessor(store, cls, testConfig()).Process(ctx, tk.ID.String())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Process error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed != 50*time.Millisecond {
			t.Errorf("Process returned after %v, want right at the cancel (50ms)", elapsed)
		}
		if n := len(cls.times()); n != 1 {
			t.Errorf("classifier calls = %d, want 1", n)
		}
		if len(store.updates) != 0 {
			t.Errorf("updates = %+v, want none", store.updates)
		}
	})
}

// TestBudgetGuard checks the spend window and the limit edge.
func TestBudgetGuard(t *testing.T) {
	tests := []struct {
		name       string
		spent      float64
		budget     float64
		wantCalls  int
		wantReason ticket.Reason
	}{
		{"under budget", 0.99, 1, 1, ""},
		{"budget reached", 1, 1, 0, ticket.ReasonBudgetExhausted},
		{"over budget", 1.5, 1, 0, ticket.ReasonBudgetExhausted},
		{"zero budget allows no calls", 0, 0, 0, ticket.ReasonBudgetExhausted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// The bubble clock starts at midnight UTC; move into the day.
				time.Sleep(13*time.Hour + 7*time.Minute)
				now := time.Now().UTC()
				midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

				tk := pendingTicket()
				store := newFakeStore(tk)
				store.spent = tt.spent
				cls := &script{steps: steps(answer(confident()))}
				cfg := testConfig()
				cfg.DailyBudgetUSD = tt.budget

				out, err := newProcessor(store, cls, cfg).Process(t.Context(), tk.ID.String())
				if err != nil || out.Kind != triage.Done {
					t.Fatalf("Process = %+v, %v; want Done", out, err)
				}
				if len(store.since) != 1 || !store.since[0].Equal(midnight) {
					t.Errorf("SpentSince(%v), want the start of the UTC day %v", store.since, midnight)
				}
				if n := len(cls.times()); n != tt.wantCalls {
					t.Errorf("classifier calls = %d, want %d", n, tt.wantCalls)
				}
				if out.Reason != tt.wantReason {
					t.Errorf("reason = %q, want %q", out.Reason, tt.wantReason)
				}
			})
		})
	}
}

// TestCallLog checks the per-call log line: the fields of PRD §4.3 and no
// ticket text.
func TestCallLog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	tk := pendingTicket()
	cls := &script{steps: steps(fail(classifier.ErrTransient), answer(confident()))}
	p := triage.NewProcessor(newFakeStore(tk), cls, testConfig(), log)
	synctest.Test(t, func(t *testing.T) {
		if _, err := p.Process(t.Context(), tk.ID.String()); err != nil {
			t.Fatal(err)
		}
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want one per call (2):\n%s", len(lines), buf.String())
	}
	for _, field := range []string{
		`"provider"`, `"model"`, `"ticket_id":"` + tk.ID.String(), `"attempt":2`, `"latency_ms"`,
		`"input_tokens"`, `"output_tokens"`, `"cost_usd"`, `"cost_source"`, `"outcome":"ok"`, `"http_status"`,
	} {
		if !strings.Contains(lines[1], field) {
			t.Errorf("log line has no %s: %s", field, lines[1])
		}
	}
	for _, secret := range []string{subject, body, "4111"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log contains ticket text %q", secret)
		}
	}
}

// TestLastErrorCapped checks that a long classifier error is cut on a rune
// boundary before it is stored.
func TestLastErrorCapped(t *testing.T) {
	long := fmt.Errorf("%w: %s", classifier.ErrPermanent, strings.Repeat("ї", 1000))
	tk := pendingTicket()
	store := newFakeStore(tk)
	if _, err := newProcessor(store, &script{steps: steps(fail(long))}, testConfig()).Process(t.Context(), tk.ID.String()); err != nil {
		t.Fatal(err)
	}
	got := store.ticket(tk.ID).LastError
	if len(got) > 512 || !utf8.ValidString(got) || !strings.HasPrefix(long.Error(), got) {
		t.Fatalf("last_error has %d bytes (valid UTF-8 %v), want a valid prefix of at most 512", len(got), utf8.ValidString(got))
	}
}

// TestGiveUp checks the fail-open before the dead-letter queue: only a
// pending ticket moves to needs_human, without a classifier call.
func TestGiveUp(t *testing.T) {
	tests := []struct {
		name string
		// prepare adjusts the store and returns the id to give up on.
		prepare    func(s *fakeStore, tk ticket.Ticket) string
		wantKind   triage.Kind
		wantCause  error
		wantStatus ticket.Status
	}{
		{"pending", func(_ *fakeStore, tk ticket.Ticket) string { return tk.ID.String() },
			triage.Done, nil, ticket.StatusNeedsHuman},
		{"already classified", func(s *fakeStore, tk ticket.Ticket) string {
			tk.Status = ticket.StatusClassified
			s.tickets[tk.ID] = tk
			return tk.ID.String()
		}, triage.Skipped, nil, ticket.StatusClassified},
		{"missing", func(*fakeStore, ticket.Ticket) string { return uuid.NewV7().String() },
			triage.Skipped, nil, ticket.StatusPending},
		{"bad id", func(*fakeStore, ticket.Ticket) string { return "not-a-uuid" },
			triage.Poison, triage.ErrBadID, ticket.StatusPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk := pendingTicket()
			store := newFakeStore(tk)
			cls := &script{steps: steps(answer(confident()))}
			id := tt.prepare(store, tk)

			out, err := newProcessor(store, cls, testConfig()).GiveUp(t.Context(), id)
			if err != nil {
				t.Fatalf("GiveUp: %v", err)
			}
			if out.Kind != tt.wantKind || !errors.Is(out.Err, tt.wantCause) {
				t.Errorf("GiveUp = %+v, want kind %v, cause %v", out, tt.wantKind, tt.wantCause)
			}
			if tt.wantKind == triage.Done &&
				(out.Status != ticket.StatusNeedsHuman || out.Reason != ticket.ReasonClassifierUnavailable) {
				t.Errorf("GiveUp = %s/%s, want needs_human/classifier_unavailable", out.Status, out.Reason)
			}
			if got := store.ticket(tk.ID).Status; got != tt.wantStatus {
				t.Errorf("stored status = %s, want %s", got, tt.wantStatus)
			}
			if n := len(cls.times()); n != 0 {
				t.Errorf("classifier calls = %d, want 0", n)
			}
		})
	}

	store := newFakeStore()
	store.err = errors.New("db down")
	if _, err := newProcessor(store, &script{}, testConfig()).GiveUp(t.Context(), uuid.NewV7().String()); err == nil {
		t.Error("GiveUp with a failing store succeeded, want an error")
	}
}
