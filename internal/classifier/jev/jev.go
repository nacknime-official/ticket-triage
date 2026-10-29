package jev

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
)

// Providers Jev is called through.
const (
	ProviderOpenRouter = "openrouter"
	ProviderTypeSafe   = "typesafe"
)

// endpointPath is the System One API path; both providers share the wire
// format (report §5.2).
const endpointPath = "/v1/systemone"

// maxResponseBytes caps a response body; a documented answer is a few KiB.
const maxResponseBytes = 1 << 20

// provider is what differs between the providers.
type provider struct {
	baseURL string
	// model maps the configured model to the provider's naming.
	model func(string) string
}

var providers = map[string]provider{
	ProviderOpenRouter: {baseURL: "https://openrouter.ai/api", model: func(m string) string { return m }},
	// The direct API answers at api.typesafe.ai/v1/systemone (a 401 for a bad
	// key, T12); its success format is [unverified] (README §7).
	ProviderTypeSafe: {baseURL: "https://api.typesafe.ai", model: typeSafeModel},
}

var openRouterVersion = regexp.MustCompile(`^jev-[0-9]+\.[0-9]+$`)

// typeSafeModel maps an OpenRouter model id to the direct API's naming
// (report §5.2) [unverified]: typesafe/jev-1.14 is jev-1.14.0 and
// ~typesafe/jev-latest is jev-latest. Other ids pass through unchanged, so a
// direct-API id can be configured as is.
func typeSafeModel(m string) string {
	m = strings.TrimPrefix(strings.TrimPrefix(m, "~"), "typesafe/")
	if openRouterVersion.MatchString(m) {
		m += ".0"
	}
	return m
}

// Config configures a Client. cmd/* fills it from config.Jev.
type Config struct {
	// Provider is ProviderOpenRouter or ProviderTypeSafe.
	Provider string
	// BaseURL overrides the provider's base URL, the prefix before
	// /v1/systemone. Tests point it at an httptest.Server.
	BaseURL string
	// APIKey is sent as a Bearer token. It is never logged.
	APIKey string
	// Model is the model to call, in OpenRouter naming or the provider's own.
	Model string
	// Timeout bounds one HTTP call, not the wait for a call slot.
	Timeout time.Duration
	// RateLimit is the client-side limit in calls per second, burst 1.
	RateLimit float64
	// MaxConcurrency caps calls in flight.
	MaxConcurrency int
	// InputPricePerMTokUSD prices input tokens when the response reports no
	// cost.
	InputPricePerMTokUSD float64
	// Taxonomy is the category list; nil means classifier.DefaultTaxonomy.
	Taxonomy classifier.Taxonomy
	// HTTPClient overrides the HTTP client; nil means a client with a pooled
	// transport that never follows redirects.
	HTTPClient *http.Client
	// Logger, if set, gets one line per HTTP call, for callers that use the
	// adapter directly (cmd/jevprobe, cmd/eval). nil logs nothing. Leave it
	// nil under triage.Processor, which writes the README §4.3 line with
	// ticket_id and attempt itself, so a call is logged once.
	Logger *slog.Logger
}

// LogValue implements slog.LogValuer. It leaves out the API key.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("provider", c.Provider),
		slog.String("base_url", c.BaseURL),
		slog.String("model", c.Model),
		slog.Duration("timeout", c.Timeout),
		slog.Float64("rate_limit", c.RateLimit),
		slog.Int("max_concurrency", c.MaxConcurrency),
		slog.Float64("input_price_per_mtok_usd", c.InputPricePerMTokUSD),
	)
}

// Client is the Jev classifier. It is safe for concurrent use.
type Client struct {
	provider string
	url      string
	apiKey   string
	model    string
	timeout  time.Duration
	price    float64
	tax      classifier.Taxonomy
	qsum     string // see QuestionsSHA256
	http     *http.Client
	logger   *slog.Logger
	limiter  *rate.Limiter
	slots    chan struct{}
}

var _ classifier.Classifier = (*Client)(nil)

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	p, ok := providers[cfg.Provider]
	var errs []error
	if !ok {
		errs = append(errs, fmt.Errorf("provider %q is not %s or %s", cfg.Provider, ProviderOpenRouter, ProviderTypeSafe))
	}
	if cfg.APIKey == "" {
		errs = append(errs, errors.New("API key is empty"))
	}
	if cfg.Model == "" {
		errs = append(errs, errors.New("model is empty"))
	}
	if cfg.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("timeout %v is not positive", cfg.Timeout))
	}
	if !(cfg.RateLimit > 0) || math.IsInf(cfg.RateLimit, 0) {
		errs = append(errs, fmt.Errorf("rate limit %v is not a positive number", cfg.RateLimit))
	}
	if cfg.MaxConcurrency < 1 {
		errs = append(errs, fmt.Errorf("max concurrency %d is below 1", cfg.MaxConcurrency))
	}
	if !(cfg.InputPricePerMTokUSD >= 0) || math.IsInf(cfg.InputPricePerMTokUSD, 0) {
		errs = append(errs, fmt.Errorf("input price %v is not a number >= 0", cfg.InputPricePerMTokUSD))
	}
	tax := cfg.Taxonomy
	if tax == nil {
		tax = classifier.DefaultTaxonomy()
	}
	if err := tax.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("jev: invalid config: %w", err)
	}

	base := cfg.BaseURL
	if base == "" {
		base = p.baseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = newHTTPClient(cfg.MaxConcurrency)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	q, err := json.Marshal(buildQuestions(tax))
	if err != nil {
		return nil, fmt.Errorf("jev: encode questions: %w", err)
	}
	qsum := sha256.Sum256(q)
	return &Client{
		provider: cfg.Provider,
		url:      strings.TrimSuffix(base, "/") + endpointPath,
		apiKey:   cfg.APIKey,
		model:    p.model(cfg.Model),
		timeout:  cfg.Timeout,
		price:    cfg.InputPricePerMTokUSD,
		tax:      tax,
		qsum:     hex.EncodeToString(qsum[:]),
		http:     hc,
		logger:   logger,
		limiter:  rate.NewLimiter(rate.Limit(cfg.RateLimit), 1),
		slots:    make(chan struct{}, cfg.MaxConcurrency),
	}, nil
}

func newHTTPClient(maxConcurrency int) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   maxConcurrency,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		// A redirect is answered as a permanent error rather than followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// QuestionsSHA256 returns the hex SHA-256 of the questions object every
// Classify sends, byte for byte: any change to a question's instructions,
// criteria, type or order, or to the taxonomy, changes it. cmd/eval keys its
// answer cache on it, so answers to old questions are not reused.
func (c *Client) QuestionsSHA256() string { return c.qsum }

// Classify implements classifier.Classifier with exactly one HTTP call. It
// waits for a concurrency slot and the rate limiter first; a ctx done while
// waiting gives an ErrTransient and no call, so no log line either.
func (c *Client) Classify(ctx context.Context, in classifier.Input) (classifier.Result, error) {
	call := classifier.Call{Provider: c.provider}
	body, err := json.Marshal(request{
		Model:     c.model,
		State:     state{Subject: in.Subject, Body: in.Body},
		Questions: buildQuestions(c.tax),
	})
	if err != nil {
		return classifier.Result{Call: call}, fmt.Errorf("%w: jev: encode request: %w", classifier.ErrPermanent, err)
	}

	if err := c.acquire(ctx); err != nil {
		return classifier.Result{Call: call}, fmt.Errorf("%w: jev: wait for a call slot: %w", classifier.ErrTransient, err)
	}
	defer c.release()

	res, err := c.do(ctx, body, call)
	c.logCall(ctx, res.Call, err)
	return res, err
}

// acquire takes a concurrency slot and then a rate-limiter token.
func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := c.limiter.Wait(ctx); err != nil {
		c.release()
		return err
	}
	return nil
}

func (c *Client) release() { <-c.slots }

// do makes the HTTP call and decodes its response into a Result whose Call
// is filled as far as the call got.
func (c *Client) do(ctx context.Context, body []byte, call classifier.Call) (classifier.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return classifier.Result{Call: call}, fmt.Errorf("%w: jev: build request: %w", classifier.ErrPermanent, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		call.Latency = time.Since(start)
		// Timeout, cancellation and network failures alike.
		return classifier.Result{Call: call}, fmt.Errorf("%w: jev: %w", classifier.ErrTransient, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	call.Latency = time.Since(start)
	call.HTTPStatus = resp.StatusCode
	if err != nil {
		return classifier.Result{Call: call}, fmt.Errorf("%w: jev: read response: %w", classifier.ErrTransient, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return classifier.Result{Call: call}, statusError(resp.StatusCode, resp.Header, raw, time.Now())
	}
	if len(raw) > maxResponseBytes {
		return classifier.Result{Call: call}, fmt.Errorf("%w: jev: response is over %d bytes", classifier.ErrInvalidAnswer, maxResponseBytes)
	}
	return c.decode(raw, call)
}

// decode fills call from a 2xx body and returns the validated answers. Usage
// and cost are recorded even when the answers are invalid: the call was paid.
func (c *Client) decode(body []byte, call classifier.Call) (classifier.Result, error) {
	env, raw, err := decodeEnvelope(body)
	if err != nil {
		return classifier.Result{Call: call}, err
	}
	call.RawResponse = raw
	call.Model = servedModel(env.Model)
	u := decodeUsage(env.Usage)
	call.InputTokens, call.OutputTokens = u.InputTokens, u.OutputTokens
	call.CostUSD, call.CostSource = c.cost(u)

	res, err := decodeAnswers(env.Answers)
	if err != nil {
		return classifier.Result{Call: call}, err
	}
	if err := res.Validate(c.tax); err != nil {
		return classifier.Result{Call: call}, fmt.Errorf("jev: %w", err)
	}
	res.Call = call
	return res, nil
}

// cost is the reported cost, else input tokens times the configured price,
// else unknown (README §4.4).
func (c *Client) cost(u usage) (float64, classifier.CostSource) {
	switch {
	case u.Cost != nil:
		return *u.Cost, classifier.CostReported
	case u.HasInputTokens:
		return float64(u.InputTokens) * c.price / 1e6, classifier.CostEstimated
	default:
		return 0, ""
	}
}

// Outcomes on the per-call log line.
const (
	outcomeOK            = "ok"
	outcomeTransient     = "transient"
	outcomePermanent     = "permanent"
	outcomeInvalidAnswer = "invalid_answer"
	outcomeAuth          = "auth"
)

func outcome(err error) string {
	switch {
	case err == nil:
		return outcomeOK
	case errors.Is(err, classifier.ErrAuth):
		return outcomeAuth
	case errors.Is(err, classifier.ErrInvalidAnswer):
		return outcomeInvalidAnswer
	case errors.Is(err, classifier.ErrTransient):
		return outcomeTransient
	default:
		return outcomePermanent
	}
}

// logCall writes the README §4.3 fields the adapter knows; ticket_id and
// attempt belong to the caller. It never logs the ticket text, the API key or
// the request headers; err texts are built here and hold none.
func (c *Client) logCall(ctx context.Context, call classifier.Call, err error) {
	attrs := []slog.Attr{
		slog.String("provider", call.Provider),
		slog.String("model", call.Model),
		slog.Int64("latency_ms", call.Latency.Milliseconds()),
		slog.Int("input_tokens", call.InputTokens),
		slog.Int("output_tokens", call.OutputTokens),
		slog.Float64("cost_usd", call.CostUSD),
		slog.String("cost_source", string(call.CostSource)),
		slog.String("outcome", outcome(err)),
		slog.Int("http_status", call.HTTPStatus),
	}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	c.logger.LogAttrs(ctx, level, "jev call", attrs...)
}
