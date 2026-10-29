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

func TestStatusChangedPayload(t *testing.T) {
	id := uuid.MustParse("0192f6c1-0000-7000-8000-000000000001")
	at := time.Date(2026, 10, 28, 12, 0, 5, 0, time.UTC)
	base := ticket.Ticket{
		ID:        id,
		Subject:   "Refund now",
		Body:      "Charged twice, unacceptable!!!",
		Attempts:  2,
		LastError: "classifier: transient error: 503",
		CreatedAt: at.Add(-5 * time.Second),
		UpdatedAt: at,
	}
	tests := []struct {
		name   string
		status ticket.Status
		reason ticket.Reason
		class  *ticket.Classification
		want   string
	}{
		{
			name:   "classified",
			status: ticket.StatusClassified,
			class: &ticket.Classification{
				Category: "feature_request", CategoryConfidence: 0.91, ComplaintP: 0.83, IsComplaint: true,
				ComplaintUncertain: true, Urgency: 2.2, UrgencyConfidence: 0.7,
			},
			want: `{"ticket_id":"0192f6c1-0000-7000-8000-000000000001","status":"classified","status_reason":null,` +
				`"category":"feature_request","priority":"high","is_complaint":true,` +
				`"message":"Your request was handed to the feature request team, priority high.",` +
				`"at":"2026-10-28T12:00:05Z"}`,
		},
		{
			name:   "needs human with a classification",
			status: ticket.StatusNeedsHuman,
			reason: ticket.ReasonLowConfidence,
			class:  &ticket.Classification{Category: "billing", CategoryConfidence: 0.4, Urgency: 0.4},
			want: `{"ticket_id":"0192f6c1-0000-7000-8000-000000000001","status":"needs_human",` +
				`"status_reason":"low_confidence","category":"billing","priority":"low","is_complaint":false,` +
				`"message":"A person will contact you.","at":"2026-10-28T12:00:05Z"}`,
		},
		{
			name:   "needs human without a classification",
			status: ticket.StatusNeedsHuman,
			reason: ticket.ReasonBudgetExhausted,
			want: `{"ticket_id":"0192f6c1-0000-7000-8000-000000000001","status":"needs_human",` +
				`"status_reason":"budget_exhausted","category":null,"priority":null,"is_complaint":null,` +
				`"message":"A person will contact you.","at":"2026-10-28T12:00:05Z"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk := base
			tk.Status, tk.StatusReason, tk.Classification = tt.status, tt.reason, tt.class
			ev := ticket.StatusChangedOf(tk)
			b, err := ev.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(b) != tt.want {
				t.Errorf("payload =\n%s\nwant\n%s", b, tt.want)
			}
			// Ticket text, diagnostics and internal scores stay out.
			for _, secret := range []string{
				tk.Subject, tk.Body, "Refund", "Charged", tk.LastError, "0.91", "0.83", "2.2",
				`"subject"`, `"body"`, `"category_confidence"`, `"urgency_confidence"`, `"complaint_p"`, `"urgency"`,
				`"complaint_uncertain"`, `"attempts"`, `"last_error"`,
			} {
				if strings.Contains(string(b), secret) {
					t.Errorf("payload %s carries %q", b, secret)
				}
			}
			got, err := ticket.UnmarshalStatusChanged(b)
			if err != nil {
				t.Fatalf("UnmarshalStatusChanged: %v", err)
			}
			if again, err := got.Marshal(); err != nil || string(again) != string(b) {
				t.Errorf("round trip = %s, %v; want %s", again, err, b)
			}
		})
	}
}

func TestUnmarshalStatusChangedRejects(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"empty", ``},
		{"not json", `ticket`},
		{"no ticket id", `{"status":"classified"}`},
		{"nil ticket id", `{"ticket_id":"00000000-0000-0000-0000-000000000000","status":"classified"}`},
		{"no status", `{"ticket_id":"0192f6c1-0000-7000-8000-000000000001"}`},
		{"unknown status", `{"ticket_id":"0192f6c1-0000-7000-8000-000000000001","status":"queued"}`},
		{"unknown field", `{"ticket_id":"0192f6c1-0000-7000-8000-000000000001","status":"classified","body":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ev, err := ticket.UnmarshalStatusChanged([]byte(tt.payload)); err == nil {
				t.Fatalf("UnmarshalStatusChanged(%q) = %+v, want an error", tt.payload, ev)
			}
		})
	}
}
