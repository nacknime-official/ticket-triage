package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/evalset"
)

const testKey = "test-key-5e1f"

const okBody = `{"model":"typesafe/jev-1.14-20261014","answers":{` +
	`"is_complaint":{"type":"noul","noul":0.85},` +
	`"category":{"type":"choice","choice":"technical","probabilities":{"technical":1},"confidence":1},` +
	`"urgency":{"type":"score","score":2.99,"legend":{"0":"a"},"probabilities":{"3":0.99},"confidence":0.99}},` +
	`"usage":{"input_tokens":582,"output_tokens":87,"cost":0.000024444},"id":"gen-dec-1","provider":"TypeSafe"}`

var row = evalset.Row{
	ID: "gen-en-001", Source: evalset.SourceGenerated, Lang: "en",
	Subject: "Dashboard down for everyone", Body: "Nobody can load the dashboard.",
	ExpectedCategory: "technical", ExpectedUrgency: 3,
}

// seen is a request the fake Jev got.
type seen struct {
	path, auth string
	body       []byte
}

// fakeJev answers like OpenRouter: 401 for any key but testKey, 400 for an unknown model or a body that
// is not JSON, else 200. answer, if set, replaces the success body.
func fakeJev(t *testing.T, answer string) (*httptest.Server, func() []seen) {
	t.Helper()
	var (
		mu   sync.Mutex
		reqs []seen
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		mu.Lock()
		reqs = append(reqs, seen{r.URL.Path, r.Header.Get("Authorization"), b})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=abc")
		w.Header().Set("Cf-Ray", "123")
		w.Header().Set("X-Provider-Name", "TypeSafe")
		var req struct {
			Model string `json:"model"`
		}
		status, body := http.StatusOK, okBody
		switch {
		case r.Header.Get("Authorization") != "Bearer "+testKey:
			status, body = http.StatusUnauthorized, `{"error":{"message":"User not found.","code":401}}`
		case json.Unmarshal(b, &req) != nil:
			status, body = http.StatusBadRequest, `{"error":{"message":"JSON parsing failed","code":400},"user_id":"user_SECRET"}`
		case req.Model == unknownModel:
			status, body = http.StatusBadRequest, `{"error":{"message":"Model does not exist","code":400}}`
		case answer != "":
			body = answer
		}
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), reqs...)
	}
}

func evalFile(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := evalset.Write(&buf, []evalset.Row{row}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "eval.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func env(key string) func(string) string {
	return func(name string) string {
		if name == "OPENROUTER_API_KEY" {
			return key
		}
		return ""
	}
}

func readRecording(t *testing.T, path string) recording {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec recording
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return rec
}

func TestRun(t *testing.T) {
	srv, requests := fakeJev(t, "")
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"-base-url", srv.URL, "-eval", evalFile(t), "-out", out,
		"ok", "bad-key", "unknown-model", "bad-criteria", "one-level", "bad-json", "decisions"}
	if err := run(t.Context(), args, env(testKey), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v\n%s", err, stderr.String())
	}

	tests := []struct {
		name, wantPath string
		wantStatus     int
		check          func(t *testing.T, rec recording, req seen)
	}{
		{"ok", "/v1/systemone", 200, func(t *testing.T, rec recording, req seen) {
			if req.auth != "Bearer "+testKey {
				t.Error("ok did not send the key")
			}
			// The recording is indented; compacted, it is the body sent.
			got := jsontext.Value(bytes.Clone(rec.Request))
			if err := got.Compact(); err != nil || !bytes.Equal(got, req.body) {
				t.Errorf("recorded request\n%s\nwant the body sent\n%s", got, req.body)
			}
			var body struct {
				Usage struct {
					Cost jsontext.Value `json:"cost"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(rec.Body, &body); err != nil || string(body.Usage.Cost) != "0.000024444" {
				t.Errorf("recorded cost = %s (%v), want the literal 0.000024444", body.Usage.Cost, err)
			}
		}},
		{"bad-key", "/v1/systemone", 401, func(t *testing.T, _ recording, req seen) {
			if req.auth != "Bearer "+invalidKey {
				t.Error("bad-key did not send the invalid key")
			}
		}},
		{"unknown-model", "/v1/systemone", 400, func(t *testing.T, rec recording, _ seen) {
			var r struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(rec.Request, &r); err != nil || r.Model != unknownModel {
				t.Errorf("model = %q (%v), want %q", r.Model, err, unknownModel)
			}
		}},
		{"bad-criteria", "/v1/systemone", 200, func(t *testing.T, rec recording, _ seen) {
			var r struct {
				Questions struct {
					Category struct {
						Criteria []string `json:"criteria"`
					} `json:"category"`
				} `json:"questions"`
			}
			if err := json.Unmarshal(rec.Request, &r); err != nil || len(r.Questions.Category.Criteria) != 5 {
				t.Errorf("category criteria = %v (%v), want an array of the 5 options", r.Questions.Category.Criteria, err)
			}
		}},
		{"one-level", "/v1/systemone", 200, func(t *testing.T, rec recording, _ seen) {
			var r struct {
				Questions struct {
					Urgency struct {
						Criteria []string `json:"criteria"`
					} `json:"urgency"`
				} `json:"questions"`
			}
			if err := json.Unmarshal(rec.Request, &r); err != nil || len(r.Questions.Urgency.Criteria) != 1 {
				t.Errorf("urgency criteria = %v (%v), want one level", r.Questions.Urgency.Criteria, err)
			}
		}},
		{"bad-json", "/v1/systemone", 400, func(t *testing.T, rec recording, req seen) {
			var sent string
			if err := json.Unmarshal(rec.Request, &sent); err != nil || sent != string(req.body) {
				t.Errorf("recorded request = %s (%v), want the truncated body as a string", rec.Request, err)
			}
			var body struct {
				UserID string `json:"user_id"`
			}
			if err := json.Unmarshal(rec.Body, &body); err != nil || body.UserID != "redacted" {
				t.Errorf("account id is not redacted: %s", rec.Body)
			}
		}},
		{"decisions", "/alpha/decisions", 200, func(t *testing.T, rec recording, _ seen) {
			if !strings.HasSuffix(rec.URL, "/alpha/decisions") {
				t.Errorf("URL = %s, want the decisions path", rec.URL)
			}
		}},
	}
	reqs := requests()
	if len(reqs) != len(tests) {
		t.Fatalf("server got %d requests, want %d", len(reqs), len(tests))
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := readRecording(t, filepath.Join(out, tt.name+".json"))
			if rec.Case != tt.name || rec.Ticket != row.ID || rec.Status != tt.wantStatus || reqs[i].path != tt.wantPath {
				t.Errorf("recording %s/%s status %d at %s, want %s/%s %d at %s",
					rec.Case, rec.Ticket, rec.Status, reqs[i].path, tt.name, row.ID, tt.wantStatus, tt.wantPath)
			}
			if _, ok := rec.Header["set-cookie"]; ok {
				t.Error("Set-Cookie is recorded")
			}
			if _, ok := rec.Header["cf-ray"]; ok {
				t.Error("an unlisted header is recorded")
			}
			if rec.Header["x-provider-name"] != "TypeSafe" {
				t.Errorf("headers = %v, want X-Provider-Name kept", rec.Header)
			}
			tt.check(t, rec, reqs[i])
		})
	}

	for _, want := range []string{
		`case ok: HTTP 200`, `model "typesafe/jev-1.14-20261014", tokens in 582 out 87, cost $0.000024444 (reported)`,
		"category technical (1.00)", "case bad-key: HTTP 401", "auth error", "total cost $0.000",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}
	outputs := map[string]string{"stdout": stdout.String(), "stderr": stderr.String()}
	files, err := filepath.Glob(filepath.Join(out, "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		outputs[f] = string(b)
	}
	for where, s := range outputs {
		for _, secret := range []string{testKey, "Bearer", "Authorization"} {
			if strings.Contains(s, secret) {
				t.Errorf("%s contains %q", where, secret)
			}
		}
		if where == "stdout" || where == "stderr" {
			if strings.Contains(s, row.Subject) || strings.Contains(s, row.Body) {
				t.Errorf("%s contains the ticket text", where)
			}
		}
	}
}

func TestRunRepeat(t *testing.T) {
	srv, requests := fakeJev(t, "")
	out := t.TempDir()
	var stdout bytes.Buffer
	args := []string{"-base-url", srv.URL, "-eval", evalFile(t), "-out", out, "-suffix", "-rate", "-repeat", "3", "ok"}
	if err := run(t.Context(), args, env(testKey), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := len(requests()); n != 3 {
		t.Errorf("server got %d requests, want 3", n)
	}
	for _, f := range []string{"ok-rate.1.json", "ok-rate.2.json", "ok-rate.3.json"} {
		if rec := readRecording(t, filepath.Join(out, f)); rec.Status != 200 {
			t.Errorf("%s status = %d", f, rec.Status)
		}
	}
}

func TestRunNoOut(t *testing.T) {
	srv, _ := fakeJev(t, "")
	var stdout bytes.Buffer
	args := []string{"-base-url", srv.URL, "-eval", evalFile(t), "-out", "", "ok"}
	if err := run(t.Context(), args, env(testKey), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "saved") || !strings.Contains(stdout.String(), "case ok: HTTP 200") {
		t.Errorf("-out \"\" output:\n%s", stdout.String())
	}
}

// TestRunRefusesKeyInRecording checks that a response echoing the key is never written.
func TestRunRefusesKeyInRecording(t *testing.T) {
	srv, _ := fakeJev(t, `{"echo":"`+testKey+`"}`)
	out := t.TempDir()
	var stdout bytes.Buffer
	args := []string{"-base-url", srv.URL, "-eval", evalFile(t), "-out", out, "ok"}
	err := run(t.Context(), args, env(testKey), &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "contains the API key") {
		t.Fatalf("run() error = %v, want a refusal", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(stdout.String(), testKey) {
		t.Error("the refusal prints the key")
	}
	if files, _ := os.ReadDir(out); len(files) != 0 {
		t.Errorf("files written: %v", files)
	}
}

func TestRunErrors(t *testing.T) {
	eval := evalFile(t)
	tests := []struct {
		name string
		args []string
		key  string
		want string
	}{
		{"unknown case", []string{"nope"}, testKey, `unknown case "nope"`},
		{"repeat above a handful", []string{"-repeat", "6"}, testKey, "-repeat 6 is outside 1..5"},
		{"too many calls", []string{"-max-calls", "2", "ok", "bad-key", "bad-json"}, testKey, "3 calls exceed -max-calls 2"},
		{"unknown provider", []string{"-provider", "acme"}, testKey, `provider "acme"`},
		{"missing key", nil, "", "OPENROUTER_API_KEY is empty"},
		{"unknown ticket", []string{"-ticket", "gen-xx-999"}, testKey, `ticket "gen-xx-999" is not in`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-base-url", "http://127.0.0.1:1", "-eval", eval, "-out", ""}, tt.args...)
			err := run(t.Context(), args, env(tt.key), io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("run() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
