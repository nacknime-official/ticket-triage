package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/evalset"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

var update = flag.Bool("update", false, "rewrite testdata/report.golden.md")

// answer builds an answered outcome for a row with the given labels.
func answer(id, lang, wantCat string, wantComplaint bool, wantUrgency int,
	cat string, catConf, complaintP, urgency, urgencyConf float64, tags ...string) outcome {
	return outcome{
		Row: evalset.Row{
			ID: id, Source: evalset.SourceGenerated, Lang: lang, Subject: "s", Body: "b",
			ExpectedCategory: wantCat, ExpectedComplaint: wantComplaint, ExpectedUrgency: wantUrgency, Tags: tags,
		},
		Result: classifier.Result{
			ComplaintP: complaintP, Category: cat, CategoryConfidence: catConf,
			Urgency: urgency, UrgencyConfidence: urgencyConf,
		},
		Attempts: 1,
	}
}

func failed(id, lang string, err error) outcome {
	return outcome{
		Row: evalset.Row{
			ID: id, Source: evalset.SourceGenerated, Lang: lang, Subject: "s", Body: "b",
			ExpectedCategory: classifier.CategoryBilling,
		},
		Err:      err,
		Attempts: 3,
	}
}

func TestPercentile(t *testing.T) {
	ms := func(ns ...int) []time.Duration {
		ds := make([]time.Duration, len(ns))
		for i, n := range ns {
			ds[i] = time.Duration(n) * time.Millisecond
		}
		return ds
	}
	twenty := ms(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20)
	tests := []struct {
		name   string
		sorted []time.Duration
		p      float64
		want   time.Duration
	}{
		{"empty", nil, 50, 0},
		{"one", ms(7), 95, 7 * time.Millisecond},
		{"p50 of twenty", twenty, 50, 10 * time.Millisecond},
		{"p95 of twenty", twenty, 95, 19 * time.Millisecond},
		{"max of twenty", twenty, 100, 20 * time.Millisecond},
		{"p50 of three", ms(1, 2, 30), 50, 2 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := percentile(tt.sorted, tt.p); got != tt.want {
				t.Errorf("percentile(%v, %v) = %v, want %v", tt.sorted, tt.p, got, tt.want)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	th := classifier.DefaultThresholds()
	tests := []struct {
		name string
		o    outcome
		want decision
	}{
		{"confident", answer("a", "en", "billing", false, 1, "billing", 0.9, 0.1, 1, 0.9),
			decision{Status: ticket.StatusClassified}},
		{"low confidence", answer("a", "en", "billing", false, 1, "billing", 0.5, 0.5, 1, 0.9),
			decision{Status: ticket.StatusNeedsHuman, Reason: ticket.ReasonLowConfidence, ComplaintUncertain: true}},
		{"other", answer("a", "en", "other", false, 1, "other", 0.9, 0.1, 1, 0.9),
			decision{Status: ticket.StatusNeedsHuman, Reason: ticket.ReasonCategoryOther}},
		{"transient", failed("a", "en", fmt.Errorf("%w: 503", classifier.ErrTransient)),
			decision{Status: ticket.StatusNeedsHuman, Reason: ticket.ReasonClassifierUnavailable}},
		{"invalid answer", failed("a", "en", fmt.Errorf("x: %w", classifier.ErrInvalidAnswer)),
			decision{Status: ticket.StatusNeedsHuman, Reason: ticket.ReasonInvalidAnswer}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decide(tt.o, th); got != tt.want {
				t.Errorf("decide() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestQuestions(t *testing.T) {
	outs := []outcome{
		answer("a", "en", "billing", true, 2, "billing", 0.9, 0.7, 2.4, 0.9),
		answer("b", "en", "technical", false, 3, "billing", 0.9, 0.5, 1.6, 0.9),
		answer("c", "uk", "account", false, 0, "account", 0.9, 0.49, 0.4, 0.9),
		failed("d", "uk", classifier.ErrTransient),
	}
	qs := questions(0.5)
	tests := []struct {
		q          question
		want, near score
		// cells lists the non-zero confusion cells as label->answer.
		cells map[[2]string]int
	}{
		{qs[0], score{2, 3}, score{}, map[[2]string]int{
			{"billing", "billing"}: 1, {"technical", "billing"}: 1, {"account", "account"}: 1,
		}},
		// 0.5 counts as a complaint at threshold 0.5; 0.49 does not.
		{qs[1], score{2, 3}, score{}, map[[2]string]int{
			{"true", "true"}: 1, {"false", "true"}: 1, {"false", "false"}: 1,
		}},
		// Urgency 2.4 rounds to 2, 1.6 to 2 (label 3, one off), 0.4 to 0.
		{qs[2], score{2, 3}, score{3, 3}, map[[2]string]int{
			{"2 high", "2 high"}: 1, {"3 critical", "2 high"}: 1, {"0 low", "0 low"}: 1,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.q.Name, func(t *testing.T) {
			if got := tt.q.accuracy(outs); got != tt.want {
				t.Errorf("accuracy = %+v, want %+v", got, tt.want)
			}
			if tt.q.Near != nil {
				if got := tt.q.nearAccuracy(outs); got != tt.near {
					t.Errorf("nearAccuracy = %+v, want %+v", got, tt.near)
				}
			}
			c := tt.q.confusion(outs)
			for i, l := range c.Labels {
				for j, a := range c.Labels {
					if got, want := c.Counts[i][j], tt.cells[[2]string{l, a}]; got != want {
						t.Errorf("confusion[%s][%s] = %d, want %d", l, a, got, want)
					}
				}
			}
		})
	}
}

func TestSweep(t *testing.T) {
	outs := []outcome{
		// Right, confident.
		answer("a", "en", "billing", false, 1, "billing", 0.9, 0.1, 1, 0.9),
		// Wrong, unsure about the category.
		answer("b", "en", "technical", false, 1, "billing", 0.42, 0.1, 1, 0.9),
		// Right, unsure about urgency.
		answer("c", "en", "account", false, 1, "account", 0.9, 0.1, 1, 0.3),
		// Category other always goes to a person.
		answer("d", "en", "other", false, 1, "other", 0.9, 0.1, 1, 0.9),
		failed("e", "en", classifier.ErrTransient),
	}
	qs := questions(0.5)
	base := classifier.DefaultThresholds()

	t.Run("at the configured thresholds", func(t *testing.T) {
		p := sweepAt(outs, qs, base, 0.6, 0.5)
		want := sweepPoint{CategoryMin: 0.6, UrgencyMin: 0.5, Total: 5, NeedsHuman: 4,
			Category: score{1, 1}, Priority: score{1, 1}, Complaint: score{1, 1}}
		if p != want {
			t.Errorf("sweepAt() = %+v, want %+v", p, want)
		}
	})

	t.Run("grid", func(t *testing.T) {
		points := sweepCategory(outs, qs, base)
		if len(points) != 21 || points[0].CategoryMin != 0 || points[20].CategoryMin != 1 ||
			points[12].CategoryMin != 0.60 {
			t.Fatalf("sweepCategory grid = %v", points)
		}
		// At 0.40 the unsure wrong answer is auto-classified, at 0.45 it is not.
		if got := points[8].Category; got != (score{1, 2}) {
			t.Errorf("category accuracy at 0.40 = %+v, want 1/2", got)
		}
		if got := points[9].Category; got != (score{1, 1}) {
			t.Errorf("category accuracy at 0.45 = %+v, want 1/1", got)
		}
		if u := sweepUrgency(outs, qs, base); u[10].UrgencyMin != 0.50 || u[10].NeedsHuman != 4 || u[6].NeedsHuman != 3 {
			t.Errorf("sweepUrgency at 0.50 and 0.30 = %+v, %+v", u[10], u[6])
		}
	})

	t.Run("frontier", func(t *testing.T) {
		got := frontier(outs, qs, base)
		var pairs [][2]float64
		for _, p := range got {
			pairs = append(pairs, [2]float64{p.CategoryMin, p.UrgencyMin})
		}
		// (0, 0): 2 to a person, 2/3 right. (0.45, 0): 3 to a person, 2/2 right.
		// Everything stricter only sends more to a person at the same accuracy.
		want := [][2]float64{{0, 0}, {0.45, 0}}
		if !slices.Equal(pairs, want) {
			t.Errorf("frontier pairs = %v, want %v", pairs, want)
		}
	})
}

func TestSumCosts(t *testing.T) {
	a := answer("a", "en", "billing", false, 1, "billing", 0.9, 0.1, 1, 0.9)
	a.Result.Call = classifier.Call{InputTokens: 400, CostUSD: 0.00002, CostSource: classifier.CostReported}
	b := failed("b", "en", classifier.ErrInvalidAnswer)
	b.Result.Call = classifier.Call{InputTokens: 500, OutputTokens: 3, CostUSD: 0.000021, CostSource: classifier.CostEstimated}
	c := failed("c", "en", classifier.ErrTransient)

	got := sumCosts([]outcome{a, b, c})
	if got.InputTokens != 900 || got.OutputTokens != 3 || fmt.Sprintf("%.6f", got.USD) != "0.000041" {
		t.Errorf("sumCosts() = %+v", got)
	}
	want := map[classifier.CostSource]int{classifier.CostReported: 1, classifier.CostEstimated: 1, "": 1}
	if fmt.Sprint(got.Sources) != fmt.Sprint(want) {
		t.Errorf("Sources = %v, want %v", got.Sources, want)
	}
}

// TestRenderGolden renders a fixed set of outcomes. Run with -update to
// rewrite the golden file after a deliberate change.
func TestRenderGolden(t *testing.T) {
	withCall := func(o outcome, latency time.Duration, cached bool) outcome {
		o.Result.Call = classifier.Call{
			Provider: "openrouter", Model: "typesafe/jev-1.14-20261014", HTTPStatus: 200, Latency: latency,
			InputTokens: 450, CostUSD: 0.000019, CostSource: classifier.CostReported,
		}
		o.Cached = cached
		return o
	}
	outs := []outcome{
		withCall(answer("en-1", "en", "billing", true, 2, "billing", 0.84, 0.93, 2.1, 0.62, "angry"), 90*time.Millisecond, true),
		withCall(answer("en-2", "en", "technical", false, 3, "technical", 0.71, 0.2, 2.8, 0.55), 110*time.Millisecond, false),
		withCall(answer("en-3", "en", "feature_request", false, 0, "feature_request", 0.58, 0.1, 0.3, 0.8), 140*time.Millisecond, false),
		withCall(answer("uk-1", "uk", "account", false, 2, "account", 0.66, 0.4, 1.4, 0.45, "surzhyk", "typos"), 300*time.Millisecond, false),
		withCall(answer("uk-2", "uk", "other", true, 1, "billing", 0.52, 0.7, 1.0, 0.7, "vague"), 450*time.Millisecond, false),
		failed("uk-3", "uk", fmt.Errorf("%w: jev: status 503", classifier.ErrTransient)),
	}
	meta := reportMeta{
		Kind: config.ClassifierJev, Provider: "openrouter", Model: "typesafe/jev-1.14", Date: "2026-10-28",
		LabelsUnreviewed: true,
		Files: []dataFile{
			{Name: "a.jsonl", Rows: 3, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			{Name: "b.jsonl", Rows: 3, SHA256: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"},
		},
		Thresholds:         classifier.DefaultThresholds(),
		ComplaintThreshold: 0.5,
	}
	got := render(meta, outs)

	golden := filepath.Join("testdata", "report.golden.md")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("render() differs from %s; run go test ./cmd/eval -run TestRenderGolden -update and diff\n%s", golden, got)
	}
}
