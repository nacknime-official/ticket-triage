package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const sampleCSV = `flags,instruction,category,intent,response
B,cancel order {{Order Number}},ORDER,cancel_order,"Sure, I can help."
BQ,can u cancel my order {{Order Number}},ORDER,cancel_order,ok
BL,"I want to cancel order {{Order Number}}, please",ORDER,cancel_order,"multi
line answer"
B,cancel order {{Order Number}},ORDER,cancel_order,duplicate instruction
BW,i want to file a goddamn claim,FEEDBACK,complaint,ok
B,how to set up a shipping address,SHIPPING,set_up_shipping_address,not kept
`

var sampleRules = map[string]intentRule{
	"cancel_order": {"other", 1, 2},
	"complaint":    {"other", 1, 1},
}

func TestBuildRows(t *testing.T) {
	rows, err := buildRows(strings.NewReader(sampleCSV), sampleRules, "seed")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if err := row.Validate(); err != nil {
			t.Errorf("row %+v: %v", row, err)
		}
		if strings.Contains(row.Body, "{{") || strings.Contains(row.Subject, "{{") {
			t.Errorf("placeholder left in %+v", row)
		}
		if !strings.HasPrefix(row.ID, "bitext-") || row.Source != "bitext" || row.Lang != "en" {
			t.Errorf("bad id, source or lang in %+v", row)
		}
	}

	// Rows are ordered by intent, so the complaint row is last.
	complaint := rows[2]
	if complaint.Body != "i want to file a goddamn claim" || !complaint.ExpectedComplaint {
		t.Errorf("complaint row = %+v, want the claim with expected_complaint true", complaint)
	}
	for _, row := range rows[:2] {
		if row.ExpectedComplaint || row.ExpectedCategory != "other" || row.ExpectedUrgency != 1 {
			t.Errorf("cancel_order row labels = %+v, want other, no complaint, urgency 1", row)
		}
	}
}

func TestBuildRowsDeterministic(t *testing.T) {
	ids := func(seed, csv string) string {
		rows, err := buildRows(strings.NewReader(csv), sampleRules, seed)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, row := range rows {
			b.WriteString(row.ID + " ")
		}
		return b.String()
	}

	// Reversing the data rows must not change the sample.
	lines := strings.SplitAfter(strings.ReplaceAll(sampleCSV, "\"multi\nline answer\"", "multi line answer"), "\n")
	header, data := lines[0], lines[1:len(lines)-1]
	reversed := header
	for i := len(data) - 1; i >= 0; i-- {
		reversed += data[i]
	}
	if a, b := ids("seed", sampleCSV), ids("seed", reversed); a != b {
		t.Errorf("sample depends on row order: %q vs %q", a, b)
	}

	seeds := map[string]bool{}
	for _, seed := range []string{"a", "b", "c", "d", "e", "f"} {
		seeds[ids(seed, sampleCSV)] = true
	}
	if len(seeds) < 2 {
		t.Errorf("six seeds gave the same sample; the seed has no effect")
	}
}

func TestBuildRowsErrors(t *testing.T) {
	tests := []struct {
		name    string
		csv     string
		rules   map[string]intentRule
		wantErr string
	}{
		{"bad header", "a,b,c\n1,2,3\n", sampleRules, "header"},
		{"too few rows", sampleCSV, map[string]intentRule{"complaint": {"other", 1, 2}}, "has 1 distinct rows, want 2"},
		{"unknown placeholder", "flags,instruction,category,intent,response\nB,hi {{Pet Name}},X,complaint,ok\n",
			map[string]intentRule{"complaint": {"other", 1, 1}}, "Pet Name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildRows(strings.NewReader(tt.csv), tt.rules, "seed")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("buildRows() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSubjectOf(t *testing.T) {
	tests := []struct {
		body, want string
	}{
		{"short body", "short body"},
		{strings.Repeat("a", 60), strings.Repeat("a", 60)},
		{"I would like to know how I can get a refund for the order I placed, last week", "I would like to know how I can get a refund for the order I…"},
		{"comma before the cut, " + strings.Repeat("x", 50), "comma before the cut…"},
		{strings.Repeat("b", 70), strings.Repeat("b", 60) + "…"},
	}
	for _, tt := range tests {
		if got := subjectOf(tt.body); got != tt.want {
			t.Errorf("subjectOf(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

func TestFetch(t *testing.T) {
	const content = "flags,instruction\n"
	sum := sha256.Sum256([]byte(content))
	wantSHA := hex.EncodeToString(sum[:])

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(content))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "raw", "bitext.csv")

	if err := fetch(t.Context(), srv.Client(), srv.URL+"/ok", wantSHA, path); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != content {
		t.Fatalf("file = %q, %v; want %q", got, err, content)
	}

	// A file with the right checksum is not downloaded again.
	if err := fetch(t.Context(), srv.Client(), srv.URL+"/ok", wantSHA, path); err != nil {
		t.Fatal(err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("server got %d requests, want 1", n)
	}

	// A checksum mismatch fails and leaves no file behind.
	other := filepath.Join(dir, "raw", "other.csv")
	err := fetch(t.Context(), srv.Client(), srv.URL+"/ok", strings.Repeat("0", 64), other)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("fetch() with a wrong checksum: error = %v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Errorf("file left after a checksum mismatch: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "raw")); len(entries) != 1 {
		t.Errorf("raw dir has %d entries, want only bitext.csv (temporary file left behind?)", len(entries))
	}

	if err := fetch(t.Context(), srv.Client(), srv.URL+"/missing", wantSHA, other); err == nil ||
		!strings.Contains(err.Error(), "404") {
		t.Errorf("fetch() of a missing file: error = %v, want a 404", err)
	}
}
