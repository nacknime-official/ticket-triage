package jev

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
)

// guidelinesREADME holds the labeling guidelines complaintGuidelines copies.
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
