package httpapi

import (
	"context"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// ClientIP resolves the client address of a request (README §4.1). The
// address is RemoteAddr, unless RemoteAddr is a trusted proxy: then
// X-Forwarded-For is walked from the right, past every trusted proxy, and
// the first untrusted hop is the client. Entries left of that hop are written
// by the client and never trusted.
type ClientIP struct {
	trusted []netip.Prefix
}

// NewClientIP returns a resolver that trusts X-Forwarded-For only from
// addresses inside trusted. With no prefixes it always uses RemoteAddr.
func NewClientIP(trusted []netip.Prefix) *ClientIP {
	return &ClientIP{trusted: slices.Clone(trusted)}
}

// Resolve returns the client address of r, or the zero Addr if RemoteAddr
// is not an IP address.
func (c *ClientIP) Resolve(r *http.Request) netip.Addr {
	addr, ok := parseHop(r.RemoteAddr)
	if !ok || !c.isTrusted(addr) {
		return addr
	}
	hops := forwardedFor(r.Header)
	for i := len(hops) - 1; i >= 0; i-- {
		hop, ok := parseHop(hops[i])
		if !ok {
			// A trusted proxy forwarded garbage; stop at the last good hop.
			return addr
		}
		addr = hop
		if !c.isTrusted(hop) {
			return hop
		}
	}
	// Every hop is a trusted proxy; the leftmost is the best we know.
	return addr
}

// Middleware stores the resolved client address in the request context,
// where ClientIPFrom reads it.
func (c *ClientIP) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clientIPKey{}, c.Resolve(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type clientIPKey struct{}

// ClientIPFrom returns the client address that ClientIP.Middleware stored.
// Without that middleware it falls back to RemoteAddr, never to
// X-Forwarded-For.
func ClientIPFrom(r *http.Request) netip.Addr {
	if addr, ok := r.Context().Value(clientIPKey{}).(netip.Addr); ok {
		return addr
	}
	addr, _ := parseHop(r.RemoteAddr)
	return addr
}

func (c *ClientIP) isTrusted(addr netip.Addr) bool {
	return slices.ContainsFunc(c.trusted, func(p netip.Prefix) bool { return p.Contains(addr) })
}

// forwardedFor returns the X-Forwarded-For hops in order, across repeated
// header lines.
func forwardedFor(h http.Header) []string {
	var hops []string
	for _, line := range h.Values("X-Forwarded-For") {
		for hop := range strings.SplitSeq(line, ",") {
			hops = append(hops, strings.TrimSpace(hop))
		}
	}
	return hops
}

// parseHop parses "ip", "ip:port" or "[ipv6]:port", unmaps IPv4-in-IPv6 and
// drops any zone, so one client always maps to one address.
func parseHop(s string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			return netip.Addr{}, false
		}
		addr = ap.Addr()
	}
	return addr.Unmap().WithZone(""), true
}
