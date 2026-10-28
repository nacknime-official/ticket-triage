package jev

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
)

// Question ids of README §3.3. Jev does not show them to the model; the
// meaning comes from instructions and criteria only.
const (
	questionComplaint = "is_complaint"
	questionCategory  = "category"
	questionUrgency   = "urgency"
)

// Jev question types.
const (
	typeNoul   = "noul"
	typeChoice = "choice"
	typeScore  = "score"
)

// urgencyLevels are the score criteria, ordered from level 0 up to
// classifier.MaxUrgency, so the weighted score is the urgency level.
var urgencyLevels = []string{
	"No time pressure: general question or feedback",
	"Normal: an issue the customer wants solved but can work around",
	"High: blocks the customer's work or involves money already lost",
	"Critical: outage, security incident, data loss, or legal threat",
}

// request is the body of POST /v1/systemone.
type request struct {
	Model     string    `json:"model"`
	State     state     `json:"state"`
	Questions questions `json:"questions"`
}

// state is everything Jev sees about the ticket: subject and body only.
type state struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// questions keeps the README §3.3 order on the wire.
type questions struct {
	IsComplaint noulQuestion   `json:"is_complaint"`
	Category    choiceQuestion `json:"category"`
	Urgency     scoreQuestion  `json:"urgency"`
}

type noulQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type choiceQuestion struct {
	Type         string         `json:"type"`
	Instructions string         `json:"instructions"`
	Criteria     choiceCriteria `json:"criteria"`
}

type scoreQuestion struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}

// choiceCriteria is the option → description object, written in taxonomy
// order rather than the sorted or random order of a map.
type choiceCriteria classifier.Taxonomy

// MarshalJSONTo implements json.MarshalerTo.
func (c choiceCriteria) MarshalJSONTo(enc *jsontext.Encoder) error {
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	for _, cat := range c {
		if err := enc.WriteToken(jsontext.String(cat.Name)); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.String(cat.Description)); err != nil {
			return err
		}
	}
	return enc.WriteToken(jsontext.EndObject)
}

var _ json.MarshalerTo = choiceCriteria(nil)

// buildQuestions returns the three questions of README §3.3 over tax.
func buildQuestions(tax classifier.Taxonomy) questions {
	return questions{
		IsComplaint: noulQuestion{
			Type: typeNoul,
			Instructions: "Does the customer in `subject` and `body` express dissatisfaction or complain " +
				"about the product, service, or a charge?",
		},
		Category: choiceQuestion{
			Type:         typeChoice,
			Instructions: "Which support team should handle the request in `subject` and `body`?",
			Criteria:     choiceCriteria(tax),
		},
		Urgency: scoreQuestion{
			Type:         typeScore,
			Instructions: "How urgent is the request in `subject` and `body` for the customer?",
			Criteria:     urgencyLevels,
		},
	}
}
