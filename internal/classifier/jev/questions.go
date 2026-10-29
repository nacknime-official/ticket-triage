package jev

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"

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

// complaintGuidelines is the complaint definition Jev answers is_complaint
// against: the "Complaint (`true`)" and "Not a complaint (`false`)" lists of
// the "Complaint" labeling guidelines in testdata/eval/README.md, sentence
// for sentence. Without it Jev counts any report of a broken product or a
// lost payment as a complaint, while the labels count only dissatisfaction.
// TestComplaintGuidelinesMatchREADME fails when the lists here and there
// differ, so change both together.
var complaintGuidelines = guidelines{
	True: []string{
		"The customer expresses dissatisfaction: anger, blame, sarcasm, \"again\", a threat to leave or to " +
			"escalate, a demand for compensation, profanity, or an explicit claim or complaint.",
		"Profanity or insults aimed at the product or company are a complaint.",
		"Asking how to file a complaint or claim is a complaint.",
		"\"You charged me AGAIN, refund it now\" is a complaint.",
	},
	False: []string{
		"Questions, requests and neutral, factual problem reports are not complaints, even when something is " +
			"broken or money was lost.",
		"A polite report of a double charge asking for a refund is not a complaint.",
		"A formal request with a conditional legal step but no dissatisfaction (\"otherwise I will have to " +
			"contact the ombudsman\") is not a complaint.",
	},
}

// guidelines are the sentences that make a noul answer true or false.
type guidelines struct {
	True, False []string
}

// criteria returns g as noul criteria, each side one paragraph.
func (g guidelines) criteria() noulCriteria {
	return noulCriteria{True: strings.Join(g.True, " "), False: strings.Join(g.False, " ")}
}

// urgencyGuidelines is the urgency definition Jev answers urgency against:
// the "All levels" and per-level lists of the "Urgency" labeling guidelines in
// testdata/eval/README.md, sentence for sentence. Without it Jev rated a
// quarter of the normal requests high, while the labels keep a request normal
// until it blocks the customer or money is lost.
// TestUrgencyGuidelinesMatchREADME fails when the lists here and there differ,
// so change both together.
var urgencyGuidelines = levelGuidelines{
	All: []string{
		"Tone does not change urgency: anger, caps, profanity or a threat to leave do not raise the level, " +
			"and an angry feature request is still low.",
		"Take the impact literally from the words, not what the customer might mean beyond them.",
	},
	Levels: [][]string{
		{
			"Low: nothing is broken and there is no time pressure.",
			"How-to and pricing questions, feature ideas, thanks, partnerships and newsletter requests are low.",
			"A feature request is low, unless the missing feature already costs the customer work or blocks a " +
				"rollout.",
			"A bare \"question about my bill\" is low.",
		},
		{
			"Normal: the customer needs something done or has a problem, but has a workaround or nothing is " +
				"blocked yet.",
			"Cancelling an order, getting an invoice, a refund for unused time, a bug with a workaround and " +
				"slowness are normal.",
			"Asking how to reset a forgotten password is normal: there is a self-service path.",
			"A missing feature that already costs the customer work or blocks a rollout (manual reconciliation " +
				"every day, \"security requires SSO before we roll you out\") is normal.",
			"Something that \"works strangely\" does not block the customer and is normal.",
		},
		{
			"High: the problem blocks the customer's work or money is already lost.",
			"A failing payment, a double charge, a charge after cancelling, being locked out with no way back " +
				"and an API failure stopping the customer's sync are high.",
			"A stated deadline (\"needed today\", \"client meeting in an hour\") moves a normal request to " +
				"high, never to critical.",
			"A partial failure (half of API calls fail) is high.",
			"One session's unsaved work lost in a crash is high.",
			"\"Can't log in\" blocks the user and is high, and so is a password reset email that never " +
				"arrives, so the user cannot get in.",
			"A bank chargeback threat is not a legal threat; it stays high when money was lost.",
		},
		{
			"Critical: an outage, a security incident, data loss, or a legal threat.",
			"The service down for a whole team or all users, a compromised account, a data leak and stored " +
				"data gone are critical.",
			"A legal threat means a lawsuit, lawyers, a court, or a regulator or ombudsman complaint (\"our " +
				"legal department will take action\"), and it is critical also when it is conditional (\"if " +
				"you do not refund by Friday, I will go to court\").",
		},
	},
}

// levelGuidelines are the sentences that decide a score answer: All for
// every level, and one list per level, ordered from level 0 up to
// classifier.MaxUrgency so the weighted score is the urgency level.
type levelGuidelines struct {
	All    []string
	Levels [][]string
}

// instructions returns question followed by the rules for all levels.
func (g levelGuidelines) instructions(question string) string {
	return strings.Join(append([]string{question}, g.All...), " ")
}

// criteria returns g.Levels as score criteria, each level one paragraph.
func (g levelGuidelines) criteria() []string {
	levels := make([]string, len(g.Levels))
	for i, sentences := range g.Levels {
		levels[i] = strings.Join(sentences, " ")
	}
	return levels
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
	Type         string       `json:"type"`
	Instructions string       `json:"instructions"`
	Criteria     noulCriteria `json:"criteria"`
}

// noulCriteria describes when the answer is yes and when no. Jev requires
// both keys; its validation also allows a record or an array per key,
// unused here.
type noulCriteria struct {
	True  string `json:"true"`
	False string `json:"false"`
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
			Type:         typeNoul,
			Instructions: "Does the customer in `subject` and `body` express dissatisfaction?",
			Criteria:     complaintGuidelines.criteria(),
		},
		Category: choiceQuestion{
			Type:         typeChoice,
			Instructions: "Which support team should handle the request in `subject` and `body`?",
			Criteria:     choiceCriteria(tax),
		},
		Urgency: scoreQuestion{
			Type:         typeScore,
			Instructions: urgencyGuidelines.instructions("How urgent is the request in `subject` and `body` for the customer?"),
			Criteria:     urgencyGuidelines.criteria(),
		},
	}
}
