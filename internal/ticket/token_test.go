package ticket_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

func TestNewTokenRoundTrip(t *testing.T) {
	tok, hash := ticket.NewToken()
	plain := tok.Reveal()
	// crypto/rand.Text: 26 base32 characters, 130 bits.
	if len(plain) < 26 {
		t.Fatalf("token length = %d, want >= 26", len(plain))
	}
	if hash != ticket.TokenHash(sha256.Sum256([]byte(plain))) {
		t.Error("hash is not sha256 of the plaintext")
	}
	if !hash.Matches(tok) {
		t.Error("hash does not match its own token")
	}
	if !hash.Matches(ticket.TokenFromString(plain)) {
		t.Error("hash does not match the token presented as a string")
	}
}

func TestTokenHashMismatch(t *testing.T) {
	tok, hash := ticket.NewToken()
	other, _ := ticket.NewToken()
	plain := tok.Reveal()
	for name, presented := range map[string]ticket.Token{
		"other token":     other,
		"empty":           ticket.TokenFromString(""),
		"prefix":          ticket.TokenFromString(plain[:len(plain)-1]),
		"extra character": ticket.TokenFromString(plain + "A"),
		"lowercase":       ticket.TokenFromString(strings.ToLower(plain)),
	} {
		if hash.Matches(presented) {
			t.Errorf("%s: Matches() = true, want false", name)
		}
	}
	var zero ticket.TokenHash
	if zero.Matches(tok) {
		t.Error("zero hash matches a token")
	}
}

func TestNewTokenUnique(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		tok, _ := ticket.NewToken()
		if seen[tok.Reveal()] {
			t.Fatal("duplicate token")
		}
		seen[tok.Reveal()] = true
	}
}

func TestTokenRedacted(t *testing.T) {
	tok, hash := ticket.NewToken()
	plain := tok.Reveal()
	js, err := json.Marshal(struct {
		Token ticket.Token
		Hash  ticket.TokenHash
	}{tok, hash})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	outputs := map[string]string{
		"%v":   fmt.Sprintf("%v", tok),
		"%s":   fmt.Sprintf("token=%s", tok),
		"%+v":  fmt.Sprintf("%+v", tok),
		"%#v":  fmt.Sprintf("%#v", tok),
		"%q":   fmt.Sprintf("%q", tok),
		"hash": fmt.Sprintf("%v %#v", hash, hash),
		"json": string(js),
	}
	for name, out := range outputs {
		if strings.Contains(out, plain) {
			t.Errorf("%s leaks the token: %q", name, out)
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s = %q, want [REDACTED]", name, out)
		}
	}
	if got := tok.LogValue().String(); got != "[REDACTED]" {
		t.Errorf("Token.LogValue() = %q", got)
	}
	if got := hash.LogValue().String(); got != "[REDACTED]" {
		t.Errorf("TokenHash.LogValue() = %q", got)
	}
}
