package config

import (
	"fmt"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Problem is one invalid or missing variable.
type Problem struct {
	Name   string
	Reason string
}

// Error lists every invalid or missing variable a Loader found, in the order
// they were read. Reasons never contain secret values.
type Error struct {
	Problems []Problem
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("config: invalid environment: ")
	for i, p := range e.Problems {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(p.Name)
		b.WriteString(": ")
		b.WriteString(p.Reason)
	}
	return b.String()
}

// Loader reads configuration groups from the environment and collects every
// problem it finds. Call the group methods a binary needs, then Err.
//
//	l := config.NewLoader(os.Getenv)
//	logCfg, httpCfg := l.Log(), l.HTTP()
//	if err := l.Err(); err != nil { ... }
type Loader struct {
	getenv   func(string) string
	problems []Problem
}

// NewLoader returns a Loader that reads variables with getenv, usually
// os.Getenv. An empty or blank variable counts as unset and takes its default.
func NewLoader(getenv func(string) string) *Loader {
	return &Loader{getenv: getenv}
}

// Err returns an *Error naming every invalid or missing variable read so far,
// or nil.
func (l *Loader) Err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return &Error{Problems: append([]Problem(nil), l.problems...)}
}

func (l *Loader) fail(name, reason string) {
	l.problems = append(l.problems, Problem{Name: name, Reason: reason})
}

func (l *Loader) invalid(name, want, raw string) {
	l.fail(name, fmt.Sprintf("must be %s, got %q", want, raw))
}

func (l *Loader) failed(names ...string) bool {
	for _, p := range l.problems {
		for _, n := range names {
			if p.Name == n {
				return true
			}
		}
	}
	return false
}

func (l *Loader) raw(name, def string) string {
	if v := strings.TrimSpace(l.getenv(name)); v != "" {
		return v
	}
	return def
}

func (l *Loader) str(name, def string) string {
	v := l.raw(name, def)
	if v == "" {
		l.fail(name, "required")
	}
	return v
}

// secret never puts the value into a Problem.
func (l *Loader) secret(name string) Secret {
	return NewSecret(l.raw(name, ""))
}

func (l *Loader) oneOf(name, def string, allowed ...string) string {
	v := l.raw(name, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.invalid(name, "one of "+strings.Join(allowed, ", "), v)
	return ""
}

func (l *Loader) duration(name, def string) time.Duration {
	raw := l.raw(name, def)
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		l.invalid(name, "a positive duration such as 3s", raw)
		return 0
	}
	return d
}

func (l *Loader) int(name, def string, minimum int) int {
	raw := l.raw(name, def)
	n, err := strconv.Atoi(raw)
	if err != nil || n < minimum {
		l.invalid(name, fmt.Sprintf("an integer >= %d", minimum), raw)
		return 0
	}
	return n
}

// float accepts finite numbers for which ok returns true; want describes them.
func (l *Loader) float(name, def, want string, ok func(float64) bool) float64 {
	raw := l.raw(name, def)
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || !ok(f) {
		l.invalid(name, want, raw)
		return 0
	}
	return f
}

func positive(f float64) bool    { return f > 0 }
func nonNegative(f float64) bool { return f >= 0 }
func unit(f float64) bool        { return f >= 0 && f <= 1 }

func (l *Loader) bool(name, def string) bool {
	raw := l.raw(name, def)
	b, err := strconv.ParseBool(raw)
	if err != nil {
		l.invalid(name, "a boolean", raw)
		return false
	}
	return b
}

func (l *Loader) list(name, def string) []string {
	var out []string
	for item := range strings.SplitSeq(l.raw(name, def), ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// validAddr checks a host:port listen or dial address and returns its host.
func validAddr(addr string) (host string, ok bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return "", false
	}
	return host, true
}

func (l *Loader) addr(name, def string) string {
	raw := l.raw(name, def)
	if _, ok := validAddr(raw); !ok {
		l.invalid(name, "a host:port address", raw)
		return ""
	}
	return raw
}

// loopbackAddr accepts only addresses that bind to the loopback interface.
func (l *Loader) loopbackAddr(name, def string) string {
	raw := l.raw(name, def)
	host, ok := validAddr(raw)
	if ok && host != "localhost" {
		ip, err := netip.ParseAddr(host)
		ok = err == nil && ip.IsLoopback()
	}
	if !ok {
		l.invalid(name, "a loopback host:port address such as 127.0.0.1:6060", raw)
		return ""
	}
	return raw
}

// prefixes parses a comma list of CIDRs; a bare IP means that single host.
func (l *Loader) prefixes(name, def string) []netip.Prefix {
	var out []netip.Prefix
	for _, item := range l.list(name, def) {
		p, err := netip.ParsePrefix(item)
		if err != nil {
			ip, ipErr := netip.ParseAddr(item)
			if ipErr != nil {
				l.invalid(name, "a comma-separated list of CIDRs", item)
				continue
			}
			p = netip.PrefixFrom(ip, ip.BitLen())
		}
		out = append(out, p.Masked())
	}
	return out
}
