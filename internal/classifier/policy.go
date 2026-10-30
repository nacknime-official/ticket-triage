package classifier

import "github.com/nacknime-official/ticket-triage/internal/ticket"

// Thresholds configures Decide (PRD §3.3).
type Thresholds struct {
	// CategoryMinConfidence: a lower category confidence is low_confidence.
	CategoryMinConfidence float64
	// UrgencyMinConfidence: a lower urgency confidence is low_confidence.
	UrgencyMinConfidence float64
	// ComplaintUncertainLow and ComplaintUncertainHigh bound, inclusively, the
	// complaint P(yes) band that is flagged complaint_uncertain.
	ComplaintUncertainLow  float64
	ComplaintUncertainHigh float64
	// RouteOtherToHuman sends category other to needs_human (category_other).
	RouteOtherToHuman bool
}

// DefaultThresholds returns the start values of PRD §3.3.
func DefaultThresholds() Thresholds {
	return Thresholds{
		CategoryMinConfidence:  0.60,
		UrgencyMinConfidence:   0.50,
		ComplaintUncertainLow:  0.35,
		ComplaintUncertainHigh: 0.65,
		RouteOtherToHuman:      true,
	}
}

// Flags are notes on a decision that do not change the status.
type Flags struct {
	// ComplaintUncertain means P(complaint) is inside the uncertain band.
	ComplaintUncertain bool
}

// Decide maps a valid Result to the ticket status and, for needs_human, the
// reason. A confidence equal to its minimum passes; NaN counts as below.
// Low confidence wins over category other. Urgency never forces needs_human.
func Decide(r Result, t Thresholds) (ticket.Status, ticket.Reason, Flags) {
	flags := Flags{
		ComplaintUncertain: r.ComplaintP >= t.ComplaintUncertainLow && r.ComplaintP <= t.ComplaintUncertainHigh,
	}
	switch {
	case below(r.CategoryConfidence, t.CategoryMinConfidence), below(r.UrgencyConfidence, t.UrgencyMinConfidence):
		return ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, flags
	case r.Category == CategoryOther && t.RouteOtherToHuman:
		return ticket.StatusNeedsHuman, ticket.ReasonCategoryOther, flags
	}
	return ticket.StatusClassified, "", flags
}

// below reports whether v is under limit, counting NaN as under.
func below(v, limit float64) bool {
	return !(v >= limit)
}
