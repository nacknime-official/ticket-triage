package httpapi

import (
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// DefaultMaxClients bounds the clients a RateLimiter tracks at once when
// RateLimit.MaxClients is zero.
const DefaultMaxClients = 100_000

// RateLimit configures a per-client RateLimiter.
type RateLimit struct {
	PerMinute  float64 // sustained requests per minute per client, > 0
	Burst      int     // requests a client may make at once, >= 1
	MaxClients int     // clients tracked at once; 0 means DefaultMaxClients
}

// RateLimiter is an in-memory token bucket per client (PRD §4.1). A
// client is one IPv4 address or one IPv6 /64, since a single IPv6 host
// usually controls a whole /64.
//
// Memory is bounded two ways. A client idle long enough for its bucket to
// refill completely is indistinguishable from a new one, so it is evicted
// without changing any decision. And at most MaxClients are tracked: while
// the table is full, requests from untracked clients are refused until the
// next eviction pass frees room.
type RateLimiter struct {
	limit      rate.Limit
	burst      int
	idle       time.Duration // time for an empty bucket to refill
	maxClients int

	mu        sync.Mutex
	clients   map[netip.Prefix]*clientBucket
	nextSweep time.Time
}

type clientBucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter returns a RateLimiter for c.
func NewRateLimiter(c RateLimit) *RateLimiter {
	perSecond := c.PerMinute / 60
	maxClients := c.MaxClients
	if maxClients <= 0 {
		maxClients = DefaultMaxClients
	}
	return &RateLimiter{
		limit:      rate.Limit(perSecond),
		burst:      c.Burst,
		idle:       time.Duration(math.Ceil(float64(c.Burst) / perSecond * float64(time.Second))),
		maxClients: maxClients,
		clients:    make(map[netip.Prefix]*clientBucket),
	}
}

// Allow takes one token from the bucket of the client at addr. When the
// bucket is empty it reports how long until the next token.
func (l *RateLimiter) Allow(addr netip.Addr) (ok bool, retryAfter time.Duration) {
	now := time.Now()
	key := clientKey(addr)

	l.mu.Lock()
	defer l.mu.Unlock()
	if !now.Before(l.nextSweep) {
		l.sweep(now)
	}
	b, found := l.clients[key]
	if !found {
		if len(l.clients) >= l.maxClients {
			return false, l.nextSweep.Sub(now)
		}
		b = &clientBucket{lim: rate.NewLimiter(l.limit, l.burst)}
		l.clients[key] = b
	}
	b.lastSeen = now
	r := b.lim.ReserveN(now, 1)
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return false, d
	}
	return true, 0
}

// sweep evicts clients whose buckets are full again. It runs at most once
// per idle period, so its cost is amortized over the requests in between.
func (l *RateLimiter) sweep(now time.Time) {
	for key, b := range l.clients {
		if now.Sub(b.lastSeen) >= l.idle {
			delete(l.clients, key)
		}
	}
	l.nextSweep = now.Add(l.idle)
}

func (l *RateLimiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}

// Middleware answers 429 with Retry-After when the client resolved by
// ClientIPFrom has no token left, and otherwise calls next. It limits every
// request it wraps; ForRoute scopes it to one route.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := l.Allow(ClientIPFrom(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(wait)))
			writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many requests, retry later")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientKey maps an address to its rate-limit key: the /32 of an IPv4
// address, the /64 of an IPv6 one, and the zero Prefix for an unknown
// address, so unresolvable clients share one bucket.
func clientKey(addr netip.Addr) netip.Prefix {
	if !addr.IsValid() {
		return netip.Prefix{}
	}
	bits := 32
	if addr.Is6() {
		bits = 64
	}
	p, _ := addr.Prefix(bits) // cannot fail: bits fits the address family
	return p
}

// retryAfterSeconds rounds d up to whole seconds, at least 1, for the
// Retry-After header.
func retryAfterSeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}
