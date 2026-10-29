package shadow_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/shadow"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// fakeStore keeps tickets and shadow rows in memory. It has no way to write
// a ticket, which is the point.
type fakeStore struct {
	mu      sync.Mutex
	tickets map[uuid.UUID]ticket.Ticket
	spent   float64
	rows    map[string]shadow.Result // by run id + ticket id
	calls   []string                 // method names, in order
	err     error
}

func newFakeStore(tickets ...ticket.Ticket) *fakeStore {
	s := &fakeStore{tickets: make(map[uuid.UUID]ticket.Ticket), rows: make(map[string]shadow.Result)}
	for _, t := range tickets {
		s.tickets[t.ID] = t
	}
	return s
}

func rowKey(runID string, id uuid.UUID) string { return runID + "/" + id.String() }

func (s *fakeStore) record(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	return s.err
}

func (s *fakeStore) Ticket(_ context.Context, id uuid.UUID) (ticket.Ticket, error) {
	if err := s.record("Ticket"); err != nil {
		return ticket.Ticket{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[id]
	if !ok {
		return ticket.Ticket{}, fmt.Errorf("get ticket: %w", ticket.ErrNotFound)
	}
	return t, nil
}

func (s *fakeStore) SpentSince(context.Context, time.Time) (float64, error) {
	if err := s.record("SpentSince"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spent, nil
}

func (s *fakeStore) Done(_ context.Context, runID string, id uuid.UUID) (bool, error) {
	if err := s.record("Done"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[rowKey(runID, id)]
	return ok && r.Outcome == shadow.OutcomeOK, nil
}

func (s *fakeStore) Save(_ context.Context, r shadow.Result) error {
	if err := s.record("Save"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[rowKey(r.RunID, r.TicketID)] = r
	return nil
}

func (s *fakeStore) row(runID string, id uuid.UUID) (shadow.Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[rowKey(runID, id)]
	return r, ok
}

func (s *fakeStore) methods() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.calls, ",")
}

// billingTicket is classified live as billing; the fake classifier answers
// billing with full category confidence and no urgency keyword (0.6).
func billingTicket() ticket.Ticket {
	return ticket.Ticket{
		ID: uuid.NewV7(), Subject: "Invoice", Body: "Please send the invoice.",
		Status: ticket.StatusClassified,
		Classification: &ticket.Classification{
			Category: "billing", CategoryConfidence: 0.9, ComplaintP: 0.1, Urgency: 1, UrgencyConfidence: 0.7,
		},
	}
}

const runID = "test-run"

func newProcessor(t *testing.T, store shadow.Store, cls classifier.Classifier, cfg shadow.Config) (*shadow.Processor, *bytes.Buffer) {
	t.Helper()
	if cfg.RunID == "" {
		cfg.RunID = runID
	}
	if cfg.Thresholds == (classifier.Thresholds{}) {
		cfg.Thresholds = classifier.DefaultThresholds()
	}
	if cfg.DailyBudgetUSD == 0 {
		cfg.DailyBudgetUSD = 1
	}
	var logs bytes.Buffer
	p, err := shadow.NewProcessor(store, cls, cfg, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	return p, &logs
}

func TestReplayClassifiesIntoShadowOnly(t *testing.T) {
	tk := billingTicket()
	tests := []struct {
		name       string
		thresholds classifier.Thresholds
		wantStatus ticket.Status
		wantReason ticket.Reason
	}{
		{"live thresholds", classifier.DefaultThresholds(), ticket.StatusClassified, ""},
		{"stricter urgency threshold", classifier.Thresholds{
			CategoryMinConfidence: 0.6, UrgencyMinConfidence: 0.65, RouteOtherToHuman: true,
		}, ticket.StatusNeedsHuman, ticket.ReasonLowConfidence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore(tk)
			p, logs := newProcessor(t, store, &classifier.Fake{}, shadow.Config{Thresholds: tt.thresholds})
			if err := p.Replay(t.Context(), tk.ID.String()); err != nil {
				t.Fatalf("Replay: %v", err)
			}
			if got, want := store.methods(), "Done,Ticket,SpentSince,Save"; got != want {
				t.Errorf("store calls = %s, want %s", got, want)
			}
			r, ok := store.row(runID, tk.ID)
			if !ok {
				t.Fatal("no shadow row saved")
			}
			if r.Outcome != shadow.OutcomeOK || r.Attempts != 1 || r.Status != tt.wantStatus || r.Reason != tt.wantReason {
				t.Errorf("row = %s/%d %s/%s, want ok/1 %s/%s",
					r.Outcome, r.Attempts, r.Status, r.Reason, tt.wantStatus, tt.wantReason)
			}
			if r.Answer.Category != "billing" || r.Answer.Call.Model != classifier.FakeModel || r.At.IsZero() {
				t.Errorf("row answer = %+v at %v", r.Answer, r.At)
			}
			if r.CostSource != classifier.CostEstimated {
				t.Errorf("cost source = %q, want estimated", r.CostSource)
			}

			rep := p.Report()
			if rep.Total() != 1 || rep.Agreement[shadow.QuestionCategory] != (shadow.Agreement{Agree: 1, Total: 1}) {
				t.Errorf("report = %+v", rep)
			}
			if wantChanged := tt.wantStatus != tk.Status; (len(rep.Changes) == 1) != wantChanged {
				t.Errorf("report changes = %+v, want changed %v", rep.Changes, wantChanged)
			}
			for _, secret := range []string{tk.Subject, tk.Body} {
				if strings.Contains(logs.String(), secret) {
					t.Errorf("logs leak ticket text %q: %s", secret, logs.String())
				}
			}
			if !strings.Contains(logs.String(), `"msg":"shadow classifier call"`) ||
				!strings.Contains(logs.String(), `"run_id":"test-run"`) {
				t.Errorf("no per-call log line with the run id: %s", logs.String())
			}
		})
	}
}

func TestReplaySkipsAndStops(t *testing.T) {
	tk := billingTicket()
	errDB := errors.New("db down")
	tests := []struct {
		name    string
		prepare func(*fakeStore)
		id      string
		wantErr error
		// wantCalls are the store methods called, in order.
		wantCalls string
		wantRep   func(shadow.Report) bool
	}{
		{
			name: "already answered in this run",
			prepare: func(s *fakeStore) {
				s.rows[rowKey(runID, tk.ID)] = shadow.Result{Outcome: shadow.OutcomeOK}
			},
			id: tk.ID.String(), wantCalls: "Done",
			wantRep: func(r shadow.Report) bool { return r.Skipped == 1 && r.Total() == 0 },
		},
		{
			name: "failed earlier in this run is retried",
			prepare: func(s *fakeStore) {
				s.rows[rowKey(runID, tk.ID)] = shadow.Result{Outcome: shadow.OutcomeTransientError}
			},
			id: tk.ID.String(), wantCalls: "Done,Ticket,SpentSince,Save",
			wantRep: func(r shadow.Report) bool { return r.Replayed[shadow.OutcomeOK] == 1 },
		},
		{
			name: "missing ticket", id: uuid.NewV7().String(), wantCalls: "Done,Ticket",
			wantRep: func(r shadow.Report) bool { return r.Missing == 1 },
		},
		{
			name: "bad id", id: "not-a-uuid", wantCalls: "",
			wantRep: func(r shadow.Report) bool { return r.Missing == 1 },
		},
		{
			name:    "budget spent",
			prepare: func(s *fakeStore) { s.spent = 1 },
			id:      tk.ID.String(), wantErr: shadow.ErrBudgetExhausted, wantCalls: "Done,Ticket,SpentSince",
			wantRep: func(r shadow.Report) bool { return r.Total() == 0 },
		},
		{
			name:    "storage failure",
			prepare: func(s *fakeStore) { s.err = errDB },
			id:      tk.ID.String(), wantErr: errDB, wantCalls: "Done",
			wantRep: func(r shadow.Report) bool { return r.Total() == 0 },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore(tk)
			if tt.prepare != nil {
				tt.prepare(store)
			}
			cls := &classifier.Fake{}
			p, _ := newProcessor(t, store, cls, shadow.Config{})
			err := p.Replay(t.Context(), tt.id)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
				t.Errorf("Replay error = %v, want %v", err, tt.wantErr)
			}
			if got := store.methods(); got != tt.wantCalls {
				t.Errorf("store calls = %q, want %q", got, tt.wantCalls)
			}
			if wantCalls := strings.HasSuffix(tt.wantCalls, "Save"); (cls.Calls() == 1) != wantCalls {
				t.Errorf("classifier calls = %d", cls.Calls())
			}
			if rep := p.Report(); !tt.wantRep(rep) {
				t.Errorf("unexpected report %+v", rep)
			}
		})
	}
}

func TestReplayDryRunWritesNothing(t *testing.T) {
	tk := billingTicket()
	store := newFakeStore(tk)
	// A spent budget does not stop a dry run: the fake costs nothing.
	store.spent = 5
	p, _ := newProcessor(t, store, &classifier.Fake{}, shadow.Config{DryRun: true})
	if err := p.Replay(t.Context(), tk.ID.String()); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got := store.methods(); got != "Ticket" {
		t.Errorf("store calls = %q, want only Ticket", got)
	}
	rep := p.Report()
	if !rep.DryRun || rep.Replayed[shadow.OutcomeOK] != 1 {
		t.Errorf("report = %+v, want a dry run with one ok ticket", rep)
	}
}

func TestReplayClassifierFailures(t *testing.T) {
	transient := fmt.Errorf("%w: 529", classifier.ErrTransient)
	permanent := fmt.Errorf("%w: 400", classifier.ErrPermanent)
	auth := fmt.Errorf("%w: 401", classifier.ErrAuth)
	tests := []struct {
		name         string
		errs         []error
		wantErr      error
		wantOutcome  shadow.Outcome // empty: no row
		wantAttempts int
	}{
		{"transient then ok", []error{transient}, nil, shadow.OutcomeOK, 2},
		{"transient on every attempt", []error{transient, transient, transient}, nil, shadow.OutcomeTransientError, 3},
		{"permanent", []error{permanent}, nil, shadow.OutcomePermanentError, 1},
		{"invalid answer", []error{classifier.ErrInvalidAnswer}, nil, shadow.OutcomeInvalidAnswer, 1},
		{"auth stops the run", []error{auth}, classifier.ErrAuth, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tk := billingTicket()
				store := newFakeStore(tk)
				cls := &classifier.Fake{Errs: tt.errs}
				p, _ := newProcessor(t, store, cls, shadow.Config{})
				err := p.Replay(t.Context(), tk.ID.String())
				if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
					t.Fatalf("Replay error = %v, want %v", err, tt.wantErr)
				}
				r, ok := store.row(runID, tk.ID)
				if tt.wantOutcome == "" {
					if ok {
						t.Fatalf("row saved for an auth failure: %+v", r)
					}
					return
				}
				if !ok {
					t.Fatal("no row saved")
				}
				if r.Outcome != tt.wantOutcome || r.Attempts != tt.wantAttempts {
					t.Errorf("row = %s after %d attempts, want %s after %d",
						r.Outcome, r.Attempts, tt.wantOutcome, tt.wantAttempts)
				}
				if failed := tt.wantOutcome != shadow.OutcomeOK; failed != (r.Status == "") || failed != (r.LastError != "") {
					t.Errorf("row status %q, last error %q for outcome %s", r.Status, r.LastError, r.Outcome)
				}
			})
		})
	}
}

// slowClassifier blocks until its call is cancelled.
type slowClassifier struct{}

func (slowClassifier) Classify(ctx context.Context, _ classifier.Input) (classifier.Result, error) {
	<-ctx.Done()
	return classifier.Result{}, ctx.Err()
}

func TestReplayTimeoutAndCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tk := billingTicket()
		store := newFakeStore(tk)
		p, _ := newProcessor(t, store, slowClassifier{}, shadow.Config{CallTimeout: time.Second, MaxAttempts: 2})
		if err := p.Replay(t.Context(), tk.ID.String()); err != nil {
			t.Fatalf("Replay: %v", err)
		}
		r, _ := store.row(runID, tk.ID)
		if r.Outcome != shadow.OutcomeTransientError || r.Attempts != 2 {
			t.Errorf("timed-out row = %s after %d attempts, want transient_error after 2", r.Outcome, r.Attempts)
		}

		ctx, cancel := context.WithCancel(t.Context())
		other := billingTicket()
		store.tickets[other.ID] = other
		go func() {
			time.Sleep(500 * time.Millisecond)
			cancel()
		}()
		if err := p.Replay(ctx, other.ID.String()); !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled Replay error = %v, want context.Canceled", err)
		}
		if _, ok := store.row(runID, other.ID); ok {
			t.Error("row saved for a cancelled replay")
		}
	})
}

func TestNewProcessorRejectsBadRunID(t *testing.T) {
	for _, id := range []string{"", "Upper", "-dash-first", "has space", strings.Repeat("a", 64)} {
		if _, err := shadow.NewProcessor(newFakeStore(), &classifier.Fake{}, shadow.Config{RunID: id},
			slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
			t.Errorf("NewProcessor(run id %q) succeeded, want an error", id)
		}
	}
	for _, id := range []string{"a", "20261029t150405z", "model-1.14_strict"} {
		if !shadow.ValidRunID(id) {
			t.Errorf("ValidRunID(%q) = false, want true", id)
		}
	}
}
