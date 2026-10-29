package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestStreamLimiter(t *testing.T) {
	type step struct {
		ip      string
		release bool // release the oldest held slot of ip instead of acquiring
		want    int  // status of an acquire; 0 means granted
	}
	tests := []struct {
		name  string
		limit StreamLimit
		steps []step
	}{
		{"per client", StreamLimit{PerClient: 2, Global: 10}, []step{
			{ip: "198.51.100.1"}, {ip: "198.51.100.1"},
			{ip: "198.51.100.1", want: http.StatusTooManyRequests},
			{ip: "198.51.100.2"},
			{ip: "198.51.100.1", release: true},
			{ip: "198.51.100.1"},
		}},
		{"global", StreamLimit{PerClient: 2, Global: 3}, []step{
			{ip: "198.51.100.1"}, {ip: "198.51.100.2"}, {ip: "198.51.100.3"},
			{ip: "198.51.100.4", want: http.StatusServiceUnavailable},
			{ip: "198.51.100.2", release: true},
			{ip: "198.51.100.4"},
		}},
		{"per client wins over global", StreamLimit{PerClient: 1, Global: 1}, []step{
			{ip: "198.51.100.1"},
			{ip: "198.51.100.1", want: http.StatusTooManyRequests},
			{ip: "198.51.100.2", want: http.StatusServiceUnavailable},
		}},
		{"ipv6 /64 is one client", StreamLimit{PerClient: 1, Global: 10}, []step{
			{ip: "2001:db8::1"},
			{ip: "2001:db8::2", want: http.StatusTooManyRequests},
			{ip: "2001:db8:0:1::1"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewStreamLimiter(tt.limit)
			held := 0
			for i, s := range tt.steps {
				key := clientKey(netip.MustParseAddr(s.ip))
				if s.release {
					l.release(key)
					held--
					continue
				}
				if got := l.acquire(key); got != s.want {
					t.Fatalf("step %d (%s): acquire = %d, want %d", i, s.ip, got, s.want)
				}
				if s.want == 0 {
					held++
				}
			}
			if l.open() != held {
				t.Errorf("open = %d, want %d", l.open(), held)
			}
		})
	}
}

func TestStreamLimiterMiddleware(t *testing.T) {
	l := NewStreamLimiter(StreamLimit{PerClient: 1, Global: 1})
	var during int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		during = l.open()
		// A second stream of the same client while this one is open.
		inner := httptest.NewRecorder()
		l.Middleware(http.NotFoundHandler()).ServeHTTP(inner, r)
		if inner.Code != http.StatusTooManyRequests ||
			!strings.Contains(inner.Body.String(), `"code":"too_many_streams"`) {
			t.Errorf("second stream: %d %s, want 429 too_many_streams", inner.Code, inner.Body)
		}
		other := r.Clone(r.Context())
		other.RemoteAddr = "198.51.100.9:1234"
		inner = httptest.NewRecorder()
		l.Middleware(http.NotFoundHandler()).ServeHTTP(inner, other)
		if inner.Code != http.StatusServiceUnavailable ||
			!strings.Contains(inner.Body.String(), `"code":"streams_full"`) {
			t.Errorf("stream of another client: %d %s, want 503 streams_full", inner.Code, inner.Body)
		}
		w.WriteHeader(http.StatusOK)
	})
	rec := serve(l.Middleware(next), newRequest(http.MethodGet, "/tickets/x/events", nil))
	if rec.Code != http.StatusOK || during != 1 {
		t.Fatalf("first stream: status %d with %d open, want 200 with 1", rec.Code, during)
	}
	if l.open() != 0 {
		t.Errorf("%d slots held after the stream ended", l.open())
	}
}
