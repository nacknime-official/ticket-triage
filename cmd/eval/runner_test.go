package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/classifier/jev"
	"github.com/nacknime-official/ticket-triage/internal/evalset"
)

// classifyFunc adapts a function to classifier.Classifier.
type classifyFunc func(context.Context, classifier.Input) (classifier.Result, error)

func (f classifyFunc) Classify(ctx context.Context, in classifier.Input) (classifier.Result, error) {
	return f(ctx, in)
}

// validResult is a valid answer whose call took latency.
func validResult(latency time.Duration) classifier.Result {
	return classifier.Result{
		ComplaintP: 0.2, Category: classifier.CategoryBilling, CategoryConfidence: 0.9,
		Urgency: 1.2, UrgencyConfidence: 0.8,
		Call: classifier.Call{
			Provider: "test", Model: "served-1", HTTPStatus: 200, Latency: latency,
			InputTokens: 450, CostUSD: 0.00002, CostSource: classifier.CostReported,
			RawResponse: []byte(`{"answers":{}}`),
		},
	}
}

func testRows(n int) []evalset.Row {
	rows := make([]evalset.Row, n)
	for i := range rows {
		rows[i] = evalset.Row{
			ID: fmt.Sprintf("row-%02d", i), Source: evalset.SourceGenerated, Lang: "en",
			Subject: fmt.Sprintf("subject %d", i), Body: fmt.Sprintf("body %d", i),
			ExpectedCategory: classifier.CategoryBilling, ExpectedUrgency: 1,
		}
	}
	return rows
}

func newRunner(clf classifier.Classifier) *runner {
	return &runner{
		clf: clf, tax: classifier.DefaultTaxonomy(), provider: "test", model: "model-1",
		concurrency: 4, retries: 2, backoff: time.Second,
	}
}

func TestRunnerBoundsConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var inFlight, peak atomic.Int32
		r := newRunner(classifyFunc(func(ctx context.Context, _ classifier.Input) (classifier.Result, error) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(time.Second)
			return validResult(time.Second), nil
		}))
		r.concurrency = 3

		start := time.Now()
		outs, err := r.run(t.Context(), testRows(9))
		if err != nil {
			t.Fatal(err)
		}
		if got := peak.Load(); got != 3 {
			t.Errorf("peak in-flight calls = %d, want 3", got)
		}
		if got := time.Since(start); got != 3*time.Second {
			t.Errorf("run took %v, want 3s (9 rows, 3 at a time)", got)
		}
		for i, o := range outs {
			if o.Row.ID != fmt.Sprintf("row-%02d", i) || o.Err != nil || o.Attempts != 1 {
				t.Errorf("outs[%d] = %s, err %v, attempts %d", i, o.Row.ID, o.Err, o.Attempts)
			}
		}
	})
}

func TestRunnerErrors(t *testing.T) {
	transient := fmt.Errorf("%w: 503", classifier.ErrTransient)
	permanent := fmt.Errorf("%w: 422", classifier.ErrPermanent)
	tests := []struct {
		name         string
		errs         []error
		retries      int
		wantErr      error
		wantAttempts int
		wantWait     time.Duration
	}{
		{"ok", nil, 2, nil, 1, 0},
		{"transient then ok", []error{transient, transient}, 2, nil, 3, 3 * time.Second},
		{"transient past retries", []error{transient, transient}, 1, classifier.ErrTransient, 2, time.Second},
		{"no retries", []error{transient}, 0, classifier.ErrTransient, 1, 0},
		{"permanent is not retried", []error{permanent}, 2, classifier.ErrPermanent, 1, 0},
		{"retry-after wins over a shorter backoff", []error{retryLater{5 * time.Second}}, 2, nil, 2, 5 * time.Second},
		{"backoff wins over a shorter retry-after", []error{retryLater{time.Millisecond}}, 2, nil, 2, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &classifier.Fake{Errs: tt.errs}
				r := newRunner(f)
				r.retries = tt.retries

				start := time.Now()
				outs, err := r.run(t.Context(), testRows(1))
				if err != nil {
					t.Fatal(err)
				}
				o := outs[0]
				if !errors.Is(o.Err, tt.wantErr) || (tt.wantErr == nil) != (o.Err == nil) {
					t.Errorf("Err = %v, want %v", o.Err, tt.wantErr)
				}
				if o.Attempts != tt.wantAttempts || f.Calls() != tt.wantAttempts {
					t.Errorf("Attempts = %d, calls = %d, want %d", o.Attempts, f.Calls(), tt.wantAttempts)
				}
				if got := time.Since(start); got != tt.wantWait {
					t.Errorf("backoff took %v, want %v", got, tt.wantWait)
				}
			})
		})
	}
}

// retryLater is a transient error with a Retry-After.
type retryLater struct{ after time.Duration }

func (e retryLater) Error() string             { return "retry later" }
func (e retryLater) Unwrap() error             { return classifier.ErrTransient }
func (e retryLater) RetryAfter() time.Duration { return e.after }

func TestRunnerCostCap(t *testing.T) {
	var calls atomic.Int32
	r := newRunner(classifyFunc(func(context.Context, classifier.Input) (classifier.Result, error) {
		calls.Add(1)
		return validResult(0), nil // $0.00002 a call
	}))
	r.concurrency, r.maxCostUSD = 1, 0.00005

	_, err := r.run(t.Context(), testRows(10))
	if !errors.Is(err, errCostCap) {
		t.Fatalf("run() error = %v, want errCostCap", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3: the third call passes the cap", got)
	}

	t.Run("cached answers cost nothing", func(t *testing.T) {
		c, err := openCache(filepath.Join(t.TempDir(), "cache.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		rows := testRows(10)
		for _, row := range rows {
			if err := c.put(newCacheKey("test", "model-1", row), validResult(0)); err != nil {
				t.Fatal(err)
			}
		}
		r := newRunner(classifyFunc(func(context.Context, classifier.Input) (classifier.Result, error) {
			t.Error("unexpected call")
			return classifier.Result{}, nil
		}))
		r.cache, r.maxCostUSD = c, 0.00005
		if _, err := r.run(t.Context(), rows); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunnerRejectsInvalidAnswer(t *testing.T) {
	r := newRunner(classifyFunc(func(context.Context, classifier.Input) (classifier.Result, error) {
		res := validResult(0)
		res.Category = "shipping"
		return res, nil
	}))
	outs, err := r.run(t.Context(), testRows(1))
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(outs[0].Err, classifier.ErrInvalidAnswer) {
		t.Errorf("Err = %v, want ErrInvalidAnswer", outs[0].Err)
	}
}

func TestRunnerStopsOnAuth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		r := newRunner(classifyFunc(func(ctx context.Context, _ classifier.Input) (classifier.Result, error) {
			if calls.Add(1) == 1 {
				return classifier.Result{}, fmt.Errorf("%w: 401", classifier.ErrAuth)
			}
			select {
			case <-time.After(time.Second):
				return validResult(time.Second), nil
			case <-ctx.Done():
				return classifier.Result{}, fmt.Errorf("%w: %w", classifier.ErrTransient, ctx.Err())
			}
		}))
		r.concurrency = 1

		_, err := r.run(t.Context(), testRows(20))
		if !errors.Is(err, classifier.ErrAuth) {
			t.Fatalf("run() error = %v, want ErrAuth", err)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("calls = %d, want 1: the run stops at the auth error", got)
		}
	})
}

func TestRunnerStopsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		r := newRunner(classifyFunc(func(ctx context.Context, _ classifier.Input) (classifier.Result, error) {
			select {
			case <-time.After(time.Second):
				return validResult(time.Second), nil
			case <-ctx.Done():
				return classifier.Result{}, fmt.Errorf("%w: %w", classifier.ErrTransient, ctx.Err())
			}
		}))
		time.AfterFunc(1500*time.Millisecond, cancel)

		start := time.Now()
		_, err := r.run(ctx, testRows(20))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run() error = %v, want context.Canceled", err)
		}
		if got := time.Since(start); got != 1500*time.Millisecond {
			t.Errorf("run returned after %v, want 1.5s", got)
		}
	})
}

func TestRunnerCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "results.jsonl")
	rows := testRows(3)
	var calls atomic.Int32
	clf := classifyFunc(func(_ context.Context, in classifier.Input) (classifier.Result, error) {
		calls.Add(1)
		if in.Subject == "subject 2" {
			return classifier.Result{}, fmt.Errorf("%w: 422", classifier.ErrPermanent)
		}
		return validResult(120 * time.Millisecond), nil
	})
	// pass runs rows with a fresh runner over the cache file, as a new process would.
	pass := func(t *testing.T, model string, refresh bool, rows []evalset.Row) []outcome {
		t.Helper()
		c, err := openCache(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := c.Close(); err != nil {
				t.Error(err)
			}
		}()
		r := newRunner(clf)
		r.cache, r.model, r.refresh = c, model, refresh
		outs, err := r.run(t.Context(), rows)
		if err != nil {
			t.Fatal(err)
		}
		return outs
	}
	check := func(t *testing.T, outs []outcome, wantCalls int32, wantCached ...bool) {
		t.Helper()
		if got := calls.Swap(0); got != wantCalls {
			t.Errorf("calls = %d, want %d", got, wantCalls)
		}
		for i, o := range outs {
			if o.Cached != wantCached[i] {
				t.Errorf("row %s: Cached = %t, want %t", o.Row.ID, o.Cached, wantCached[i])
			}
		}
	}

	first := pass(t, "model-1", false, rows)
	check(t, first, 3, false, false, false)

	second := pass(t, "model-1", false, rows)
	// The failed row is not cached, so it is called again.
	check(t, second, 1, true, true, false)
	want := first[0].Result
	if got := second[0].Result; got.Category != want.Category || got.Call.Latency != want.Call.Latency ||
		got.Call.CostUSD != want.Call.CostUSD || got.Call.Model != want.Call.Model ||
		!bytes.Equal(got.Call.RawResponse, want.Call.RawResponse) {
		t.Errorf("cached result = %+v, want %+v", got, want)
	}

	t.Run("another model misses", func(t *testing.T) {
		check(t, pass(t, "model-2", false, rows[:1]), 1, false)
	})
	t.Run("edited text misses", func(t *testing.T) {
		edited := []evalset.Row{rows[0]}
		edited[0].Body += " edited"
		check(t, pass(t, "model-1", false, edited), 1, false)
	})
	t.Run("refresh calls again", func(t *testing.T) {
		check(t, pass(t, "model-1", true, rows[1:2]), 1, false)
	})
	t.Run("a broken line is skipped", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, `{"id":"row-00","provi`); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		c, err := openCache(path)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if c.Skipped != 1 {
			t.Errorf("Skipped = %d, want 1", c.Skipped)
		}
		if _, ok := c.get(newCacheKey("test", "model-1", rows[1])); !ok {
			t.Error("entry before the broken line is gone")
		}
	})
}

// TestRunnerWithJevRateLimit runs the real adapter over an in-memory transport:
// the runner's concurrency does not get past the adapter's rate limit.
func TestRunnerWithJevRateLimit(t *testing.T) {
	// A success recorded live by cmd/jevprobe.
	recorded, err := os.ReadFile("../../internal/classifier/jev/testdata/live/ok.json")
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Body jsontext.Value `json:"body"`
	}
	if err := json.Unmarshal(recorded, &rec); err != nil {
		t.Fatal(err)
	}
	body := []byte(rec.Body)
	synctest.Test(t, func(t *testing.T) {
		var (
			mu    sync.Mutex
			times []time.Duration
		)
		start := time.Now()
		clf, err := jev.New(jev.Config{
			Provider: jev.ProviderOpenRouter, BaseURL: "http://jev.test", APIKey: "test-key",
			Model: "typesafe/jev-1.14", Timeout: time.Second, RateLimit: 4, MaxConcurrency: 8,
			HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				times = append(times, time.Since(start))
				mu.Unlock()
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(bytes.NewReader(body)), Request: r,
				}, nil
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		r := newRunner(clf)
		r.concurrency = 8

		outs, err := r.run(t.Context(), testRows(8))
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range outs {
			if o.Err != nil {
				t.Errorf("row %s: %v", o.Row.ID, o.Err)
			}
		}
		// Burst 1 at 4 calls per second: one call every 250ms.
		for i, d := range times {
			if want := time.Duration(i) * 250 * time.Millisecond; d != want {
				t.Errorf("call %d at %v, want %v", i, d, want)
			}
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
