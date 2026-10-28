package httpapi

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"testing/synctest"
	"time"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })

func submit(t *testing.T, h http.Handler, remote string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/tickets", nil)
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRateLimiterMiddleware(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 5/min, burst 3: one token every 12s.
		h := NewRateLimiter(RateLimit{PerMinute: 5, Burst: 3}).Middleware(okHandler)

		for i := range 3 {
			if w := submit(t, h, "203.0.113.7:4000"); w.Code != http.StatusAccepted {
				t.Fatalf("request %d: status %d, want 202", i, w.Code)
			}
		}
		w := submit(t, h, "203.0.113.7:4001")
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429", w.Code)
		}
		if got := w.Header().Get("Retry-After"); got != "12" {
			t.Errorf("Retry-After = %q, want 12", got)
		}
		if got := w.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		var body errorBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body %q: %v", w.Body, err)
		}
		if body.Error.Code != "rate_limited" || body.Error.Message == "" {
			t.Errorf("body = %+v, want code rate_limited and a message", body)
		}

		// Another client has its own bucket.
		if w := submit(t, h, "198.51.100.1:4000"); w.Code != http.StatusAccepted {
			t.Errorf("other client: status %d, want 202", w.Code)
		}

		time.Sleep(5 * time.Second)
		if got := submit(t, h, "203.0.113.7:4000").Header().Get("Retry-After"); got != "7" {
			t.Errorf("Retry-After after 5s = %q, want 7", got)
		}
	})
}

func TestRateLimiterRefill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewRateLimiter(RateLimit{PerMinute: 60, Burst: 2}) // one token per second
		ip := netip.MustParseAddr("203.0.113.7")
		allow := func() bool { ok, _ := l.Allow(ip); return ok }
		// allowed makes n requests and counts the allowed ones.
		allowed := func(n int) int {
			c := 0
			for range n {
				if allow() {
					c++
				}
			}
			return c
		}

		if got := allowed(2); got != 2 {
			t.Fatalf("burst of 2: %d allowed", got)
		}
		if ok, wait := l.Allow(ip); ok || wait != time.Second {
			t.Fatalf("Allow on empty bucket = %v, %v; want false, 1s", ok, wait)
		}
		time.Sleep(999 * time.Millisecond)
		if allow() {
			t.Fatal("allowed before the token refilled")
		}
		time.Sleep(time.Millisecond)
		if !allow() {
			t.Fatal("refused after one token refilled")
		}
		if allow() {
			t.Fatal("allowed a second request on one refilled token")
		}
		// Refused requests take no tokens: a long wait refills to the burst only.
		time.Sleep(time.Hour)
		if got := allowed(3); got != 2 {
			t.Fatalf("after a long wait %d of 3 allowed, want the burst of 2", got)
		}
	})
}

func TestRateLimiterEviction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewRateLimiter(RateLimit{PerMinute: 60, Burst: 3}) // refills fully in 3s
		for i := range 100 {
			l.Allow(netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}))
		}
		if got := l.len(); got != 100 {
			t.Fatalf("tracked %d clients, want 100", got)
		}

		busy := netip.MustParseAddr("203.0.113.7")
		time.Sleep(2 * time.Second)
		for range 3 {
			l.Allow(busy) // drain the bucket
		}
		time.Sleep(time.Second)
		// The next request runs the sweep: the 100 idle clients are full again
		// and go; the busy one was seen 1s ago and stays.
		if ok, _ := l.Allow(busy); !ok {
			t.Fatal("busy client refused its refilled token")
		}
		if got := l.len(); got != 1 {
			t.Fatalf("after idle period tracked %d clients, want 1", got)
		}

		// Eviction never changes a decision: the busy client keeps its empty
		// bucket instead of getting a fresh one.
		if ok, _ := l.Allow(busy); ok {
			t.Error("busy client got a fresh bucket after the sweep")
		}
	})
}

func TestRateLimiterMaxClients(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewRateLimiter(RateLimit{PerMinute: 60, Burst: 1, MaxClients: 2}) // idle period 1s
		a, b, c := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("192.0.2.3")
		l.Allow(a)
		l.Allow(b)
		if ok, wait := l.Allow(c); ok || wait != time.Second {
			t.Fatalf("new client at capacity = %v, %v; want false, 1s", ok, wait)
		}
		time.Sleep(time.Second)
		if ok, _ := l.Allow(c); !ok {
			t.Fatal("new client refused after the sweep freed room")
		}
	})
}

func TestClientKey(t *testing.T) {
	tests := []struct {
		a, b string
		same bool
	}{
		{"192.0.2.1", "192.0.2.2", false},
		{"2001:db8:1:2::1", "2001:db8:1:2:ffff::9", true},
		{"2001:db8:1:2::1", "2001:db8:1:3::1", false},
		{"192.0.2.1", "::ffff:192.0.2.1", false}, // ClientIP unmaps before keys are built
	}
	for _, tt := range tests {
		ka, kb := clientKey(netip.MustParseAddr(tt.a)), clientKey(netip.MustParseAddr(tt.b))
		if (ka == kb) != tt.same {
			t.Errorf("clientKey(%s) = %v, clientKey(%s) = %v; same = %v, want %v", tt.a, ka, tt.b, kb, ka == kb, tt.same)
		}
	}
	if clientKey(netip.Addr{}) != (netip.Prefix{}) {
		t.Error("unknown address does not map to the shared zero key")
	}
}
