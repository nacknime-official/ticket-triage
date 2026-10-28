package ticket

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
)

const redacted = "[REDACTED]"

// Token is a plaintext bearer secret, such as an access token. Its value is
// reachable only through Reveal; fmt, slog and encoding/json all see a redacted
// placeholder, so it cannot be logged by accident.
type Token struct {
	value string
}

// NewToken issues a random token (crypto/rand.Text, at least 128 bits) and
// returns it with its hash. Store only the hash.
func NewToken() (Token, TokenHash) {
	t := Token{value: rand.Text()}
	return t, t.Hash()
}

// TokenFromString wraps a token presented by a client, e.g. from a Bearer header.
func TokenFromString(s string) Token {
	return Token{value: s}
}

// Reveal returns the plaintext. Use it only to hand the token to its owner.
func (t Token) Reveal() string { return t.value }

// Hash returns sha256 of the token.
func (t Token) Hash() TokenHash { return sha256.Sum256([]byte(t.value)) }

func (t Token) String() string   { return redacted }
func (t Token) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (t Token) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText keeps the plaintext out of encoding/json and other text encoders.
func (t Token) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// TokenHash is sha256 of a token, the only form that is stored.
type TokenHash [sha256.Size]byte

// Matches reports whether t hashes to h, in constant time.
func (h TokenHash) Matches(t Token) bool {
	sum := t.Hash()
	return subtle.ConstantTimeCompare(h[:], sum[:]) == 1
}

func (h TokenHash) String() string   { return redacted }
func (h TokenHash) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (h TokenHash) LogValue() slog.Value { return slog.StringValue(redacted) }

// dummyHash is compared against when no ticket exists, so a missing ticket
// costs the same as a wrong token. It is the hash of a random token nobody holds.
var dummyHash = func() TokenHash {
	_, h := NewToken()
	return h
}()
