package config

import (
	"fmt"
	"log/slog"
)

// redacted replaces a secret value wherever a Secret is printed.
const redacted = "[REDACTED]"

// Secret holds a sensitive value. Printing it with any fmt verb, logging it
// with slog, or marshaling it to JSON or text yields "[REDACTED]" (or "" when
// unset); only Reveal returns the value.
type Secret struct {
	value string
}

// NewSecret wraps value as a Secret.
func NewSecret(value string) Secret {
	return Secret{value: value}
}

// Reveal returns the secret value. Call it only where the value is used, such
// as building an Authorization header or a connection pool.
func (s Secret) Reveal() string {
	return s.value
}

// IsZero reports whether the secret is unset.
func (s Secret) IsZero() bool {
	return s.value == ""
}

func (s Secret) mask() string {
	if s.IsZero() {
		return ""
	}
	return redacted
}

// String implements fmt.Stringer.
func (s Secret) String() string {
	return s.mask()
}

// GoString implements fmt.GoStringer.
func (s Secret) GoString() string {
	return fmt.Sprintf("config.Secret(%q)", s.mask())
}

// Format implements fmt.Formatter, so no verb (%v, %+v, %#v, %s, %q, %x)
// reaches the value.
func (s Secret) Format(f fmt.State, verb rune) {
	switch verb {
	case 'v':
		if f.Flag('#') {
			_, _ = fmt.Fprint(f, s.GoString())
			return
		}
		_, _ = fmt.Fprint(f, s.mask())
	case 'q':
		_, _ = fmt.Fprintf(f, "%q", s.mask())
	default:
		_, _ = fmt.Fprint(f, s.mask())
	}
}

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value {
	return slog.StringValue(s.mask())
}

// MarshalText implements encoding.TextMarshaler, which JSON encoders use too.
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(s.mask()), nil
}
