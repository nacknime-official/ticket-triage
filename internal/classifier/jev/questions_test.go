package jev

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
)

// guidelinesREADME holds the labeling guidelines complaintGuidelines and
// urgencyGuidelines copy.
const guidelinesREADME = "../../../testdata/eval/README.md"

// TestComplaintGuidelinesMatchREADME keeps the complaint definition sent to
// Jev and the one the labels follow the same text.
func TestComplaintGuidelinesMatchREADME(t *testing.T) {
	b, err := os.ReadFile(guidelinesREADME)
	if err != nil {
		t.Fatal(err)
	}
	section := markdownSection(string(b), "### Complaint")
	if section == "" {
		t.Fatalf("%s has no ### Complaint section", guidelinesREADME)
	}
	for _, tt := range []struct {
		lead string
		want []string
	}{
		{"Complaint (`true`):", complaintGuidelines.True},
		{"Not a complaint (`false`):", complaintGuidelines.False},
	} {
		if got := bulletsAfter(section, tt.lead); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s list %q =\n%q\nwant complaintGuidelines\n%q", guidelinesREADME, tt.lead, got, tt.want)
		}
	}
}

// TestUrgencyGuidelinesMatchREADME keeps the urgency definition sent to Jev
// and the one the labels follow the same text.
func TestUrgencyGuidelinesMatchREADME(t *testing.T) {
	b, err := os.ReadFile(guidelinesREADME)
	if err != nil {
		t.Fatal(err)
	}
	section := markdownSection(string(b), "### Urgency")
	if section == "" {
		t.Fatalf("%s has no ### Urgency section", guidelinesREADME)
	}
	leads := []string{"`0` low:", "`1` normal:", "`2` high:", "`3` critical:"}
	if len(urgencyGuidelines.Levels) != len(leads) {
		t.Fatalf("urgencyGuidelines has %d levels, want %d", len(urgencyGuidelines.Levels), len(leads))
	}
	if got := bulletsAfter(section, "All levels:"); !reflect.DeepEqual(got, urgencyGuidelines.All) {
		t.Errorf("%s list %q =\n%q\nwant urgencyGuidelines.All\n%q", guidelinesREADME, "All levels:", got,
			urgencyGuidelines.All)
	}
	for i, lead := range leads {
		if got := bulletsAfter(section, lead); !reflect.DeepEqual(got, urgencyGuidelines.Levels[i]) {
			t.Errorf("%s list %q =\n%q\nwant urgencyGuidelines.Levels[%d]\n%q", guidelinesREADME, lead, got, i,
				urgencyGuidelines.Levels[i])
		}
	}
}

// markdownSection returns the text under heading, up to the next heading of
// level three or higher.
func markdownSection(doc, heading string) string {
	_, rest, ok := strings.Cut(doc, "\n"+heading+"\n")
	if !ok {
		return ""
	}
	for _, next := range []string{"\n### ", "\n## ", "\n# "} {
		if i := strings.Index(rest, next); i >= 0 {
			rest = rest[:i+1]
		}
	}
	return rest
}

// bulletsAfter returns the bullet list that follows the line lead, each item
// with its wrapped lines joined by single spaces.
func bulletsAfter(section, lead string) []string {
	_, rest, ok := strings.Cut(section, "\n"+lead+"\n")
	if !ok {
		return nil
	}
	var items []string
	for line := range strings.Lines(strings.TrimLeft(rest, "\n")) {
		switch {
		case strings.HasPrefix(line, "- "):
			items = append(items, strings.TrimSpace(line[2:]))
		case strings.HasPrefix(line, "  ") && len(items) > 0:
			items[len(items)-1] += " " + strings.TrimSpace(line)
		default:
			return items
		}
	}
	return items
}

// TestRecordedRequestIsCurrent checks that live/ok-uk.json, the newest live
// success, recorded the request the adapter sends now, so Jev is known to
// accept it. After a question change, re-record it:
//
//	go run ./cmd/jevprobe -ticket gen-uk-001 -suffix -uk ok
func TestRecordedRequestIsCurrent(t *testing.T) {
	var rec struct {
		Request jsontext.Value `json:"request"`
	}
	if err := json.Unmarshal(fixture(t, "live/ok-uk.json"), &rec); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Model string `json:"model"`
		State state  `json:"state"`
	}
	if err := json.Unmarshal(rec.Request, &sent); err != nil {
		t.Fatal(err)
	}
	want := rec.Request
	if err := want.Compact(); err != nil {
		t.Fatal(err)
	}

	srv, last := serve(t, http.StatusOK, nil, live(t, "ok-uk.json").body())
	cfg := testConfig(srv.URL)
	cfg.Model = sent.Model
	in := classifier.Input{Subject: sent.State.Subject, Body: sent.State.Body}
	if _, err := newClient(t, cfg).Classify(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if got := last.Load().body; !bytes.Equal(got, want) {
		t.Errorf("adapter request =\n%s\nlive/ok-uk.json recorded\n%s\nre-record it with cmd/jevprobe", got, want)
	}
}

// TestQuestionsSHA256 checks that the hash covers exactly the questions on the
// wire, so cmd/eval's cache misses after any question change.
func TestQuestionsSHA256(t *testing.T) {
	srv, last := serve(t, http.StatusOK, nil, live(t, "ok.json").body())
	c := newClient(t, testConfig(srv.URL))
	if _, err := c.Classify(t.Context(), testInput); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Questions jsontext.Value `json:"questions"`
	}
	if err := json.Unmarshal(last.Load().body, &sent); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(sent.Questions)
	if got, want := c.QuestionsSHA256(), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("QuestionsSHA256() = %s, sha256 of the questions sent = %s", got, want)
	}

	other := testConfig(srv.URL)
	other.Model, other.APIKey = "typesafe/jev-2", "sk-other"
	if got := newClient(t, other).QuestionsSHA256(); got != c.QuestionsSHA256() {
		t.Errorf("another model and key: QuestionsSHA256() = %s, want %s", got, c.QuestionsSHA256())
	}

	edited := testConfig(srv.URL)
	edited.Taxonomy = classifier.DefaultTaxonomy()
	edited.Taxonomy[0].Description += " Refunds too."
	if got := newClient(t, edited).QuestionsSHA256(); got == c.QuestionsSHA256() {
		t.Errorf("an edited category description kept QuestionsSHA256() = %s", got)
	}
}
