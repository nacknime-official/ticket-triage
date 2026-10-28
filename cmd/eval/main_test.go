package main

import (
	"bufio"
	"bytes"
	"encoding/json/v2"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/evalset"
)

func envFunc(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func writeRows(t *testing.T, path string, rows []evalset.Row) {
	t.Helper()
	var b bytes.Buffer
	if err := evalset.Write(&b, rows); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// secretText marks ticket text that must never reach any output.
const secretText = "Zyx-ticket-text"

func TestRunFake(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRows(t, filepath.Join(data, "en.jsonl"), []evalset.Row{
		{ID: "en-1", Source: evalset.SourceGenerated, Lang: "en", Subject: "Refund " + secretText,
			Body: "Please refund the double charge " + secretText, ExpectedCategory: "billing", ExpectedUrgency: 2},
		{ID: "en-2", Source: evalset.SourceGenerated, Lang: "en", Subject: "Login",
			Body: "I forgot my password " + secretText, ExpectedCategory: "account", ExpectedUrgency: 1},
	})
	writeRows(t, filepath.Join(data, "uk.jsonl"), []evalset.Row{
		{ID: "uk-1", Source: evalset.SourceGenerated, Lang: "uk", Subject: "Помилка " + secretText,
			Body: "Застосунок вилітає " + secretText, ExpectedCategory: "technical", ExpectedUrgency: 1,
			Tags: []string{"typos"}},
	})
	out := filepath.Join(dir, "docs", "eval.md")
	results := filepath.Join(dir, "results", "eval.jsonl")
	cachePath := filepath.Join(dir, "cache.jsonl")

	var stdout, stderr bytes.Buffer
	args := []string{"-data", data, "-out", out, "-jsonl", results, "-cache", cachePath}
	if err := run(t.Context(), args, envFunc(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, stderr %s", err, stderr.String())
	}
	if want := "wrote " + out + ": 3 rows, 3 calls, 0 from cache, 0 failed\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}

	report, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**Fake classifier: a pipeline check, not a quality number.**",
		"| Classifier | `fake` (provider `fake`, model `fake-keywords-1`) |",
		"`en.jsonl` (2 rows, sha256 ", "| Rows | 3 (en 2, uk 1) |",
		"## Accuracy", "## Confusion matrices", "#### Priority uk", "## Latency", "## Cost",
		"## Threshold sweep", "| **0.60** |", "## Reproduce", liveCommand,
	} {
		if !bytes.Contains(report, []byte(want)) {
			t.Errorf("report has no %q", want)
		}
	}

	lines, err := os.ReadFile(results)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(lines))
	var ids []string
	for sc.Scan() {
		var l resultLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, l.ID)
		if l.Provider != "fake" || l.Status == "" || l.Attempts != 1 {
			t.Errorf("result line %+v", l)
		}
	}
	if strings.Join(ids, ",") != "en-1,en-2,uk-1" {
		t.Errorf("result ids = %v", ids)
	}

	for name, b := range map[string][]byte{"report": report, "results": lines, "stdout": stdout.Bytes(), "stderr": stderr.Bytes()} {
		if bytes.Contains(b, []byte(secretText)) {
			t.Errorf("%s holds ticket text", name)
		}
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Errorf("the fake wrote a cache file: %v", err)
	}
}

// TestRunEvalSet runs the fake over the committed eval set, as the committed
// docs/eval.md was produced.
func TestRunEvalSet(t *testing.T) {
	out := filepath.Join(t.TempDir(), "eval.md")
	var stdout, stderr bytes.Buffer
	args := []string{"-data", "../../testdata/eval", "-out", out}
	if err := run(t.Context(), args, envFunc(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, stderr %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), " 0 failed") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestRunErrors(t *testing.T) {
	dir := t.TempDir()
	dup := filepath.Join(dir, "dup")
	if err := os.Mkdir(dup, 0o755); err != nil {
		t.Fatal(err)
	}
	row := evalset.Row{ID: "x-1", Source: evalset.SourceGenerated, Lang: "en", Subject: "s", Body: "b",
		ExpectedCategory: "other"}
	writeRows(t, filepath.Join(dup, "a.jsonl"), []evalset.Row{row})
	writeRows(t, filepath.Join(dup, "b.jsonl"), []evalset.Row{row})

	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{"extra argument", []string{"jev"}, nil, "unexpected arguments"},
		{"bad threshold", []string{"-complaint-threshold", "1.5"}, nil, "outside [0, 1]"},
		{"negative limit", []string{"-limit", "-1"}, nil, "-limit -1 is negative"},
		{"unknown classifier", []string{"-classifier", "gpt"}, nil, "CLASSIFIER"},
		{"jev without a key", []string{"-classifier", "jev"}, nil, "OPENROUTER_API_KEY"},
		{"bad env", nil, map[string]string{"CATEGORY_MIN_CONFIDENCE": "2"}, "CATEGORY_MIN_CONFIDENCE"},
		{"missing env file", []string{"-env-file", filepath.Join(dir, "none.env")}, nil, "none.env"},
		{"no data", []string{"-data", dir}, nil, "no *.jsonl files"},
		{"duplicate ids", []string{"-data", dup}, nil, `row id "x-1" is also in a.jsonl`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-out", filepath.Join(t.TempDir(), "eval.md")}, tt.args...)
			var stdout, stderr bytes.Buffer
			err := run(t.Context(), args, envFunc(tt.env), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("run() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestReadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\n\nOPENROUTER_API_KEY=sk-or-secret\nexport JEV_MODEL = \"typesafe/jev-1.14\" \n" +
		"JEV_TIMEOUT='5s'\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"OPENROUTER_API_KEY": "sk-or-secret", "JEV_MODEL": "typesafe/jev-1.14", "JEV_TIMEOUT": "5s", "EMPTY": "",
	}
	if !maps.Equal(got, want) {
		t.Errorf("readEnvFile() = %v, want %v", got, want)
	}

	getenv := withFallback(envFunc(map[string]string{"JEV_MODEL": "from-env", "JEV_TIMEOUT": "  "}), got)
	for name, want := range map[string]string{
		"JEV_MODEL": "from-env", "JEV_TIMEOUT": "5s", "OPENROUTER_API_KEY": "sk-or-secret", "UNSET": "",
	} {
		if v := getenv(name); v != want {
			t.Errorf("getenv(%s) = %q, want %q", name, v, want)
		}
	}

	t.Run("a bad line names its number, not its text", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(bad, []byte("A=1\nsk-or-secret-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := readEnvFile(bad)
		if err == nil || !strings.Contains(err.Error(), "line 2") || strings.Contains(err.Error(), "secret") {
			t.Errorf("readEnvFile() error = %v", err)
		}
	})
}
