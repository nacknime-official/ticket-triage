package classifier

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// Classifier answers the three triage questions of README §3.3 for one ticket.
//
// On error the returned Result carries no answers, but its Call may still
// describe the failed call (latency, HTTP status, raw response) for the audit
// row. Errors wrap ErrTransient, ErrPermanent or ErrAuth.
type Classifier interface {
	Classify(ctx context.Context, in Input) (Result, error)
}

// Input is everything a classifier may see: the ticket text and nothing else.
// It must never be logged.
type Input struct {
	Subject string
	Body    string
}

// Errors adapters map failures to. The processor reacts to these, not to
// provider-specific codes.
var (
	// ErrTransient is a failure worth retrying: 429, 529, 5xx, timeout, network.
	ErrTransient = errors.New("classifier: transient error")
	// ErrPermanent is a failure a retry will not fix: 400, 422, a bad answer.
	ErrPermanent = errors.New("classifier: permanent error")
	// ErrAuth is a configuration failure (401, 403), not a ticket failure.
	ErrAuth = errors.New("classifier: auth error")
	// ErrInvalidAnswer is a response with missing, mistyped or out-of-range
	// answers. It wraps ErrPermanent.
	ErrInvalidAnswer = fmt.Errorf("%w: invalid answer", ErrPermanent)
)

// MaxUrgency is the highest urgency level (critical).
const MaxUrgency = float64(ticket.PriorityCritical)

// Result is a classifier answer plus the metadata of the call that produced it.
type Result struct {
	// ComplaintP is P(yes) for "is this a complaint".
	ComplaintP         float64
	Category           string
	CategoryConfidence float64
	// Urgency is the weighted urgency level in [0, MaxUrgency].
	Urgency           float64
	UrgencyConfidence float64
	Call              Call
}

// Call describes one classifier call for the per-call log line and the
// classifications audit row (README §4.3).
type Call struct {
	Provider string
	// Model is the model as served, taken from the response; empty when there
	// was no response.
	Model string
	// HTTPStatus is 0 when there was no HTTP response (timeout, network, fake).
	HTTPStatus   int
	Latency      time.Duration
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	// CostSource is empty when the cost is unknown.
	CostSource CostSource
	// RawResponse is the raw answer with all probabilities and confidences. It
	// holds no ticket text.
	RawResponse []byte
}

// CostSource says where Call.CostUSD came from.
type CostSource string

const (
	// CostReported is a cost reported by the provider.
	CostReported CostSource = "reported"
	// CostEstimated is input tokens times the configured price.
	CostEstimated CostSource = "estimated"
)

// Validate reports, as an ErrInvalidAnswer, the first answer that is out of
// range or a category outside tax.
func (r Result) Validate(tax Taxonomy) error {
	probs := []struct {
		name string
		v    float64
	}{
		{"complaint_p", r.ComplaintP},
		{"category_confidence", r.CategoryConfidence},
		{"urgency_confidence", r.UrgencyConfidence},
	}
	for _, p := range probs {
		if !inRange(p.v, 0, 1) {
			return fmt.Errorf("%w: %s %v is outside [0, 1]", ErrInvalidAnswer, p.name, p.v)
		}
	}
	if !inRange(r.Urgency, 0, MaxUrgency) {
		return fmt.Errorf("%w: urgency %v is outside [0, %v]", ErrInvalidAnswer, r.Urgency, MaxUrgency)
	}
	if !tax.Has(r.Category) {
		return fmt.Errorf("%w: category %q is not one of %v", ErrInvalidAnswer, r.Category, tax.Names())
	}
	return nil
}

func inRange(v, lo, hi float64) bool {
	return !math.IsNaN(v) && v >= lo && v <= hi
}

// Priority is the urgency score rounded to the nearest level.
func (r Result) Priority() ticket.Priority {
	return ticket.PriorityFromUrgency(r.Urgency)
}

// Classification is the part of r stored on the ticket.
func (r Result) Classification() ticket.Classification {
	return ticket.Classification{
		Category:           r.Category,
		CategoryConfidence: r.CategoryConfidence,
		ComplaintP:         r.ComplaintP,
		Urgency:            r.Urgency,
		UrgencyConfidence:  r.UrgencyConfidence,
	}
}
