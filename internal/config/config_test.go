package config

import (
	"bufio"
	"errors"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type all struct {
	Log        Log
	HTTP       HTTP
	Postgres   Postgres
	Kafka      Kafka
	Outbox     Outbox
	Classifier Classifier
	Thresholds Thresholds
	Worker     Worker
	Limits     Limits
}

func loadAll(getenv func(string) string) (all, error) {
	l := NewLoader(getenv)
	c := all{
		Log:        l.Log(),
		HTTP:       l.HTTP(),
		Postgres:   l.Postgres(),
		Kafka:      l.Kafka(),
		Outbox:     l.Outbox(),
		Classifier: l.Classifier(),
		Thresholds: l.Thresholds(),
		Worker:     l.Worker(),
		Limits:     l.Limits(),
	}
	return c, l.Err()
}

func envOf(m map[string]string) func(string) string {
	return func(name string) string { return m[name] }
}

// readEnvExample parses KEY=VALUE lines of the repository's .env.example.
func readEnvExample(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("../../.env.example")
	if err != nil {
		t.Fatalf("open .env.example: %v", err)
	}
	defer func() { _ = f.Close() }()
	vars := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf(".env.example: malformed line %q", line)
		}
		vars[name] = value
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	return vars
}

func TestDefaultsMatchEnvExample(t *testing.T) {
	example := readEnvExample(t)

	var read []string
	fromExample, err := loadAll(func(name string) string {
		read = append(read, name)
		return example[name]
	})
	if err != nil {
		t.Fatalf("load with .env.example values: %v", err)
	}
	defaults, err := loadAll(envOf(nil))
	if err != nil {
		t.Fatalf("load with empty environment: %v", err)
	}
	if !reflect.DeepEqual(fromExample, defaults) {
		t.Errorf("defaults differ from .env.example:\n got %+v\nwant %+v", defaults, fromExample)
	}

	slices.Sort(read)
	if names := slices.Sorted(maps.Keys(example)); !slices.Equal(slices.Compact(read), names) {
		t.Errorf("variables read by config = %v, want the .env.example names %v", read, names)
	}
}

func TestDefaults(t *testing.T) {
	c, err := loadAll(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := all{
		Log: Log{Level: slog.LevelInfo},
		HTTP: HTTP{
			Addr:              ":8080",
			DebugAddr:         "127.0.0.1:6060",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			IdleTimeout:       60 * time.Second,
			WriteTimeout:      10 * time.Second,
		},
		Postgres: Postgres{URL: NewSecret("postgres://triage:triage@localhost:5432/triage?sslmode=disable")},
		Kafka: Kafka{Brokers: []string{"localhost:29092"}, ConsumerGroup: "triage-workers",
			RetryConsumerGroup: "triage-workers-retry"},
		Outbox: Outbox{
			PollInterval:    500 * time.Millisecond,
			BatchSize:       100,
			PublishTimeout:  10 * time.Second,
			Retention:       7 * 24 * time.Hour,
			CleanupInterval: time.Hour,
		},
		Classifier: Classifier{
			Kind:           ClassifierFake,
			DailyBudgetUSD: 1,
			Jev: Jev{
				Provider:             ProviderOpenRouter,
				Model:                "typesafe/jev-1.14",
				Timeout:              3 * time.Second,
				RateLimit:            5,
				MaxConcurrency:       8,
				InputPricePerMTokUSD: 0.042,
			},
		},
		Thresholds: Thresholds{
			CategoryMinConfidence:  0.60,
			UrgencyMinConfidence:   0.50,
			ComplaintUncertainLow:  0.35,
			ComplaintUncertainHigh: 0.65,
			RouteOtherToHuman:      true,
		},
		Worker: Worker{Concurrency: 8, BatchSize: 64, ShutdownTimeout: 30 * time.Second,
			RetryMaxAttempts: 3, RetryBackoffBase: 30 * time.Second, RetryBackoffMax: 10 * time.Minute},
		Limits: Limits{
			SubmitRatePerMin:      5,
			SubmitBurst:           3,
			StreamTokenRatePerMin: 10,
			StreamTokenBurst:      3,
			MaxBodyBytes:          16384,
			PendingCap:            1000,
			StreamTokenTTL:        60 * time.Second,
			SSEMaxPerIP:           5,
			SSEMaxGlobal:          1000,
			SSEMaxDuration:        10 * time.Minute,
			SSEHeartbeat:          15 * time.Second,
		},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("defaults:\n got %+v\nwant %+v", c, want)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		check func(t *testing.T, c all)
	}{
		{
			name: "log level",
			env:  map[string]string{"LOG_LEVEL": "debug"},
			check: func(t *testing.T, c all) {
				if c.Log.Level != slog.LevelDebug {
					t.Errorf("Level = %v, want DEBUG", c.Log.Level)
				}
			},
		},
		{
			name: "blank value takes the default",
			env:  map[string]string{"HTTP_ADDR": "  ", "JEV_TIMEOUT": ""},
			check: func(t *testing.T, c all) {
				if c.HTTP.Addr != ":8080" || c.Classifier.Jev.Timeout != 3*time.Second {
					t.Errorf("Addr = %q, Timeout = %v, want defaults", c.HTTP.Addr, c.Classifier.Jev.Timeout)
				}
			},
		},
		{
			name: "trusted proxies: CIDRs and bare IPs, spaces and empty items",
			env:  map[string]string{"TRUSTED_PROXIES": " 10.0.0.0/8, ,192.168.1.7,2001:db8::/32,10.1.2.3/16"},
			check: func(t *testing.T, c all) {
				want := []netip.Prefix{
					netip.MustParsePrefix("10.0.0.0/8"),
					netip.MustParsePrefix("192.168.1.7/32"),
					netip.MustParsePrefix("2001:db8::/32"),
					netip.MustParsePrefix("10.1.0.0/16"),
				}
				if !slices.Equal(c.HTTP.TrustedProxies, want) {
					t.Errorf("TrustedProxies = %v, want %v", c.HTTP.TrustedProxies, want)
				}
			},
		},
		{
			name: "debug addr on localhost and IPv6 loopback",
			env:  map[string]string{"DEBUG_ADDR": "[::1]:6061"},
			check: func(t *testing.T, c all) {
				if c.HTTP.DebugAddr != "[::1]:6061" {
					t.Errorf("DebugAddr = %q", c.HTTP.DebugAddr)
				}
			},
		},
		{
			name: "kafka brokers list",
			env:  map[string]string{"KAFKA_BROKERS": "a:9092, b:9092"},
			check: func(t *testing.T, c all) {
				if want := []string{"a:9092", "b:9092"}; !slices.Equal(c.Kafka.Brokers, want) {
					t.Errorf("Brokers = %v, want %v", c.Kafka.Brokers, want)
				}
			},
		},
		{
			name: "worker",
			env: map[string]string{"WORKER_CONCURRENCY": "2", "WORKER_BATCH_SIZE": "10", "WORKER_SHUTDOWN_TIMEOUT": "1m",
				"WORKER_RETRY_MAX_ATTEMPTS": "5", "WORKER_RETRY_BACKOFF_BASE": "1s", "WORKER_RETRY_BACKOFF_MAX": "1h"},
			check: func(t *testing.T, c all) {
				want := Worker{Concurrency: 2, BatchSize: 10, ShutdownTimeout: time.Minute,
					RetryMaxAttempts: 5, RetryBackoffBase: time.Second, RetryBackoffMax: time.Hour}
				if c.Worker != want {
					t.Errorf("Worker = %+v, want %+v", c.Worker, want)
				}
			},
		},
		{
			name: "jev via openrouter picks the openrouter key",
			env: map[string]string{
				"CLASSIFIER": "jev", "OPENROUTER_API_KEY": "or-key", "TYPESAFE_API_KEY": "ts-key",
				"JEV_TIMEOUT": "1500ms", "JEV_RATE_LIMIT": "2.5", "JEV_MAX_CONCURRENCY": "4",
				"JEV_INPUT_PRICE_PER_MTOK_USD": "0", "DAILY_BUDGET_USD": "0",
			},
			check: func(t *testing.T, c all) {
				j := c.Classifier.Jev
				if c.Classifier.Kind != ClassifierJev || j.APIKey.Reveal() != "or-key" ||
					j.Timeout != 1500*time.Millisecond || j.RateLimit != 2.5 || j.MaxConcurrency != 4 ||
					j.InputPricePerMTokUSD != 0 || c.Classifier.DailyBudgetUSD != 0 {
					t.Errorf("Classifier = %+v (key %q)", c.Classifier, j.APIKey.Reveal())
				}
			},
		},
		{
			name: "jev via typesafe picks the typesafe key",
			env:  map[string]string{"CLASSIFIER": "jev", "JEV_PROVIDER": "typesafe", "TYPESAFE_API_KEY": "ts-key"},
			check: func(t *testing.T, c all) {
				if got := c.Classifier.Jev.APIKey.Reveal(); got != "ts-key" {
					t.Errorf("APIKey = %q, want ts-key", got)
				}
			},
		},
		{
			name: "thresholds and bool",
			env: map[string]string{
				"CATEGORY_MIN_CONFIDENCE": "1", "URGENCY_MIN_CONFIDENCE": "0",
				"COMPLAINT_UNCERTAIN_LOW": "0.5", "COMPLAINT_UNCERTAIN_HIGH": "0.5", "ROUTE_OTHER_TO_HUMAN": "false",
			},
			check: func(t *testing.T, c all) {
				want := Thresholds{
					CategoryMinConfidence: 1, ComplaintUncertainLow: 0.5, ComplaintUncertainHigh: 0.5,
				}
				if c.Thresholds != want {
					t.Errorf("Thresholds = %+v, want %+v", c.Thresholds, want)
				}
			},
		},
		{
			name: "outbox",
			env: map[string]string{
				"OUTBOX_POLL_INTERVAL": "2s", "OUTBOX_BATCH_SIZE": "1", "OUTBOX_PUBLISH_TIMEOUT": "1m",
				"OUTBOX_RETENTION": "24h", "OUTBOX_CLEANUP_INTERVAL": "5m",
			},
			check: func(t *testing.T, c all) {
				want := Outbox{
					PollInterval: 2 * time.Second, BatchSize: 1, PublishTimeout: time.Minute,
					Retention: 24 * time.Hour, CleanupInterval: 5 * time.Minute,
				}
				if c.Outbox != want {
					t.Errorf("Outbox = %+v, want %+v", c.Outbox, want)
				}
			},
		},
		{
			name: "limits",
			env: map[string]string{
				"SUBMIT_RATE_PER_MIN": "0.5", "MAX_BODY_BYTES": "1", "SSE_MAX_PER_IP": "7", "SSE_MAX_GLOBAL": "7",
				"STREAM_TOKEN_TTL": "2m", "SSE_HEARTBEAT": "1s", "SSE_MAX_DURATION": "2s",
			},
			check: func(t *testing.T, c all) {
				l := c.Limits
				if l.SubmitRatePerMin != 0.5 || l.MaxBodyBytes != 1 || l.SSEMaxPerIP != 7 || l.SSEMaxGlobal != 7 ||
					l.StreamTokenTTL != 2*time.Minute || l.SSEHeartbeat != time.Second || l.SSEMaxDuration != 2*time.Second {
					t.Errorf("Limits = %+v", l)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := loadAll(envOf(tt.env))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, c)
		})
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string // names in the error, in order
	}{
		{"log level", map[string]string{"LOG_LEVEL": "loud"}, []string{"LOG_LEVEL"}},
		{"http addr without port", map[string]string{"HTTP_ADDR": "8080"}, []string{"HTTP_ADDR"}},
		{"http addr bad port", map[string]string{"HTTP_ADDR": ":99999"}, []string{"HTTP_ADDR"}},
		{"debug addr on all interfaces", map[string]string{"DEBUG_ADDR": ":6060"}, []string{"DEBUG_ADDR"}},
		{"debug addr public", map[string]string{"DEBUG_ADDR": "10.0.0.1:6060"}, []string{"DEBUG_ADDR"}},
		{"trusted proxies", map[string]string{"TRUSTED_PROXIES": "10.0.0.0/8,nope"}, []string{"TRUSTED_PROXIES"}},
		{"http timeout zero", map[string]string{"HTTP_IDLE_TIMEOUT": "0s"}, []string{"HTTP_IDLE_TIMEOUT"}},
		{
			"read header timeout above read timeout",
			map[string]string{"HTTP_READ_HEADER_TIMEOUT": "20s"},
			[]string{"HTTP_READ_HEADER_TIMEOUT"},
		},
		{"kafka broker without port", map[string]string{"KAFKA_BROKERS": "a:9092,b"}, []string{"KAFKA_BROKERS"}},
		{"kafka broker without host", map[string]string{"KAFKA_BROKERS": ":9092"}, []string{"KAFKA_BROKERS"}},
		{"kafka brokers only commas", map[string]string{"KAFKA_BROKERS": ", ,"}, []string{"KAFKA_BROKERS"}},
		{"worker concurrency zero", map[string]string{"WORKER_CONCURRENCY": "0"}, []string{"WORKER_CONCURRENCY"}},
		{"worker shutdown timeout zero", map[string]string{"WORKER_SHUTDOWN_TIMEOUT": "0s"}, []string{"WORKER_SHUTDOWN_TIMEOUT"}},
		{"worker retry max attempts zero", map[string]string{"WORKER_RETRY_MAX_ATTEMPTS": "0"}, []string{"WORKER_RETRY_MAX_ATTEMPTS"}},
		{"worker retry backoff negative", map[string]string{"WORKER_RETRY_BACKOFF_BASE": "-1s"}, []string{"WORKER_RETRY_BACKOFF_BASE"}},
		{"classifier kind", map[string]string{"CLASSIFIER": "magic"}, []string{"CLASSIFIER"}},
		{"jev provider", map[string]string{"JEV_PROVIDER": "acme"}, []string{"JEV_PROVIDER"}},
		{"openrouter key missing", map[string]string{"CLASSIFIER": "jev", "TYPESAFE_API_KEY": "k"}, []string{"OPENROUTER_API_KEY"}},
		{
			"typesafe key missing",
			map[string]string{"CLASSIFIER": "jev", "JEV_PROVIDER": "typesafe", "OPENROUTER_API_KEY": "k"},
			[]string{"TYPESAFE_API_KEY"},
		},
		{"duration syntax", map[string]string{"JEV_TIMEOUT": "3x"}, []string{"JEV_TIMEOUT"}},
		{"duration zero", map[string]string{"STREAM_TOKEN_TTL": "0s"}, []string{"STREAM_TOKEN_TTL"}},
		{"duration without unit", map[string]string{"SSE_HEARTBEAT": "15"}, []string{"SSE_HEARTBEAT"}},
		{"rate zero", map[string]string{"JEV_RATE_LIMIT": "0"}, []string{"JEV_RATE_LIMIT"}},
		{"rate infinite", map[string]string{"SUBMIT_RATE_PER_MIN": "Inf"}, []string{"SUBMIT_RATE_PER_MIN"}},
		{"price NaN", map[string]string{"JEV_INPUT_PRICE_PER_MTOK_USD": "NaN"}, []string{"JEV_INPUT_PRICE_PER_MTOK_USD"}},
		{"budget negative", map[string]string{"DAILY_BUDGET_USD": "-1"}, []string{"DAILY_BUDGET_USD"}},
		{"int syntax", map[string]string{"JEV_MAX_CONCURRENCY": "eight"}, []string{"JEV_MAX_CONCURRENCY"}},
		{"int below minimum", map[string]string{"PENDING_CAP": "0"}, []string{"PENDING_CAP"}},
		{"int float", map[string]string{"MAX_BODY_BYTES": "16.5"}, []string{"MAX_BODY_BYTES"}},
		{"outbox batch size zero", map[string]string{"OUTBOX_BATCH_SIZE": "0"}, []string{"OUTBOX_BATCH_SIZE"}},
		{"outbox poll interval zero", map[string]string{"OUTBOX_POLL_INTERVAL": "0s"}, []string{"OUTBOX_POLL_INTERVAL"}},
		{"threshold above one", map[string]string{"CATEGORY_MIN_CONFIDENCE": "1.2"}, []string{"CATEGORY_MIN_CONFIDENCE"}},
		{"bool", map[string]string{"ROUTE_OTHER_TO_HUMAN": "yes"}, []string{"ROUTE_OTHER_TO_HUMAN"}},
		{
			"complaint band inverted",
			map[string]string{"COMPLAINT_UNCERTAIN_LOW": "0.7", "COMPLAINT_UNCERTAIN_HIGH": "0.6"},
			[]string{"COMPLAINT_UNCERTAIN_LOW"},
		},
		{
			"complaint band not compared when one side is invalid",
			map[string]string{"COMPLAINT_UNCERTAIN_LOW": "0.9", "COMPLAINT_UNCERTAIN_HIGH": "x"},
			[]string{"COMPLAINT_UNCERTAIN_HIGH"},
		},
		{"sse per-ip above global", map[string]string{"SSE_MAX_PER_IP": "11", "SSE_MAX_GLOBAL": "10"}, []string{"SSE_MAX_PER_IP"}},
		{"sse heartbeat not shorter", map[string]string{"SSE_HEARTBEAT": "10m"}, []string{"SSE_HEARTBEAT"}},
		{
			"every problem reported at once",
			map[string]string{
				"LOG_LEVEL": "loud", "DEBUG_ADDR": "0.0.0.0:6060", "CLASSIFIER": "jev",
				"JEV_TIMEOUT": "soon", "URGENCY_MIN_CONFIDENCE": "2", "SSE_MAX_GLOBAL": "0",
			},
			[]string{"LOG_LEVEL", "DEBUG_ADDR", "JEV_TIMEOUT", "OPENROUTER_API_KEY", "URGENCY_MIN_CONFIDENCE", "SSE_MAX_GLOBAL"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadAll(envOf(tt.env))
			var cfgErr *Error
			if !errors.As(err, &cfgErr) {
				t.Fatalf("err = %v, want *Error", err)
			}
			var names []string
			for _, p := range cfgErr.Problems {
				names = append(names, p.Name)
			}
			if !slices.Equal(names, tt.want) {
				t.Errorf("problem names = %v, want %v", names, tt.want)
			}
			for _, name := range tt.want {
				if !strings.Contains(err.Error(), name+": ") {
					t.Errorf("message %q does not name %s", err, name)
				}
			}
		})
	}
}

func TestErrorMessage(t *testing.T) {
	_, err := loadAll(envOf(map[string]string{"JEV_TIMEOUT": "3x", "PENDING_CAP": "-1"}))
	want := `config: invalid environment: JEV_TIMEOUT: must be a positive duration such as 3s, got "3x"; ` +
		`PENDING_CAP: must be an integer >= 1, got "-1"`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}

func TestErrRepeatsNoSecrets(t *testing.T) {
	// The secrets are valid here, but every other problem must still keep them out.
	env := map[string]string{
		"CLASSIFIER": "jev", "JEV_PROVIDER": "typesafe", "OPENROUTER_API_KEY": "sk-or-secret",
		"DATABASE_URL": "postgres://u:hunter2@db/x", "JEV_TIMEOUT": "bad",
	}
	_, err := loadAll(envOf(env))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, secret := range []string{"sk-or-secret", "hunter2"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error %q leaks %q", err, secret)
		}
	}
}
