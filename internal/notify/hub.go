package notify

import (
	"sync"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// DefaultBuffer is the per-subscription buffer when NewHub gets buffer <= 0.
// A ticket changes status once, so a few slots absorb duplicates from the
// at-least-once events topic.
const DefaultBuffer = 4

// Hub fans status events out to subscribers by ticket id. Publish never
// blocks: a subscriber whose buffer is full is dropped instead of slowing the
// events reader and every other subscriber down. A dropped subscriber sees
// its channel closed and, for SSE, reconnects and rereads the current state.
//
// The Hub starts no goroutines. It is safe for concurrent use.
type Hub struct {
	buffer int

	mu     sync.Mutex
	subs   map[uuid.UUID]map[*Subscription]struct{}
	closed bool
}

// NewHub returns a Hub whose subscriptions buffer up to buffer events.
func NewHub(buffer int) *Hub {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	return &Hub{buffer: buffer, subs: make(map[uuid.UUID]map[*Subscription]struct{})}
}

// Subscription receives the events of one ticket. Every channel close
// happens under the Hub's lock, once, when the subscription leaves the Hub.
type Subscription struct {
	hub *Hub
	id  uuid.UUID
	ch  chan ticket.StatusChanged
	// dropped is guarded by hub.mu.
	dropped bool
}

// Subscribe returns a subscription to the events of the ticket id. The
// caller must Close it. On a closed Hub the subscription starts closed.
func (h *Hub) Subscribe(id uuid.UUID) *Subscription {
	s := &Subscription{hub: h, id: id, ch: make(chan ticket.StatusChanged, h.buffer)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(s.ch)
		return s
	}
	subs := h.subs[id]
	if subs == nil {
		subs = make(map[*Subscription]struct{})
		h.subs[id] = subs
	}
	subs[s] = struct{}{}
	return s
}

// Publish delivers e to every subscription of e.TicketID without blocking,
// and drops each subscription whose buffer is full. It is a no-op on a closed
// Hub.
func (h *Hub) Publish(e ticket.StatusChanged) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[e.TicketID] {
		select {
		case s.ch <- e:
		default:
			s.dropped = true
			h.remove(s)
		}
	}
}

// Close closes every subscription, so their readers stop, and makes later
// subscriptions start closed. It is idempotent.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, subs := range h.subs {
		for s := range subs {
			h.remove(s)
		}
	}
}

// Len returns the number of open subscriptions.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, subs := range h.subs {
		n += len(subs)
	}
	return n
}

// remove takes s out of the Hub and closes its channel. h.mu must be held,
// and s must still be in the Hub.
func (h *Hub) remove(s *Subscription) {
	subs := h.subs[s.id]
	delete(subs, s)
	if len(subs) == 0 {
		delete(h.subs, s.id)
	}
	close(s.ch)
}

// Events returns the channel of the subscription's events. It is closed when
// the subscription is closed, dropped for falling behind, or the Hub closes.
func (s *Subscription) Events() <-chan ticket.StatusChanged { return s.ch }

// Dropped reports whether the Hub dropped the subscription because its
// buffer was full.
func (s *Subscription) Dropped() bool {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.dropped
}

// Close removes the subscription from the Hub. It is idempotent, and safe
// after the Hub dropped the subscription or closed.
func (s *Subscription) Close() {
	h := s.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s.id][s]; ok {
		h.remove(s)
	}
}
