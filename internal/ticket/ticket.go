package ticket

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"uuid"
)

// Ticket is one support request and its triage state.
//
// Subject and Body are user text: they must never be logged. LogValue keeps
// them, and the token hash, out of slog output.
type Ticket struct {
	ID           uuid.UUID
	Subject      string
	Body         string
	Status       Status
	StatusReason Reason
	// Classification is nil until the classifier produced a usable answer.
	Classification  *Classification
	AccessTokenHash TokenHash
	// Attempts and LastError are kept for diagnostics.
	Attempts  int
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
	// ClassifiedAt is zero until the ticket was classified.
	ClassifiedAt time.Time
}

// LogValue implements slog.LogValuer. It logs only non-sensitive fields.
func (t Ticket) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("id", t.ID.String()),
		slog.String("status", string(t.Status)),
	}
	if t.StatusReason != "" {
		attrs = append(attrs, slog.String("status_reason", string(t.StatusReason)))
	}
	return slog.GroupValue(attrs...)
}

// Message is the user-facing status message (README §2, §3.1).
func (t Ticket) Message() string {
	switch t.Status {
	case StatusPending:
		return "Your request was received and is waiting to be classified."
	case StatusClassified:
		if t.Classification == nil {
			return "Your request was handed to a team."
		}
		return fmt.Sprintf("Your request was handed to the %s team, priority %s.",
			strings.ReplaceAll(t.Classification.Category, "_", " "), t.Classification.Priority())
	case StatusNeedsHuman:
		return "A person will contact you."
	}
	return ""
}

// Classification is the stored classifier result for a ticket.
type Classification struct {
	Category           string
	CategoryConfidence float64
	// ComplaintP is P(yes) for "is this a complaint".
	ComplaintP float64
	// IsComplaint is the complaint flag decided from ComplaintP.
	IsComplaint bool
	// ComplaintUncertain means ComplaintP fell inside the configured uncertain
	// band (README §3.3); the status is unaffected.
	ComplaintUncertain bool
	// Urgency is the weighted urgency level in [0, 3].
	Urgency           float64
	UrgencyConfidence float64
}

// Priority is the urgency score rounded to the nearest level.
func (c Classification) Priority() Priority {
	return PriorityFromUrgency(c.Urgency)
}

// Priority is a discrete urgency level (README §3.3).
type Priority int

const (
	PriorityLow Priority = iota
	PriorityNormal
	PriorityHigh
	PriorityCritical
)

// PriorityFromUrgency rounds a weighted urgency score to the nearest level,
// clamped to [PriorityLow, PriorityCritical].
func PriorityFromUrgency(u float64) Priority {
	switch r := math.Round(u); {
	case math.IsNaN(r) || r <= float64(PriorityLow):
		return PriorityLow
	case r >= float64(PriorityCritical):
		return PriorityCritical
	default:
		return Priority(r)
	}
}

func (p Priority) String() string {
	switch p {
	case PriorityLow:
		return "low"
	case PriorityNormal:
		return "normal"
	case PriorityHigh:
		return "high"
	case PriorityCritical:
		return "critical"
	}
	return fmt.Sprintf("Priority(%d)", int(p))
}
