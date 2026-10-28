// Command jevprobe makes live Jev calls through the internal/classifier/jev adapter. For each case it prints
// the latency, served model, usage and cost, and records the exact HTTP exchange (request body, response
// status, headers and body) as a fixture that the offline adapter tests replay.
//
// It spends real credits, so it runs only by hand with a key, never in tests or CI. Run it from the
// repository root with the key loaded into the environment:
//
//	set -a; . ~/.config/ticket-triage/.env; set +a
//	go run ./cmd/jevprobe ok bad-key unknown-model
//
// Cases: ok (the adapter's own request), bad-key (an invalid key instead of the real one), unknown-model,
// bad-criteria (choice criteria as an array instead of an object), one-level (a score question with a
// single level; Jev documents 2 to 10), bad-json (a truncated body) and decisions (the request sent to
// OpenRouter's Decisions API path instead of /v1/systemone). The ticket is one English or Ukrainian row of
// the committed eval set. Recordings go to -out, one <case><suffix>.json each; -out "" only prints.
//
// The API key and the Authorization header are never printed or recorded: request headers are not
// recorded at all, and a recording that contains the key is refused. Only the response headers in
// keptHeaders are recorded, and an account id in an error body is redacted.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/classifier/jev"
	"github.com/nacknime-official/ticket-triage/internal/evalset"
)

const (
	// endpointPath is the adapter's API path; the decisions case swaps it for decisionsPath.
	endpointPath  = "/v1/systemone"
	decisionsPath = "/alpha/decisions"
	// invalidKey belongs to no account. It does not look like a real key, so secret scanners leave it be.
	invalidKey = "jevprobe-invalid-key"
	// unknownModel is a Jev id that does not exist.
	unknownModel = "typesafe/jev-0.0"
	// maxRepeat keeps a rate-limit probe to a handful of concurrent calls.
	maxRepeat = 5
	// maxRecordedBytes caps a recorded response body.
	maxRecordedBytes = 1 << 20
)

// keyEnv names the API key variable of each provider, as in .env.example.
var keyEnv = map[string]string{
	jev.ProviderOpenRouter: "OPENROUTER_API_KEY",
	jev.ProviderTypeSafe:   "TYPESAFE_API_KEY",
}

// probeCase is one deliberate scenario. Zero fields keep the adapter's own request.
type probeCase struct {
	badKey bool
	model  string
	path   string
	edit   func([]byte) ([]byte, error)
}

var cases = map[string]probeCase{
	"ok":            {},
	"bad-key":       {badKey: true},
	"unknown-model": {model: unknownModel},
	"bad-criteria":  {edit: arrayCriteria},
	"one-level":     {edit: oneUrgencyLevel},
	"bad-json":      {edit: truncate},
	"decisions":     {path: decisionsPath},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "jevprobe:", err)
		os.Exit(1)
	}
}

type options struct {
	provider, baseURL, model string
	evalPath, ticketID, out  string
	suffix                   string
	timeout                  time.Duration
	repeat, maxCalls         int
	cases                    []string
}

func parse(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("jevprobe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.provider, "provider", jev.ProviderOpenRouter, "jev provider: openrouter or typesafe")
	fs.StringVar(&o.baseURL, "base-url", "", "override the provider base URL (before /v1/systemone)")
	fs.StringVar(&o.model, "model", "typesafe/jev-1.14", "model to call, in OpenRouter naming or the provider's own")
	fs.StringVar(&o.evalPath, "eval", "testdata/eval/generated.jsonl", "eval set JSONL file the ticket comes from")
	fs.StringVar(&o.ticketID, "ticket", "gen-en-001", "id of the eval row to send (English or Ukrainian)")
	fs.StringVar(&o.out, "out", "internal/classifier/jev/testdata/live", `directory for the recordings; "" only prints`)
	fs.StringVar(&o.suffix, "suffix", "", "appended to each recording's file name, e.g. -uk")
	fs.DurationVar(&o.timeout, "timeout", 10*time.Second, "per-call timeout")
	fs.IntVar(&o.repeat, "repeat", 1, fmt.Sprintf("concurrent calls per case, at most %d (rate-limit probe)", maxRepeat))
	fs.IntVar(&o.maxCalls, "max-calls", 10, "refuse to run more live calls than this")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: set -a; . ~/.config/ticket-triage/.env; set +a; jevprobe [flags] [case ...]\n"+
			"cases: %s (default ok)\n",
			strings.Join(slices.Sorted(maps.Keys(cases)), ", "))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.cases = fs.Args()
	if len(o.cases) == 0 {
		o.cases = []string{"ok"}
	}
	for _, name := range o.cases {
		if _, ok := cases[name]; !ok {
			return o, fmt.Errorf("unknown case %q", name)
		}
	}
	if _, ok := keyEnv[o.provider]; !ok {
		return o, fmt.Errorf("provider %q is not %s or %s", o.provider, jev.ProviderOpenRouter, jev.ProviderTypeSafe)
	}
	if o.repeat < 1 || o.repeat > maxRepeat {
		return o, fmt.Errorf("-repeat %d is outside 1..%d", o.repeat, maxRepeat)
	}
	if n := len(o.cases) * o.repeat; n > o.maxCalls {
		return o, fmt.Errorf("%d calls exceed -max-calls %d", n, o.maxCalls)
	}
	return o, nil
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	o, err := parse(args, stderr)
	if err != nil {
		return err
	}
	key := getenv(keyEnv[o.provider])
	if key == "" {
		return fmt.Errorf("%s is empty; load it with: set -a; . ~/.config/ticket-triage/.env; set +a", keyEnv[o.provider])
	}
	row, err := findRow(o.evalPath, o.ticketID)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	var total float64
	for _, name := range o.cases {
		results, err := probe(ctx, o, key, row, name, cases[name], logger)
		if err != nil {
			return fmt.Errorf("case %s: %w", name, err)
		}
		for i, r := range results {
			total += r.res.Call.CostUSD
			report(stdout, name, r)
			if r.recErr != nil {
				fmt.Fprintf(stdout, "  not recorded: %v\n", r.recErr)
				continue
			}
			if o.out == "" {
				continue
			}
			file := name + o.suffix + ".json"
			if o.repeat > 1 {
				file = fmt.Sprintf("%s%s.%d.json", name, o.suffix, i+1)
			}
			path := filepath.Join(o.out, file)
			if err := save(path, r.rec, key); err != nil {
				return fmt.Errorf("case %s: %w", name, err)
			}
			fmt.Fprintf(stdout, "  saved %s\n", path)
		}
	}
	fmt.Fprintf(stdout, "total cost $%.9f\n", total)
	return nil
}

// findRow returns the eval row with id; only English and Ukrainian rows exist in the set.
func findRow(path, id string) (evalset.Row, error) {
	rows, err := evalset.ReadFile(path)
	if err != nil {
		return evalset.Row{}, err
	}
	i := slices.IndexFunc(rows, func(r evalset.Row) bool { return r.ID == id })
	if i < 0 {
		return evalset.Row{}, fmt.Errorf("ticket %q is not in %s", id, path)
	}
	return rows[i], nil
}

// result is one call: what the adapter made of it and what went over the wire.
type result struct {
	res    classifier.Result
	err    error
	rec    recording
	recErr error
}

// probe runs one case o.repeat times concurrently through the adapter.
func probe(ctx context.Context, o options, key string, row evalset.Row, name string, pc probeCase, logger *slog.Logger) ([]result, error) {
	cfg := jev.Config{
		Provider:             o.provider,
		BaseURL:              o.baseURL,
		APIKey:               key,
		Model:                o.model,
		Timeout:              o.timeout,
		RateLimit:            maxRepeat,
		MaxConcurrency:       maxRepeat,
		InputPricePerMTokUSD: 0.042,
		Logger:               logger,
	}
	if pc.badKey {
		cfg.APIKey = invalidKey
	}
	if pc.model != "" {
		cfg.Model = pc.model
	}
	// Every client is built before any call starts, so a config error leaves no goroutine behind.
	recs := make([]*recorder, o.repeat)
	clients := make([]*jev.Client, o.repeat)
	for i := range o.repeat {
		recs[i] = &recorder{next: http.DefaultTransport, path: pc.path, edit: pc.edit}
		c := cfg
		c.HTTPClient = &http.Client{
			Transport: recs[i],
			// Like the adapter's own client: a redirect is recorded, not followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		client, err := jev.New(c)
		if err != nil {
			return nil, err
		}
		clients[i] = client
	}
	results := make([]result, o.repeat)
	var wg sync.WaitGroup
	for i, client := range clients {
		wg.Go(func() {
			res, err := client.Classify(ctx, classifier.Input{Subject: row.Subject, Body: row.Body})
			results[i] = result{res: res, err: err}
		})
	}
	wg.Wait()
	for i := range results {
		rec, err := recs[i].recording()
		rec.Case, rec.Ticket = name, row.ID
		results[i].rec, results[i].recErr = rec, err
	}
	return results, nil
}

func report(w io.Writer, name string, r result) {
	call := r.res.Call
	fmt.Fprintf(w, "case %s: HTTP %d in %dms, model %q, tokens in %d out %d, cost $%.9f (%s)\n",
		name, call.HTTPStatus, call.Latency.Milliseconds(), call.Model, call.InputTokens, call.OutputTokens,
		call.CostUSD, cmp.Or(string(call.CostSource), "unknown"))
	if r.err != nil {
		fmt.Fprintf(w, "  error: %v\n", r.err)
		return
	}
	fmt.Fprintf(w, "  complaint_p %.2f, category %s (%.2f), urgency %.2f (%.2f)\n",
		r.res.ComplaintP, r.res.Category, r.res.CategoryConfidence, r.res.Urgency, r.res.UrgencyConfidence)
}

// recording is one HTTP exchange as saved to a fixture. It has no request headers, so neither the key
// nor the Authorization header can reach it.
type recording struct {
	Case       string            `json:"case"`
	Ticket     string            `json:"ticket"`
	RecordedAt time.Time         `json:"recorded_at"`
	Method     string            `json:"method"`
	URL        string            `json:"url"`
	Request    jsontext.Value    `json:"request"`
	Status     int               `json:"status"`
	LatencyMS  int64             `json:"latency_ms"`
	Header     map[string]string `json:"header"`
	Body       jsontext.Value    `json:"body"`
}

// keptHeaders are the response headers a recording keeps; the rest (cookies, CDN and security headers)
// say nothing about the API. Rate-limit headers are kept by prefix.
var keptHeaders = []string{"Content-Type", "Retry-After", "X-Generation-Id", "X-Provider-Name", "Request-Id"}

func keepHeader(k string) bool {
	l := strings.ToLower(k)
	return slices.Contains(keptHeaders, k) || strings.HasPrefix(l, "x-ratelimit") || strings.HasPrefix(l, "ratelimit")
}

// recorder is an http.RoundTripper that applies a case's edits to the request and records the
// exchange. It is used for one call.
type recorder struct {
	next http.RoundTripper
	path string
	edit func([]byte) ([]byte, error)

	mu  sync.Mutex
	rec *recording
	err error
}

// RoundTrip implements http.RoundTripper.
func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readBody(req)
	if err != nil {
		return nil, err
	}
	if r.edit != nil {
		if body, err = r.edit(body); err != nil {
			return nil, fmt.Errorf("edit request: %w", err)
		}
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	if r.path != "" {
		u := *req.URL
		u.Path = strings.TrimSuffix(u.Path, endpointPath) + r.path
		out.URL = &u
		out.Host = ""
	}

	rec := &recording{RecordedAt: time.Now().UTC().Truncate(time.Second), Method: out.Method, URL: out.URL.String(), Request: asJSON(body)}
	start := time.Now()
	resp, err := r.next.RoundTrip(out)
	if err != nil {
		r.store(nil, err)
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRecordedBytes))
	rec.LatencyMS = time.Since(start).Milliseconds()
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		r.store(nil, err)
		return nil, err
	}
	rec.Status = resp.StatusCode
	rec.Header = map[string]string{}
	for k, v := range resp.Header {
		if keepHeader(k) {
			rec.Header[strings.ToLower(k)] = strings.Join(v, ", ")
		}
	}
	rec.Body = asJSON(redactAccount(raw))
	r.store(rec, nil)
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return resp, nil
}

func (r *recorder) store(rec *recording, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec, r.err = rec, err
}

// recording returns the recorded exchange, or why there is none.
func (r *recorder) recording() (recording, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.err != nil:
		return recording{}, fmt.Errorf("no response: %w", r.err)
	case r.rec == nil:
		return recording{}, errors.New("no call was made")
	}
	return *r.rec, nil
}

func readBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	defer req.Body.Close()
	b, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	return b, nil
}

// asJSON returns b itself when it is valid JSON, else b as a JSON string, so a recording keeps the
// exact bytes either way.
func asJSON(b []byte) jsontext.Value {
	if v := jsontext.Value(b); len(b) > 0 && v.IsValid() {
		return bytes.Clone(v)
	}
	// Invalid UTF-8 is replaced and reported; the quoted string is still valid JSON.
	s, _ := jsontext.AppendQuote(nil, b)
	return s
}

// accountField is the member in which OpenRouter error bodies name the account.
const accountField = "user_id"

// redactAccount replaces a top-level account id in a JSON object body; other bodies are returned as they
// are.
func redactAccount(body []byte) []byte {
	var obj map[string]jsontext.Value
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	if _, ok := obj[accountField]; !ok {
		return body
	}
	obj[accountField] = jsontext.Value(`"redacted"`)
	b, err := json.Marshal(obj, json.Deterministic(true))
	if err != nil {
		return body
	}
	return b
}

// save writes rec as indented JSON, refusing a recording that contains the key.
func save(path string, rec recording, key string) error {
	b, err := json.Marshal(rec, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		return fmt.Errorf("encode recording: %w", err)
	}
	if bytes.Contains(b, []byte(key)) {
		return fmt.Errorf("recording for %s contains the API key; not saved", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// arrayCriteria sends the category options as an array instead of an option → description object.
func arrayCriteria(body []byte) ([]byte, error) {
	return editQuestion(body, "category", func(q map[string]jsontext.Value) error {
		var opts map[string]string
		if err := json.Unmarshal(q["criteria"], &opts); err != nil {
			return err
		}
		b, err := json.Marshal(slices.Sorted(maps.Keys(opts)))
		q["criteria"] = b
		return err
	})
}

// oneUrgencyLevel gives the score question a single level; Jev documents 2 to 10.
func oneUrgencyLevel(body []byte) ([]byte, error) {
	return editQuestion(body, "urgency", func(q map[string]jsontext.Value) error {
		q["criteria"] = jsontext.Value(`["Any request"]`)
		return nil
	})
}

// editQuestion applies edit to question id of a request body. The result has its members sorted.
func editQuestion(body []byte, id string, edit func(map[string]jsontext.Value) error) ([]byte, error) {
	var req map[string]jsontext.Value
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	var qs map[string]map[string]jsontext.Value
	if err := json.Unmarshal(req["questions"], &qs); err != nil {
		return nil, err
	}
	q, ok := qs[id]
	if !ok {
		return nil, fmt.Errorf("request has no %s question", id)
	}
	if err := edit(q); err != nil {
		return nil, err
	}
	b, err := json.Marshal(qs, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	req["questions"] = b
	return json.Marshal(req, json.Deterministic(true))
}

// truncate cuts the body in half, so it is not valid JSON.
func truncate(body []byte) ([]byte, error) {
	return body[:len(body)/2], nil
}
