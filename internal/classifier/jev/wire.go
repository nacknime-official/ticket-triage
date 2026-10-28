package jev

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"math"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
)

// The response shape below is the documented one (README §7, report §5.2):
//
//	{"model": "...", "answers": {"<id>": {...}}, "usage": {"input_tokens": n, "output_tokens": n, "cost": usd}}
//
// [unverified] until a live call (T12). Each part is decoded by its own
// helper so T12 can correct one without touching the rest. Decoding is
// lenient (unknown members are ignored, usage and model may be missing);
// answers are then validated strictly.

// envelope keeps the raw parts of a response. Only these members reach the
// audit row, so an unexpected member that echoes the request cannot carry
// ticket text into it.
type envelope struct {
	Model   jsontext.Value `json:"model,omitzero"`
	Answers jsontext.Value `json:"answers,omitzero"`
	Usage   jsontext.Value `json:"usage,omitzero"`
}

// decodeEnvelope splits a response body into its parts and returns the
// filtered raw response for Call.RawResponse.
func decodeEnvelope(body []byte) (envelope, []byte, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return envelope{}, nil, fmt.Errorf("%w: jev: malformed response: %w", classifier.ErrInvalidAnswer, err)
	}
	raw, err := json.Marshal(env, json.Deterministic(true))
	if err != nil {
		return envelope{}, nil, fmt.Errorf("%w: jev: re-encode response: %w", classifier.ErrInvalidAnswer, err)
	}
	return env, raw, nil
}

// servedModel returns the model as served. [unverified] The documented
// response has a top-level "model" string (e.g. typesafe/jev-1.14-20261014
// on OpenRouter); anything else gives "".
func servedModel(raw jsontext.Value) string {
	var m string
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return m
}

// usage is the token and cost report of a call.
type usage struct {
	InputTokens  int
	OutputTokens int
	// Cost is nil when the provider did not report one.
	Cost *float64
	// HasInputTokens is false when input_tokens was missing.
	HasInputTokens bool
}

// decodeUsage reads "usage". [unverified] input_tokens and output_tokens are
// documented for both providers; "cost" (USD) only for OpenRouter. A missing
// or malformed usage gives the zero usage: it never fails the call. A
// negative or non-finite cost is ignored.
func decodeUsage(raw jsontext.Value) usage {
	var w struct {
		InputTokens  *int     `json:"input_tokens"`
		OutputTokens *int     `json:"output_tokens"`
		Cost         *float64 `json:"cost"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &w) != nil {
		return usage{}
	}
	var u usage
	if w.InputTokens != nil && *w.InputTokens >= 0 {
		u.InputTokens, u.HasInputTokens = *w.InputTokens, true
	}
	if w.OutputTokens != nil && *w.OutputTokens >= 0 {
		u.OutputTokens = *w.OutputTokens
	}
	if w.Cost != nil && *w.Cost >= 0 && !math.IsInf(*w.Cost, 0) {
		u.Cost = w.Cost
	}
	return u
}

// Answer shapes (README §7) [unverified]: noul → {type, noul: P(yes)} without
// a confidence; choice → {choice, probabilities, confidence}; score →
// {score, legend, probabilities, confidence}. Only the fields the triage uses
// are decoded; the probabilities stay in the raw response. A "type" member is
// checked when present.
type (
	noulAnswer struct {
		Noul *float64 `json:"noul"`
	}
	choiceAnswer struct {
		Choice     *string  `json:"choice"`
		Confidence *float64 `json:"confidence"`
	}
	scoreAnswer struct {
		Score      *float64 `json:"score"`
		Confidence *float64 `json:"confidence"`
	}
)

// decodeAnswers reads the three answers into a Result without Call. It
// reports a missing, mistyped or incomplete answer as ErrInvalidAnswer; range
// and taxonomy checks are left to Result.Validate.
func decodeAnswers(raw jsontext.Value) (classifier.Result, error) {
	var answers map[string]jsontext.Value
	if len(raw) == 0 {
		return classifier.Result{}, fmt.Errorf("%w: jev: response has no answers", classifier.ErrInvalidAnswer)
	}
	if err := json.Unmarshal(raw, &answers); err != nil {
		return classifier.Result{}, fmt.Errorf("%w: jev: answers: %w", classifier.ErrInvalidAnswer, err)
	}

	var (
		n noulAnswer
		c choiceAnswer
		s scoreAnswer
	)
	if err := decodeAnswer(answers, questionComplaint, typeNoul, &n); err != nil {
		return classifier.Result{}, err
	}
	if err := decodeAnswer(answers, questionCategory, typeChoice, &c); err != nil {
		return classifier.Result{}, err
	}
	if err := decodeAnswer(answers, questionUrgency, typeScore, &s); err != nil {
		return classifier.Result{}, err
	}

	fields := []struct {
		name string
		ok   bool
	}{
		{questionComplaint + ".noul", n.Noul != nil},
		{questionCategory + ".choice", c.Choice != nil},
		{questionCategory + ".confidence", c.Confidence != nil},
		{questionUrgency + ".score", s.Score != nil},
		{questionUrgency + ".confidence", s.Confidence != nil},
	}
	for _, f := range fields {
		if !f.ok {
			return classifier.Result{}, fmt.Errorf("%w: jev: answer %s is missing", classifier.ErrInvalidAnswer, f.name)
		}
	}
	return classifier.Result{
		ComplaintP:         *n.Noul,
		Category:           *c.Choice,
		CategoryConfidence: *c.Confidence,
		Urgency:            *s.Score,
		UrgencyConfidence:  *s.Confidence,
	}, nil
}

// decodeAnswer decodes answers[id] into out and checks its "type" member, if
// any, against wantType.
func decodeAnswer(answers map[string]jsontext.Value, id, wantType string, out any) error {
	raw, ok := answers[id]
	if !ok {
		return fmt.Errorf("%w: jev: answer %s is missing", classifier.ErrInvalidAnswer, id)
	}
	var typ struct {
		Type *string `json:"type"`
	}
	if err := json.Unmarshal(raw, &typ); err != nil {
		return fmt.Errorf("%w: jev: answer %s: %w", classifier.ErrInvalidAnswer, id, err)
	}
	if typ.Type != nil && *typ.Type != wantType {
		return fmt.Errorf("%w: jev: answer %s has type %q, want %q", classifier.ErrInvalidAnswer, id, *typ.Type, wantType)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: jev: answer %s: %w", classifier.ErrInvalidAnswer, id, err)
	}
	return nil
}
