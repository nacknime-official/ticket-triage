package ticket_test

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

func TestSubmittedRoundTrip(t *testing.T) {
	tk := ticket.Ticket{
		ID:        uuid.MustParse("0192f6c1-0000-7000-8000-000000000001"),
		Subject:   "Refund",
		Body:      "Charged twice, please refund.",
		CreatedAt: time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC),
	}
	ev := ticket.SubmittedOf(tk)
	b, err := ev.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	const want = `{"ticket_id":"0192f6c1-0000-7000-8000-000000000001","submitted_at":"2026-10-28T12:00:00.123456Z"}`
	if string(b) != want {
		t.Errorf("payload = %s, want %s", b, want)
	}
	if strings.Contains(string(b), tk.Subject) || strings.Contains(string(b), tk.Body) {
		t.Errorf("payload %s carries ticket text", b)
	}
	got, err := ticket.UnmarshalSubmitted(b)
	if err != nil {
		t.Fatalf("UnmarshalSubmitted: %v", err)
	}
	if got != ev {
		t.Errorf("UnmarshalSubmitted = %+v, want %+v", got, ev)
	}
}

func TestUnmarshalSubmittedRejects(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"empty", ``},
		{"not json", `ticket`},
		{"no ticket id", `{"submitted_at":"2026-10-28T12:00:00Z"}`},
		{"nil ticket id", `{"ticket_id":"00000000-0000-0000-0000-000000000000"}`},
		{"malformed ticket id", `{"ticket_id":"42"}`},
		{"unknown field", `{"ticket_id":"0192f6c1-0000-7000-8000-000000000001","body":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ev, err := ticket.UnmarshalSubmitted([]byte(tt.payload)); err == nil {
				t.Fatalf("UnmarshalSubmitted(%q) = %+v, want an error", tt.payload, ev)
			}
		})
	}
}
