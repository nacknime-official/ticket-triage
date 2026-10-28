package ticket_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

func TestLimitsValidate(t *testing.T) {
	l := ticket.Limits{MaxSubjectChars: ticket.DefaultMaxSubjectChars, MaxBodyChars: ticket.DefaultMaxBodyChars}
	const ok = "ok"
	tests := []struct {
		name    string
		subject string
		body    string
		want    []ticket.Problem
	}{
		{name: "minimal", subject: "a", body: "b"},
		{name: "subject at max ascii", subject: strings.Repeat("a", 200), body: ok},
		{name: "subject at max cyrillic (400 bytes)", subject: strings.Repeat("ї", 200), body: ok},
		{name: "subject at max emoji (800 bytes)", subject: strings.Repeat("🙂", 200), body: ok},
		{name: "body at max cyrillic", subject: ok, body: strings.Repeat("ґ", 5000)},
		{name: "surrounding whitespace allowed", subject: "  hi  ", body: "\nbody\n"},
		{
			name: "subject empty", subject: "", body: ok,
			want: []ticket.Problem{{Field: ticket.FieldSubject, Code: ticket.ProblemRequired}},
		},
		{
			name: "subject whitespace only", subject: " \t\n", body: ok,
			want: []ticket.Problem{{Field: ticket.FieldSubject, Code: ticket.ProblemRequired}},
		},
		{
			name: "subject over max ascii", subject: strings.Repeat("a", 201), body: ok,
			want: []ticket.Problem{{Field: ticket.FieldSubject, Code: ticket.ProblemTooLong, Max: 200}},
		},
		{
			name: "subject over max cyrillic", subject: strings.Repeat("ї", 201), body: ok,
			want: []ticket.Problem{{Field: ticket.FieldSubject, Code: ticket.ProblemTooLong, Max: 200}},
		},
		{
			name: "body empty", subject: ok, body: "",
			want: []ticket.Problem{{Field: ticket.FieldBody, Code: ticket.ProblemRequired}},
		},
		{
			name: "body over max", subject: ok, body: strings.Repeat("ґ", 5001),
			want: []ticket.Problem{{Field: ticket.FieldBody, Code: ticket.ProblemTooLong, Max: 5000}},
		},
		{
			name: "body invalid utf8", subject: ok, body: "a\xffb",
			want: []ticket.Problem{{Field: ticket.FieldBody, Code: ticket.ProblemInvalidText}},
		},
		{
			name: "subject with NUL", subject: "a\x00b", body: ok,
			want: []ticket.Problem{{Field: ticket.FieldSubject, Code: ticket.ProblemInvalidText}},
		},
		{
			name: "both invalid", subject: "", body: strings.Repeat("x", 5001),
			want: []ticket.Problem{
				{Field: ticket.FieldSubject, Code: ticket.ProblemRequired},
				{Field: ticket.FieldBody, Code: ticket.ProblemTooLong, Max: 5000},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := l.Validate(tt.subject, tt.body)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var verr *ticket.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Validate() = %v, want *ValidationError", err)
			}
			if !slices.Equal(verr.Problems, tt.want) {
				t.Errorf("Problems = %+v, want %+v", verr.Problems, tt.want)
			}
		})
	}
}

func TestValidationErrorOmitsInput(t *testing.T) {
	l := ticket.Limits{MaxSubjectChars: 5, MaxBodyChars: 5}
	err := l.Validate("secret-subject", "\xffsecret")
	if err == nil {
		t.Fatal("Validate() = nil, want error")
	}
	msg := err.Error()
	if strings.Contains(msg, "secret") {
		t.Errorf("error message leaks input: %q", msg)
	}
	want := "ticket: invalid input: subject must be at most 5 characters; body must be valid UTF-8 without NUL characters"
	if msg != want {
		t.Errorf("Error() = %q, want %q", msg, want)
	}
}
