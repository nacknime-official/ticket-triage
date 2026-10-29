package shadow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// Store is the storage the Processor needs. It reads tickets but never
// writes them.
type Store interface {
	// Ticket returns the ticket with the id, or an error wrapping
	// ticket.ErrNotFound.
	Ticket(ctx context.Context, id uuid.UUID) (ticket.Ticket, error)
	// SpentSince returns the total classifier cost recorded at or after
	// since, live and shadow calls together.
	SpentSince(ctx context.Context, since time.Time) (float64, error)
	// Done reports whether the run already has a valid answer for the ticket.
	Done(ctx context.Context, runID string, id uuid.UUID) (bool, error)
	// Save stores r as the shadow row of its run and ticket, replacing an
	// earlier one.
	Save(ctx context.Context, r Result) error
}

// Outcome is the outcome of a shadow classification.
type Outcome string

const (
	OutcomeOK             Outcome = "ok"
	OutcomeTransientError Outcome = "transient_error"
	OutcomePermanentError Outcome = "permanent_error"
	OutcomeInvalidAnswer  Outcome = "invalid_answer"
)

// Result is the shadow classification of one ticket in one run.
type Result struct {
	RunID    string
	TicketID uuid.UUID
	Outcome  Outcome
	// Attempts is the number of classifier calls made.
	Attempts int
	// Answer is the answer and the metadata of the last call; its answers are
	// meaningful only when Outcome is OutcomeOK.
	Answer classifier.Result
	// CostUSD is the cost of all calls; CostSource is empty when no call had
	// a known cost.
	CostUSD    float64
	CostSource classifier.CostSource
	// Status and Reason are what the run's thresholds decide; empty unless
	// Outcome is OutcomeOK.
	Status             ticket.Status
	Reason             ticket.Reason
	ComplaintUncertain bool
	// LastError is the error of the last failed call, capped; never ticket text.
	LastError string
	At        time.Time
}

// IsComplaint is the complaint flag of an OutcomeOK result, decided as the
// live flow does: the more likely answer.
func (r Result) IsComplaint() bool {
	return r.Answer.ComplaintP >= 0.5
}

// ErrBudgetExhausted stops a run once the day's classifier spend reached the
// daily budget.
var ErrBudgetExhausted = errors.New("shadow: daily budget exhausted")

// runIDPattern keeps a run id usable in a consumer group name; the
// shadow_classifications table checks the same pattern.
var runIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidRunID reports whether id can name a run: 1 to 63 lower-case letters,
// digits, '.', '_' or '-', starting with a letter or digit.
func ValidRunID(id string) bool {
	return runIDPattern.MatchString(id)
}

// Defaults of Config, matching the live processor.
const (
	DefaultCallTimeout = 3 * time.Second
	DefaultMaxAttempts = 3
	DefaultBackoffBase = 200 * time.Millisecond
	DefaultBackoffMax  = 2 * time.Second
)

// maxLastErrorBytes caps the stored last_error.
const maxLastErrorBytes = 512

// Config configures a Processor. Thresholds and DailyBudgetUSD are used as
// they are; other zero fields take the defaults.
type Config struct {
	// RunID names the run; see ValidRunID.
	RunID      string
	Thresholds classifier.Thresholds
	// Taxonomy validates answers; nil means classifier.DefaultTaxonomy.
	Taxonomy classifier.Taxonomy
	// DailyBudgetUSD is the classifier spend per UTC day, shared with the
	// live flow. Once the recorded cost reaches it, Replay returns
	// ErrBudgetExhausted without a call.
	DailyBudgetUSD float64
	// DryRun skips the budget check, the Done check and Save: nothing is
	// written. It is meant for the fake classifier.
	DryRun bool
	// CallTimeout bounds each classifier call.
	CallTimeout time.Duration
	// MaxAttempts is the number of calls per ticket for transient errors.
	MaxAttempts int
	// BackoffBase is the wait before the second call; each later wait
	// doubles, up to BackoffMax. Waits are jittered to [d/2, d].
	BackoffBase time.Duration
	BackoffMax  time.Duration
}

func (c Config) withDefaults() Config {
	if c.Taxonomy == nil {
		c.Taxonomy = classifier.DefaultTaxonomy()
	}
	if c.CallTimeout <= 0 {
		c.CallTimeout = DefaultCallTimeout
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = DefaultBackoffBase
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = DefaultBackoffMax
	}
	return c
}

// Processor classifies tickets for one shadow run and collects its Report.
// It is safe for concurrent use.
type Processor struct {
	store Store
	cls   classifier.Classifier
	cfg   Config
	log   *slog.Logger

	mu     sync.Mutex
	report Report
}

// NewProcessor returns a Processor for the run of cfg that reads tickets from
// store and classifies them with cls.
func NewProcessor(store Store, cls classifier.Classifier, cfg Config, log *slog.Logger) (*Processor, error) {
	if !ValidRunID(cfg.RunID) {
		return nil, fmt.Errorf("shadow: invalid run id %q", cfg.RunID)
	}
	cfg = cfg.withDefaults()
	return &Processor{
		store: store, cls: cls, cfg: cfg, log: log,
		report: Report{RunID: cfg.RunID, DryRun: cfg.DryRun},
	}, nil
}

// Report returns a copy of the report of the tickets replayed so far.
func (p *Processor) Report() Report {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.report.clone()
}

// Replay classifies the ticket with the id for the run and stores the
// result: skip it when the run already has a valid answer, check the daily
// budget, call the classifier with a per-call timeout and in-process retries,
// apply the run's thresholds, and save the shadow row. A failed
// classification is saved too, with no decision, and is retried by a later
// run with the same id.
//
// A non-nil error means the record must not be committed: a storage failure,
// a cancelled ctx, ErrBudgetExhausted, or an error wrapping
// classifier.ErrAuth.
func (p *Processor) Replay(ctx context.Context, ticketID string) error {
	id, err := uuid.Parse(ticketID)
	if err != nil {
		p.log.WarnContext(ctx, "shadow: bad ticket id skipped",
			slog.String("run_id", p.cfg.RunID), slog.String("error", err.Error()))
		p.add(func(r *Report) { r.Missing++ })
		return nil
	}
	if !p.cfg.DryRun {
		done, err := p.store.Done(ctx, p.cfg.RunID, id)
		if err != nil {
			return fmt.Errorf("shadow: check ticket %s: %w", id, err)
		}
		if done {
			p.add(func(r *Report) { r.Skipped++ })
			return nil
		}
	}
	live, err := p.store.Ticket(ctx, id)
	if errors.Is(err, ticket.ErrNotFound) {
		p.log.WarnContext(ctx, "shadow: missing ticket skipped",
			slog.String("run_id", p.cfg.RunID), slog.String("ticket_id", id.String()))
		p.add(func(r *Report) { r.Missing++ })
		return nil
	}
	if err != nil {
		return fmt.Errorf("shadow: read ticket %s: %w", id, err)
	}
	if !p.cfg.DryRun {
		if err := p.checkBudget(ctx); err != nil {
			return err
		}
	}

	res, err := p.classify(ctx, live)
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("shadow: classify ticket %s: %w", id, context.Cause(ctx))
	case errors.Is(err, classifier.ErrAuth):
		return fmt.Errorf("shadow: classify ticket %s: %w", id, err)
	}
	res.At = time.Now().UTC()
	if !p.cfg.DryRun {
		if err := p.store.Save(ctx, res); err != nil {
			return fmt.Errorf("shadow: save ticket %s: %w", id, err)
		}
	}
	p.add(func(r *Report) { r.Add(live, res) })
	return nil
}

func (p *Processor) add(fn func(*Report)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(&p.report)
}

func (p *Processor) checkBudget(ctx context.Context) error {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	spent, err := p.store.SpentSince(ctx, today)
	if err != nil {
		return fmt.Errorf("shadow: read today's spend: %w", err)
	}
	if spent >= p.cfg.DailyBudgetUSD {
		return fmt.Errorf("%w: spent $%.8f of $%.8f", ErrBudgetExhausted, spent, p.cfg.DailyBudgetUSD)
	}
	return nil
}

// classify calls the classifier until it answers validly, fails with an error
// other than ErrTransient, or MaxAttempts calls were made, and returns the
// result of the run with the last error. An auth error and a cancelled ctx
// are returned for the caller to stop on; other failures become the Outcome.
func (p *Processor) classify(ctx context.Context, t ticket.Ticket) (Result, error) {
	in := classifier.Input{Subject: t.Subject, Body: t.Body}
	r := Result{RunID: p.cfg.RunID, TicketID: t.ID}
	for i := 0; ; i++ {
		ans, err := p.call(ctx, in)
		if err == nil {
			err = ans.Validate(p.cfg.Taxonomy)
		}
		r.Attempts++
		r.Answer = ans
		if ans.Call.CostSource != "" {
			r.CostUSD += ans.Call.CostUSD
			if r.CostSource == "" || ans.Call.CostSource == classifier.CostEstimated {
				r.CostSource = ans.Call.CostSource
			}
		}
		r.Outcome = outcome(err)
		p.logCall(ctx, t.ID, r.Attempts, ans.Call, err)

		if err != nil {
			r.LastError = capError(err)
		}
		if err == nil || !errors.Is(err, classifier.ErrTransient) || ctx.Err() != nil || i+1 >= p.cfg.MaxAttempts {
			if err == nil {
				status, reason, flags := classifier.Decide(ans, p.cfg.Thresholds)
				r.Status, r.Reason, r.ComplaintUncertain = status, reason, flags.ComplaintUncertain
				r.LastError = ""
			}
			return r, err
		}
		if err := sleep(ctx, p.backoff(i)); err != nil {
			return r, fmt.Errorf("shadow: wait to retry: %w", err)
		}
	}
}

// call makes one classifier call under the per-call timeout.
func (p *Processor) call(ctx context.Context, in classifier.Input) (classifier.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.CallTimeout)
	defer cancel()
	start := time.Now()
	res, err := p.cls.Classify(ctx, in)
	if res.Call.Latency == 0 {
		res.Call.Latency = time.Since(start)
	}
	if res.Call.Provider == "" {
		res.Call.Provider = "unknown"
	}
	if err != nil && ctx.Err() != nil && !errors.Is(err, classifier.ErrTransient) {
		// A call cut off by its timeout is transient whatever the classifier
		// made of the cancelled request.
		err = fmt.Errorf("%w: %w", classifier.ErrTransient, err)
	}
	return res, err
}

// backoff returns the jittered wait after the call with index i.
func (p *Processor) backoff(i int) time.Duration {
	d := p.cfg.BackoffBase << i
	if d <= 0 || d > p.cfg.BackoffMax {
		d = p.cfg.BackoffMax
	}
	return d/2 + rand.N(d/2+1)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func outcome(err error) Outcome {
	switch {
	case err == nil:
		return OutcomeOK
	case errors.Is(err, classifier.ErrInvalidAnswer):
		return OutcomeInvalidAnswer
	case errors.Is(err, classifier.ErrTransient):
		return OutcomeTransientError
	}
	return OutcomePermanentError
}

// capError is the message of err capped to maxLastErrorBytes on a rune
// boundary. Classifier errors never carry ticket text.
func capError(err error) string {
	s := err.Error()
	if len(s) <= maxLastErrorBytes {
		return s
	}
	s = s[:maxLastErrorBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// logCall writes the per-call log line of README §4.3, marked with the run.
func (p *Processor) logCall(ctx context.Context, id uuid.UUID, attempt int, c classifier.Call, err error) {
	level := slog.LevelInfo
	attrs := []slog.Attr{
		slog.String("run_id", p.cfg.RunID),
		slog.String("provider", c.Provider),
		slog.String("model", c.Model),
		slog.String("ticket_id", id.String()),
		slog.Int("attempt", attempt),
		slog.Int64("latency_ms", c.Latency.Milliseconds()),
		slog.Int("input_tokens", c.InputTokens),
		slog.Int("output_tokens", c.OutputTokens),
		slog.Float64("cost_usd", c.CostUSD),
		slog.String("cost_source", string(c.CostSource)),
		slog.String("outcome", string(outcome(err))),
		slog.Int("http_status", c.HTTPStatus),
	}
	if err != nil {
		level = slog.LevelWarn
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	p.log.LogAttrs(ctx, level, "shadow classifier call", attrs...)
}
