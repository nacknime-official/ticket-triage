package ticket_test

import (
	"errors"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

var allStatuses = []ticket.Status{ticket.StatusPending, ticket.StatusClassified, ticket.StatusNeedsHuman}

func TestStatusValid(t *testing.T) {
	for _, s := range allStatuses {
		if !s.Valid() {
			t.Errorf("%q.Valid() = false, want true", s)
		}
	}
	for _, s := range []ticket.Status{"", "resolved", "queued", "PENDING"} {
		if s.Valid() {
			t.Errorf("%q.Valid() = true, want false", s)
		}
	}
}

func TestStatusCanTransitionTo(t *testing.T) {
	allowed := map[[2]ticket.Status]bool{
		{ticket.StatusPending, ticket.StatusClassified}: true,
		{ticket.StatusPending, ticket.StatusNeedsHuman}: true,
	}
	for _, from := range allStatuses {
		for _, to := range append(allStatuses, "", "resolved") {
			want := allowed[[2]ticket.Status{from, to}]
			if got := from.CanTransitionTo(to); got != want {
				t.Errorf("%q.CanTransitionTo(%q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestStatusFinal(t *testing.T) {
	tests := map[ticket.Status]bool{
		ticket.StatusPending:    false,
		ticket.StatusClassified: true,
		ticket.StatusNeedsHuman: true,
	}
	for s, want := range tests {
		if got := s.Final(); got != want {
			t.Errorf("%q.Final() = %v, want %v", s, got, want)
		}
	}
}

func TestReasonValid(t *testing.T) {
	for _, r := range []ticket.Reason{
		ticket.ReasonLowConfidence, ticket.ReasonCategoryOther, ticket.ReasonClassifierUnavailable,
		ticket.ReasonInvalidAnswer, ticket.ReasonBudgetExhausted,
	} {
		if !r.Valid() {
			t.Errorf("%q.Valid() = false, want true", r)
		}
	}
	for _, r := range []ticket.Reason{"", "timeout", "LOW_CONFIDENCE"} {
		if r.Valid() {
			t.Errorf("%q.Valid() = true, want false", r)
		}
	}
}

func TestCheckTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    ticket.Status
		to      ticket.Status
		reason  ticket.Reason
		wantErr bool
	}{
		{"pending to classified", ticket.StatusPending, ticket.StatusClassified, "", false},
		{"pending to needs_human with reason", ticket.StatusPending, ticket.StatusNeedsHuman, ticket.ReasonBudgetExhausted, false},
		{"classified with reason", ticket.StatusPending, ticket.StatusClassified, ticket.ReasonLowConfidence, true},
		{"needs_human without reason", ticket.StatusPending, ticket.StatusNeedsHuman, "", true},
		{"needs_human with unknown reason", ticket.StatusPending, ticket.StatusNeedsHuman, "nope", true},
		{"pending to pending", ticket.StatusPending, ticket.StatusPending, "", true},
		{"classified to needs_human", ticket.StatusClassified, ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, true},
		{"needs_human to classified", ticket.StatusNeedsHuman, ticket.StatusClassified, "", true},
		{"classified to pending", ticket.StatusClassified, ticket.StatusPending, "", true},
		{"unknown target", ticket.StatusPending, "resolved", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ticket.CheckTransition(tt.from, tt.to, tt.reason)
			if tt.wantErr != (err != nil) {
				t.Fatalf("CheckTransition() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ticket.ErrInvalidTransition) {
				t.Errorf("error %v does not wrap ErrInvalidTransition", err)
			}
		})
	}
}
