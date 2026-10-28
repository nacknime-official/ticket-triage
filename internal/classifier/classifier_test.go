package classifier

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

func TestErrInvalidAnswerIsPermanent(t *testing.T) {
	if !errors.Is(ErrInvalidAnswer, ErrPermanent) {
		t.Error("ErrInvalidAnswer does not wrap ErrPermanent")
	}
	for _, err := range []error{ErrTransient, ErrAuth} {
		if errors.Is(ErrInvalidAnswer, err) {
			t.Errorf("ErrInvalidAnswer wraps %v", err)
		}
	}
}

func TestResultValidate(t *testing.T) {
	tax := DefaultTaxonomy()
	valid := Result{ComplaintP: 0.2, Category: CategoryTechnical, CategoryConfidence: 0.7, Urgency: 1.4, UrgencyConfidence: 0.6}
	with := func(edit func(*Result)) Result {
		r := valid
		edit(&r)
		return r
	}

	tests := []struct {
		name    string
		r       Result
		wantErr bool
	}{
		{"valid", valid, false},
		{"bounds", Result{ComplaintP: 1, Category: CategoryOther, CategoryConfidence: 0, Urgency: 3, UrgencyConfidence: 1}, false},
		{"zero urgency", with(func(r *Result) { r.Urgency = 0 }), false},
		{"complaint above 1", with(func(r *Result) { r.ComplaintP = 1.01 }), true},
		{"complaint NaN", with(func(r *Result) { r.ComplaintP = math.NaN() }), true},
		{"category confidence negative", with(func(r *Result) { r.CategoryConfidence = -0.1 }), true},
		{"urgency confidence infinite", with(func(r *Result) { r.UrgencyConfidence = math.Inf(1) }), true},
		{"urgency negative", with(func(r *Result) { r.Urgency = -0.01 }), true},
		{"urgency above max", with(func(r *Result) { r.Urgency = 3.01 }), true},
		{"urgency NaN", with(func(r *Result) { r.Urgency = math.NaN() }), true},
		{"unknown category", with(func(r *Result) { r.Category = "sales" }), true},
		{"empty category", with(func(r *Result) { r.Category = "" }), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.r.Validate(tax)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() = %v, want error %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidAnswer) {
				t.Errorf("Validate() = %v, want an ErrInvalidAnswer", err)
			}
		})
	}
}

func TestResultPriority(t *testing.T) {
	tests := []struct {
		urgency float64
		want    ticket.Priority
	}{
		{0, ticket.PriorityLow},
		{0.49, ticket.PriorityLow},
		{0.5, ticket.PriorityNormal},
		{1.49, ticket.PriorityNormal},
		{1.5, ticket.PriorityHigh},
		{2.49, ticket.PriorityHigh},
		{2.5, ticket.PriorityCritical},
		{3, ticket.PriorityCritical},
	}
	for _, tt := range tests {
		if got := (Result{Urgency: tt.urgency}).Priority(); got != tt.want {
			t.Errorf("Result{Urgency: %v}.Priority() = %v, want %v", tt.urgency, got, tt.want)
		}
	}
}

func TestResultClassification(t *testing.T) {
	r := Result{ComplaintP: 0.8, Category: CategoryAccount, CategoryConfidence: 0.7, Urgency: 2.2, UrgencyConfidence: 0.6,
		Call: Call{Provider: "x", RawResponse: []byte("{}")}}
	want := ticket.Classification{Category: CategoryAccount, CategoryConfidence: 0.7, ComplaintP: 0.8, Urgency: 2.2, UrgencyConfidence: 0.6}
	if got := r.Classification(); got != want {
		t.Errorf("Classification() = %+v, want %+v", got, want)
	}
}

func TestTaxonomy(t *testing.T) {
	tax := DefaultTaxonomy()
	if err := tax.Validate(); err != nil {
		t.Fatalf("DefaultTaxonomy().Validate() = %v", err)
	}
	want := []string{CategoryBilling, CategoryTechnical, CategoryAccount, CategoryFeatureRequest, CategoryOther}
	if got := tax.Names(); !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	if tax.Has("sales") || !tax.Has(CategoryOther) {
		t.Error("Has() gives wrong answers")
	}

	// DefaultTaxonomy returns a fresh copy each time.
	tax[0].Name = "changed"
	if DefaultTaxonomy()[0].Name != CategoryBilling {
		t.Error("DefaultTaxonomy() shares its backing array")
	}

	bad := []struct {
		name string
		tax  Taxonomy
	}{
		{"empty", Taxonomy{}},
		{"no other", Taxonomy{{"billing", "b"}}},
		{"empty name", Taxonomy{{"", "x"}, {CategoryOther, "o"}}},
		{"empty description", Taxonomy{{"billing", ""}, {CategoryOther, "o"}}},
		{"repeated", Taxonomy{{"billing", "b"}, {"billing", "c"}, {CategoryOther, "o"}}},
	}
	for _, tt := range bad {
		if err := tt.tax.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", tt.name)
		}
	}
}
