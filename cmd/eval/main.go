// Command eval runs a classifier over the labeled eval set in testdata/eval and
// writes a markdown report (README §8): per-question accuracy and confusion
// matrices split by language, the decisions at the configured thresholds,
// p50/p95 latency, cost, and a sweep of the confidence thresholds. It can also
// write one JSON line per ticket with the answers and call metadata, without
// the ticket text.
//
// Usage, from the repository root:
//
//	go run ./cmd/eval                       # fake classifier, no key, no network
//	go run ./cmd/eval -classifier jev -env-file ~/.config/ticket-triage/.env
//
// Configuration comes from the environment as in the service (CLASSIFIER,
// JEV_*, OPENROUTER_API_KEY, the threshold variables); -env-file fills in
// variables the environment leaves unset. Jev answers are cached per ticket and
// model in a git-ignored file, so re-rendering costs nothing. Never run the
// live classifier from tests or CI.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/classifier/jev"
	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/evalset"
	"github.com/nacknime-official/ticket-triage/internal/lifecycle"
	"github.com/nacknime-official/ticket-triage/internal/logging"
)

const (
	defaultCache   = ".cache/eval/results.jsonl"
	defaultMaxCost = 0.05
	// The live report and the fake pipeline check live side by side.
	defaultLiveOut = "docs/eval.md"
	defaultFakeOut = "docs/eval-fake.md"
	defaultRetries = 2
	retryBackoff   = time.Second
)

func main() {
	ctx, stop := lifecycle.SignalContext(context.Background())
	err := run(ctx, os.Args[1:], os.Getenv, time.Now, os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "eval:", err)
		os.Exit(1)
	}
}

// options are the command-line flags.
type options struct {
	classifier         string
	envFile            string
	data               string
	out                string
	jsonl              string
	cache              string
	refresh            bool
	concurrency        int
	retries            int
	limit              int
	maxCost            float64
	complaintThreshold float64
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.classifier, "classifier", "", "fake or jev; overrides CLASSIFIER (default fake)")
	fs.StringVar(&o.envFile, "env-file", "", "KEY=VALUE file for variables the environment leaves unset, e.g. the API key")
	fs.StringVar(&o.data, "data", "testdata/eval", "directory of eval set *.jsonl files")
	fs.StringVar(&o.out, "out", "", "markdown report path (default "+defaultLiveOut+" for jev, "+defaultFakeOut+" for the fake)")
	fs.StringVar(&o.jsonl, "jsonl", "", "optional per-ticket results JSONL path, kept outside the repository")
	fs.StringVar(&o.cache, "cache", defaultCache, "git-ignored cache of answers per ticket and model; empty disables it; "+
		"not used by the fake, which is free and deterministic")
	fs.BoolVar(&o.refresh, "refresh", false, "call the classifier even for cached answers and overwrite them")
	fs.IntVar(&o.concurrency, "concurrency", 0, "rows classified at once (default JEV_MAX_CONCURRENCY); "+
		"the adapter still applies JEV_RATE_LIMIT")
	fs.IntVar(&o.retries, "retries", defaultRetries, "extra attempts after a transient error")
	fs.IntVar(&o.limit, "limit", 0, "classify only the first N rows; 0 means all")
	fs.Float64Var(&o.maxCost, "max-cost", defaultMaxCost, "stop once this run's calls cost more than this many USD; 0 means no cap")
	fs.Float64Var(&o.complaintThreshold, "complaint-threshold", 0.5, "P(complaint) at or above which a ticket counts as a complaint")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch {
	case fs.NArg() != 0:
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	case o.concurrency < 0:
		return o, fmt.Errorf("-concurrency %d is negative", o.concurrency)
	case o.retries < 0:
		return o, fmt.Errorf("-retries %d is negative", o.retries)
	case o.limit < 0:
		return o, fmt.Errorf("-limit %d is negative", o.limit)
	case !(o.maxCost >= 0) || math.IsInf(o.maxCost, 0):
		return o, fmt.Errorf("-max-cost %v is not a number >= 0", o.maxCost)
	case !(o.complaintThreshold >= 0 && o.complaintThreshold <= 1):
		return o, fmt.Errorf("-complaint-threshold %v is outside [0, 1]", o.complaintThreshold)
	}
	return o, nil
}

func run(ctx context.Context, args []string, getenv func(string) string, now func() time.Time,
	stdout, stderr io.Writer) error {
	o, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	if o.envFile != "" {
		file, err := readEnvFile(o.envFile)
		if err != nil {
			return err
		}
		getenv = withFallback(getenv, file)
	}
	if o.classifier != "" {
		getenv = withOverride(getenv, "CLASSIFIER", o.classifier)
	}

	l := config.NewLoader(getenv)
	logCfg, clfCfg, thresholds := l.Log(), l.Classifier(), l.Thresholds()
	if err := l.Err(); err != nil {
		return err
	}
	logger := logging.New(stderr, logCfg.Level)

	rows, files, err := loadRows(o.data)
	if err != nil {
		return err
	}
	unreviewed, err := labelsUnreviewed(o.data)
	if err != nil {
		return err
	}
	if o.limit > 0 && o.limit < len(rows) {
		rows = rows[:o.limit]
	}

	tax := classifier.DefaultTaxonomy()
	r := &runner{
		tax: tax, refresh: o.refresh, concurrency: o.concurrency, retries: o.retries, backoff: retryBackoff,
		maxCostUSD: o.maxCost,
	}
	out := o.out
	switch clfCfg.Kind {
	case config.ClassifierJev:
		r.clf, err = jev.New(jev.Config{
			Provider:             clfCfg.Jev.Provider,
			APIKey:               clfCfg.Jev.APIKey.Reveal(),
			Model:                clfCfg.Jev.Model,
			Timeout:              clfCfg.Jev.Timeout,
			RateLimit:            clfCfg.Jev.RateLimit,
			MaxConcurrency:       clfCfg.Jev.MaxConcurrency,
			InputPricePerMTokUSD: clfCfg.Jev.InputPricePerMTokUSD,
			Taxonomy:             tax,
			Logger:               logger,
		})
		if err != nil {
			return err
		}
		r.provider, r.model = clfCfg.Jev.Provider, clfCfg.Jev.Model
		if out == "" {
			out = defaultLiveOut
		}
		if o.cache != "" {
			c, err := openCache(o.cache)
			if err != nil {
				return err
			}
			defer c.Close()
			if c.Skipped > 0 {
				logger.Warn("cache lines skipped", slog.String("path", o.cache), slog.Int("lines", c.Skipped))
			}
			r.cache = c
		}
	default:
		r.clf, r.provider, r.model = new(classifier.Fake), classifier.FakeProvider, classifier.FakeModel
		if out == "" {
			out = defaultFakeOut
		}
	}
	if r.concurrency == 0 {
		r.concurrency = clfCfg.Jev.MaxConcurrency
	}

	outs, err := r.run(ctx, rows)
	if err != nil {
		return err
	}

	meta := reportMeta{
		Kind:               clfCfg.Kind,
		Provider:           r.provider,
		Model:              r.model,
		Files:              files,
		Limit:              o.limit,
		LabelsUnreviewed:   unreviewed,
		Thresholds:         toThresholds(thresholds),
		ComplaintThreshold: o.complaintThreshold,
	}
	// The fake report stays reproducible byte for byte, so only a live run is dated.
	if meta.Kind != config.ClassifierFake {
		meta.Date = now().UTC().Format(time.DateOnly)
	}
	if err := writeFile(out, render(meta, outs)); err != nil {
		return err
	}
	if o.jsonl != "" {
		b, err := resultLines(outs, meta)
		if err != nil {
			return err
		}
		if err := writeFile(o.jsonl, b); err != nil {
			return err
		}
	}

	calls, cached, failed := 0, 0, 0
	for _, x := range outs {
		calls += x.Attempts
		if x.Cached {
			cached++
		}
		if x.Err != nil {
			failed++
		}
	}
	_, err = fmt.Fprintf(stdout, "wrote %s: %d rows, %d calls, %d from cache, %d failed\n",
		out, len(outs), calls, cached, failed)
	return err
}

func toThresholds(t config.Thresholds) classifier.Thresholds {
	return classifier.Thresholds{
		CategoryMinConfidence:  t.CategoryMinConfidence,
		UrgencyMinConfidence:   t.UrgencyMinConfidence,
		ComplaintUncertainLow:  t.ComplaintUncertainLow,
		ComplaintUncertainHigh: t.ComplaintUncertainHigh,
		RouteOtherToHuman:      t.RouteOtherToHuman,
	}
}

// loadRows reads every *.jsonl file in dir, in name order, and checks that
// row ids are unique across files.
func loadRows(dir string) ([]evalset.Row, []dataFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no *.jsonl files in %s", dir)
	}
	var rows []evalset.Row
	var files []dataFile
	seen := make(map[string]string)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		fileRows, err := evalset.Read(bytes.NewReader(b))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		name := filepath.Base(p)
		for _, row := range fileRows {
			if other, ok := seen[row.ID]; ok {
				return nil, nil, fmt.Errorf("%s: row id %q is also in %s", name, row.ID, other)
			}
			seen[row.ID] = name
		}
		sum := sha256.Sum256(b)
		files = append(files, dataFile{Name: name, Rows: len(fileRows), SHA256: hex.EncodeToString(sum[:])})
		rows = append(rows, fileRows...)
	}
	return rows, files, nil
}

// unreviewedMarker is the status note testdata/eval/README.md carries until a
// human has reviewed the labels; the review replaces it.
const unreviewedMarker = "labels are unreviewed drafts"

// labelsUnreviewed reports whether the README.md next to the eval set still
// carries unreviewedMarker. A missing README counts as unreviewed.
func labelsUnreviewed(dir string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return bytes.Contains(bytes.ToLower(b), []byte(unreviewedMarker)), nil
}

// resultLine is one line of the -jsonl output. It never holds ticket text.
type resultLine struct {
	ID                 string                `json:"id"`
	Source             string                `json:"source"`
	Lang               string                `json:"lang"`
	Tags               []string              `json:"tags,omitempty"`
	ExpectedCategory   string                `json:"expected_category"`
	ExpectedComplaint  bool                  `json:"expected_complaint"`
	ExpectedUrgency    int                   `json:"expected_urgency"`
	Error              string                `json:"error,omitempty"`
	Category           string                `json:"category,omitempty"`
	CategoryConfidence float64               `json:"category_confidence"`
	ComplaintP         float64               `json:"complaint_p"`
	Urgency            float64               `json:"urgency"`
	UrgencyConfidence  float64               `json:"urgency_confidence"`
	Priority           int                   `json:"priority"`
	Status             string                `json:"status"`
	Reason             string                `json:"reason,omitempty"`
	ComplaintUncertain bool                  `json:"complaint_uncertain"`
	Cached             bool                  `json:"cached"`
	Attempts           int                   `json:"attempts"`
	Provider           string                `json:"provider"`
	Model              string                `json:"model"`
	ServedModel        string                `json:"served_model,omitempty"`
	HTTPStatus         int                   `json:"http_status,omitzero"`
	LatencyMS          float64               `json:"latency_ms"`
	InputTokens        int                   `json:"input_tokens"`
	OutputTokens       int                   `json:"output_tokens"`
	CostUSD            float64               `json:"cost_usd"`
	CostSource         classifier.CostSource `json:"cost_source,omitempty"`
	RawResponse        jsontext.Value        `json:"raw_response,omitempty"`
}

func resultLines(outs []outcome, m reportMeta) ([]byte, error) {
	var b bytes.Buffer
	for _, o := range outs {
		res, call := o.Result, o.Result.Call
		d := decide(o, m.Thresholds)
		line := resultLine{
			ID:                o.Row.ID,
			Source:            o.Row.Source,
			Lang:              o.Row.Lang,
			Tags:              o.Row.Tags,
			ExpectedCategory:  o.Row.ExpectedCategory,
			ExpectedComplaint: o.Row.ExpectedComplaint,
			ExpectedUrgency:   o.Row.ExpectedUrgency,
			Status:            string(d.Status),
			Reason:            string(d.Reason),
			Cached:            o.Cached,
			Attempts:          o.Attempts,
			Provider:          m.Provider,
			Model:             m.Model,
			ServedModel:       call.Model,
			HTTPStatus:        call.HTTPStatus,
			LatencyMS:         float64(call.Latency.Microseconds()) / 1000,
			InputTokens:       call.InputTokens,
			OutputTokens:      call.OutputTokens,
			CostUSD:           call.CostUSD,
			CostSource:        call.CostSource,
		}
		if raw := jsontext.Value(call.RawResponse); raw.IsValid() {
			line.RawResponse = raw
		}
		if o.Err != nil {
			line.Error = o.Err.Error()
		} else {
			line.Category = res.Category
			line.CategoryConfidence = res.CategoryConfidence
			line.ComplaintP = res.ComplaintP
			line.Urgency = res.Urgency
			line.UrgencyConfidence = res.UrgencyConfidence
			line.Priority = int(res.Priority())
			line.ComplaintUncertain = d.ComplaintUncertain
		}
		if err := json.MarshalWrite(&b, line); err != nil {
			return nil, fmt.Errorf("encode result %s: %w", o.Row.ID, err)
		}
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

func writeFile(path string, b []byte) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, b, 0o644)
}

// readEnvFile reads KEY=VALUE lines, skipping blank lines and # comments and
// stripping an optional "export " and matching quotes around the value.
// Errors name the line number only: a line may hold a secret.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	vars := make(map[string]string)
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("%s: line %d is not KEY=VALUE", path, n)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		vars[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return vars, nil
}

// withFallback returns getenv, falling back to vars for unset or blank names.
func withFallback(getenv func(string) string, vars map[string]string) func(string) string {
	return func(name string) string {
		if v := getenv(name); strings.TrimSpace(v) != "" {
			return v
		}
		return vars[name]
	}
}

// withOverride returns getenv with name set to value.
func withOverride(getenv func(string) string, name, value string) func(string) string {
	return func(n string) string {
		if n == name {
			return value
		}
		return getenv(n)
	}
}
