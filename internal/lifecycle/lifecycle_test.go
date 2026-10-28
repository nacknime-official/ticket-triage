package lifecycle

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type ctxKey struct{}

// recorder records the order in which steps run. Steps run on their own
// goroutines, and an abandoned one has no happens-before edge to the test.
type recorder struct {
	mu  sync.Mutex
	ran []string
}

func (r *recorder) step(name string, stop func(ctx context.Context) error) Step {
	return Step{Name: name, Stop: func(ctx context.Context) error {
		r.mu.Lock()
		r.ran = append(r.ran, name)
		r.mu.Unlock()
		return stop(ctx)
	}}
}

func (r *recorder) names() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.ran, ",")
}

func ok(context.Context) error { return nil }

func TestShutdown(t *testing.T) {
	errFlush := errors.New("flush failed")
	errClose := errors.New("close failed")

	tests := []struct {
		name    string
		steps   func(r *recorder) []Step
		ran     []string
		elapsed time.Duration
		errs    []error  // errors.Is targets
		msgs    []string // substrings of the error
	}{
		{
			name: "runs every step in order",
			steps: func(r *recorder) []Step {
				return []Step{r.step("http", ok), r.step("relay", ok), r.step("pool", ok)}
			},
			ran: []string{"http", "relay", "pool"},
		},
		{
			name: "failing steps do not stop the rest",
			steps: func(r *recorder) []Step {
				return []Step{
					r.step("relay", func(context.Context) error { return errFlush }),
					r.step("consumer", ok),
					r.step("pool", func(context.Context) error { return errClose }),
				}
			},
			ran:  []string{"relay", "consumer", "pool"},
			errs: []error{errFlush, errClose},
			msgs: []string{"stop relay: flush failed", "stop pool: close failed"},
		},
		{
			name: "slow steps share one deadline",
			steps: func(r *recorder) []Step {
				sleep := func(context.Context) error { time.Sleep(2 * time.Second); return nil }
				return []Step{r.step("a", sleep), r.step("b", sleep), r.step("c", ok)}
			},
			ran:     []string{"a", "b", "c"},
			elapsed: 4 * time.Second,
		},
		{
			name: "timeout in a step that honors ctx skips the rest",
			steps: func(r *recorder) []Step {
				return []Step{
					r.step("http", func(context.Context) error { time.Sleep(time.Second); return nil }),
					r.step("consumer", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }),
					r.step("relay", ok),
					r.step("pool", ok),
				}
			},
			ran:     []string{"http", "consumer"},
			elapsed: 5 * time.Second,
			errs:    []error{context.DeadlineExceeded},
			// The step may return at the deadline or be abandoned there: both name it.
			msgs: []string{"stop consumer: ", `skipped ["relay" "pool"]`, "after 5s"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := &recorder{}
				start := time.Now()
				err := Shutdown(t.Context(), 5*time.Second, tt.steps(r)...)

				if elapsed := time.Since(start); elapsed != tt.elapsed {
					t.Errorf("elapsed = %v, want %v", elapsed, tt.elapsed)
				}
				if got := r.names(); got != strings.Join(tt.ran, ",") {
					t.Errorf("ran = %v, want %v", got, tt.ran)
				}
				if len(tt.errs) == 0 && err != nil {
					t.Errorf("err = %v, want nil", err)
				}
				for _, target := range tt.errs {
					if !errors.Is(err, target) {
						t.Errorf("err = %v, want it to wrap %v", err, target)
					}
				}
				for _, msg := range tt.msgs {
					if err == nil || !strings.Contains(err.Error(), msg) {
						t.Errorf("err = %v, want it to contain %q", err, msg)
					}
				}
			})
		})
	}
}

func TestShutdownAbandonsStepIgnoringContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var poolRan bool
		start := time.Now()
		err := Shutdown(t.Context(), 3*time.Second,
			Step{Name: "stuck", Stop: func(context.Context) error { <-release; return nil }},
			Step{Name: "pool", Stop: func(context.Context) error { poolRan = true; return nil }},
		)
		if elapsed := time.Since(start); elapsed != 3*time.Second {
			t.Errorf("elapsed = %v, want the 3s timeout", elapsed)
		}
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "stop stuck: abandoned") {
			t.Errorf("err = %v, want the stuck step abandoned at the deadline", err)
		}
		if poolRan {
			t.Error("step after the deadline ran")
		}
		close(release) // let the abandoned goroutine exit before the bubble ends
		synctest.Wait()
	})
}

func TestShutdownIgnoresParentCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "v"))
		cancel() // the signal context is already done when shutdown starts

		start := time.Now()
		err := Shutdown(parent, 5*time.Second, Step{Name: "http", Stop: func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Errorf("step ctx already done: %v", ctx.Err())
			}
			if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(start.Add(5*time.Second)) {
				t.Errorf("deadline = %v, %v; want start+5s", deadline, ok)
			}
			if ctx.Value(ctxKey{}) != "v" {
				t.Error("step ctx lost the parent values")
			}
			return nil
		}})
		if err != nil {
			t.Errorf("err = %v", err)
		}
	})
}
