//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/migrations"
)

// postgresImage matches the Postgres service in compose.yaml.
const postgresImage = "pgvector/pgvector:0.8.7-pg16"

// syncBuffer is a bytes.Buffer safe for the logger and the test at once.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startPostgres starts a migrated Postgres container and returns its URL.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	ctr, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("triage"),
		tcpostgres.WithUsername("triage"),
		tcpostgres.WithPassword("triage"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() { _ = db.Close() }()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return dsn
}

// startKafka starts an in-process broker with every topic of the service.
func startKafka(t *testing.T) *kfake.Cluster {
	t.Helper()
	opts := []kfake.Opt{kfake.NumBrokers(1)}
	for _, s := range kafka.Topics() {
		opts = append(opts, kfake.SeedTopics(s.Partitions, s.Name))
	}
	cluster, err := kfake.NewCluster(opts...)
	if err != nil {
		t.Fatalf("start kfake cluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	return cluster
}

// firstSubmitted reads the first record of tickets.submitted.
func firstSubmitted(t *testing.T, cluster *kfake.Cluster) *kgo.Record {
	t.Helper()
	cl, err := kafka.NewClient(cluster.ListenAddrs(), "api-test-consumer",
		kgo.ConsumeTopics(kafka.TopicSubmitted), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		fetches := cl.PollFetches(ctx)
		if err := ctx.Err(); err != nil {
			t.Fatalf("no record on %s: %v", kafka.TopicSubmitted, err)
		}
		if recs := fetches.Records(); len(recs) > 0 {
			return recs[0]
		}
	}
}

// startedAddr waits for the "api started" log line and returns its addr.
func startedAddr(t *testing.T, logs *syncBuffer, done <-chan error) string {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		for line := range strings.Lines(logs.String()) {
			var entry struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "api started" {
				return entry.Addr
			}
		}
		select {
		case err := <-done:
			t.Fatalf("run returned before serving: %v\n%s", err, logs)
		case <-deadline:
			t.Fatalf("api did not start:\n%s", logs)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestRunServesAndShutsDown(t *testing.T) {
	cluster := startKafka(t)
	env := map[string]string{
		"HTTP_ADDR":            "127.0.0.1:0",
		"DEBUG_ADDR":           "127.0.0.1:0",
		"DATABASE_URL":         startPostgres(t),
		"KAFKA_BROKERS":        strings.Join(cluster.ListenAddrs(), ","),
		"OUTBOX_POLL_INTERVAL": "50ms",
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var logs syncBuffer
	var stopped sync.WaitGroup
	stopped.Add(1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, stopped.Done, func(k string) string { return env[k] }, &logs) }()
	base := "http://" + startedAddr(t, &logs, done)

	call := func(method, path, body, token string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var b bytes.Buffer
		if _, err := b.ReadFrom(resp.Body); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, b.String()
	}

	if status, body := call(http.MethodGet, "/readyz", "", ""); status != http.StatusOK {
		t.Fatalf("readyz: %d %s", status, body)
	}
	status, body := call(http.MethodPost, "/tickets", `{"subject":"Refund","body":"Charged twice"}`, "")
	if status != http.StatusAccepted {
		t.Fatalf("submit: %d %s", status, body)
	}
	var submitted struct {
		ID          string `json:"id"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &submitted); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	status, body = call(http.MethodGet, "/tickets/"+submitted.ID, "", submitted.AccessToken)
	if status != http.StatusOK || !strings.Contains(body, `"status":"pending"`) {
		t.Fatalf("get: %d %s", status, body)
	}

	// The relay publishes the submitted ticket, keyed by its id.
	if rec := firstSubmitted(t, cluster); string(rec.Key) != submitted.ID {
		t.Errorf("record key = %s, want ticket id %s", rec.Key, submitted.ID)
	} else if strings.Contains(string(rec.Value), "Charged twice") {
		t.Errorf("record carries ticket text: %s", rec.Value)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v\n%s", err, &logs)
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatalf("run did not return after cancel:\n%s", &logs)
	}
	stopped.Wait()
	if out := logs.String(); !strings.Contains(out, `"msg":"shutdown complete"`) {
		t.Errorf("no shutdown complete line:\n%s", out)
	}
	for _, secret := range []string{submitted.AccessToken, "Charged twice", ":triage@"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs leak %q:\n%s", secret, &logs)
		}
	}
}
