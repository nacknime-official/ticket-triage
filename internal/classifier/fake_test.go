package classifier

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

func TestFakeKeywords(t *testing.T) {
	tests := []struct {
		name          string
		in            Input
		wantCategory  string
		wantUrgency   float64
		wantComplaint float64
	}{
		{"en billing", Input{"Charged twice", "My card was charged twice for the subscription."},
			CategoryBilling, 2, 0.1},
		{"uk billing angry", Input{"Знову зняли гроші!!!", "Ви знову списали 299 грн. Подам до суду."},
			CategoryBilling, 3, 0.9},
		{"en technical", Input{"App crash", "The app shows an error and crashes on start."},
			CategoryTechnical, 1, 0.1},
		{"uk technical", Input{"Сайт не працює", "Сторінка гальмує і видає помилку."},
			CategoryTechnical, 1, 0.1},
		{"en account", Input{"Locked out", "I forgot my password and cannot sign in."},
			CategoryAccount, 2, 0.1},
		{"uk account", Input{"Пароль", "Не можу увійти в акаунт, пароль не підходить."},
			CategoryAccount, 2, 0.1},
		{"en feature request", Input{"Idea", "Please add dark mode, it would be nice."},
			CategoryFeatureRequest, 0, 0.1},
		{"uk feature request", Input{"Пропозиція", "Пропоную додати темну тему, було б добре."},
			CategoryFeatureRequest, 0, 0.1},
		{"other", Input{"Hello", "Who are you?"}, CategoryOther, 1, 0.1},
		{"case insensitive", Input{"INVOICE", "WHERE IS MY INVOICE"}, CategoryBilling, 1, 0.1},
		{"one complaint word is uncertain", Input{"Invoice", "Terrible invoice layout."}, CategoryBilling, 1, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := new(Fake).Classify(t.Context(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if r.Category != tt.wantCategory || r.Urgency != tt.wantUrgency || r.ComplaintP != tt.wantComplaint {
				t.Errorf("Classify() = {category %q, urgency %v, complaint %v}, want {%q, %v, %v}",
					r.Category, r.Urgency, r.ComplaintP, tt.wantCategory, tt.wantUrgency, tt.wantComplaint)
			}
			if err := r.Validate(DefaultTaxonomy()); err != nil {
				t.Errorf("Validate() = %v", err)
			}
		})
	}
}

func TestFakeConfidence(t *testing.T) {
	th := DefaultThresholds()
	tests := []struct {
		name    string
		in      Input
		wantCat float64
	}{
		{"single category", Input{"Refund", "Please refund the payment."}, 1},
		{"tie is low confidence", Input{"Invoice", "The invoice page shows an error."}, 0.5},
		{"no keyword is other", Input{"Hi", "Thanks."}, fakeOtherConfidence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := new(Fake).Classify(t.Context(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if r.CategoryConfidence != tt.wantCat {
				t.Errorf("CategoryConfidence = %v, want %v", r.CategoryConfidence, tt.wantCat)
			}
		})
	}
	// A tie goes to the earlier category and to needs_human.
	r, err := new(Fake).Classify(t.Context(), Input{"Invoice", "The invoice page shows an error."})
	if err != nil {
		t.Fatal(err)
	}
	if r.Category != CategoryBilling {
		t.Errorf("tie Category = %q, want %q", r.Category, CategoryBilling)
	}
	if _, reason, _ := Decide(r, th); reason != ticket.ReasonLowConfidence {
		t.Errorf("tie decision reason = %q, want low_confidence", reason)
	}
}

func TestFakeDeterministic(t *testing.T) {
	in := Input{"Знову зняли гроші!!!", "Помилка оплати, не можу увійти в акаунт. Please add a refund button."}
	f := new(Fake)
	first, err := f.Classify(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Call.Provider != FakeProvider || first.Call.Model != FakeModel || first.Call.CostSource != CostEstimated {
		t.Errorf("Call = %+v, want fake provider, model and estimated cost", first.Call)
	}
	if len(first.Call.RawResponse) == 0 {
		t.Error("RawResponse is empty")
	}
	if bytes.Contains(first.Call.RawResponse, []byte("гроші")) || bytes.Contains(first.Call.RawResponse, []byte("refund button")) {
		t.Errorf("RawResponse holds ticket text: %s", first.Call.RawResponse)
	}
	for range 20 {
		for _, c := range []*Fake{f, new(Fake)} {
			got, err := c.Classify(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, first) {
				t.Fatalf("Classify() = %+v, want %+v", got, first)
			}
		}
	}
}

func TestFakeErrs(t *testing.T) {
	errBoom := errors.New("boom")
	f := &Fake{Errs: []error{ErrTransient, nil, errBoom}}
	in := Input{"Refund", "Please refund."}

	wants := []error{ErrTransient, nil, errBoom, nil, nil}
	for i, want := range wants {
		r, err := f.Classify(t.Context(), in)
		if !errors.Is(err, want) || (want == nil) != (err == nil) {
			t.Fatalf("call %d: err = %v, want %v", i, err, want)
		}
		if err != nil && (r.Category != "" || r.Call.Provider != FakeProvider || r.Call.Model != "") {
			t.Errorf("call %d: failed Result = %+v, want only the provider", i, r)
		}
		if err == nil && r.Category != CategoryBilling {
			t.Errorf("call %d: Category = %q, want %q", i, r.Category, CategoryBilling)
		}
	}
	if got := f.Calls(); got != len(wants) {
		t.Errorf("Calls() = %d, want %d", got, len(wants))
	}
}

func TestFakeContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := new(Fake).Classify(ctx, Input{"Refund", "Please refund."})
	if !errors.Is(err, ErrTransient) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want ErrTransient wrapping context.Canceled", err)
	}
}

func TestFakeConcurrent(t *testing.T) {
	f := &Fake{Errs: []error{ErrTransient, ErrTransient}}
	const n = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for range n {
		wg.Go(func() {
			if _, err := f.Classify(t.Context(), Input{"Refund", "Please refund."}); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if f.Calls() != n || failed != 2 {
		t.Errorf("Calls() = %d, failed = %d, want %d and 2", f.Calls(), failed, n)
	}
}
