package ticket

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Default text limits in characters (runes), not bytes (README §3.1).
const (
	DefaultMaxSubjectChars = 200
	DefaultMaxBodyChars    = 5000
)

// Field names used in validation problems; they match the JSON request fields.
const (
	FieldSubject = "subject"
	FieldBody    = "body"
)

// ProblemCode classifies a validation problem.
type ProblemCode string

const (
	// ProblemRequired means the field is empty or only whitespace.
	ProblemRequired ProblemCode = "required"
	// ProblemTooLong means the field has more characters than allowed.
	ProblemTooLong ProblemCode = "too_long"
	// ProblemInvalidText means the field is not valid UTF-8 or contains NUL,
	// which Postgres text cannot store.
	ProblemInvalidText ProblemCode = "invalid_text"
)

// Problem is one invalid field. It never carries the field's value.
type Problem struct {
	Field string
	Code  ProblemCode
	// Max is the character limit for ProblemTooLong, zero otherwise.
	Max int
}

func (p Problem) String() string {
	switch p.Code {
	case ProblemRequired:
		return p.Field + " is required"
	case ProblemTooLong:
		return fmt.Sprintf("%s must be at most %d characters", p.Field, p.Max)
	case ProblemInvalidText:
		return p.Field + " must be valid UTF-8 without NUL characters"
	}
	return fmt.Sprintf("%s is invalid (%s)", p.Field, p.Code)
}

// ValidationError lists every invalid field of a submission.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	msgs := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		msgs[i] = p.String()
	}
	return "ticket: invalid input: " + strings.Join(msgs, "; ")
}

// Limits bounds the text a submitter may send.
type Limits struct {
	MaxSubjectChars int
	MaxBodyChars    int
}

// Validate checks subject and body against l. It returns a *ValidationError
// listing every problem, or nil.
func (l Limits) Validate(subject, body string) error {
	var problems []Problem
	if p, ok := checkText(FieldSubject, subject, l.MaxSubjectChars); !ok {
		problems = append(problems, p)
	}
	if p, ok := checkText(FieldBody, body, l.MaxBodyChars); !ok {
		problems = append(problems, p)
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

func checkText(field, s string, maxChars int) (Problem, bool) {
	switch {
	case !utf8.ValidString(s) || strings.ContainsRune(s, 0):
		return Problem{Field: field, Code: ProblemInvalidText}, false
	case strings.TrimSpace(s) == "":
		return Problem{Field: field, Code: ProblemRequired}, false
	case utf8.RuneCountInString(s) > maxChars:
		return Problem{Field: field, Code: ProblemTooLong, Max: maxChars}, false
	}
	return Problem{}, true
}
