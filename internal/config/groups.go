package config

import (
	"log/slog"
	"net/netip"
	"time"
)

// Classifier kinds and Jev providers.
const (
	ClassifierFake = "fake"
	ClassifierJev  = "jev"

	ProviderOpenRouter = "openrouter"
	ProviderTypeSafe   = "typesafe"
)

// Log configures the process logger.
type Log struct {
	Level slog.Level // LOG_LEVEL
}

// Log reads the Log group.
func (l *Loader) Log() Log {
	raw := l.raw("LOG_LEVEL", "info")
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		l.invalid("LOG_LEVEL", "one of debug, info, warn, error", raw)
	}
	return Log{Level: level}
}

// LogValue implements slog.LogValuer.
func (c Log) LogValue() slog.Value {
	return slog.GroupValue(slog.String("level", c.Level.String()))
}

// HTTP configures the cmd/api listeners.
type HTTP struct {
	Addr           string         // HTTP_ADDR
	DebugAddr      string         // DEBUG_ADDR, always a loopback address
	TrustedProxies []netip.Prefix // TRUSTED_PROXIES; empty means use RemoteAddr only

	ReadHeaderTimeout time.Duration // HTTP_READ_HEADER_TIMEOUT
	ReadTimeout       time.Duration // HTTP_READ_TIMEOUT, the whole request incl. body
	IdleTimeout       time.Duration // HTTP_IDLE_TIMEOUT, between keep-alive requests
	// WriteTimeout is HTTP_WRITE_TIMEOUT: the per-request write deadline of
	// non-streaming handlers. The server itself has no WriteTimeout (SSE).
	WriteTimeout time.Duration
}

// HTTP reads the HTTP group.
func (l *Loader) HTTP() HTTP {
	c := HTTP{
		Addr:              l.addr("HTTP_ADDR", ":8080"),
		DebugAddr:         l.loopbackAddr("DEBUG_ADDR", "127.0.0.1:6060"),
		TrustedProxies:    l.prefixes("TRUSTED_PROXIES", ""),
		ReadHeaderTimeout: l.duration("HTTP_READ_HEADER_TIMEOUT", "5s"),
		ReadTimeout:       l.duration("HTTP_READ_TIMEOUT", "10s"),
		IdleTimeout:       l.duration("HTTP_IDLE_TIMEOUT", "60s"),
		WriteTimeout:      l.duration("HTTP_WRITE_TIMEOUT", "10s"),
	}
	if !l.failed("HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT") && c.ReadHeaderTimeout > c.ReadTimeout {
		l.fail("HTTP_READ_HEADER_TIMEOUT", "must not exceed HTTP_READ_TIMEOUT")
	}
	return c
}

// LogValue implements slog.LogValuer.
func (c HTTP) LogValue() slog.Value {
	proxies := make([]string, len(c.TrustedProxies))
	for i, p := range c.TrustedProxies {
		proxies[i] = p.String()
	}
	return slog.GroupValue(
		slog.String("addr", c.Addr),
		slog.String("debug_addr", c.DebugAddr),
		slog.Any("trusted_proxies", proxies),
		slog.Duration("read_header_timeout", c.ReadHeaderTimeout),
		slog.Duration("read_timeout", c.ReadTimeout),
		slog.Duration("idle_timeout", c.IdleTimeout),
		slog.Duration("write_timeout", c.WriteTimeout),
	)
}

// Postgres configures the connection pool.
type Postgres struct {
	URL Secret // DATABASE_URL; it carries the password
}

// Postgres reads the Postgres group.
func (l *Loader) Postgres() Postgres {
	return Postgres{
		URL: NewSecret(l.str("DATABASE_URL", "postgres://triage:triage@localhost:5432/triage?sslmode=disable")),
	}
}

// LogValue implements slog.LogValuer.
func (c Postgres) LogValue() slog.Value {
	return slog.GroupValue(slog.Any("url", c.URL))
}

// Kafka configures the franz-go clients.
type Kafka struct {
	Brokers            []string // KAFKA_BROKERS
	ConsumerGroup      string   // KAFKA_CONSUMER_GROUP
	RetryConsumerGroup string   // KAFKA_RETRY_CONSUMER_GROUP, of the tickets.submitted.retry consumer
}

// Kafka reads the Kafka group.
func (l *Loader) Kafka() Kafka {
	brokers := l.list("KAFKA_BROKERS", "localhost:29092")
	if len(brokers) == 0 {
		l.fail("KAFKA_BROKERS", "required")
	}
	for _, b := range brokers {
		if host, ok := validAddr(b); !ok || host == "" {
			l.invalid("KAFKA_BROKERS", "a comma-separated list of host:port", b)
		}
	}
	return Kafka{
		Brokers:            brokers,
		ConsumerGroup:      l.str("KAFKA_CONSUMER_GROUP", "triage-workers"),
		RetryConsumerGroup: l.str("KAFKA_RETRY_CONSUMER_GROUP", "triage-workers-retry"),
	}
}

// LogValue implements slog.LogValuer.
func (c Kafka) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("brokers", c.Brokers),
		slog.String("consumer_group", c.ConsumerGroup),
		slog.String("retry_consumer_group", c.RetryConsumerGroup),
	)
}

// Outbox configures the outbox relay in cmd/api.
type Outbox struct {
	PollInterval    time.Duration // OUTBOX_POLL_INTERVAL
	BatchSize       int           // OUTBOX_BATCH_SIZE
	PublishTimeout  time.Duration // OUTBOX_PUBLISH_TIMEOUT, per batch
	Retention       time.Duration // OUTBOX_RETENTION, of published rows
	CleanupInterval time.Duration // OUTBOX_CLEANUP_INTERVAL
}

// Outbox reads the Outbox group.
func (l *Loader) Outbox() Outbox {
	return Outbox{
		PollInterval:    l.duration("OUTBOX_POLL_INTERVAL", "500ms"),
		BatchSize:       l.int("OUTBOX_BATCH_SIZE", "100", 1),
		PublishTimeout:  l.duration("OUTBOX_PUBLISH_TIMEOUT", "10s"),
		Retention:       l.duration("OUTBOX_RETENTION", "168h"),
		CleanupInterval: l.duration("OUTBOX_CLEANUP_INTERVAL", "1h"),
	}
}

// LogValue implements slog.LogValuer.
func (c Outbox) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Duration("poll_interval", c.PollInterval),
		slog.Int("batch_size", c.BatchSize),
		slog.Duration("publish_timeout", c.PublishTimeout),
		slog.Duration("retention", c.Retention),
		slog.Duration("cleanup_interval", c.CleanupInterval),
	)
}

// Classifier selects the classifier and configures Jev and the spend cap.
type Classifier struct {
	Kind           string  // CLASSIFIER: ClassifierFake or ClassifierJev
	DailyBudgetUSD float64 // DAILY_BUDGET_USD, per UTC day
	Jev            Jev
}

// Jev configures the Jev adapter.
type Jev struct {
	Provider string // JEV_PROVIDER: ProviderOpenRouter or ProviderTypeSafe
	// APIKey is OPENROUTER_API_KEY or TYPESAFE_API_KEY, whichever Provider
	// selects. It is required when Kind is ClassifierJev.
	APIKey               Secret
	Model                string        // JEV_MODEL
	Timeout              time.Duration // JEV_TIMEOUT, per call
	RateLimit            float64       // JEV_RATE_LIMIT, requests per second
	MaxConcurrency       int           // JEV_MAX_CONCURRENCY, per worker
	InputPricePerMTokUSD float64       // JEV_INPUT_PRICE_PER_MTOK_USD
}

// Classifier reads the Classifier group.
func (l *Loader) Classifier() Classifier {
	c := Classifier{
		Kind: l.oneOf("CLASSIFIER", ClassifierFake, ClassifierFake, ClassifierJev),
		Jev: Jev{
			Provider:             l.oneOf("JEV_PROVIDER", ProviderOpenRouter, ProviderOpenRouter, ProviderTypeSafe),
			Model:                l.str("JEV_MODEL", "typesafe/jev-1.14"),
			Timeout:              l.duration("JEV_TIMEOUT", "3s"),
			RateLimit:            l.float("JEV_RATE_LIMIT", "5", "a number > 0", positive),
			MaxConcurrency:       l.int("JEV_MAX_CONCURRENCY", "8", 1),
			InputPricePerMTokUSD: l.float("JEV_INPUT_PRICE_PER_MTOK_USD", "0.042", "a number >= 0", nonNegative),
		},
		DailyBudgetUSD: l.float("DAILY_BUDGET_USD", "1.00", "a number >= 0", nonNegative),
	}
	keyName, typeSafeKey := "OPENROUTER_API_KEY", l.secret("TYPESAFE_API_KEY")
	c.Jev.APIKey = l.secret(keyName)
	if c.Jev.Provider == ProviderTypeSafe {
		keyName, c.Jev.APIKey = "TYPESAFE_API_KEY", typeSafeKey
	}
	if c.Kind == ClassifierJev && c.Jev.Provider != "" && c.Jev.APIKey.IsZero() {
		l.fail(keyName, "required when CLASSIFIER=jev and JEV_PROVIDER="+c.Jev.Provider)
	}
	return c
}

// LogValue implements slog.LogValuer.
func (c Classifier) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("kind", c.Kind),
		slog.Float64("daily_budget_usd", c.DailyBudgetUSD),
		slog.Any("jev", c.Jev),
	)
}

// LogValue implements slog.LogValuer.
func (c Jev) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("provider", c.Provider),
		slog.Any("api_key", c.APIKey),
		slog.String("model", c.Model),
		slog.Duration("timeout", c.Timeout),
		slog.Float64("rate_limit", c.RateLimit),
		slog.Int("max_concurrency", c.MaxConcurrency),
		slog.Float64("input_price_per_mtok_usd", c.InputPricePerMTokUSD),
	)
}

// Thresholds configures the decision policy.
type Thresholds struct {
	CategoryMinConfidence  float64 // CATEGORY_MIN_CONFIDENCE
	UrgencyMinConfidence   float64 // URGENCY_MIN_CONFIDENCE
	ComplaintUncertainLow  float64 // COMPLAINT_UNCERTAIN_LOW
	ComplaintUncertainHigh float64 // COMPLAINT_UNCERTAIN_HIGH
	RouteOtherToHuman      bool    // ROUTE_OTHER_TO_HUMAN
}

// Thresholds reads the Thresholds group.
func (l *Loader) Thresholds() Thresholds {
	const want = "a number in [0, 1]"
	t := Thresholds{
		CategoryMinConfidence:  l.float("CATEGORY_MIN_CONFIDENCE", "0.60", want, unit),
		UrgencyMinConfidence:   l.float("URGENCY_MIN_CONFIDENCE", "0.50", want, unit),
		ComplaintUncertainLow:  l.float("COMPLAINT_UNCERTAIN_LOW", "0.35", want, unit),
		ComplaintUncertainHigh: l.float("COMPLAINT_UNCERTAIN_HIGH", "0.65", want, unit),
		RouteOtherToHuman:      l.bool("ROUTE_OTHER_TO_HUMAN", "true"),
	}
	if !l.failed("COMPLAINT_UNCERTAIN_LOW", "COMPLAINT_UNCERTAIN_HIGH") &&
		t.ComplaintUncertainLow > t.ComplaintUncertainHigh {
		l.fail("COMPLAINT_UNCERTAIN_LOW", "must not exceed COMPLAINT_UNCERTAIN_HIGH")
	}
	return t
}

// LogValue implements slog.LogValuer.
func (c Thresholds) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Float64("category_min_confidence", c.CategoryMinConfidence),
		slog.Float64("urgency_min_confidence", c.UrgencyMinConfidence),
		slog.Float64("complaint_uncertain_low", c.ComplaintUncertainLow),
		slog.Float64("complaint_uncertain_high", c.ComplaintUncertainHigh),
		slog.Bool("route_other_to_human", c.RouteOtherToHuman),
	)
}

// Worker configures the classifier consumer in cmd/worker.
type Worker struct {
	Concurrency     int           // WORKER_CONCURRENCY, tickets processed at once
	BatchSize       int           // WORKER_BATCH_SIZE, max records per poll and commit
	ShutdownTimeout time.Duration // WORKER_SHUTDOWN_TIMEOUT, to drain in-flight tickets and leave the group
	// RetryMaxAttempts is the number of trips through tickets.submitted.retry
	// before a ticket goes to needs_human and the DLQ (README §4.2).
	RetryMaxAttempts int           // WORKER_RETRY_MAX_ATTEMPTS
	RetryBackoffBase time.Duration // WORKER_RETRY_BACKOFF_BASE, before retry 1; doubled per retry
	RetryBackoffMax  time.Duration // WORKER_RETRY_BACKOFF_MAX
}

// Worker reads the Worker group.
func (l *Loader) Worker() Worker {
	return Worker{
		Concurrency:      l.int("WORKER_CONCURRENCY", "8", 1),
		BatchSize:        l.int("WORKER_BATCH_SIZE", "64", 1),
		ShutdownTimeout:  l.duration("WORKER_SHUTDOWN_TIMEOUT", "30s"),
		RetryMaxAttempts: l.int("WORKER_RETRY_MAX_ATTEMPTS", "3", 1),
		RetryBackoffBase: l.duration("WORKER_RETRY_BACKOFF_BASE", "30s"),
		RetryBackoffMax:  l.duration("WORKER_RETRY_BACKOFF_MAX", "10m"),
	}
}

// LogValue implements slog.LogValuer.
func (c Worker) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("concurrency", c.Concurrency),
		slog.Int("batch_size", c.BatchSize),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
		slog.Int("retry_max_attempts", c.RetryMaxAttempts),
		slog.Duration("retry_backoff_base", c.RetryBackoffBase),
		slog.Duration("retry_backoff_max", c.RetryBackoffMax),
	)
}

// Limits configures the resource limits of README §4.1.
type Limits struct {
	SubmitRatePerMin      float64       // SUBMIT_RATE_PER_MIN, per client IP
	SubmitBurst           int           // SUBMIT_BURST
	StreamTokenRatePerMin float64       // STREAM_TOKEN_RATE_PER_MIN, per client IP
	StreamTokenBurst      int           // STREAM_TOKEN_BURST
	MaxBodyBytes          int64         // MAX_BODY_BYTES
	PendingCap            int           // PENDING_CAP
	StreamTokenTTL        time.Duration // STREAM_TOKEN_TTL
	SSEMaxPerIP           int           // SSE_MAX_PER_IP
	SSEMaxGlobal          int           // SSE_MAX_GLOBAL
	SSEMaxDuration        time.Duration // SSE_MAX_DURATION
	SSEHeartbeat          time.Duration // SSE_HEARTBEAT
}

// Limits reads the Limits group.
func (l *Loader) Limits() Limits {
	const rate = "a number > 0"
	c := Limits{
		SubmitRatePerMin:      l.float("SUBMIT_RATE_PER_MIN", "5", rate, positive),
		SubmitBurst:           l.int("SUBMIT_BURST", "3", 1),
		StreamTokenRatePerMin: l.float("STREAM_TOKEN_RATE_PER_MIN", "10", rate, positive),
		StreamTokenBurst:      l.int("STREAM_TOKEN_BURST", "3", 1),
		MaxBodyBytes:          int64(l.int("MAX_BODY_BYTES", "16384", 1)),
		PendingCap:            l.int("PENDING_CAP", "1000", 1),
		StreamTokenTTL:        l.duration("STREAM_TOKEN_TTL", "60s"),
		SSEMaxPerIP:           l.int("SSE_MAX_PER_IP", "5", 1),
		SSEMaxGlobal:          l.int("SSE_MAX_GLOBAL", "1000", 1),
		SSEMaxDuration:        l.duration("SSE_MAX_DURATION", "10m"),
		SSEHeartbeat:          l.duration("SSE_HEARTBEAT", "15s"),
	}
	if !l.failed("SSE_MAX_PER_IP", "SSE_MAX_GLOBAL") && c.SSEMaxPerIP > c.SSEMaxGlobal {
		l.fail("SSE_MAX_PER_IP", "must not exceed SSE_MAX_GLOBAL")
	}
	if !l.failed("SSE_HEARTBEAT", "SSE_MAX_DURATION") && c.SSEHeartbeat >= c.SSEMaxDuration {
		l.fail("SSE_HEARTBEAT", "must be shorter than SSE_MAX_DURATION")
	}
	return c
}

// LogValue implements slog.LogValuer.
func (c Limits) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Float64("submit_rate_per_min", c.SubmitRatePerMin),
		slog.Int("submit_burst", c.SubmitBurst),
		slog.Float64("stream_token_rate_per_min", c.StreamTokenRatePerMin),
		slog.Int("stream_token_burst", c.StreamTokenBurst),
		slog.Int64("max_body_bytes", c.MaxBodyBytes),
		slog.Int("pending_cap", c.PendingCap),
		slog.Duration("stream_token_ttl", c.StreamTokenTTL),
		slog.Int("sse_max_per_ip", c.SSEMaxPerIP),
		slog.Int("sse_max_global", c.SSEMaxGlobal),
		slog.Duration("sse_max_duration", c.SSEMaxDuration),
		slog.Duration("sse_heartbeat", c.SSEHeartbeat),
	)
}
