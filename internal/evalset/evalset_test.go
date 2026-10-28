package evalset

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
		wantErr string           // part of an error message that evalset writes
		isErr   func(error) bool // matches a decode error, whose json/v2 text is not stable
	}{
		{"valid", `{` + valid + `}`, "", nil},
		{"valid with tags", `{` + valid + `,"tags":["angry","typos"]}`, "", nil},
		{"unknown field", `{` + valid + `,"priority":"high"}`, "", semanticErrAt("/priority", json.ErrUnknownName)},
		{"missing complaint", `{"id":"x","source":"generated","lang":"en","subject":"s","body":"b",` +
			`"expected_category":"billing","expected_urgency":2}`, "expected_complaint is missing", nil},
		{"missing urgency", `{"id":"x","source":"generated","lang":"en","subject":"s","body":"b",` +
			`"expected_category":"billing","expected_complaint":true}`, "expected_urgency is missing", nil},
		{"bad category", strings.Replace(`{`+valid+`}`, `"billing"`, `"sales"`, 1), "expected_category", nil},
		{"urgency too high", strings.Replace(`{`+valid+`}`, `"expected_urgency":2`, `"expected_urgency":4`, 1), "outside", nil},
		{"urgency negative", strings.Replace(`{`+valid+`}`, `"expected_urgency":2`, `"expected_urgency":-1`, 1), "outside", nil},
		{"urgency not an integer", strings.Replace(`{`+valid+`}`, `"expected_urgency":2`, `"expected_urgency":1.5`, 1), "", notAnInt("/expected_urgency", jsontext.Kind('0'))},
		{"urgency as string", strings.Replace(`{`+valid+`}`, `"expected_urgency":2`, `"expected_urgency":"high"`, 1), "", notAnInt("/expected_urgency", jsontext.Kind('"'))},
		{"bad lang", strings.Replace(`{`+valid+`}`, `"lang":"en"`, `"lang":"ru"`, 1), "lang", nil},
		{"bad source", strings.Replace(`{`+valid+`}`, `"source":"generated"`, `"source":"twitter"`, 1), "source", nil},
		{"blank subject", strings.Replace(`{`+valid+`}`, `"subject":"s"`, `"subject":"  "`, 1), "subject is blank", nil},
		{"long subject", strings.Replace(`{`+valid+`}`, `"subject":"s"`, `"subject":"`+strings.Repeat("я", 201)+`"`, 1), "more than 200", nil},
		{"long body", strings.Replace(`{`+valid+`}`, `"body":"b"`, `"body":"`+strings.Repeat("b", 5001)+`"`, 1), "more than 5000", nil},
		{"unknown tag", `{` + valid + `,"tags":["rude"]}`, "tag", nil},
		{"repeated tag", `{` + valid + `,"tags":["angry","angry"]}`, "repeated", nil},
		{"duplicate name", `{` + valid + `,"lang":"uk"}`, "", syntacticErrAt("/lang", int64(len(`{`+valid+`,`)), jsontext.ErrDuplicateName)},
		{"two values on a line", `{` + valid + `}{}`, "", syntacticErrAt("", int64(len(`{`+valid+`}`)), nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := Read(strings.NewReader(tt.line + "\n"))
			switch {
			case tt.isErr != nil:
				if !tt.isErr(err) {
					t.Fatalf("Read() error = %v, want a decode error for this case", err)
				}
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Read() error = %v, want it to contain %q", err, tt.wantErr)
				}
			default:
				if err != nil || len(rows) != 1 {
					t.Fatalf("Read() = %d rows, %v; want 1 row", len(rows), err)
				}
			}
		})
	}
}

// json/v2 deliberately varies its error text between processes ("cannot" or "unable to"),
// so decode errors are matched by type and location instead.

// notAnInt matches a json/v2 error for a JSON value of kind at pointer that does not decode into an int.
func notAnInt(pointer jsontext.Pointer, kind jsontext.Kind) func(error) bool {
	return func(err error) bool {
		var se *json.SemanticError
		return errors.As(err, &se) && se.JSONPointer == pointer && se.JSONKind == kind && se.GoType == reflect.TypeFor[int]()
	}
}

// semanticErrAt matches a json/v2 semantic error at pointer that wraps target.
func semanticErrAt(pointer jsontext.Pointer, target error) func(error) bool {
	return func(err error) bool {
		var se *json.SemanticError
		return errors.As(err, &se) && se.JSONPointer == pointer && errors.Is(se.Err, target)
	}
}

// syntacticErrAt matches a jsontext syntactic error at pointer and byte offset that wraps target, if target is not nil.
func syntacticErrAt(pointer jsontext.Pointer, offset int64, target error) func(error) bool {
	return func(err error) bool {
		var se *jsontext.SyntacticError
		return errors.As(err, &se) && se.JSONPointer == pointer && se.ByteOffset == offset &&
			(target == nil || errors.Is(se.Err, target))
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
