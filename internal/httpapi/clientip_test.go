package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPResolve(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32")}
	tests := []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		xff     []string
		want    string // "" means the zero Addr
	}{
		{"remote addr", trusted, "203.0.113.7:4000", nil, "203.0.113.7"},
		{"xff from untrusted peer is ignored", trusted, "203.0.113.7:4000", []string{"198.51.100.1"}, "203.0.113.7"},
		{"xff ignored without trusted proxies", nil, "10.0.0.1:4000", []string{"198.51.100.1"}, "10.0.0.1"},
		{"xff from trusted proxy", trusted, "10.0.0.1:4000", []string{"198.51.100.1"}, "198.51.100.1"},
		{"spoofed left entries are skipped", trusted, "10.0.0.1:4000", []string{"1.1.1.1, 198.51.100.1"}, "198.51.100.1"},
		{
			"chain of trusted proxies",
			trusted, "10.0.0.1:4000", []string{"1.1.1.1, 198.51.100.1, 10.2.3.4", "10.9.9.9"}, "198.51.100.1",
		},
		{"all hops trusted", trusted, "10.0.0.1:4000", []string{"10.0.0.3, 10.0.0.2"}, "10.0.0.3"},
		{"garbage hop stops the walk", trusted, "10.0.0.1:4000", []string{"198.51.100.1, nonsense, 10.0.0.2"}, "10.0.0.2"},
		{"empty xff", trusted, "10.0.0.1:4000", []string{""}, "10.0.0.1"},
		{"no xff from trusted proxy", trusted, "10.0.0.1:4000", nil, "10.0.0.1"},
		{"hop with port", trusted, "10.0.0.1:4000", []string{"198.51.100.1:5555"}, "198.51.100.1"},
		{"ipv6 hop", trusted, "[2001:db8::1]:4000", []string{"[2001:db9::5]:80"}, "2001:db9::5"},
		{"ipv4-mapped remote is unmapped", trusted, "[::ffff:10.0.0.1]:4000", []string{"::ffff:198.51.100.1"}, "198.51.100.1"},
		{"zone is dropped", nil, "[fe80::1%eth0]:4000", nil, "fe80::1"},
		{"unparsable remote addr", trusted, "pipe", []string{"198.51.100.1"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			var want netip.Addr
			if tt.want != "" {
				want = netip.MustParseAddr(tt.want)
			}
			if got := NewClientIP(tt.trusted).Resolve(r); got != want {
				t.Errorf("Resolve = %v, want %v", got, want)
			}
		})
	}
}

func TestClientIPMiddleware(t *testing.T) {
	c := NewClientIP([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	var got netip.Addr
	h := c.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = ClientIPFrom(r) }))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:4000"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if want := netip.MustParseAddr("198.51.100.1"); got != want {
		t.Errorf("ClientIPFrom after middleware = %v, want %v", got, want)
	}

	// Without the middleware only RemoteAddr counts.
	if got, want := ClientIPFrom(r), netip.MustParseAddr("10.0.0.1"); got != want {
		t.Errorf("ClientIPFrom without middleware = %v, want %v", got, want)
	}
}
