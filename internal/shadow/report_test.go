package shadow_test

import (
	"fmt"
	"strings"
	"testing"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/shadow"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// liveTicket returns a ticket in status with reason and, when c is not nil,
// its live classification.
func liveTicket(status ticket.Status, reason ticket.Reason, c *ticket.Classification) ticket.Ticket {
	return ticket.Ticket{ID: uuid.NewV7(), Status: status, StatusReason: reason, Classification: c}
}

// liveBilling is a confident live answer: billing, complaint, high priority.
func liveBilling() *ticket.Classification {
	return &ticket.Classification{
		Category: "billing", CategoryConfidence: 0.9, ComplaintP: 0.8, IsComplaint: true,
		Urgency: 2.1, UrgencyConfidence: 0.8,
	}
}

// shadowOK returns a valid shadow result with the answers and decision.
func shadowOK(category string, complaintP, urgency float64, status ticket.Status, reason ticket.Reason) shadow.Result {
	return shadow.Result{
		Outcome: shadow.OutcomeOK, Attempts: 1,
		Answer: classifier.Result{
			Category: category, CategoryConfidence: 0.9, ComplaintP: complaintP,
			Urgency: urgency, UrgencyConfidence: 0.8,
		},
		Status: status, Reason: reason,
	}
}

func TestCompare(t *testing.T) {
	classified, needsHuman := ticket.StatusClassified, ticket.StatusNeedsHuman
	tests := []struct {
		name string
		live ticket.Ticket
		sh   shadow.Result
		want shadow.Comparison
	}{
		{
			name: "all agree",
			live: liveTicket(classified, "", liveBilling()),
			sh:   shadowOK("billing", 0.7, 1.6, classified, ""),
			want: shadow.Comparison{Answers: true, Complaint: true, Category: true, Priority: true, Decided: true},
		},
		{
			name: "every question differs and status changes",
			live: liveTicket(classified, "", liveBilling()),
			sh:   shadowOK("other", 0.2, 0.4, needsHuman, ticket.ReasonCategoryOther),
			want: shadow.Comparison{Answers: true, Decided: true, Changed: true},
		},
		{
			name: "complaint flag at exactly 0.5 is a complaint",
			live: liveTicket(classified, "", liveBilling()),
			sh:   shadowOK("billing", 0.5, 2, classified, ""),
			want: shadow.Comparison{Answers: true, Complaint: true, Category: true, Priority: true, Decided: true},
		},
		{
			name: "only the reason differs: not a status change",
			live: liveTicket(needsHuman, ticket.ReasonLowConfidence, liveBilling()),
			sh:   shadowOK("other", 0.8, 2, needsHuman, ticket.ReasonCategoryOther),
			want: shadow.Comparison{Answers: true, Complaint: true, Priority: true, Decided: true},
		},
		{
			name: "live needs_human without answers: status only",
			live: liveTicket(needsHuman, ticket.ReasonBudgetExhausted, nil),
			sh:   shadowOK("billing", 0.8, 2, classified, ""),
			want: shadow.Comparison{Decided: true, Changed: true},
		},
		{
			name: "live pending: answers but no decision",
			live: liveTicket(ticket.StatusPending, "", nil),
			sh:   shadowOK("billing", 0.8, 2, classified, ""),
			want: shadow.Comparison{},
		},
		{
			name: "failed shadow call: nothing to compare",
			live: liveTicket(classified, "", liveBilling()),
			sh:   shadow.Result{Outcome: shadow.OutcomeTransientError, Attempts: 3},
			want: shadow.Comparison{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shadow.Compare(tt.live, tt.sh)
			tt.want.Live = shadow.Decision{Status: tt.live.Status, Reason: tt.live.StatusReason}
			tt.want.Shadow = shadow.Decision{Status: tt.sh.Status, Reason: tt.sh.Reason}
			if got != tt.want {
				t.Errorf("Compare =\n %+v, want\n %+v", got, tt.want)
			}
		})
	}
}

func TestReport(t *testing.T) {
	classified, needsHuman := ticket.StatusClassified, ticket.StatusNeedsHuman
	var rep shadow.Report
	rep.RunID = "r1"

	// Agrees on everything.
	rep.Add(liveTicket(classified, "", liveBilling()), shadowOK("billing", 0.9, 2, classified, ""))
	// Same status, other priority.
	rep.Add(liveTicket(classified, "", liveBilling()), shadowOK("billing", 0.9, 3, classified, ""))
	// Stricter thresholds: would go to a person.
	lowConf := liveTicket(classified, "", liveBilling())
	rep.Add(lowConf, shadowOK("billing", 0.9, 2, needsHuman, ticket.ReasonLowConfidence))
	// Live ran out of budget; the shadow answers.
	budget := liveTicket(needsHuman, ticket.ReasonBudgetExhausted, nil)
	rep.Add(budget, shadowOK("technical", 0.1, 1, classified, ""))
	// Still pending live.
	rep.Add(liveTicket(ticket.StatusPending, "", nil), shadowOK("account", 0.1, 1, classified, ""))
	// A failed shadow call with a cost.
	failed := shadow.Result{Outcome: shadow.OutcomeTransientError, Attempts: 3, CostUSD: 0.00001}
	rep.Add(liveTicket(classified, "", liveBilling()), failed)
	rep.Skipped, rep.Missing = 2, 1

	if got := rep.Total(); got != 6 {
		t.Errorf("Total = %d, want 6", got)
	}
	if got := rep.LivePending; got != 1 {
		t.Errorf("LivePending = %d, want 1", got)
	}
	wantAgree := map[string]shadow.Agreement{
		shadow.QuestionComplaint: {Agree: 3, Total: 3},
		shadow.QuestionCategory:  {Agree: 3, Total: 3},
		shadow.QuestionPriority:  {Agree: 2, Total: 3},
		shadow.QuestionStatus:    {Agree: 2, Total: 4},
	}
	for q, want := range wantAgree {
		if got := rep.Agreement[q]; got != want {
			t.Errorf("Agreement[%s] = %+v, want %+v", q, got, want)
		}
	}
	if len(rep.Changes) != 2 {
		t.Fatalf("Changes = %+v, want 2", rep.Changes)
	}
	key := [2]shadow.Decision{
		{Status: classified}, {Status: needsHuman, Reason: ticket.ReasonLowConfidence},
	}
	if got := rep.Transitions[key]; got != 1 {
		t.Errorf("Transitions[%v] = %d, want 1", key, got)
	}

	var b strings.Builder
	if err := rep.Write(&b); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"Shadow run r1\n",
		"Tickets classified                     6\n",
		"  transient_error                      1\n",
		"Skipped, answered earlier in this run  2\n",
		"Still pending live                     1\n",
		"Cost                                   $0.00001000\n",
		"is_complaint      3      3  100.0%",
		"    priority      2      3   66.7%",
		"      status      2      4   50.0%",
		"  classified -> needs_human (low_confidence)    1\n",
		"Tickets that would change status: 2\n",
		fmt.Sprintf("  %s  classified -> needs_human (low_confidence)\n", lowConf.ID),
		fmt.Sprintf("  %s  needs_human (budget_exhausted) -> classified\n", budget.ID),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dry run") {
		t.Errorf("report of a real run says dry run:\n%s", out)
	}
}

func TestReportEmptyAndCapped(t *testing.T) {
	var empty shadow.Report
	empty.RunID, empty.DryRun = "r2", true
	var b strings.Builder
	if err := empty.Write(&b); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, want := range []string{"(dry run: fake classifier, nothing written)", "category      0      0     -",
		"Tickets that would change status: 0\n"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("empty report misses %q:\n%s", want, b.String())
		}
	}

	var rep shadow.Report
	for range shadow.MaxListedChanges + 5 {
		rep.Add(liveTicket(ticket.StatusClassified, "", liveBilling()),
			shadowOK("billing", 0.9, 2, ticket.StatusNeedsHuman, ticket.ReasonLowConfidence))
	}
	b.Reset()
	if err := rep.Write(&b); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := strings.Count(b.String(), "-> needs_human (low_confidence)\n"); got != shadow.MaxListedChanges {
		t.Errorf("listed %d changes, want %d", got, shadow.MaxListedChanges)
	}
	if !strings.Contains(b.String(), "  ... and 5 more\n") {
		t.Errorf("capped report misses the remainder line:\n%s", b.String())
	}
}
