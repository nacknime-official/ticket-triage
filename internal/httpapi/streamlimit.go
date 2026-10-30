package httpapi

import (
	"net/http"
	"net/netip"
	"sync"
)

// StreamLimit configures a StreamLimiter.
type StreamLimit struct {
	PerClient int // open streams per client, >= 1
	Global    int // open streams per instance, >= PerClient
}

// StreamLimiter caps the open SSE streams per client and per instance
// (PRD §4.1). A client is keyed like in RateLimiter: one IPv4 address or
// one IPv6 /64. Only clients with an open stream are tracked, so memory is
// bounded by Global.
type StreamLimiter struct {
	perClient, global int

	mu      sync.Mutex
	clients map[netip.Prefix]int
	total   int
}

// NewStreamLimiter returns a StreamLimiter for c.
func NewStreamLimiter(c StreamLimit) *StreamLimiter {
	return &StreamLimiter{perClient: c.PerClient, global: c.Global, clients: make(map[netip.Prefix]int)}
}

// acquire takes a stream slot for the client key. It returns 0, or the
// status to refuse with: 429 when the client has PerClient streams open, 503
// when the instance has Global.
func (l *StreamLimiter) acquire(key netip.Prefix) (refuse int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clients[key] >= l.perClient {
		return http.StatusTooManyRequests
	}
	if l.total >= l.global {
		return http.StatusServiceUnavailable
	}
	l.clients[key]++
	l.total++
	return 0
}

// release frees a slot that acquire took for key.
func (l *StreamLimiter) release(key netip.Prefix) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.clients[key]--; l.clients[key] <= 0 {
		delete(l.clients, key)
	}
}

// open returns the number of streams open, for tests.
func (l *StreamLimiter) open() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// Middleware holds a slot of the client resolved by ClientIPFrom while next
// serves, and refuses the request when none is free. It runs before the
// handler consumes the stream token, so a refused client keeps its token.
// Scope it to the SSE route with ForRoute.
func (l *StreamLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientKey(ClientIPFrom(r))
		switch refuse := l.acquire(key); refuse {
		case 0:
		case http.StatusTooManyRequests:
			writeError(w, refuse, codeTooManyStreams, "too many open streams from this client")
			return
		default:
			writeError(w, refuse, codeStreamsFull, "too many open streams, retry later")
			return
		}
		defer l.release(key)
		next.ServeHTTP(w, r)
	})
}
