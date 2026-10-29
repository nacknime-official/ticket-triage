package jev

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
)

const testKey = "sk-test-KEY-7f3a9c"

var testInput = classifier.Input{
	Subject: "Charged twice for October",
	Body:    "Hi, my card was charged twice for the October invoice.",
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// recording is a response recorded live by cmd/jevprobe (testdata/live).
type recording struct {
	Status int               `json:"status"`
	Header map[string]string `json:"header"`
	Body   jsontext.Value    `json:"body"`
}

func live(t *testing.T, name string) recording {
	t.Helper()
	var rec recording
	if err := json.Unmarshal(fixture(t, filepath.Join("live", name)), &rec); err != nil {
		t.Fatalf("live/%s: %v", name, err)
	}
	return rec
}

// body returns the response body as it was sent; jevprobe stores a body that
// is not JSON as a JSON string.
func (r recording) body() []byte {
	var s string
	if r.Body.Kind() == '"' && json.Unmarshal(r.Body, &s) == nil {
		return []byte(s)
	}
	return r.Body
}

// serveLive starts a server that replays a recorded response.
func serveLive(t *testing.T, name string) *httptest.Server {
	t.Helper()
	rec := live(t, name)
	h := http.Header{}
	for k, v := range rec.Header {
		h.Set(k, v)
	}
	srv, _ := serve(t, rec.Status, h, rec.body())
	return srv
}

func testConfig(baseURL string) Config {
	return Config{
		Provider:             ProviderOpenRouter,
		BaseURL:              baseURL,
		APIKey:               testKey,
		Model:                "typesafe/jev-1.14",
		Timeout:              time.Second,
		RateLimit:            1000,
		MaxConcurrency:       4,
		InputPricePerMTokUSD: 0.042,
	}
}

func newClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// recorded is the request a test server saw.
type recorded struct {
	method, path string
	header       http.Header
	body         []byte
}

// serve starts a server that answers every call with status, header and
// body, and records the last request.
func serve(t *testing.T, status int, header http.Header, body []byte) (*httptest.Server, *atomic.Pointer[recorded]) {
	t.Helper()
	var last atomic.Pointer[recorded]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		last.Store(&recorded{r.Method, r.URL.Path, r.Header.Clone(), b})
		for k, v := range header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write(body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func TestClassifyGolden(t *testing.T) {
	// The openrouter cases replay live recordings; the direct API one is built
	// by hand (testdata/README.md).
	tests := []struct {
		name, provider string
		body           func(*testing.T) []byte
		wantWireModel  string
		want           classifier.Result
	}{
		{
			name: "openrouter english", provider: ProviderOpenRouter, body: liveBody("ok.json"),
			wantWireModel: "typesafe/jev-1.14",
			want: classifier.Result{
				ComplaintP: 0.86, Category: classifier.CategoryTechnical, CategoryConfidence: 1,
				Urgency: 2.99, UrgencyConfidence: 0.99,
				Call: classifier.Call{
					Provider: ProviderOpenRouter, Model: "typesafe/jev-1.14-20261014", HTTPStatus: 200,
					InputTokens: 582, OutputTokens: 87, CostUSD: 0.000024444, CostSource: classifier.CostReported,
				},
			},
		},
		{
			name: "openrouter ukrainian", provider: ProviderOpenRouter, body: liveBody("ok-uk.json"),
			wantWireModel: "typesafe/jev-1.14",
			want: classifier.Result{
				ComplaintP: 0.12, Category: classifier.CategoryBilling, CategoryConfidence: 1,
				Urgency: 1.95, UrgencyConfidence: 0.95,
				Call: classifier.Call{
					Provider: ProviderOpenRouter, Model: "typesafe/jev-1.14-20261014", HTTPStatus: 200,
					InputTokens: 807, OutputTokens: 87, CostUSD: 0.000033894, CostSource: classifier.CostReported,
				},
			},
		},
		{
			name: "openrouter decisions api", provider: ProviderOpenRouter, body: liveBody("decisions.json"),
			wantWireModel: "typesafe/jev-1.14",
			want: classifier.Result{
				ComplaintP: 0.86, Category: classifier.CategoryTechnical, CategoryConfidence: 1,
				Urgency: 2.99, UrgencyConfidence: 0.99,
				Call: classifier.Call{
					Provider: ProviderOpenRouter, Model: "typesafe/jev-1.14-20261014", HTTPStatus: 200,
					InputTokens: 582, OutputTokens: 87, CostUSD: 0.000024444, CostSource: classifier.CostReported,
				},
			},
		},
		{
			name: "openrouter score with one level", provider: ProviderOpenRouter, body: liveBody("one-level.json"),
			wantWireModel: "typesafe/jev-1.14",
			want: classifier.Result{
				ComplaintP: 0.86, Category: classifier.CategoryTechnical, CategoryConfidence: 1,
				Urgency: 0, UrgencyConfidence: 1,
				Call: classifier.Call{
					Provider: ProviderOpenRouter, Model: "typesafe/jev-1.14-20261014", HTTPStatus: 200,
					InputTokens: 524, OutputTokens: 87, CostUSD: 0.000022008, CostSource: classifier.CostReported,
				},
			},
		},
		{
			name: "typesafe cost is estimated", provider: ProviderTypeSafe, body: fixtureBody("response_typesafe.json"),
			wantWireModel: "jev-1.14.0",
			want: classifier.Result{
				ComplaintP: 0.12, Category: classifier.CategoryTechnical, CategoryConfidence: 0.79,
				Urgency: 1.2, UrgencyConfidence: 0.55,
				Call: classifier.Call{
					Provider: ProviderTypeSafe, Model: "jev-1.14.0", HTTPStatus: 200,
					InputTokens: 452, OutputTokens: 87, CostUSD: 452 * 0.042 / 1e6, CostSource: classifier.CostEstimated,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, last := serve(t, http.StatusOK, nil, tt.body(t))
			cfg := testConfig(srv.URL)
			cfg.Provider = tt.provider
			got, err := newClient(t, cfg).Classify(t.Context(), testInput)
			if err != nil {
				t.Fatalf("Classify() error = %v", err)
			}

			if got.Call.Latency <= 0 {
				t.Errorf("Latency = %v, want > 0", got.Call.Latency)
			}
			if len(got.Call.RawResponse) == 0 {
				t.Error("RawResponse is empty")
			}
			cmp := got
			cmp.Call.Latency, cmp.Call.RawResponse = 0, nil
			if math.Abs(cmp.Call.CostUSD-tt.want.Call.CostUSD) < 1e-15 {
				cmp.Call.CostUSD = tt.want.Call.CostUSD
			}
			if !reflect.DeepEqual(cmp, tt.want) {
				t.Errorf("Classify() = %+v\nwant         %+v", cmp, tt.want)
			}

			var req struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(last.Load().body, &req); err != nil {
				t.Fatal(err)
			}
			if req.Model != tt.wantWireModel {
				t.Errorf("request model = %q, want %q", req.Model, tt.wantWireModel)
			}
		})
	}
}

func liveBody(name string) func(*testing.T) []byte {
	return func(t *testing.T) []byte { return live(t, name).body() }
}

func fixtureBody(name string) func(*testing.T) []byte {
	return func(t *testing.T) []byte { return fixture(t, name) }
}

func TestRequest(t *testing.T) {
	srv, last := serve(t, http.StatusOK, nil, live(t, "ok.json").body())
	if _, err := newClient(t, testConfig(srv.URL+"/api/")).Classify(t.Context(), testInput); err != nil {
		t.Fatal(err)
	}
	r := last.Load()
	if r.method != http.MethodPost || r.path != "/api/v1/systemone" {
		t.Errorf("request = %s %s, want POST /api/v1/systemone", r.method, r.path)
	}
	for k, want := range map[string]string{
		"Authorization": "Bearer " + testKey,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	} {
		if got := r.header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}

	// Byte equality after compacting also checks the member order, so the
	// categories reach Jev in taxonomy order.
	golden := jsontext.Value(fixture(t, "request.golden.json"))
	if err := golden.Compact(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.body, golden) {
		t.Errorf("request body =\n%s\nwant\n%s", r.body, golden)
	}
}

func TestProviderBaseURLs(t *testing.T) {
	for _, p := range []string{ProviderOpenRouter, ProviderTypeSafe} {
		cfg := testConfig("")
		cfg.Provider = p
		c := newClient(t, cfg)
		if want := providers[p].baseURL + endpointPath; c.url != want {
			t.Errorf("%s url = %q, want %q", p, c.url, want)
		}
	}
	if got := providers[ProviderOpenRouter].baseURL; got != "https://openrouter.ai/api" {
		t.Errorf("openrouter base URL = %q", got)
	}
	if got := providers[ProviderTypeSafe].baseURL; got != "https://api.typesafe.ai" {
		t.Errorf("typesafe base URL = %q", got)
	}
}

func TestTypeSafeModel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"typesafe/jev-1.14", "jev-1.14.0"},
		{"~typesafe/jev-latest", "jev-latest"},
		{"typesafe/jev-1.13", "jev-1.13.0"},
		{"jev-1.14.0", "jev-1.14.0"},
		{"jev-latest", "jev-latest"},
		{"typesafe/jev-1.14-20261014", "jev-1.14-20261014"},
	}
	for _, tt := range tests {
		if got := typeSafeModel(tt.in); got != tt.want {
			t.Errorf("typeSafeModel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStatusErrors(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		retryAfter     string
		body           string
		want           error
		wantRetryAfter time.Duration
	}{
		{"400", http.StatusBadRequest, "", `{"error":{"code":400,"message":"bad"}}`, classifier.ErrPermanent, 0},
		{"401", http.StatusUnauthorized, "", `{"error":{"code":401,"message":"No auth"}}`, classifier.ErrAuth, 0},
		{"403", http.StatusForbidden, "", "", classifier.ErrAuth, 0},
		{"404", http.StatusNotFound, "", "not json", classifier.ErrPermanent, 0},
		{"422", http.StatusUnprocessableEntity, "", `{"detail":[{"loc":["questions"],"msg":"bad"}]}`, classifier.ErrPermanent, 0},
		{"429 with Retry-After", http.StatusTooManyRequests, "7", "", classifier.ErrTransient, 7 * time.Second},
		{"429 without Retry-After", http.StatusTooManyRequests, "", "", classifier.ErrTransient, 0},
		{"529", statusOverloaded, "2", `{"error":"overloaded"}`, classifier.ErrTransient, 2 * time.Second},
		{"500", http.StatusInternalServerError, "", "", classifier.ErrTransient, 0},
		{"503", http.StatusServiceUnavailable, "1", "", classifier.ErrTransient, time.Second},
		{"408", http.StatusRequestTimeout, "", "", classifier.ErrTransient, 0},
		{"401 ignores Retry-After", http.StatusUnauthorized, "5", "", classifier.ErrAuth, 0},
		{"redirect is not followed", http.StatusFound, "", "", classifier.ErrPermanent, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.retryAfter != "" {
				h.Set("Retry-After", tt.retryAfter)
			}
			if tt.status == http.StatusFound {
				h.Set("Location", "/elsewhere")
			}
			srv, _ := serve(t, tt.status, h, []byte(tt.body))
			got, err := newClient(t, testConfig(srv.URL)).Classify(t.Context(), testInput)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Classify() error = %v, want %v", err, tt.want)
			}
			for _, other := range []error{classifier.ErrTransient, classifier.ErrPermanent, classifier.ErrAuth} {
				if !errors.Is(tt.want, other) && errors.Is(err, other) {
					t.Errorf("error %v also matches %v", err, other)
				}
			}
			var je *Error
			if !errors.As(err, &je) {
				t.Fatalf("error %v is not an *Error", err)
			}
			if je.HTTPStatus() != tt.status || je.RetryAfter() != tt.wantRetryAfter {
				t.Errorf("Error status %d, retry after %v, want %d, %v", je.HTTPStatus(), je.RetryAfter(), tt.status, tt.wantRetryAfter)
			}
			if got.Call.HTTPStatus != tt.status || got.Call.Provider != ProviderOpenRouter {
				t.Errorf("Call = %+v, want HTTP %d", got.Call, tt.status)
			}
			if got.Category != "" || got.Call.RawResponse != nil || got.Call.CostSource != "" {
				t.Errorf("failed call has answers, raw response or cost: %+v", got)
			}
		})
	}
}

// TestLiveErrors replays the error responses recorded by cmd/jevprobe and
// checks the mapping, the code, and that the message, which can quote the
// request, stays out of the error.
func TestLiveErrors(t *testing.T) {
	tests := []struct {
		fixture, provider string
		want              error
		wantCode          string
	}{
		{"bad-key.json", ProviderOpenRouter, classifier.ErrAuth, "401"},
		{"unknown-model.json", ProviderOpenRouter, classifier.ErrPermanent, "400"},
		{"bad-criteria.json", ProviderOpenRouter, classifier.ErrPermanent, "400"},
		{"bad-json.json", ProviderOpenRouter, classifier.ErrPermanent, "400"},
		{"bad-key-typesafe.json", ProviderTypeSafe, classifier.ErrAuth, "authentication_error"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			rec := live(t, tt.fixture)
			cfg := testConfig(serveLive(t, tt.fixture).URL)
			cfg.Provider = tt.provider
			got, err := newClient(t, cfg).Classify(t.Context(), testInput)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Classify() error = %v, want %v", err, tt.want)
			}
			var je *Error
			if !errors.As(err, &je) {
				t.Fatalf("error %v is not an *Error", err)
			}
			if je.code != tt.wantCode || je.HTTPStatus() != rec.Status || je.RetryAfter() != 0 {
				t.Errorf("Error code %q, status %d, retry after %v, want %q, %d, 0",
					je.code, je.HTTPStatus(), je.RetryAfter(), tt.wantCode, rec.Status)
			}
			// The recorded messages, one per fixture.
			for _, msg := range []string{"Missing Authentication", "does not exist", "Invalid input", "JSON parsing", "Cannot authenticate"} {
				if strings.Contains(err.Error(), msg) {
					t.Errorf("error %q contains the body message %q", err, msg)
				}
			}
			if got.Call.HTTPStatus != rec.Status || got.Call.RawResponse != nil {
				t.Errorf("Call = %+v, want HTTP %d without a raw response", got.Call, rec.Status)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"7", 7 * time.Second},
		{" 3 ", 3 * time.Second},
		{"0", 0},
		{"-1", 0},
		{"1.5", 0},
		{"soon", 0},
		{now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
	}
	for _, tt := range tests {
		if got := parseRetryAfter(tt.in, now); got != tt.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestErrorCode(t *testing.T) {
	tests := []struct{ body, want string }{
		{`{"error":{"code":429,"message":"Rate limited"}}`, "429"},
		{`{"error":{"code":"rate_limited","message":"x"}}`, "rate_limited"},
		{`{"error":{"code":"has spaces in it"}}`, ""},
		{`{"error":{"code":{"nested":1}}}`, ""},
		{`{"error":"overloaded"}`, ""},
		{`{"detail":"x"}`, ""},
		{`{"detail":{"error_type":"authentication_error","message":"Cannot authenticate"}}`, "authentication_error"},
		{`{"detail":[{"loc":["questions"],"msg":"bad"}]}`, ""},
		{`{"error":{"message":"m"},"detail":{"error_type":"x"}}`, "x"},
		{`not json`, ""},
		{``, ""},
	}
	for _, tt := range tests {
		if got := errorCode([]byte(tt.body)); got != tt.want {
			t.Errorf("errorCode(%s) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

// answersBody builds a 2xx body from answer JSON objects.
func answersBody(complaint, category, urgency string) string {
	var parts []string
	for _, p := range []struct{ id, v string }{
		{questionComplaint, complaint}, {questionCategory, category}, {questionUrgency, urgency},
	} {
		if p.v != "" {
			parts = append(parts, `"`+p.id+`":`+p.v)
		}
	}
	return `{"model":"m","answers":{` + strings.Join(parts, ",") + `},"usage":{"input_tokens":100,"output_tokens":0}}`
}

func TestInvalidAnswers(t *testing.T) {
	const (
		noul   = `{"type":"noul","noul":0.4}`
		choice = `{"type":"choice","choice":"billing","confidence":0.9}`
		score  = `{"type":"score","score":1,"confidence":0.7}`
	)
	tests := []struct {
		name, body string
		wantUsage  bool
	}{
		{"malformed JSON", `{"answers":`, false},
		{"empty body", ``, false},
		{"not an object", `[1,2]`, false},
		{"no answers", `{"model":"m","usage":{"input_tokens":100}}`, true},
		{"answers not an object", `{"answers":[],"usage":{"input_tokens":100}}`, true},
		{"missing complaint", answersBody("", choice, score), true},
		{"missing category", answersBody(noul, "", score), true},
		{"missing urgency", answersBody(noul, choice, ""), true},
		{"wrong type", answersBody(`{"type":"choice","noul":0.4}`, choice, score), true},
		{"noul missing", answersBody(`{"type":"noul"}`, choice, score), true},
		{"noul null", answersBody(`{"type":"noul","noul":null}`, choice, score), true},
		{"noul mistyped", answersBody(`{"type":"noul","noul":"0.4"}`, choice, score), true},
		{"choice missing", answersBody(noul, `{"confidence":0.9}`, score), true},
		{"choice confidence missing", answersBody(noul, `{"choice":"billing"}`, score), true},
		{"score confidence missing", answersBody(noul, choice, `{"score":1}`), true},
		{"noul above 1", answersBody(`{"noul":1.2}`, choice, score), true},
		{"confidence negative", answersBody(noul, `{"choice":"billing","confidence":-0.1}`, score), true},
		{"score above max", answersBody(noul, choice, `{"score":3.5,"confidence":0.7}`), true},
		{"unknown category", answersBody(noul, `{"choice":"sales","confidence":0.9}`, score), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, http.StatusOK, nil, []byte(tt.body))
			got, err := newClient(t, testConfig(srv.URL)).Classify(t.Context(), testInput)
			if !errors.Is(err, classifier.ErrInvalidAnswer) || !errors.Is(err, classifier.ErrPermanent) {
				t.Fatalf("Classify() error = %v, want ErrInvalidAnswer", err)
			}
			if got.Category != "" || got.ComplaintP != 0 {
				t.Errorf("invalid answer returned answers: %+v", got)
			}
			if got.Call.HTTPStatus != http.StatusOK {
				t.Errorf("HTTPStatus = %d, want 200", got.Call.HTTPStatus)
			}
			// The call was paid for, so its usage and cost are kept.
			if tt.wantUsage && (got.Call.InputTokens != 100 || got.Call.CostSource != classifier.CostEstimated) {
				t.Errorf("Call = %+v, want usage and estimated cost", got.Call)
			}
		})
	}
}

func TestValidAnswerVariants(t *testing.T) {
	tests := []struct{ name, body string }{
		{"no type members", answersBody(`{"noul":0}`, `{"choice":"other","confidence":0}`, `{"score":0,"confidence":1}`)},
		{"bounds", answersBody(`{"noul":1}`, `{"choice":"account","confidence":1}`, `{"score":3,"confidence":0}`)},
		{"unknown members ignored", `{"model":"m","extra":1,"answers":{` +
			`"is_complaint":{"noul":0.5,"x":[1]},"category":{"choice":"billing","confidence":0.7,"y":{}},` +
			`"urgency":{"score":1,"confidence":0.6},"bonus":{}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, http.StatusOK, nil, []byte(tt.body))
			if _, err := newClient(t, testConfig(srv.URL)).Classify(t.Context(), testInput); err != nil {
				t.Errorf("Classify() error = %v", err)
			}
		})
	}
}

func TestCost(t *testing.T) {
	tests := []struct {
		name       string
		usage      string
		wantCost   float64
		wantSource classifier.CostSource
	}{
		{"reported", `{"input_tokens":447,"output_tokens":0,"cost":0.00002}`, 0.00002, classifier.CostReported},
		{"reported zero", `{"input_tokens":447,"cost":0}`, 0, classifier.CostReported},
		{"estimated", `{"input_tokens":1000000,"output_tokens":3}`, 0.042, classifier.CostEstimated},
		{"negative cost is estimated", `{"input_tokens":1000000,"cost":-1}`, 0.042, classifier.CostEstimated},
		{"null cost is estimated", `{"input_tokens":1000000,"cost":null}`, 0.042, classifier.CostEstimated},
		{"no usage is unknown", ``, 0, ""},
		{"malformed usage is unknown", `{"input_tokens":"many"}`, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"answers":{"is_complaint":{"noul":0.1},"category":{"choice":"billing","confidence":0.9},` +
				`"urgency":{"score":1,"confidence":0.7}}`
			if tt.usage != "" {
				body += `,"usage":` + tt.usage
			}
			body += "}"
			srv, _ := serve(t, http.StatusOK, nil, []byte(body))
			got, err := newClient(t, testConfig(srv.URL)).Classify(t.Context(), testInput)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got.Call.CostUSD-tt.wantCost) > 1e-12 || got.Call.CostSource != tt.wantSource {
				t.Errorf("cost = %v (%q), want %v (%q)", got.Call.CostUSD, got.Call.CostSource, tt.wantCost, tt.wantSource)
			}
			if got.Call.Model != "" {
				t.Errorf("Model = %q, want empty without a model member", got.Call.Model)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(done) })

	cfg := testConfig(srv.URL)
	cfg.Timeout = 20 * time.Millisecond
	got, err := newClient(t, cfg).Classify(t.Context(), testInput)
	if !errors.Is(err, classifier.ErrTransient) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Classify() error = %v, want ErrTransient from a deadline", err)
	}
	if got.Call.HTTPStatus != 0 || got.Call.Latency < cfg.Timeout {
		t.Errorf("Call = %+v, want no status and latency >= %v", got.Call, cfg.Timeout)
	}
}

func TestNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	got, err := newClient(t, testConfig(srv.URL)).Classify(t.Context(), testInput)
	if !errors.Is(err, classifier.ErrTransient) {
		t.Fatalf("Classify() error = %v, want ErrTransient", err)
	}
	if got.Call.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d, want 0", got.Call.HTTPStatus)
	}
}

func TestCanceledContext(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	cfg := testConfig(srv.URL)
	cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := newClient(t, cfg).Classify(ctx, testInput)
	if !errors.Is(err, classifier.ErrTransient) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Classify() error = %v, want ErrTransient from cancellation", err)
	}
	if calls.Load() != 0 || logs.Len() != 0 {
		t.Errorf("calls = %d, logs = %q, want no call and no log line", calls.Load(), logs.String())
	}
}

func TestResponseTooLarge(t *testing.T) {
	big := append([]byte(`{"pad":"`), bytes.Repeat([]byte("x"), maxResponseBytes)...)
	srv, _ := serve(t, http.StatusOK, nil, append(big, `"}`...))
	_, err := newClient(t, testConfig(srv.URL)).Classify(t.Context(), testInput)
	if !errors.Is(err, classifier.ErrInvalidAnswer) {
		t.Fatalf("Classify() error = %v, want ErrInvalidAnswer", err)
	}
}

func TestLogLine(t *testing.T) {
	srv, _ := serve(t, http.StatusOK, nil, live(t, "ok.json").body())
	var logs bytes.Buffer
	cfg := testConfig(srv.URL)
	cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	if _, err := newClient(t, cfg).Classify(t.Context(), testInput); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1:\n%s", len(lines), logs.String())
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"level":         "INFO",
		"msg":           "jev call",
		"provider":      ProviderOpenRouter,
		"model":         "typesafe/jev-1.14-20261014",
		"input_tokens":  582.0,
		"output_tokens": 87.0,
		"cost_usd":      0.000024444,
		"cost_source":   "reported",
		"outcome":       "ok",
		"http_status":   200.0,
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("log %s = %v, want %v", k, line[k], v)
		}
	}
	if _, ok := line["latency_ms"].(float64); !ok {
		t.Errorf("log latency_ms = %v, want a number", line["latency_ms"])
	}
	if _, ok := line["error"]; ok {
		t.Errorf("successful call logs an error: %v", line["error"])
	}
}

func TestLogOutcomes(t *testing.T) {
	tests := []struct {
		status      int
		body        string
		wantOutcome string
	}{
		{http.StatusUnauthorized, "", outcomeAuth},
		{http.StatusUnprocessableEntity, "", outcomePermanent},
		{http.StatusTooManyRequests, "", outcomeTransient},
		{http.StatusOK, `{"answers":{}}`, outcomeInvalidAnswer},
	}
	for _, tt := range tests {
		t.Run(tt.wantOutcome, func(t *testing.T) {
			srv, _ := serve(t, tt.status, nil, []byte(tt.body))
			var logs bytes.Buffer
			cfg := testConfig(srv.URL)
			cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			if _, err := newClient(t, cfg).Classify(t.Context(), testInput); err == nil {
				t.Fatal("Classify() error = nil")
			}
			var line map[string]any
			if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
				t.Fatal(err)
			}
			if line["outcome"] != tt.wantOutcome || line["level"] != "WARN" || line["error"] == nil {
				t.Errorf("log line = %v, want outcome %s at WARN with an error", line, tt.wantOutcome)
			}
			if line["http_status"] != float64(tt.status) {
				t.Errorf("log line = %v, want http_status %d", line, tt.status)
			}
		})
	}
}

// TestRedaction sends a ticket whose text a provider echoes back, in an error
// body and in an unexpected member of a success, and checks that neither the
// text nor the key reaches the log, the error, or the raw response. The
// logger is a plain JSON handler: the adapter must not rely on redaction.
func TestRedaction(t *testing.T) {
	in := classifier.Input{Subject: "SUBJ-c0ffee my secret plan", Body: "BODY-d00d card 4111 1111"}
	echo := `"state":{"subject":"` + in.Subject + `","body":"` + in.Body + `"},"key":"` + testKey + `"`
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"422 echoes the input", http.StatusUnprocessableEntity,
			`{"error":{"code":"` + in.Subject + `","message":"bad ` + in.Body + `"},` + echo + `}`},
		{"500 echoes the input", http.StatusInternalServerError, `{"detail":[{"input":{` + echo + `}}]}`},
		{"success echoes the input", http.StatusOK,
			`{"model":"m",` + echo + `,"answers":{"is_complaint":{"noul":0.1},` +
				`"category":{"choice":"billing","confidence":0.9},"urgency":{"score":1,"confidence":0.7}}}`},
		{"invalid answer echoes the input", http.StatusOK, `{` + echo + `,"answers":{"category":{"choice":"billing"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := serve(t, tt.status, nil, []byte(tt.body))
			var logs bytes.Buffer
			cfg := testConfig(srv.URL)
			cfg.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			c := newClient(t, cfg)
			cfg.Logger.Info("config", "jev", cfg)
			got, err := c.Classify(t.Context(), in)

			outputs := map[string]string{"log": logs.String(), "raw response": string(got.Call.RawResponse)}
			if err != nil {
				outputs["error"] = err.Error()
			}
			for where, out := range outputs {
				for _, secret := range []string{"SUBJ-c0ffee", "BODY-d00d", testKey, "Bearer"} {
					if strings.Contains(out, secret) {
						t.Errorf("%s contains %q:\n%s", where, secret, out)
					}
				}
			}
		})
	}
}

func TestUrgencyLevels(t *testing.T) {
	if got, want := len(urgencyLevels), int(classifier.MaxUrgency)+1; got != want {
		t.Errorf("%d urgency levels, want %d so the score is the level", got, want)
	}
}

func TestNew(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
	}{
		{"unknown provider", func(c *Config) { c.Provider = "acme" }},
		{"empty key", func(c *Config) { c.APIKey = "" }},
		{"empty model", func(c *Config) { c.Model = "" }},
		{"zero timeout", func(c *Config) { c.Timeout = 0 }},
		{"zero rate", func(c *Config) { c.RateLimit = 0 }},
		{"NaN rate", func(c *Config) { c.RateLimit = math.NaN() }},
		{"infinite rate", func(c *Config) { c.RateLimit = math.Inf(1) }},
		{"zero concurrency", func(c *Config) { c.MaxConcurrency = 0 }},
		{"negative price", func(c *Config) { c.InputPricePerMTokUSD = -1 }},
		{"NaN price", func(c *Config) { c.InputPricePerMTokUSD = math.NaN() }},
		{"taxonomy without other", func(c *Config) { c.Taxonomy = classifier.Taxonomy{{Name: "a", Description: "b"}} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("")
			tt.edit(&cfg)
			if _, err := New(cfg); err == nil {
				t.Error("New() error = nil")
			} else if strings.Contains(err.Error(), testKey) {
				t.Errorf("New() error %q contains the key", err)
			}
		})
	}
	if _, err := New(testConfig("")); err != nil {
		t.Errorf("New(valid) error = %v", err)
	}
}
