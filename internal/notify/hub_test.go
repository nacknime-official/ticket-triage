package notify_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/notify"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

func event(id uuid.UUID, status ticket.Status) ticket.StatusChanged {
	return ticket.StatusChanged{TicketID: id, Status: status, At: time.Now()}
}

// receive returns the next event of s, or fails if none is buffered.
func receive(t *testing.T, s *notify.Subscription) ticket.StatusChanged {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatal("subscription closed, want an event")
		}
		return e
	default:
		t.Fatal("no event buffered")
		return ticket.StatusChanged{}
	}
}

// closed reports whether the channel of s is closed with nothing buffered.
func closed(s *notify.Subscription) bool {
	select {
	case _, ok := <-s.Events():
		return !ok
	default:
		return false
	}
}

func TestHubDeliversByTicket(t *testing.T) {
	hub := notify.NewHub(0)
	a, b := uuid.NewV7(), uuid.NewV7()
	subA1, subA2, subB := hub.Subscribe(a), hub.Subscribe(a), hub.Subscribe(b)
	defer subA1.Close()
	defer subA2.Close()
	defer subB.Close()

	hub.Publish(event(a, ticket.StatusClassified))
	for _, s := range []*notify.Subscription{subA1, subA2} {
		if e := receive(t, s); e.TicketID != a || e.Status != ticket.StatusClassified {
			t.Errorf("got %+v, want the classified event of %s", e, a)
		}
	}
	select {
	case e := <-subB.Events():
		t.Errorf("subscriber of another ticket got %+v", e)
	default:
	}
	hub.Publish(event(uuid.NewV7(), ticket.StatusNeedsHuman)) // nobody listens: a no-op
	if n := hub.Len(); n != 3 {
		t.Errorf("Len = %d, want 3", n)
	}
}

func TestHubDropsSlowSubscriber(t *testing.T) {
	const buffer = 2
	hub := notify.NewHub(buffer)
	id := uuid.NewV7()
	slow, fast := hub.Subscribe(id), hub.Subscribe(id)
	defer slow.Close()
	defer fast.Close()

	for i := range buffer + 1 {
		hub.Publish(event(id, ticket.StatusClassified))
		// fast keeps up; slow never reads.
		receive(t, fast)
		if i < buffer && slow.Dropped() {
			t.Fatalf("dropped after %d events, buffer is %d", i+1, buffer)
		}
	}
	if !slow.Dropped() {
		t.Fatal("slow subscriber not dropped after overflowing its buffer")
	}
	// What was buffered is still delivered, then the channel is closed.
	for range buffer {
		receive(t, slow)
	}
	if !closed(slow) {
		t.Error("dropped subscription's channel is not closed")
	}
	if fast.Dropped() || closed(fast) {
		t.Error("the subscriber that kept up was dropped")
	}
	if n := hub.Len(); n != 1 {
		t.Errorf("Len = %d, want 1", n)
	}
	slow.Close() // after a drop: a no-op, no double close
}

func TestSubscriptionClose(t *testing.T) {
	hub := notify.NewHub(1)
	id := uuid.NewV7()
	s := hub.Subscribe(id)
	s.Close()
	s.Close()
	if !closed(s) {
		t.Error("channel not closed after Close")
	}
	if s.Dropped() {
		t.Error("Close reported as a drop")
	}
	hub.Publish(event(id, ticket.StatusClassified)) // must not send on the closed channel
	if n := hub.Len(); n != 0 {
		t.Errorf("Len = %d after Close, want 0", n)
	}
}

func TestHubClose(t *testing.T) {
	hub := notify.NewHub(1)
	id := uuid.NewV7()
	before := hub.Subscribe(id)
	hub.Close()
	hub.Close()
	after := hub.Subscribe(id)
	hub.Publish(event(id, ticket.StatusClassified))
	for name, s := range map[string]*notify.Subscription{"before": before, "after": after} {
		if !closed(s) {
			t.Errorf("subscription %s is open", name)
		}
		s.Close()
	}
	if n := hub.Len(); n != 0 {
		t.Errorf("Len = %d after Close, want 0", n)
	}
}

// TestHubReadersExit checks that every goroutine blocked on a subscription
// returns once the subscription is closed, dropped or the Hub closes:
// synctest.Test fails on a goroutine left blocked in the bubble.
func TestHubReadersExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := notify.NewHub(1)
		id := uuid.NewV7()
		read := func(s *notify.Subscription) *atomic.Int32 {
			n := new(atomic.Int32)
			go func() {
				for range s.Events() {
					n.Add(1)
				}
			}()
			return n
		}
		unsubscribed, dropped, shutdown := hub.Subscribe(id), hub.Subscribe(id), hub.Subscribe(id)
		nUnsub, nShut := read(unsubscribed), read(shutdown)

		hub.Publish(event(id, ticket.StatusClassified))
		synctest.Wait()
		if nUnsub.Load() != 1 || nShut.Load() != 1 {
			t.Errorf("readers got %d and %d events, want 1 each", nUnsub.Load(), nShut.Load())
		}
		// dropped has no reader: its single slot is full, so the next event drops it.
		hub.Publish(event(id, ticket.StatusClassified))
		synctest.Wait()
		if !dropped.Dropped() {
			t.Error("subscription with a full buffer not dropped")
		}

		unsubscribed.Close()
		hub.Close()
		synctest.Wait()
	})
}

func TestHubConcurrentUse(t *testing.T) {
	hub := notify.NewHub(1)
	ids := []uuid.UUID{uuid.NewV7(), uuid.NewV7()}
	var wg sync.WaitGroup
	for i := range 8 {
		id := ids[i%len(ids)]
		wg.Go(func() {
			for range 100 {
				s := hub.Subscribe(id)
				hub.Publish(event(id, ticket.StatusClassified))
				_ = s.Dropped()
				s.Close()
			}
		})
	}
	wg.Wait()
	hub.Close()
	if n := hub.Len(); n != 0 {
		t.Errorf("Len = %d, want 0", n)
	}
}
