package ticket

import (
	"errors"
	"fmt"
)

// Status is the lifecycle state of a ticket (README §3.2).
type Status string

const (
	// StatusPending means accepted and waiting for classification.
	StatusPending Status = "pending"
	// StatusClassified means the classification is stored and confident.
	StatusClassified Status = "classified"
	// StatusNeedsHuman means a person takes the ticket; the Reason says why.
	StatusNeedsHuman Status = "needs_human"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusClassified, StatusNeedsHuman:
		return true
	}
	return false
}

// Final reports whether no further transition is allowed from s.
func (s Status) Final() bool {
	return s == StatusClassified || s == StatusNeedsHuman
}

// CanTransitionTo reports whether a ticket in status s may move to next.
// Only pending tickets move: to classified or to needs_human.
func (s Status) CanTransitionTo(next Status) bool {
	return s == StatusPending && (next == StatusClassified || next == StatusNeedsHuman)
}

// Reason explains why a ticket went to needs_human. The zero value means no
// reason, which is the only valid value for every other status.
type Reason string

const (
	ReasonLowConfidence         Reason = "low_confidence"
	ReasonCategoryOther         Reason = "category_other"
	ReasonClassifierUnavailable Reason = "classifier_unavailable"
	ReasonInvalidAnswer         Reason = "invalid_answer"
	ReasonBudgetExhausted       Reason = "budget_exhausted"
)

// Valid reports whether r is a known, non-empty reason.
func (r Reason) Valid() bool {
	switch r {
	case ReasonLowConfidence, ReasonCategoryOther, ReasonClassifierUnavailable,
		ReasonInvalidAnswer, ReasonBudgetExhausted:
		return true
	}
	return false
}

// ErrInvalidTransition is returned by CheckTransition for a disallowed move or
// a reason that does not fit the target status.
var ErrInvalidTransition = errors.New("ticket: invalid status transition")

// CheckTransition validates moving from one status to another with the given
// reason: the move must be allowed by CanTransitionTo, needs_human requires a
// valid reason, and classified must have none.
func CheckTransition(from, to Status, reason Reason) error {
	if !from.CanTransitionTo(to) {
		return fmt.Errorf("%w: %q to %q", ErrInvalidTransition, from, to)
	}
	switch to {
	case StatusNeedsHuman:
		if !reason.Valid() {
			return fmt.Errorf("%w: %q needs a valid reason, got %q", ErrInvalidTransition, to, reason)
		}
	default:
		if reason != "" {
			return fmt.Errorf("%w: %q takes no reason, got %q", ErrInvalidTransition, to, reason)
		}
	}
	return nil
}
