package main

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// dataFile describes one eval set file the report was computed on.
type dataFile struct {
	Name   string
	Rows   int
	SHA256 string
}

// reportMeta is everything about a run the report states besides outcomes.
type reportMeta struct {
	Kind     string // config.ClassifierFake or config.ClassifierJev
	Provider string
	Model    string // the configured model
	// Date is the UTC date of a live run; empty for the fake.
	Date string
	// LabelsUnreviewed says the eval set labels are still agent drafts.
	LabelsUnreviewed bool
	Files            []dataFile
	// Limit is the row limit of the run, 0 for all rows.
	Limit              int
	Thresholds         classifier.Thresholds
	ComplaintThreshold float64
}

// The commands that reproduce a report. liveCommand reads the key from the
// file named in the PR, so the key never reaches the shell history.
const (
	fakeCommand = "go run ./cmd/eval  # writes " + defaultFakeOut
	liveCommand = "go run ./cmd/eval -classifier jev -env-file ~/.config/ticket-triage/.env \\\n" +
		"  -jsonl ~/.cache/ticket-triage/eval-results.jsonl"
)

// render writes the markdown report.
func render(m reportMeta, outs []outcome) []byte {
	var b bytes.Buffer
	qs := questions(m.ComplaintThreshold)
	langs := byLang(outs)

	b.WriteString("# Eval report\n\n")
	if m.Kind == config.ClassifierFake {
		b.WriteString("> **Fake classifier: a pipeline check, not a quality number.** This report was produced with\n" +
			"> the deterministic keyword fake (`internal/classifier/fake.go`), not with Jev. It shows that the eval\n" +
			"> pipeline runs end to end; its accuracy says nothing about Jev. The live Jev report is\n" +
			"> `" + defaultLiveOut + "`.\n\n")
	} else {
		fmt.Fprintf(&b, "Live run of `%s` (served as %s) through `%s` on %s, over the eval set in `testdata/eval/`.\n\n",
			m.Model, code(servedModels(outs)), m.Provider, m.Date)
	}
	if m.LabelsUnreviewed {
		b.WriteString("> **Labels are unreviewed drafts.** An agent drafted every label and no human has reviewed them yet\n" +
			"> (`testdata/eval/README.md`), so every accuracy number here is provisional.\n\n")
	} else {
		b.WriteString("Labels come from `testdata/eval/`; its README names the reviewer.\n\n")
	}

	renderRun(&b, m, outs, langs)
	renderAccuracy(&b, qs, langs, outs)
	renderConfusion(&b, qs, langs)
	renderDecisions(&b, m, langs)
	renderLatency(&b, m, langs)
	renderCost(&b, outs)
	renderSweep(&b, m, qs, outs)
	renderReproduce(&b)
	return b.Bytes()
}

func renderRun(b *bytes.Buffer, m reportMeta, outs []outcome, langs []group) {
	b.WriteString("## Run\n\n")
	files := make([]string, len(m.Files))
	for i, f := range m.Files {
		files[i] = fmt.Sprintf("`%s` (%d rows, sha256 `%.12s`)", f.Name, f.Rows, f.SHA256)
	}
	var perLang []string
	for _, g := range langs[1:] {
		perLang = append(perLang, fmt.Sprintf("%s %d", g.Name, len(g.Outcomes)))
	}
	rows := fmt.Sprintf("%d (%s)", len(outs), strings.Join(perLang, ", "))
	if m.Limit > 0 {
		rows += fmt.Sprintf(", limited to the first %d", m.Limit)
	}
	served := servedModels(outs)
	t := m.Thresholds
	table(b, []string{"Setting", "Value"}, [][]string{
		{"Classifier", fmt.Sprintf("`%s` (provider `%s`, model `%s`)", m.Kind, m.Provider, m.Model)},
		{"Date", cmp.Or(m.Date, "—")},
		{"Served model", code(served)},
		{"Eval set", strings.Join(files, ", ")},
		{"Rows", rows},
		{"Answered", fmt.Sprintf("%d; failed %d", len(answered(outs)), len(outs)-len(answered(outs)))},
		{"From cache", fmt.Sprintf("%d", len(filter(outs, func(o outcome) bool { return o.Cached })))},
		{"Thresholds", fmt.Sprintf("CategoryMinConfidence %.2f, UrgencyMinConfidence %.2f, "+
			"ComplaintUncertainBand %.2f–%.2f, RouteOtherToHuman %t",
			t.CategoryMinConfidence, t.UrgencyMinConfidence, t.ComplaintUncertainLow, t.ComplaintUncertainHigh,
			t.RouteOtherToHuman)},
		{"Complaint decision", fmt.Sprintf("P(yes) ≥ %.2f counts as a complaint", m.ComplaintThreshold)},
	})
	if errs := errorCounts(outs); len(errs) > 0 {
		b.WriteString("Failed calls are left out of accuracy, confusion matrices and latency, and count as\n" +
			"`needs_human` in the decisions and the sweep.\n\n")
		table(b, []string{"Error", "Rows"}, errs)
	}
}

func servedModels(outs []outcome) []string {
	var models []string
	for _, o := range answered(outs) {
		if m := o.Result.Call.Model; m != "" && !slices.Contains(models, m) {
			models = append(models, m)
		}
	}
	slices.Sort(models)
	return models
}

func code(items []string) string {
	if len(items) == 0 {
		return "—"
	}
	return "`" + strings.Join(items, "`, `") + "`"
}

// errorCounts counts failed rows by error text. Adapter errors carry no
// ticket text.
func errorCounts(outs []outcome) [][]string {
	counts := make(map[string]int)
	for _, o := range outs {
		if o.Err != nil {
			counts[o.Err.Error()]++
		}
	}
	var rows [][]string
	for _, e := range slices.Sorted(maps.Keys(counts)) {
		rows = append(rows, []string{"`" + strings.ReplaceAll(e, "|", `\|`) + "`", fmt.Sprintf("%d", counts[e])})
	}
	return rows
}

func renderAccuracy(b *bytes.Buffer, qs []question, langs []group, outs []outcome) {
	b.WriteString("## Accuracy\n\n")
	b.WriteString("Share of answered rows whose answer matches the label. Priority is the urgency score rounded to\n" +
		"the nearest level; \"within one level\" also counts an answer one level off.\n\n")
	header := []string{"Question"}
	for _, g := range langs {
		header = append(header, g.Name)
	}
	var rows [][]string
	for _, q := range qs {
		row := []string{q.Name}
		for _, g := range langs {
			row = append(row, pct(q.accuracy(g.Outcomes)))
		}
		rows = append(rows, row)
		if q.Near != nil {
			row := []string{q.Name + ", within one level"}
			for _, g := range langs {
				row = append(row, pct(q.nearAccuracy(g.Outcomes)))
			}
			rows = append(rows, row)
		}
	}
	table(b, header, rows)

	tags := byTag(outs)
	if len(tags) == 0 {
		return
	}
	b.WriteString("### By hard-case tag\n\n")
	b.WriteString("A row can carry several tags.\n\n")
	header = []string{"Tag", "Rows"}
	for _, q := range qs {
		header = append(header, q.Name)
	}
	rows = nil
	for _, g := range tags {
		row := []string{"`" + g.Name + "`", fmt.Sprintf("%d", len(g.Outcomes))}
		for _, q := range qs {
			row = append(row, pct(q.accuracy(g.Outcomes)))
		}
		rows = append(rows, row)
	}
	table(b, header, rows)
}

func renderConfusion(b *bytes.Buffer, qs []question, langs []group) {
	b.WriteString("## Confusion matrices\n\n")
	b.WriteString("Rows are the label, columns the answer; answered rows only.\n\n")
	for _, q := range qs {
		fmt.Fprintf(b, "### %s\n\n", q.Name)
		for _, g := range langs {
			fmt.Fprintf(b, "#### %s %s\n\n", q.Name, g.Name)
			c := q.confusion(g.Outcomes)
			header := append([]string{"label \\ answer"}, c.Labels...)
			rows := make([][]string, len(c.Labels))
			for i, l := range c.Labels {
				rows[i] = []string{l}
				for _, n := range c.Counts[i] {
					rows[i] = append(rows[i], fmt.Sprintf("%d", n))
				}
			}
			table(b, header, rows)
		}
	}
}

func renderDecisions(b *bytes.Buffer, m reportMeta, langs []group) {
	b.WriteString("## Decisions at the configured thresholds\n\n")
	b.WriteString("`classifier.Decide` on each answer; a failed call goes to `needs_human` as in the processor.\n\n")
	type key struct {
		Status ticket.Status
		Reason ticket.Reason
	}
	counts := make([]map[key]int, len(langs))
	uncertain := make([]int, len(langs))
	var keys []key
	for i, g := range langs {
		counts[i] = make(map[key]int)
		for _, o := range g.Outcomes {
			d := decide(o, m.Thresholds)
			k := key{d.Status, d.Reason}
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
			counts[i][k]++
			if d.ComplaintUncertain {
				uncertain[i]++
			}
		}
	}
	slices.SortFunc(keys, func(a, b key) int {
		return cmp.Or(cmp.Compare(a.Status, b.Status), cmp.Compare(a.Reason, b.Reason))
	})
	header := []string{"Status", "Reason"}
	for _, g := range langs {
		header = append(header, g.Name)
	}
	var rows [][]string
	for _, k := range keys {
		row := []string{"`" + string(k.Status) + "`", reasonCell(k.Reason)}
		for i, g := range langs {
			row = append(row, share(counts[i][k], len(g.Outcomes)))
		}
		rows = append(rows, row)
	}
	row := []string{"flag", "`complaint_uncertain`"}
	for i, g := range langs {
		row = append(row, share(uncertain[i], len(g.Outcomes)))
	}
	table(b, header, append(rows, row))
}

func reasonCell(r ticket.Reason) string {
	if r == "" {
		return "—"
	}
	return "`" + string(r) + "`"
}

func renderLatency(b *bytes.Buffer, m reportMeta, langs []group) {
	b.WriteString("## Latency\n\n")
	b.WriteString("Latency of the call behind each answered row, as the classifier measured it; a cached row keeps\n" +
		"the latency of its original call.")
	if m.Kind == config.ClassifierFake {
		b.WriteString(" The fake makes no call and reports 0.")
	}
	b.WriteString("\n\n")
	var rows [][]string
	for _, g := range langs {
		ds := latencies(g.Outcomes)
		rows = append(rows, []string{g.Name, fmt.Sprintf("%d", len(ds)),
			ms(percentile(ds, 50)), ms(percentile(ds, 95)), ms(percentile(ds, 100))})
	}
	table(b, []string{"Language", "Answered", "p50", "p95", "max"}, rows)
}

func renderCost(b *bytes.Buffer, outs []outcome) {
	b.WriteString("## Cost\n\n")
	b.WriteString("What the calls behind these answers cost, failed calls included; a cached row counts the cost\n" +
		"recorded when it was first classified.\n\n")
	c := sumCosts(outs)
	perTicket := 0.0
	if len(outs) > 0 {
		perTicket = c.USD / float64(len(outs))
	}
	var sources []string
	for _, s := range slices.Sorted(maps.Keys(c.Sources)) {
		name := string(s)
		if name == "" {
			name = "unknown"
		}
		sources = append(sources, fmt.Sprintf("%s %d", name, c.Sources[s]))
	}
	table(b, []string{"Total", "Per ticket", "Input tokens", "Output tokens", "Cost source"}, [][]string{{
		fmt.Sprintf("$%.6f", c.USD), fmt.Sprintf("$%.8f", perTicket),
		fmt.Sprintf("%d", c.InputTokens), fmt.Sprintf("%d", c.OutputTokens), strings.Join(sources, ", "),
	}})
}

func renderSweep(b *bytes.Buffer, m reportMeta, qs []question, outs []outcome) {
	t := m.Thresholds
	b.WriteString("## Threshold sweep\n\n")
	b.WriteString("Each line decides every row again with `classifier.Decide` at other minimum confidences, the\n" +
		"remaining thresholds as configured. `needs_human` is the share of all rows sent to a person, failed calls\n" +
		"included; the accuracy columns score only the auto-classified rows. Bold marks the configured value.\n\n")

	fmt.Fprintf(b, "### CategoryMinConfidence (UrgencyMinConfidence %.2f)\n\n", t.UrgencyMinConfidence)
	sweepTable(b, "CategoryMinConfidence", sweepCategory(outs, qs, t),
		func(p sweepPoint) float64 { return p.CategoryMin }, t.CategoryMinConfidence)

	fmt.Fprintf(b, "### UrgencyMinConfidence (CategoryMinConfidence %.2f)\n\n", t.CategoryMinConfidence)
	sweepTable(b, "UrgencyMinConfidence", sweepUrgency(outs, qs, t),
		func(p sweepPoint) float64 { return p.UrgencyMin }, t.UrgencyMinConfidence)

	b.WriteString("### Best trade-offs on the full grid\n\n")
	b.WriteString("Both thresholds over the 0.05 grid; a pair is listed when no other pair sends as few or fewer rows to\n" +
		"`needs_human` with as high or higher category accuracy on the auto-classified rows. Pairs with the same\n" +
		"result are shown once, with the lowest thresholds.\n\n")
	var rows [][]string
	for _, p := range frontier(outs, qs, t) {
		rows = append(rows, append([]string{fmt.Sprintf("%.2f", p.CategoryMin), fmt.Sprintf("%.2f", p.UrgencyMin)},
			sweepCells(p)...))
	}
	table(b, append([]string{"CategoryMinConfidence", "UrgencyMinConfidence"}, sweepHeader...), rows)
}

var sweepHeader = []string{"needs_human", "Auto-classified", "Category", "Priority", "Complaint"}

func sweepCells(p sweepPoint) []string {
	return []string{
		share(p.NeedsHuman, p.Total), fmt.Sprintf("%d", p.Total-p.NeedsHuman),
		pct(p.Category), pct(p.Priority), pct(p.Complaint),
	}
}

func sweepTable(b *bytes.Buffer, name string, points []sweepPoint, value func(sweepPoint) float64, configured float64) {
	var rows [][]string
	for _, p := range points {
		v := fmt.Sprintf("%.2f", value(p))
		if v == fmt.Sprintf("%.2f", configured) {
			v = "**" + v + "**"
		}
		rows = append(rows, append([]string{v}, sweepCells(p)...))
	}
	table(b, append([]string{name}, sweepHeader...), rows)
}

func renderReproduce(b *bytes.Buffer) {
	b.WriteString("## Reproduce\n\n")
	b.WriteString("From the repository root. The fake run needs no key and makes no network call:\n\n")
	fmt.Fprintf(b, "```sh\n%s\n```\n\n", fakeCommand)
	b.WriteString("The live run calls Jev once per row (about 100 calls, a fraction of a cent at the README §4.4 price)\n" +
		"with the key from `~/.config/ticket-triage/.env`, stops if the calls cost more than `-max-cost` ($0.05),\n" +
		"and writes `" + defaultLiveOut + "`:\n\n")
	fmt.Fprintf(b, "```sh\n%s\n```\n\n", liveCommand)
	b.WriteString("Answers are cached per ticket and model in `.cache/eval/results.jsonl` (git-ignored), so re-rendering\n" +
		"the report or re-running the sweep costs nothing. `-refresh` calls Jev again, for example after the adapter\n" +
		"or its questions change. `-limit 5` makes a small first run. `go run ./cmd/eval -h` lists every flag.\n")
}

// table writes a markdown table.
func table(b *bytes.Buffer, header []string, rows [][]string) {
	b.WriteString("| " + strings.Join(header, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(header)) + "\n")
	for _, r := range rows {
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
	}
	b.WriteString("\n")
}

func pct(s score) string {
	r := s.rate()
	if math.IsNaN(r) {
		return "—"
	}
	return fmt.Sprintf("%.1f%% (%d/%d)", 100*r, s.Correct, s.N)
}

func share(n, total int) string {
	return pct(score{Correct: n, N: total})
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%d ms", d.Milliseconds())
}
