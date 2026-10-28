package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/outbox"
)

// fakeStore is an in-memory outbox.Store.
type fakeStore struct {
	mu        sync.Mutex
	nextID    int64
	pending   []outbox.Message
	published []string // keys, in publish order
	attempts  map[int64]int
	claimErr  error
	deletes   []time.Time
}

func newFakeStore() *fakeStore { return &fakeStore{attempts: map[int64]int{}} }

func (s *fakeStore) add(keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		s.nextID++
		s.pending = append(s.pending, outbox.Message{ID: s.nextID, Topic: "t", Key: []byte(k)})
	}
}

func (s *fakeStore) PublishPending(ctx context.Context, limit int, publish outbox.PublishFunc) (int, error) {
	s.mu.Lock()
	if s.claimErr != nil {
		defer s.mu.Unlock()
		return 0, s.claimErr
	}
	batch := slices.Clone(s.pending[:min(limit, len(s.pending))])
	s.mu.Unlock()
	if len(batch) == 0 {
		return 0, nil
	}

	errs := publish(ctx, batch)

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(errs) != len(batch) {
		return 0, fmt.Errorf("%d results for %d messages", len(errs), len(batch))
	}
	for i, m := range batch {
		if errs[i] != nil {
			s.attempts[m.ID]++
			continue
		}
		s.published = append(s.published, string(m.Key))
		s.pending = slices.DeleteFunc(s.pending, func(p outbox.Message) bool { return p.ID == m.ID })
	}
	return len(batch), nil
}

func (s *fakeStore) DeletePublished(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes = append(s.deletes, before)
	return 0, nil
}

func (s *fakeStore) state() (published []string, pending int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.published), len(s.pending)
}

// fakePublisher acknowledges every message unless fail is set.
type fakePublisher struct {
	mu      sync.Mutex
	fail    error
	batches [][]string
	publish func(ctx context.Context, msgs []outbox.Message) []error // overrides when set
}

func (p *fakePublisher) Publish(ctx context.Context, msgs []outbox.Message) []error {
	p.mu.Lock()
	var keys []string
	for _, m := range msgs {
		keys = append(keys, string(m.Key))
	}
	p.batches = append(p.batches, keys)
	fail, publish := p.fail, p.publish
	p.mu.Unlock()
	if publish != nil {
		return publish(ctx, msgs)
	}
	errs := make([]error, len(msgs))
	for i := range errs {
		errs[i] = fail
	}
	return errs
}

func (p *fakePublisher) setFail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = err
}

func (p *fakePublisher) batchSizes() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	var sizes []int
	for _, b := range p.batches {
		sizes = append(sizes, len(b))
	}
	return sizes
}

var discard = slog.New(slog.DiscardHandler)

const poll = 500 * time.Millisecond

// start runs a Relay with cfg until the test ends.
func start(t *testing.T, store outbox.Store, pub outbox.Publisher, cfg outbox.Config) *outbox.Relay {
	t.Helper()
	r := outbox.NewRelay(store, pub, cfg, discard)
	r.Start(t.Context())
	t.Cleanup(func() {
		if err := r.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return r
}

func TestRelayDrainsBacklogInBatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, pub := newFakeStore(), &fakePublisher{}
		store.add("a", "b", "c", "d", "e")
		start(t, store, pub, outbox.Config{PollInterval: poll, BatchSize: 2})
		synctest.Wait()

		// The whole backlog goes out before the first poll interval.
		published, pending := store.state()
		if want := []string{"a", "b", "c", "d", "e"}; !slices.Equal(published, want) || pending != 0 {
			t.Fatalf("published %v with %d pending, want %v and none", published, pending, want)
		}
		if got, want := pub.batchSizes(), []int{2, 2, 1}; !slices.Equal(got, want) {
			t.Errorf("batch sizes %v, want %v", got, want)
		}
	})
}

func TestRelayPolls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, pub := newFakeStore(), &fakePublisher{}
		start(t, store, pub, outbox.Config{PollInterval: poll})
		synctest.Wait()

		store.add("late")
		time.Sleep(poll - time.Millisecond)
		synctest.Wait()
		if published, _ := store.state(); len(published) != 0 {
			t.Fatalf("published %v before the poll interval", published)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if published, _ := store.state(); !slices.Equal(published, []string{"late"}) {
			t.Fatalf("published %v after the poll interval, want [late]", published)
		}
	})
}

func TestRelayLeavesFailedUnpublishedUntilRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, pub := newFakeStore(), &fakePublisher{}
		pub.setFail(errors.New("broker down"))
		store.add("a")
		start(t, store, pub, outbox.Config{PollInterval: poll})

		time.Sleep(3 * poll)
		synctest.Wait()
		published, pending := store.state()
		if len(published) != 0 || pending != 1 {
			t.Fatalf("published %v with %d pending while failing, want none and 1", published, pending)
		}
		if n := len(pub.batchSizes()); n != 4 {
			t.Errorf("%d publish attempts in 3 poll intervals, want 4 (one per poll, no hot loop)", n)
		}

		pub.setFail(nil)
		time.Sleep(poll)
		synctest.Wait()
		if published, pending := store.state(); !slices.Equal(published, []string{"a"}) || pending != 0 {
			t.Fatalf("after recovery published %v with %d pending, want [a] and none", published, pending)
		}
	})
}

func TestRelayFullFailedBatchWaitsForPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, pub := newFakeStore(), &fakePublisher{}
		pub.setFail(errors.New("broker down"))
		store.add("a", "b", "c", "d")
		start(t, store, pub, outbox.Config{PollInterval: poll, BatchSize: 2})
		synctest.Wait()
		if got, want := pub.batchSizes(), []int{2}; !slices.Equal(got, want) {
			t.Fatalf("batches %v before the first poll, want %v", got, want)
		}
	})
}

func TestRelayRetriesStoreErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, pub := newFakeStore(), &fakePublisher{}
		store.claimErr = errors.New("db down")
		store.add("a")
		start(t, store, pub, outbox.Config{PollInterval: poll})
		time.Sleep(poll)
		synctest.Wait()

		store.mu.Lock()
		store.claimErr = nil
		store.mu.Unlock()
		time.Sleep(poll)
		synctest.Wait()
		if published, _ := store.state(); !slices.Equal(published, []string{"a"}) {
			t.Fatalf("published %v after the store recovered, want [a]", published)
		}
	})
}

func TestRelayWrongResultCountMarksNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		pub := &fakePublisher{publish: func(context.Context, []outbox.Message) []error { return nil }}
		store.add("a", "b")
		start(t, store, pub, outbox.Config{PollInterval: poll})
		synctest.Wait()

		published, pending := store.state()
		if len(published) != 0 || pending != 2 {
			t.Fatalf("published %v with %d pending, want none and 2", published, pending)
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.attempts[1] != 1 || store.attempts[2] != 1 {
			t.Errorf("attempts = %v, want one failed attempt each", store.attempts)
		}
	})
}

func TestRelayPublishTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		var waited time.Duration
		pub := &fakePublisher{publish: func(ctx context.Context, msgs []outbox.Message) []error {
			begin := time.Now()
			<-ctx.Done()
			waited = time.Since(begin)
			errs := make([]error, len(msgs))
			for i := range errs {
				errs[i] = ctx.Err()
			}
			return errs
		}}
		store.add("a")
		start(t, store, pub, outbox.Config{PollInterval: time.Hour, PublishTimeout: 3 * time.Second})
		time.Sleep(3 * time.Second)
		synctest.Wait()

		if waited != 3*time.Second {
			t.Errorf("publish waited %v, want the 3s timeout", waited)
		}
		if _, pending := store.state(); pending != 1 {
			t.Errorf("%d pending after a timed-out publish, want 1", pending)
		}
	})
}

func TestRelayCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		const every, keep = time.Hour, 24 * time.Hour
		start(t, store, &fakePublisher{}, outbox.Config{PollInterval: poll, CleanupInterval: every, Retention: keep})

		time.Sleep(2*every + time.Second)
		synctest.Wait()
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.deletes) != 2 {
			t.Fatalf("%d cleanups in 2 intervals, want 2", len(store.deletes))
		}
		for _, before := range store.deletes {
			// The fake clock starts at a fixed instant and cleanups run at
			// whole intervals, so the cutoff is exact.
			if age := time.Since(before); age < keep || age > keep+every+time.Second {
				t.Errorf("cleanup cutoff %v ago, want about %v", age, keep)
			}
		}
	})
}

// TestRelayStopFinishesBatch checks that Stop lets a batch in flight finish,
// so messages the broker acknowledged are marked published.
func TestRelayStopFinishesBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		release := make(chan struct{})
		pub := &fakePublisher{publish: func(_ context.Context, msgs []outbox.Message) []error {
			<-release
			return make([]error, len(msgs))
		}}
		store.add("a")
		r := outbox.NewRelay(store, pub, outbox.Config{PollInterval: poll}, discard)
		r.Start(t.Context())
		synctest.Wait() // blocked in publish

		stopped := make(chan error, 1)
		go func() { stopped <- r.Stop(context.Background()) }()
		synctest.Wait()
		select {
		case err := <-stopped:
			t.Fatalf("Stop returned %v while a batch was in flight", err)
		default:
		}

		close(release)
		if err := <-stopped; err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if published, _ := store.state(); !slices.Equal(published, []string{"a"}) {
			t.Errorf("published %v, want [a]", published)
		}
		if n := len(pub.batchSizes()); n != 1 {
			t.Errorf("%d batches, want 1: no new batch after Stop", n)
		}
	})
}

func TestRelayStopDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		release := make(chan struct{})
		pub := &fakePublisher{publish: func(_ context.Context, msgs []outbox.Message) []error {
			<-release
			return make([]error, len(msgs))
		}}
		store.add("a")
		r := outbox.NewRelay(store, pub, outbox.Config{}, discard)
		r.Start(t.Context())
		synctest.Wait()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := r.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop past its deadline = %v, want context.DeadlineExceeded", err)
		}
		close(release) // let the abandoned batch end inside the bubble
		if err := r.Stop(context.Background()); err != nil {
			t.Fatalf("second Stop: %v", err)
		}
	})
}

func TestRelayStopNotStarted(t *testing.T) {
	r := outbox.NewRelay(newFakeStore(), &fakePublisher{}, outbox.Config{}, discard)
	if err := r.Stop(t.Context()); err != nil {
		t.Fatalf("Stop of a relay never started: %v", err)
	}
}

func TestRelayStopsWithContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		r := outbox.NewRelay(newFakeStore(), &fakePublisher{}, outbox.Config{}, discard)
		done := make(chan struct{})
		go func() {
			r.Run(ctx)
			close(done)
		}()
		synctest.Wait()
		cancel()
		<-done
	})
}
