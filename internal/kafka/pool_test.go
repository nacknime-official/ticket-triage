package kafka

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestRunPool(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name  string
		n     int
		items int
		// fail makes item i fail; nil means every item succeeds.
		fail    func(i int) bool
		wantErr error
		// wantRun is the number of items started.
		wantRun int
	}{
		{name: "all succeed", n: 3, items: 10, wantRun: 10},
		{name: "fewer items than slots", n: 8, items: 2, wantRun: 2},
		{name: "one slot", n: 1, items: 4, wantRun: 4},
		{name: "empty", n: 2, items: 0, wantRun: 0},
		// Items take 1s each in slots of 2: items 0 and 1 run first, item 1
		// fails, and no later item starts.
		{name: "error stops the rest", n: 2, items: 6, fail: func(i int) bool { return i == 1 }, wantErr: errBoom, wantRun: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var (
					mu              sync.Mutex
					running, peak   int
					started, failed []int
				)
				items := make([]int, tt.items)
				for i := range items {
					items[i] = i
				}
				err := runPool(t.Context(), tt.n, items, func(ctx context.Context, i int) error {
					mu.Lock()
					running++
					peak = max(peak, running)
					started = append(started, i)
					mu.Unlock()
					defer func() {
						mu.Lock()
						running--
						mu.Unlock()
					}()
					if tt.fail != nil && tt.fail(i) {
						time.Sleep(time.Second / 2)
						mu.Lock()
						failed = append(failed, i)
						mu.Unlock()
						return errBoom
					}
					select {
					case <-time.After(time.Second):
						return nil
					case <-ctx.Done():
						return context.Cause(ctx)
					}
				})
				if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
					t.Errorf("runPool error = %v, want %v", err, tt.wantErr)
				}
				if len(started) != tt.wantRun {
					t.Errorf("started %v, want %d items", started, tt.wantRun)
				}
				if peak > tt.n {
					t.Errorf("peak concurrency %d, want at most %d", peak, tt.n)
				}
				if tt.wantErr == nil && tt.items >= tt.n && peak != tt.n {
					t.Errorf("peak concurrency %d, want %d", peak, tt.n)
				}
				if running != 0 {
					t.Errorf("%d calls still running after runPool returned", running)
				}
			})
		})
	}
}

// A done ctx cancels running items and starts no more; runPool waits for the
// running ones and returns the cause.
func TestRunPoolCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cause := errors.New("drain timed out")
		ctx, cancel := context.WithCancelCause(t.Context())
		var (
			mu       sync.Mutex
			started  []int
			returned int
		)
		go func() {
			time.Sleep(time.Second)
			cancel(cause)
		}()
		err := runPool(ctx, 2, []int{0, 1, 2, 3}, func(ctx context.Context, i int) error {
			mu.Lock()
			started = append(started, i)
			mu.Unlock()
			<-ctx.Done()
			mu.Lock()
			returned++
			mu.Unlock()
			return context.Cause(ctx)
		})
		if !errors.Is(err, cause) {
			t.Errorf("runPool error = %v, want %v", err, cause)
		}
		slices.Sort(started)
		if !slices.Equal(started, []int{0, 1}) {
			t.Errorf("started %v, want [0 1]", started)
		}
		if returned != len(started) {
			t.Errorf("%d of %d started calls returned", returned, len(started))
		}
	})
}

// Items that all succeed are not failed by ctx being done afterwards.
func TestRunPoolDoneAfterSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		err := runPool(ctx, 2, []int{0}, func(context.Context, int) error {
			cancel()
			return nil
		})
		if err != nil {
			t.Errorf("runPool error = %v, want nil", err)
		}
	})
}
