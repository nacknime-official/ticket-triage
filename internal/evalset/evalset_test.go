package evalset

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const evalDir = "../../testdata/eval"

// TestCommittedFiles validates every committed eval row and the coverage the eval set promises.
func TestCommittedFiles(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(evalDir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no *.jsonl files in %s", evalDir)
	}

	var all []Row
	for _, path := range paths {
		rows, err := ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Errorf("%s: no rows", path)
		}

		// The files are in the form Write produces, so a re-import or a hand edit shows up as a clean diff.
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := Write(&got, rows); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: not in canonical form: one compact JSON object per line, fields in schema order", path)
		}
		all = append(all, rows...)
	}

	ids := make(map[string]bool)
	langCount := make(map[string]int)
	categoriesByLang := make(map[string]map[string]bool)
	urgencies := make(map[int]bool)
	complaints := make(map[bool]bool)
	for _, row := range all {
		if ids[row.ID] {
			t.Errorf("id %q is used more than once", row.ID)
		}
		ids[row.ID] = true

		wantPrefix := map[string]string{SourceGenerated: "gen-" + row.Lang + "-", SourceBitext: "bitext-"}[row.Source]
		if !strings.HasPrefix(row.ID, wantPrefix) {
			t.Errorf("id %q: source %s rows use the prefix %q", row.ID, row.Source, wantPrefix)
		}

		langCount[row.Lang]++
		if categoriesByLang[row.Lang] == nil {
			categoriesByLang[row.Lang] = make(map[string]bool)
		}
		categoriesByLang[row.Lang][row.ExpectedCategory] = true
		urgencies[row.ExpectedUrgency] = true
		complaints[row.ExpectedComplaint] = true
	}

	for _, lang := range Langs {
		if share := float64(langCount[lang]) / float64(len(all)); share < 0.4 {
			t.Errorf("lang %s: %d of %d rows, want at least 40%%", lang, langCount[lang], len(all))
		}
		for _, category := range Categories {
			if !categoriesByLang[lang][category] {
				t.Errorf("lang %s: no row with category %s", lang, category)
			}
		}
	}
	for level := range MaxUrgency + 1 {
		if !urgencies[level] {
			t.Errorf("no row with urgency %d", level)
		}
	}
	if !complaints[true] || !complaints[false] {
		t.Errorf("complaint values covered: %v, want both", complaints)
	}
}

func TestRead(t *testing.T) {
	const valid = `"id":"gen-en-001","source":"generated","lang":"en","subject":"s","body":"b",` +
		`"expected_category":"billing","expected_complaint":false,"expected_urgency":2`

	tests := []struct {
		name    string
		line    string
		wantErr string
	}{
		{"valid", `{` + valid + `}`, ""},
		{"valid with tags", `{` + valid + `,"tags":["angry","typos"]}`, ""},
		{"unknown field", `{` + valid + `,"priority":"high"}`, "unknown object member"},
		{"missing complaint", `{"id":"x","source":"generated","lang":"en","subject":"s","body":"b",` +
			`"expected_category":"billing","expected_urgency":2}`, "expected_complaint is missing"},
		{"missing urgency", `{"id":"x","source":"generated","lang":"en","subject":"s","body":"b",` +
			`"expected_category":"billing","expected_complaint":true}`, "expected_urgency is missing"},
		{"bad category", strings.Replace(`{`+valid+`}`, `"billing"`, `"sales"`, 1), "expected_category"},
		{"urgency too high", strings.Replace(`{`+valid+`}`, `"expected_urgency":2`, `"expected_urgency":4`, 1), "outside"},
		{"urgency negative", strings.Replace(`{`+valid+`}`, `"expected_urgency":2`, `"expected_urgency":-1`, 1), "outside"},
		{"urgency not an integer", strings.Replace(`{`+valid+`}`, `"expected_urgency":2`, `"expected_urgency":1.5`, 1), "cannot unmarshal"},
		{"urgency as string", strings.Replace(`{`+valid+`}`, `"expected_urgency":2`, `"expected_urgency":"high"`, 1), "cannot unmarshal"},
		{"bad lang", strings.Replace(`{`+valid+`}`, `"lang":"en"`, `"lang":"ru"`, 1), "lang"},
		{"bad source", strings.Replace(`{`+valid+`}`, `"source":"generated"`, `"source":"twitter"`, 1), "source"},
		{"blank subject", strings.Replace(`{`+valid+`}`, `"subject":"s"`, `"subject":"  "`, 1), "subject is blank"},
		{"long subject", strings.Replace(`{`+valid+`}`, `"subject":"s"`, `"subject":"`+strings.Repeat("я", 201)+`"`, 1), "more than 200"},
		{"long body", strings.Replace(`{`+valid+`}`, `"body":"b"`, `"body":"`+strings.Repeat("b", 5001)+`"`, 1), "more than 5000"},
		{"unknown tag", `{` + valid + `,"tags":["rude"]}`, "tag"},
		{"repeated tag", `{` + valid + `,"tags":["angry","angry"]}`, "repeated"},
		{"duplicate name", `{` + valid + `,"lang":"uk"}`, "duplicate"},
		{"two values on a line", `{` + valid + `}{}`, "after top-level value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := Read(strings.NewReader(tt.line + "\n"))
			if tt.wantErr == "" {
				if err != nil || len(rows) != 1 {
					t.Fatalf("Read() = %d rows, %v; want 1 row", len(rows), err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Read() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestWriteRoundTrip(t *testing.T) {
	rows := []Row{{
		ID: "gen-uk-001", Source: SourceGenerated, Lang: "uk", Subject: "Оплата <не> проходить",
		Body: "Пише «помилка» & все.", ExpectedCategory: "billing", ExpectedUrgency: 2, Tags: []string{"typos"},
	}}
	var buf bytes.Buffer
	if err := Write(&buf, rows); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "<не>") {
		t.Errorf("Write escaped HTML: %s", buf.String())
	}
	got, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(got, rows, func(a, b Row) bool { return a.ID == b.ID && a.Body == b.Body && slices.Equal(a.Tags, b.Tags) }) {
		t.Errorf("round trip = %+v, want %+v", got, rows)
	}
}
