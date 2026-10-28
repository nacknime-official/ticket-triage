package triage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// Store is the storage the Processor needs.
type Store interface {
	// Ticket returns the ticket with the id, or an error wrapping
	// ticket.ErrNotFound. It is the cheap read before any classifier call.
	Ticket(ctx context.Context, id uuid.UUID) (ticket.Ticket, error)
	// SpentSince returns the total cost_usd of the classifier calls recorded
	// at or after since.
	SpentSince(ctx context.Context, since time.Time) (float64, error)
	// Apply writes u in one transaction: a conditional update of the ticket
	// that matches only while it is still pending, then one audit row per
	// call. It reports false, having written nothing, when the ticket was no
	// longer pending. The status event of a final update joins this
	// transaction too (README §2, step 4).
	Apply(ctx context.Context, u Update) (bool, error)
}

// Update is what one Process run writes to a pending ticket.
type Update struct {
	TicketID uuid.UUID
	// Status is the new status. StatusPending keeps the ticket pending and
	// records only the calls, attempts and last error.
	Status ticket.Status
	// Reason is set exactly when Status is needs_human.
	Reason ticket.Reason
	// Classification is nil when there is no usable answer.
	Classification *ticket.Classification
	// Calls are the classifier calls of this run in order; attempts grows by
	// len(Calls).
	Calls []Call
	// LastError is the last call error of this run; empty keeps the stored one.
	LastError string
	// At is the update time; it is also classified_at for a final status.
	At time.Time
}

// CallOutcome is the outcome of one classifier call, as stored in the
// classifications audit table.
type CallOutcome string

const (
	CallOK             CallOutcome = "ok"
	CallTransientError CallOutcome = "transient_error"
	CallPermanentError CallOutcome = "permanent_error"
	CallAuthError      CallOutcome = "auth_error"
	CallInvalidAnswer  CallOutcome = "invalid_answer"
)

// Call is one classifier call for the audit table.
type Call struct {
	// Attempt numbers the call among all calls for the ticket, from 1.
	Attempt int
	Outcome CallOutcome
	// Err is the call error; nil when Outcome is CallOK.
	Err error
	// Result holds the call metadata; its answers are meaningful only when
	// Outcome is CallOK.
	Result classifier.Result
}

// Kind tells the transport what to do with the message after Process.
type Kind int

const (
	// Done means the ticket left pending; commit.
	Done Kind = iota + 1
	// Skipped means the ticket was no longer pending; commit.
	Skipped
	// Retryable means transient classifier failures outlasted the in-process
	// retries; the ticket is still pending and the message should be retried
	// after Outcome.RetryAfter.
	Retryable
	// Poison means retrying cannot help: a bad id, a missing ticket, or a
	// permanent classifier error (the ticket already went to needs_human).
	Poison
)

func (k Kind) String() string {
	switch k {
	case Done:
		return "done"
	case Skipped:
		return "skipped"
	case Retryable:
		return "retryable"
	case Poison:
		return "poison"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Outcome is the result of Process for the transport.
type Outcome struct {
	Kind Kind
	// Status and Reason are what was written, for Done and for a Poison that
	// moved the ticket to needs_human.
	Status ticket.Status
	Reason ticket.Reason
	// RetryAfter is the suggested delay of a Retryable outcome.
	RetryAfter time.Duration
	// Err is the cause of a Retryable or Poison outcome.
	Err error
}

// ErrBadID is the Poison cause for a ticket id that does not parse.
var ErrBadID = errors.New("triage: bad ticket id")

// Defaults of Config.
const (
	DefaultCallTimeout = 3 * time.Second
	DefaultMaxAttempts = 3
	DefaultBackoffBase = 200 * time.Millisecond
	DefaultBackoffMax  = 2 * time.Second
	DefaultRetryAfter  = 30 * time.Second
)

// maxLastErrorBytes caps the stored last_error.
const maxLastErrorBytes = 512

// Config configures the Processor. Thresholds and DailyBudgetUSD are used as
// they are; other zero fields take the defaults.
type Config struct {
	Thresholds classifier.Thresholds
	// Taxonomy validates answers; nil means classifier.DefaultTaxonomy.
	Taxonomy classifier.Taxonomy
	// DailyBudgetUSD is the classifier spend per UTC day. Once the recorded
	// cost reaches it, tickets go to needs_human without a call; zero allows
	// no calls at all.
	DailyBudgetUSD float64
	// CallTimeout bounds each classifier call.
	CallTimeout time.Duration
	// MaxAttempts is the number of calls per Process for transient errors.
	MaxAttempts int
	// BackoffBase is the wait before the second call; each later wait
	// doubles, up to BackoffMax. Waits are jittered to [d/2, d].
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// RetryAfter is the delay suggested by a Retryable outcome when the
	// classifier gave none.
	RetryAfter time.Duration
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
	if c.RetryAfter <= 0 {
		c.RetryAfter = DefaultRetryAfter
	}
	return c
}

// Processor classifies one pending ticket per Process call. It is safe for
// concurrent use.
type Processor struct {
	store Store
	cls   classifier.Classifier
	cfg   Config
	log   *slog.Logger
}

// NewProcessor returns a Processor that reads and writes tickets in store and
// classifies them with cls.
func NewProcessor(store Store, cls classifier.Classifier, cfg Config, log *slog.Logger) *Processor {
	return &Processor{store: store, cls: cls, cfg: cfg.withDefaults(), log: log}
}

// retryAfterer is implemented by classifier errors that carry a Retry-After.
type retryAfterer interface {
	error
	RetryAfter() time.Duration
}

// Process classifies the ticket with the id (README §3.3, §4.2): skip unless
// pending, check the daily budget, call the classifier with a per-call
// timeout and in-process retries, apply the policy, and write the result with
// the audit rows through Store.Apply.
//
// A non-nil error means the message must not be committed: a storage
// failure, a cancelled ctx, or an error wrapping classifier.ErrAuth, which is
// a configuration failure that should stop consumption.
func (p *Processor) Process(ctx context.Context, ticketID string) (Outcome, error) {
	id, err := uuid.Parse(ticketID)
	if err != nil {
		return Outcome{Kind: Poison, Err: fmt.Errorf("%w: %w", ErrBadID, err)}, nil
	}
	t, err := p.store.Ticket(ctx, id)
	if errors.Is(err, ticket.ErrNotFound) {
		return Outcome{Kind: Poison, Err: fmt.Errorf("triage: ticket %s: %w", id, ticket.ErrNotFound)}, nil
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("triage: read ticket %s: %w", id, err)
	}
	if t.Status != ticket.StatusPending {
		return Outcome{Kind: Skipped}, nil
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	spent, err := p.store.SpentSince(ctx, today)
	if err != nil {
		return Outcome{}, fmt.Errorf("triage: read today's spend: %w", err)
	}
	if spent >= p.cfg.DailyBudgetUSD {
		return p.finish(ctx, Update{
			TicketID: id, Status: ticket.StatusNeedsHuman, Reason: ticket.ReasonBudgetExhausted, At: now,
		}, Done, nil)
	}

	res, calls, err := p.classify(ctx, t)
	u := Update{TicketID: id, Calls: calls, LastError: lastError(calls)}
	switch {
	case err == nil:
		status, reason, flags := classifier.Decide(res, p.cfg.Thresholds)
		c := res.Classification()
		// The flag is the more likely answer; the band only marks doubt.
		c.IsComplaint = c.ComplaintP >= 0.5
		c.ComplaintUncertain = flags.ComplaintUncertain
		u.Status, u.Reason, u.Classification = status, reason, &c
		u.At = time.Now().UTC()
		return p.finish(ctx, u, Done, nil)
	case ctx.Err() != nil:
		// Shutdown or a lost partition: the message is redelivered, and the
		// calls made are not recorded.
		return Outcome{}, fmt.Errorf("triage: classify ticket %s: %w", id, context.Cause(ctx))
	case errors.Is(err, classifier.ErrAuth):
		u.Status, u.At = ticket.StatusPending, time.Now().UTC()
		if _, aerr := p.store.Apply(ctx, u); aerr != nil {
			return Outcome{}, errors.Join(fmt.Errorf("triage: classify ticket %s: %w", id, err),
				fmt.Errorf("triage: record calls of ticket %s: %w", id, aerr))
		}
		return Outcome{}, fmt.Errorf("triage: classify ticket %s: %w", id, err)
	case errors.Is(err, classifier.ErrTransient):
		u.Status, u.At = ticket.StatusPending, time.Now().UTC()
		retryAfter := p.cfg.RetryAfter
		if ra, ok := errors.AsType[retryAfterer](err); ok && ra.RetryAfter() > 0 {
			retryAfter = ra.RetryAfter()
		}
		return p.finish(ctx, u, Retryable, &Outcome{RetryAfter: retryAfter, Err: err})
	default:
		// Permanent errors, invalid answers, and anything unmapped fail open.
		u.Status, u.Reason = ticket.StatusNeedsHuman, ticket.ReasonClassifierUnavailable
		if errors.Is(err, classifier.ErrInvalidAnswer) {
			u.Reason = ticket.ReasonInvalidAnswer
		}
		u.At = time.Now().UTC()
		return p.finish(ctx, u, Poison, &Outcome{Err: err})
	}
}

// finish validates the transition, applies u, and returns an outcome of kind,
// or Skipped when the ticket was no longer pending. base carries the
// kind-specific fields.
func (p *Processor) finish(ctx context.Context, u Update, kind Kind, base *Outcome) (Outcome, error) {
	if u.Status != ticket.StatusPending {
		if err := ticket.CheckTransition(ticket.StatusPending, u.Status, u.Reason); err != nil {
			return Outcome{}, fmt.Errorf("triage: ticket %s: %w", u.TicketID, err)
		}
	}
	applied, err := p.store.Apply(ctx, u)
	if err != nil {
		return Outcome{}, fmt.Errorf("triage: write ticket %s: %w", u.TicketID, err)
	}
	if !applied {
		return Outcome{Kind: Skipped}, nil
	}
	var out Outcome
	if base != nil {
		out = *base
	}
	out.Kind = kind
	if u.Status != ticket.StatusPending {
		out.Status, out.Reason = u.Status, u.Reason
	}
	return out, nil
}

// classify calls the classifier until it answers validly, fails with an error
// other than ErrTransient, or MaxAttempts calls were made. It returns every
// call for the audit table and the last error.
func (p *Processor) classify(ctx context.Context, t ticket.Ticket) (classifier.Result, []Call, error) {
	in := classifier.Input{Subject: t.Subject, Body: t.Body}
	calls := make([]Call, 0, p.cfg.MaxAttempts)
	for i := 0; ; i++ {
		attempt := t.Attempts + i + 1
		res, err := p.call(ctx, in)
		if err == nil {
			err = res.Validate(p.cfg.Taxonomy)
		}
		calls = append(calls, Call{Attempt: attempt, Outcome: callOutcome(err), Err: err, Result: res})
		p.logCall(ctx, t.ID, attempt, res.Call, err)

		if err == nil || !errors.Is(err, classifier.ErrTransient) || ctx.Err() != nil || i+1 >= p.cfg.MaxAttempts {
			return res, calls, err
		}
		wait := p.backoff(i)
		if ra, ok := errors.AsType[retryAfterer](err); ok && ra.RetryAfter() > wait {
			if ra.RetryAfter() > p.cfg.BackoffMax {
				// Too long to hold the message here; let the transport wait.
				return res, calls, err
			}
			wait = ra.RetryAfter()
		}
		if err := sleep(ctx, wait); err != nil {
			return res, calls, fmt.Errorf("triage: wait to retry: %w", err)
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

func callOutcome(err error) CallOutcome {
	switch {
	case err == nil:
		return CallOK
	case errors.Is(err, classifier.ErrAuth):
		return CallAuthError
	case errors.Is(err, classifier.ErrInvalidAnswer):
		return CallInvalidAnswer
	case errors.Is(err, classifier.ErrTransient):
		return CallTransientError
	}
	return CallPermanentError
}

// lastError is the message of the last failed call, capped to
// maxLastErrorBytes on a rune boundary, or "" when no call failed. Classifier
// errors never carry ticket text.
func lastError(calls []Call) string {
	var s string
	for _, c := range slices.Backward(calls) {
		if c.Err != nil {
			s = c.Err.Error()
			break
		}
	}
	if len(s) <= maxLastErrorBytes {
		return s
	}
	s = s[:maxLastErrorBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// logCall writes the per-call log line of README §4.3.
func (p *Processor) logCall(ctx context.Context, id uuid.UUID, attempt int, c classifier.Call, err error) {
	level := slog.LevelInfo
	attrs := []slog.Attr{
		slog.String("provider", c.Provider),
		slog.String("model", c.Model),
		slog.String("ticket_id", id.String()),
		slog.Int("attempt", attempt),
		slog.Int64("latency_ms", c.Latency.Milliseconds()),
		slog.Int("input_tokens", c.InputTokens),
		slog.Int("output_tokens", c.OutputTokens),
		slog.Float64("cost_usd", c.CostUSD),
		slog.String("cost_source", string(c.CostSource)),
		slog.String("outcome", string(callOutcome(err))),
		slog.Int("http_status", c.HTTPStatus),
	}
	if err != nil {
		level = slog.LevelWarn
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	p.log.LogAttrs(ctx, level, "classifier call", attrs...)
}
