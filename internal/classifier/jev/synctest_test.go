package jev

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
)

// These tests run in a synctest bubble with a fake clock, so the HTTP client
// gets an in-memory transport instead of a network.

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func okResponse(r *http.Request, body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    r,
	}
}

func fakeClient(t *testing.T, cfg Config, rt roundTripFunc) *Client {
	t.Helper()
	cfg.HTTPClient = &http.Client{Transport: rt}
	return newClient(t, cfg)
}

func TestRateLimit(t *testing.T) {
	body := fixture(t, "response_openrouter.json")
	synctest.Test(t, func(t *testing.T) {
		var (
			mu    sync.Mutex
			times []time.Time
		)
		cfg := testConfig("http://jev.test")
		cfg.RateLimit = 2 // one call per 500ms, burst 1
		c := fakeClient(t, cfg, func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
			return okResponse(r, body), nil
		})

		start := time.Now()
		for range 4 {
			if _, err := c.Classify(t.Context(), testInput); err != nil {
				t.Fatal(err)
			}
		}
		for i, at := range times {
			if want := time.Duration(i) * 500 * time.Millisecond; at.Sub(start) != want {
				t.Errorf("call %d at %v, want %v", i, at.Sub(start), want)
			}
		}
	})
}

func TestRateLimitHonorsContext(t *testing.T) {
	body := fixture(t, "response_openrouter.json")
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig("http://jev.test")
		cfg.RateLimit = 0.1 // one call per 10s
		var calls atomic.Int32
		c := fakeClient(t, cfg, func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return okResponse(r, body), nil
		})
		if _, err := c.Classify(t.Context(), testInput); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := c.Classify(ctx, testInput); !errors.Is(err, classifier.ErrTransient) {
			t.Errorf("Classify() error = %v, want ErrTransient while rate limited", err)
		}
		if calls.Load() != 1 {
			t.Errorf("calls = %d, want 1", calls.Load())
		}
	})
}

func TestConcurrencyCap(t *testing.T) {
	body := fixture(t, "response_openrouter.json")
	synctest.Test(t, func(t *testing.T) {
		var inFlight, peak atomic.Int32
		release := make(chan struct{})
		cfg := testConfig("http://jev.test")
		cfg.MaxConcurrency = 2
		c := fakeClient(t, cfg, func(r *http.Request) (*http.Response, error) {
			n := inFlight.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			<-release
			inFlight.Add(-1)
			return okResponse(r, body), nil
		})

		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() {
				if _, err := c.Classify(t.Context(), testInput); err != nil {
					t.Error(err)
				}
			})
		}
		time.Sleep(time.Second) // past every rate-limiter wait
		synctest.Wait()
		if got := inFlight.Load(); got != 2 {
			t.Errorf("in flight = %d, want 2", got)
		}
		close(release)
		wg.Wait()
		if got := peak.Load(); got != 2 {
			t.Errorf("peak in flight = %d, want 2", got)
		}
	})
}

func TestConcurrencyCapHonorsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		cfg := testConfig("http://jev.test")
		cfg.MaxConcurrency = 1
		c := fakeClient(t, cfg, func(r *http.Request) (*http.Response, error) {
			<-release
			return nil, errors.New("released")
		})
		var wg sync.WaitGroup
		wg.Go(func() {
			if _, err := c.Classify(t.Context(), testInput); err == nil {
				t.Error("first Classify() error = nil")
			}
		})
		synctest.Wait()

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := c.Classify(ctx, testInput); !errors.Is(err, classifier.ErrTransient) ||
			!errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Classify() error = %v, want ErrTransient while waiting for a slot", err)
		}
		close(release)
		wg.Wait()
	})
}

func TestPerCallTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig("http://jev.test")
		cfg.Timeout = 3 * time.Second
		c := fakeClient(t, cfg, func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		start := time.Now()
		got, err := c.Classify(t.Context(), testInput)
		if !errors.Is(err, classifier.ErrTransient) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Classify() error = %v, want ErrTransient from the deadline", err)
		}
		if elapsed := time.Since(start); elapsed != cfg.Timeout || got.Call.Latency != cfg.Timeout {
			t.Errorf("elapsed %v, latency %v, want %v", elapsed, got.Call.Latency, cfg.Timeout)
		}
	})
}
