package shadow

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// Questions of the agreement table, in report order.
const (
	QuestionComplaint = "is_complaint"
	QuestionCategory  = "category"
	QuestionPriority  = "priority"
	QuestionStatus    = "status"
)

// Agreement counts the tickets on which shadow and live answers agree.
type Agreement struct {
	Agree, Total int
}

// Rate is Agree / Total, or 0 when there is nothing to compare.
func (a Agreement) Rate() float64 {
	if a.Total == 0 {
		return 0
	}
	return float64(a.Agree) / float64(a.Total)
}

// Decision is a status with its reason.
type Decision struct {
	Status ticket.Status
	Reason ticket.Reason
}

func (d Decision) String() string {
	if d.Reason == "" {
		return string(d.Status)
	}
	return fmt.Sprintf("%s (%s)", d.Status, d.Reason)
}

// Change is a ticket whose final live status differs from the shadow one.
type Change struct {
	TicketID uuid.UUID
	Live     Decision
	Shadow   Decision
}

// Comparison is the shadow result of one ticket next to its live state.
type Comparison struct {
	// Answers reports whether both sides have a classification, so the
	// per-question fields mean something.
	Answers                       bool
	Complaint, Category, Priority bool
	// Decided reports whether the live status is final and the shadow one
	// exists, so Changed means something.
	Decided bool
	// Changed reports a different status (the reason alone does not count).
	Changed bool
	Live    Decision
	Shadow  Decision
}

// Compare compares the shadow result r with the live ticket t. A failed shadow
// classification, or a live ticket without a classification (pending, or sent
// to a person without an answer), has no answers to compare.
func Compare(t ticket.Ticket, r Result) Comparison {
	c := Comparison{
		Live:   Decision{t.Status, t.StatusReason},
		Shadow: Decision{r.Status, r.Reason},
	}
	ok := r.Outcome == OutcomeOK
	if lc := t.Classification; ok && lc != nil {
		c.Answers = true
		c.Complaint = lc.IsComplaint == r.IsComplaint()
		c.Category = lc.Category == r.Answer.Category
		c.Priority = lc.Priority() == r.Answer.Priority()
	}
	if ok && t.Status.Final() {
		c.Decided = true
		c.Changed = t.Status != r.Status
	}
	return c
}

// Report summarizes a shadow run against the live classification.
type Report struct {
	RunID  string
	DryRun bool
	// Replayed counts the tickets classified, by outcome.
	Replayed map[Outcome]int
	// Skipped counts tickets the run had already answered; Missing counts
	// records whose ticket id was bad or whose ticket is gone.
	Skipped, Missing int
	// LivePending counts replayed tickets that are still pending live.
	LivePending int
	// CostUSD is the cost of this run's calls.
	CostUSD float64
	// Agreement is keyed by the Question constants.
	Agreement map[string]Agreement
	// Transitions counts live → shadow decisions of the decided tickets.
	Transitions map[[2]Decision]int
	// Changes lists the tickets that would change status.
	Changes []Change
}

// Add records the shadow result r of the live ticket t.
func (rep *Report) Add(t ticket.Ticket, r Result) {
	if rep.Replayed == nil {
		rep.Replayed = make(map[Outcome]int)
		rep.Agreement = make(map[string]Agreement)
		rep.Transitions = make(map[[2]Decision]int)
	}
	rep.Replayed[r.Outcome]++
	rep.CostUSD += r.CostUSD
	if t.Status == ticket.StatusPending {
		rep.LivePending++
	}
	c := Compare(t, r)
	if c.Answers {
		rep.count(QuestionComplaint, c.Complaint)
		rep.count(QuestionCategory, c.Category)
		rep.count(QuestionPriority, c.Priority)
	}
	if c.Decided {
		rep.count(QuestionStatus, !c.Changed)
		rep.Transitions[[2]Decision{c.Live, c.Shadow}]++
	}
	if c.Changed {
		rep.Changes = append(rep.Changes, Change{TicketID: t.ID, Live: c.Live, Shadow: c.Shadow})
	}
}

func (rep *Report) count(q string, agree bool) {
	a := rep.Agreement[q]
	a.Total++
	if agree {
		a.Agree++
	}
	rep.Agreement[q] = a
}

// Total is the number of tickets classified, whatever the outcome.
func (rep *Report) Total() int {
	n := 0
	for _, v := range rep.Replayed {
		n += v
	}
	return n
}

func (rep *Report) clone() Report {
	c := *rep
	c.Replayed = maps.Clone(rep.Replayed)
	c.Agreement = maps.Clone(rep.Agreement)
	c.Transitions = maps.Clone(rep.Transitions)
	c.Changes = slices.Clone(rep.Changes)
	return c
}

// MaxListedChanges caps the ticket list of Write; the transition table still
// counts every change.
const MaxListedChanges = 100

// Write renders the report as plain text. It names tickets by id only.
func (rep *Report) Write(w io.Writer) error {
	var b strings.Builder
	mode := ""
	if rep.DryRun {
		mode = " (dry run: fake classifier, nothing written)"
	}
	fmt.Fprintf(&b, "Shadow run %s%s\n\n", rep.RunID, mode)

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Tickets classified\t%d\n", rep.Total())
	for _, o := range []Outcome{OutcomeOK, OutcomeTransientError, OutcomePermanentError, OutcomeInvalidAnswer} {
		if n := rep.Replayed[o]; n > 0 {
			fmt.Fprintf(tw, "  %s\t%d\n", o, n)
		}
	}
	fmt.Fprintf(tw, "Skipped, answered earlier in this run\t%d\n", rep.Skipped)
	fmt.Fprintf(tw, "Skipped, bad id or missing ticket\t%d\n", rep.Missing)
	fmt.Fprintf(tw, "Still pending live\t%d\n", rep.LivePending)
	fmt.Fprintf(tw, "Cost\t$%.8f\n", rep.CostUSD)
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("shadow: render report: %w", err)
	}

	b.WriteString("\nAgreement with the live classification\n")
	tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(tw, "question\tagree\ttotal\trate\t\n")
	for _, q := range []string{QuestionComplaint, QuestionCategory, QuestionPriority, QuestionStatus} {
		a := rep.Agreement[q]
		rate := "-"
		if a.Total > 0 {
			rate = fmt.Sprintf("%.1f%%", 100*a.Rate())
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t\n", q, a.Agree, a.Total, rate)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("shadow: render report: %w", err)
	}

	if len(rep.Transitions) > 0 {
		b.WriteString("\nLive -> shadow decisions\n")
		tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		keys := slices.SortedFunc(maps.Keys(rep.Transitions), func(x, y [2]Decision) int {
			return strings.Compare(x[0].String()+"\x00"+x[1].String(), y[0].String()+"\x00"+y[1].String())
		})
		for _, k := range keys {
			fmt.Fprintf(tw, "  %s -> %s\t%d\n", k[0], k[1], rep.Transitions[k])
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("shadow: render report: %w", err)
		}
	}

	fmt.Fprintf(&b, "\nTickets that would change status: %d\n", len(rep.Changes))
	changes := slices.SortedFunc(slices.Values(rep.Changes), func(x, y Change) int {
		return strings.Compare(x.TicketID.String(), y.TicketID.String())
	})
	for i, c := range changes {
		if i == MaxListedChanges {
			fmt.Fprintf(&b, "  ... and %d more\n", len(changes)-i)
			break
		}
		fmt.Fprintf(&b, "  %s  %s -> %s\n", c.TicketID, c.Live, c.Shadow)
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("shadow: write report: %w", err)
	}
	return nil
}
