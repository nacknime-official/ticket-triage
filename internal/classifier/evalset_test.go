package classifier_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/evalset"
)

func TestTaxonomyMatchesEvalSet(t *testing.T) {
	if got := classifier.DefaultTaxonomy().Names(); !slices.Equal(got, evalset.Categories) {
		t.Errorf("DefaultTaxonomy().Names() = %v, evalset.Categories = %v", got, evalset.Categories)
	}
}

// TestFakeOnEvalSet runs the fake over the merged eval set. It checks only that
// every row loads and gives a valid result, not accuracy.
func TestFakeOnEvalSet(t *testing.T) {
	paths, err := filepath.Glob("../../testdata/eval/*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no eval files")
	}
	var rows []evalset.Row
	for _, p := range paths {
		r, err := evalset.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r...)
	}

	tax := classifier.DefaultTaxonomy()
	th := classifier.DefaultThresholds()
	f := new(classifier.Fake)
	decisions := make(map[string]int)
	for _, row := range rows {
		r, err := f.Classify(t.Context(), classifier.Input{Subject: row.Subject, Body: row.Body})
		if err != nil {
			t.Fatalf("row %s: %v", row.ID, err)
		}
		if err := r.Validate(tax); err != nil {
			t.Errorf("row %s: %v", row.ID, err)
		}
		status, reason, _ := classifier.Decide(r, th)
		if !status.Final() {
			t.Errorf("row %s: Decide() status %q is not final", row.ID, status)
		}
		decisions[string(status)+" "+string(reason)]++
	}
	t.Logf("%d rows, decisions: %v", len(rows), decisions)
	if f.Calls() != len(rows) {
		t.Errorf("Calls() = %d, want %d", f.Calls(), len(rows))
	}
}
