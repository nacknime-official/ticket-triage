package main

import (
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/evalset"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// sweepSteps divides [0, 1] into the 0.05 threshold grid: tuning finer than 0.05
// is pointless (README §3.3).
const sweepSteps = 20

// Complaint and priority labels in confusion matrices.
var (
	complaintLabels = []string{"false", "true"}
	priorityLabels  = []string{"0 low", "1 normal", "2 high", "3 critical"}
)

// group is a named subset of the outcomes, such as one language.
type group struct {
	Name     string
	Outcomes []outcome
}

// byLang returns all outcomes, then one group per language in evalset order.
func byLang(outs []outcome) []group {
	groups := []group{{Name: "all", Outcomes: outs}}
	for _, lang := range evalset.Langs {
		groups = append(groups, group{Name: lang, Outcomes: filter(outs, func(o outcome) bool { return o.Row.Lang == lang })})
	}
	return groups
}

// byTag returns one group per hard-case tag that occurs, in evalset order.
func byTag(outs []outcome) []group {
	var groups []group
	for _, tag := range evalset.Tags {
		g := group{Name: tag, Outcomes: filter(outs, func(o outcome) bool { return slices.Contains(o.Row.Tags, tag) })}
		if len(g.Outcomes) > 0 {
			groups = append(groups, g)
		}
	}
	return groups
}

func filter(outs []outcome, keep func(outcome) bool) []outcome {
	var out []outcome
	for _, o := range outs {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

func answered(outs []outcome) []outcome {
	return filter(outs, func(o outcome) bool { return o.Err == nil })
}

// question is one triage question scored against its label.
type question struct {
	Name   string
	Labels []string
	// Expected and Got give the row's label and the answer's label.
	Expected, Got func(outcome) string
	// Near, if set, also counts answers that are close enough.
	Near func(outcome) bool
}

func questions(complaintThreshold float64) []question {
	return []question{
		{
			Name:     "Category",
			Labels:   evalset.Categories,
			Expected: func(o outcome) string { return o.Row.ExpectedCategory },
			Got:      func(o outcome) string { return o.Result.Category },
		},
		{
			Name:     "Complaint",
			Labels:   complaintLabels,
			Expected: func(o outcome) string { return strconv.FormatBool(o.Row.ExpectedComplaint) },
			Got:      func(o outcome) string { return strconv.FormatBool(o.Result.ComplaintP >= complaintThreshold) },
		},
		{
			Name:     "Priority",
			Labels:   priorityLabels,
			Expected: func(o outcome) string { return priorityLabels[o.Row.ExpectedUrgency] },
			Got:      func(o outcome) string { return priorityLabels[o.Result.Priority()] },
			Near: func(o outcome) bool {
				return math.Abs(float64(int(o.Result.Priority())-o.Row.ExpectedUrgency)) <= 1
			},
		},
	}
}

// score counts correct answers out of n.
type score struct {
	Correct, N int
}

func (s score) rate() float64 {
	if s.N == 0 {
		return math.NaN()
	}
	return float64(s.Correct) / float64(s.N)
}

// accuracy scores q on the answered outcomes of outs.
func (q question) accuracy(outs []outcome) score {
	var s score
	for _, o := range answered(outs) {
		s.N++
		if q.Expected(o) == q.Got(o) {
			s.Correct++
		}
	}
	return s
}

// nearAccuracy scores q.Near on the answered outcomes of outs.
func (q question) nearAccuracy(outs []outcome) score {
	var s score
	for _, o := range answered(outs) {
		s.N++
		if q.Near(o) {
			s.Correct++
		}
	}
	return s
}

// confusion counts answers by expected label (row) and answer (column).
type confusion struct {
	Labels []string
	Counts [][]int
}

func (q question) confusion(outs []outcome) confusion {
	c := confusion{Labels: q.Labels, Counts: make([][]int, len(q.Labels))}
	for i := range c.Counts {
		c.Counts[i] = make([]int, len(q.Labels))
	}
	for _, o := range answered(outs) {
		e, g := slices.Index(q.Labels, q.Expected(o)), slices.Index(q.Labels, q.Got(o))
		if e >= 0 && g >= 0 {
			c.Counts[e][g]++
		}
	}
	return c
}

// decision is the status and reason a ticket would get, as triage.Processor
// assigns them: a failed call goes to needs_human too.
type decision struct {
	Status             ticket.Status
	Reason             ticket.Reason
	ComplaintUncertain bool
}

func decide(o outcome, t classifier.Thresholds) decision {
	switch {
	case errors.Is(o.Err, classifier.ErrPermanent):
		return decision{Status: ticket.StatusNeedsHuman, Reason: ticket.ReasonInvalidAnswer}
	case o.Err != nil:
		return decision{Status: ticket.StatusNeedsHuman, Reason: ticket.ReasonClassifierUnavailable}
	}
	status, reason, flags := classifier.Decide(o.Result, t)
	return decision{Status: status, Reason: reason, ComplaintUncertain: flags.ComplaintUncertain}
}

// sweepPoint is the outcome of one pair of confidence thresholds.
type sweepPoint struct {
	CategoryMin, UrgencyMin float64
	// Total is every row; NeedsHuman includes failed calls.
	Total, NeedsHuman int
	// Category, Priority and Complaint score the auto-classified rows.
	Category, Priority, Complaint score
}

// threshold returns grid step i of [0, 1]; i/20 is exact enough that the
// README start values 0.60 and 0.50 land on the grid.
func threshold(i int) float64 {
	return float64(i) / sweepSteps
}

// sweepAt decides every outcome with base and the given minimum confidences
// and scores the auto-classified ones.
func sweepAt(outs []outcome, qs []question, base classifier.Thresholds, categoryMin, urgencyMin float64) sweepPoint {
	t := base
	t.CategoryMinConfidence, t.UrgencyMinConfidence = categoryMin, urgencyMin
	var auto []outcome
	p := sweepPoint{CategoryMin: categoryMin, UrgencyMin: urgencyMin, Total: len(outs)}
	for _, o := range outs {
		if decide(o, t).Status == ticket.StatusClassified {
			auto = append(auto, o)
		} else {
			p.NeedsHuman++
		}
	}
	p.Category, p.Complaint, p.Priority = qs[0].accuracy(auto), qs[1].accuracy(auto), qs[2].accuracy(auto)
	return p
}

// sweepCategory varies CategoryMinConfidence over the grid with the other
// thresholds at base.
func sweepCategory(outs []outcome, qs []question, base classifier.Thresholds) []sweepPoint {
	points := make([]sweepPoint, 0, sweepSteps+1)
	for i := range sweepSteps + 1 {
		points = append(points, sweepAt(outs, qs, base, threshold(i), base.UrgencyMinConfidence))
	}
	return points
}

// sweepUrgency varies UrgencyMinConfidence over the grid with the other
// thresholds at base.
func sweepUrgency(outs []outcome, qs []question, base classifier.Thresholds) []sweepPoint {
	points := make([]sweepPoint, 0, sweepSteps+1)
	for i := range sweepSteps + 1 {
		points = append(points, sweepAt(outs, qs, base, base.CategoryMinConfidence, threshold(i)))
	}
	return points
}

// frontier sweeps the full grid of both thresholds and returns the points no
// other point beats: none sends fewer or as many rows to needs_human with as
// many or more correct auto-classified categories, and is better in one.
// Of points with equal results it keeps the first, the lowest thresholds. The
// result is ordered by needs_human share.
func frontier(outs []outcome, qs []question, base classifier.Thresholds) []sweepPoint {
	var grid []sweepPoint
	for i := range sweepSteps + 1 {
		for j := range sweepSteps + 1 {
			grid = append(grid, sweepAt(outs, qs, base, threshold(i), threshold(j)))
		}
	}
	// beats reports whether a is at least as good as b on both axes and better
	// on one. Accuracy compares rates; a point with no auto rows has none.
	acc := func(p sweepPoint) float64 {
		if r := p.Category.rate(); !math.IsNaN(r) {
			return r
		}
		return -1
	}
	beats := func(a, b sweepPoint) bool {
		return a.NeedsHuman <= b.NeedsHuman && acc(a) >= acc(b) && (a.NeedsHuman < b.NeedsHuman || acc(a) > acc(b))
	}
	var out []sweepPoint
	for i, p := range grid {
		dominated := slices.ContainsFunc(grid, func(q sweepPoint) bool { return beats(q, p) })
		duplicate := slices.ContainsFunc(grid[:i], func(q sweepPoint) bool {
			return q.NeedsHuman == p.NeedsHuman && q.Category == p.Category
		})
		if !dominated && !duplicate && p.Category.N > 0 {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b sweepPoint) int { return a.NeedsHuman - b.NeedsHuman })
	return out
}

// latencies returns the call latencies of the answered outcomes, sorted.
func latencies(outs []outcome) []time.Duration {
	var ds []time.Duration
	for _, o := range answered(outs) {
		ds = append(ds, o.Result.Call.Latency)
	}
	slices.Sort(ds)
	return ds
}

// percentile returns the nearest-rank p-th percentile (0 < p <= 100) of sorted
// ds, or 0 when ds is empty.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[min(max(rank, 1), len(sorted))-1]
}

// costs sums what the calls behind outs cost.
type costs struct {
	USD                       float64
	InputTokens, OutputTokens int
	// Sources counts outcomes by cost source; "" is unknown.
	Sources map[classifier.CostSource]int
}

func sumCosts(outs []outcome) costs {
	c := costs{Sources: make(map[classifier.CostSource]int)}
	for _, o := range outs {
		call := o.Result.Call
		c.USD += call.CostUSD
		c.InputTokens += call.InputTokens
		c.OutputTokens += call.OutputTokens
		c.Sources[call.CostSource]++
	}
	return c
}
