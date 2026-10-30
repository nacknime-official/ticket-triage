package evalset

import (
	"bufio"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"
)

// Allowed field values. Categories and urgency levels follow PRD §3.3.
var (
	Categories = []string{"billing", "technical", "account", "feature_request", "other"}
	Langs      = []string{"en", "uk"}
	Sources    = []string{SourceGenerated, SourceBitext}
	Tags       = []string{"surzhyk", "typos", "angry", "vague", "mixed_topics", "translit", "code_switching"}
)

// Row sources.
const (
	SourceGenerated = "generated"
	SourceBitext    = "bitext"
)

// Limits on the ticket text, the same as POST /tickets.
const (
	MaxUrgency       = 3
	MaxSubjectLength = 200
	MaxBodyLength    = 5000
)

// Row is one labeled ticket. Only Subject and Body may be sent to a classifier.
type Row struct {
	ID                string   `json:"id"`
	Source            string   `json:"source"`
	Lang              string   `json:"lang"`
	Subject           string   `json:"subject"`
	Body              string   `json:"body"`
	ExpectedCategory  string   `json:"expected_category"`
	ExpectedComplaint bool     `json:"expected_complaint"`
	ExpectedUrgency   int      `json:"expected_urgency"`
	Tags              []string `json:"tags,omitempty"`
}

// rawRow uses pointers so that a missing required field is an error instead of a zero value.
type rawRow struct {
	ID                *string  `json:"id"`
	Source            *string  `json:"source"`
	Lang              *string  `json:"lang"`
	Subject           *string  `json:"subject"`
	Body              *string  `json:"body"`
	ExpectedCategory  *string  `json:"expected_category"`
	ExpectedComplaint *bool    `json:"expected_complaint"`
	ExpectedUrgency   *int     `json:"expected_urgency"`
	Tags              []string `json:"tags"`
}

// Validate reports the first way r breaks the schema.
func (r Row) Validate() error {
	switch {
	case r.ID == "":
		return errors.New("id is empty")
	case !slices.Contains(Sources, r.Source):
		return fmt.Errorf("source %q is not one of %v", r.Source, Sources)
	case !slices.Contains(Langs, r.Lang):
		return fmt.Errorf("lang %q is not one of %v", r.Lang, Langs)
	case !slices.Contains(Categories, r.ExpectedCategory):
		return fmt.Errorf("expected_category %q is not one of %v", r.ExpectedCategory, Categories)
	case r.ExpectedUrgency < 0 || r.ExpectedUrgency > MaxUrgency:
		return fmt.Errorf("expected_urgency %d is outside 0..%d", r.ExpectedUrgency, MaxUrgency)
	}
	if err := checkText("subject", r.Subject, MaxSubjectLength); err != nil {
		return err
	}
	if err := checkText("body", r.Body, MaxBodyLength); err != nil {
		return err
	}
	for i, tag := range r.Tags {
		if !slices.Contains(Tags, tag) {
			return fmt.Errorf("tag %q is not one of %v", tag, Tags)
		}
		if slices.Contains(r.Tags[:i], tag) {
			return fmt.Errorf("tag %q is repeated", tag)
		}
	}
	return nil
}

func checkText(field, s string, maxLen int) error {
	switch n := utf8.RuneCountInString(s); {
	case !utf8.ValidString(s):
		return fmt.Errorf("%s is not valid UTF-8", field)
	case strings.TrimSpace(s) == "":
		return fmt.Errorf("%s is blank", field)
	case n > maxLen:
		return fmt.Errorf("%s has %d characters, more than %d", field, n, maxLen)
	}
	return nil
}

// Read decodes and validates JSONL rows. Unknown fields and missing required fields are errors.
func Read(r io.Reader) ([]Row, error) {
	var rows []Row
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		row, err := decodeLine(sc.Bytes())
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func decodeLine(b []byte) (Row, error) {
	// Unmarshal also rejects trailing data, duplicate names and invalid UTF-8.
	var raw rawRow
	if err := json.Unmarshal(b, &raw, json.RejectUnknownMembers(true)); err != nil {
		return Row{}, err
	}
	required := []struct {
		name    string
		missing bool
	}{
		{"id", raw.ID == nil},
		{"source", raw.Source == nil},
		{"lang", raw.Lang == nil},
		{"subject", raw.Subject == nil},
		{"body", raw.Body == nil},
		{"expected_category", raw.ExpectedCategory == nil},
		{"expected_complaint", raw.ExpectedComplaint == nil},
		{"expected_urgency", raw.ExpectedUrgency == nil},
	}
	for _, f := range required {
		if f.missing {
			return Row{}, fmt.Errorf("%s is missing", f.name)
		}
	}
	row := Row{
		ID:                *raw.ID,
		Source:            *raw.Source,
		Lang:              *raw.Lang,
		Subject:           *raw.Subject,
		Body:              *raw.Body,
		ExpectedCategory:  *raw.ExpectedCategory,
		ExpectedComplaint: *raw.ExpectedComplaint,
		ExpectedUrgency:   *raw.ExpectedUrgency,
		Tags:              raw.Tags,
	}
	if err := row.Validate(); err != nil {
		return Row{}, fmt.Errorf("row %q: %w", row.ID, err)
	}
	return row, nil
}

// ReadFile reads and validates the JSONL file at path.
func ReadFile(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rows, nil
}

// Write validates rows and encodes them as JSONL, one compact row per line, without HTML escaping.
func Write(w io.Writer, rows []Row) error {
	for _, row := range rows {
		if err := row.Validate(); err != nil {
			return fmt.Errorf("row %q: %w", row.ID, err)
		}
		b, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}
