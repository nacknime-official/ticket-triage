package classifier

import (
	"math"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

func TestDefaultThresholds(t *testing.T) {
	want := Thresholds{
		CategoryMinConfidence:  0.60,
		UrgencyMinConfidence:   0.50,
		ComplaintUncertainLow:  0.35,
		ComplaintUncertainHigh: 0.65,
		RouteOtherToHuman:      true,
	}
	if got := DefaultThresholds(); got != want {
		t.Errorf("DefaultThresholds() = %+v, want %+v", got, want)
	}
}

func TestDecide(t *testing.T) {
	def := DefaultThresholds()
	keepOther := def
	keepOther.RouteOtherToHuman = false
	justBelow := func(v float64) float64 { return math.Nextafter(v, 0) }
	justAbove := func(v float64) float64 { return math.Nextafter(v, 1) }

	// confident is a result that passes every threshold and is not flagged.
	confident := Result{
		ComplaintP:         0.9,
		Category:           CategoryBilling,
		CategoryConfidence: 0.9,
		Urgency:            2,
		UrgencyConfidence:  0.9,
	}
	with := func(edit func(*Result)) Result {
		r := confident
		edit(&r)
		return r
	}

	tests := []struct {
		name       string
		r          Result
		th         Thresholds
		wantStatus ticket.Status
		wantReason ticket.Reason
		wantFlags  Flags
	}{
		{"confident", confident, def, ticket.StatusClassified, "", Flags{}},

		{"category confidence at minimum", with(func(r *Result) { r.CategoryConfidence = 0.60 }), def,
			ticket.StatusClassified, "", Flags{}},
		{"category confidence just below minimum", with(func(r *Result) { r.CategoryConfidence = justBelow(0.60) }), def,
			ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, Flags{}},
		{"category confidence zero", with(func(r *Result) { r.CategoryConfidence = 0 }), def,
			ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, Flags{}},
		{"category confidence NaN", with(func(r *Result) { r.CategoryConfidence = math.NaN() }), def,
			ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, Flags{}},

		{"urgency confidence at minimum", with(func(r *Result) { r.UrgencyConfidence = 0.50 }), def,
			ticket.StatusClassified, "", Flags{}},
		{"urgency confidence just below minimum", with(func(r *Result) { r.UrgencyConfidence = justBelow(0.50) }), def,
			ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, Flags{}},
		{"urgency confidence NaN", with(func(r *Result) { r.UrgencyConfidence = math.NaN() }), def,
			ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, Flags{}},

		{"other routed to human", with(func(r *Result) { r.Category = CategoryOther }), def,
			ticket.StatusNeedsHuman, ticket.ReasonCategoryOther, Flags{}},
		{"other kept when routing is off", with(func(r *Result) { r.Category = CategoryOther }), keepOther,
			ticket.StatusClassified, "", Flags{}},
		{"low confidence wins over other", with(func(r *Result) {
			r.Category, r.CategoryConfidence = CategoryOther, 0.3
		}), def, ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, Flags{}},
		{"low urgency confidence wins over other", with(func(r *Result) {
			r.Category, r.UrgencyConfidence = CategoryOther, 0.3
		}), def, ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, Flags{}},

		{"critical urgency does not force needs_human", with(func(r *Result) { r.Urgency = 3 }), def,
			ticket.StatusClassified, "", Flags{}},

		{"complaint just below band", with(func(r *Result) { r.ComplaintP = justBelow(0.35) }), def,
			ticket.StatusClassified, "", Flags{}},
		{"complaint at band low", with(func(r *Result) { r.ComplaintP = 0.35 }), def,
			ticket.StatusClassified, "", Flags{ComplaintUncertain: true}},
		{"complaint mid band", with(func(r *Result) { r.ComplaintP = 0.5 }), def,
			ticket.StatusClassified, "", Flags{ComplaintUncertain: true}},
		{"complaint at band high", with(func(r *Result) { r.ComplaintP = 0.65 }), def,
			ticket.StatusClassified, "", Flags{ComplaintUncertain: true}},
		{"complaint just above band", with(func(r *Result) { r.ComplaintP = justAbove(0.65) }), def,
			ticket.StatusClassified, "", Flags{}},
		{"complaint uncertain kept on needs_human", with(func(r *Result) {
			r.ComplaintP, r.CategoryConfidence = 0.5, 0.1
		}), def, ticket.StatusNeedsHuman, ticket.ReasonLowConfidence, Flags{ComplaintUncertain: true}},
		{"complaint uncertain kept on other", with(func(r *Result) {
			r.ComplaintP, r.Category = 0.5, CategoryOther
		}), def, ticket.StatusNeedsHuman, ticket.ReasonCategoryOther, Flags{ComplaintUncertain: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason, flags := Decide(tt.r, tt.th)
			if status != tt.wantStatus || reason != tt.wantReason || flags != tt.wantFlags {
				t.Errorf("Decide() = (%q, %q, %+v), want (%q, %q, %+v)",
					status, reason, flags, tt.wantStatus, tt.wantReason, tt.wantFlags)
			}
			// Every decision must be a transition the ticket domain accepts.
			if err := ticket.CheckTransition(ticket.StatusPending, status, reason); err != nil {
				t.Errorf("Decide() gave an invalid transition: %v", err)
			}
		})
	}
}
