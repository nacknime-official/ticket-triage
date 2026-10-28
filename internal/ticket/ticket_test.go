package ticket_test

import (
	"bytes"
	"log/slog"
	"math"
	"strings"
	"testing"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

func TestPriorityFromUrgency(t *testing.T) {
	tests := []struct {
		urgency float64
		want    ticket.Priority
	}{
		{-1, ticket.PriorityLow},
		{0, ticket.PriorityLow},
		{0.49, ticket.PriorityLow},
		{0.5, ticket.PriorityNormal},
		{1.49, ticket.PriorityNormal},
		{1.5, ticket.PriorityHigh},
		{2.49, ticket.PriorityHigh},
		{2.5, ticket.PriorityCritical},
		{3, ticket.PriorityCritical},
		{7, ticket.PriorityCritical},
		{math.NaN(), ticket.PriorityLow},
		{math.Inf(1), ticket.PriorityCritical},
	}
	for _, tt := range tests {
		if got := ticket.PriorityFromUrgency(tt.urgency); got != tt.want {
			t.Errorf("PriorityFromUrgency(%v) = %v, want %v", tt.urgency, got, tt.want)
		}
	}
}

func TestPriorityString(t *testing.T) {
	tests := map[ticket.Priority]string{
		ticket.PriorityLow:      "low",
		ticket.PriorityNormal:   "normal",
		ticket.PriorityHigh:     "high",
		ticket.PriorityCritical: "critical",
		ticket.Priority(9):      "Priority(9)",
	}
	for p, want := range tests {
		if got := p.String(); got != want {
			t.Errorf("Priority(%d).String() = %q, want %q", int(p), got, want)
		}
	}
}

func TestTicketMessage(t *testing.T) {
	tests := []struct {
		name string
		t    ticket.Ticket
		want string
	}{
		{
			name: "pending",
			t:    ticket.Ticket{Status: ticket.StatusPending},
			want: "Your request was received and is waiting to be classified.",
		},
		{
			name: "classified",
			t: ticket.Ticket{Status: ticket.StatusClassified, Classification: &ticket.Classification{
				Category: "billing", Urgency: 2.2,
			}},
			want: "Your request was handed to the billing team, priority high.",
		},
		{
			name: "classified multi-word category",
			t: ticket.Ticket{Status: ticket.StatusClassified, Classification: &ticket.Classification{
				Category: "feature_request", Urgency: 0.1,
			}},
			want: "Your request was handed to the feature request team, priority low.",
		},
		{
			name: "classified without classification",
			t:    ticket.Ticket{Status: ticket.StatusClassified},
			want: "Your request was handed to a team.",
		},
		{
			name: "needs_human",
			t:    ticket.Ticket{Status: ticket.StatusNeedsHuman, StatusReason: ticket.ReasonLowConfidence},
			want: "A person will contact you.",
		},
		{
			name: "unknown status",
			t:    ticket.Ticket{Status: "bogus"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.t.Message(); got != tt.want {
				t.Errorf("Message() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTicketLogValueOmitsSensitiveFields(t *testing.T) {
	tok, hash := ticket.NewToken()
	tk := ticket.Ticket{
		ID:              uuid.NewV7(),
		Subject:         "secret-subject",
		Body:            "secret-body",
		Status:          ticket.StatusNeedsHuman,
		StatusReason:    ticket.ReasonCategoryOther,
		AccessTokenHash: hash,
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "ticket", tk, "token", tok, "hash", hash)
	out := buf.String()

	for _, leak := range []string{"secret-subject", "secret-body", tok.Reveal()} {
		if strings.Contains(out, leak) {
			t.Errorf("log output leaks %q: %s", leak, out)
		}
	}
	for _, want := range []string{tk.ID.String(), `"status":"needs_human"`, `"status_reason":"category_other"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log output misses %q: %s", want, out)
		}
	}
}
