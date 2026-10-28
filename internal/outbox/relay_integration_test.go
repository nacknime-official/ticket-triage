//go:build integration

package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/outbox"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/migrations"
)

// postgresImage matches the Postgres service in compose.yaml.
const postgresImage = "pgvector/pgvector:0.8.7-pg16"

// newPool starts a fresh Postgres container, applies every migration and
// returns a pool connected to it.
func newPool(t *testing.T) *pgxpool.Pool {
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
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newCluster starts an in-process Kafka with every topic of the service.
func newCluster(t *testing.T) *kfake.Cluster {
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

// startRelay runs a relay that publishes from pool to cluster until the
// test ends.
func startRelay(t *testing.T, pool *pgxpool.Pool, cluster *kfake.Cluster, cfg outbox.Config) {
	t.Helper()
	cl, err := kafka.NewClient(cluster.ListenAddrs(), "outbox-relay-test")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	r := outbox.NewRelay(postgres.NewOutbox(pool), kafka.NewPublisher(cl), cfg, discard)
	r.Start(t.Context())
	t.Cleanup(func() {
		// t.Context is already canceled when cleanups run.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
		cl.Close()
	})
}

// submit stores n tickets through the ticket service and returns their ids.
func submit(t *testing.T, pool *pgxpool.Pool, n int) []string {
	t.Helper()
	svc := ticket.NewService(postgres.NewTickets(pool, kafka.TopicSubmitted), ticket.Config{MaxPending: n + 1})
	ids := make([]string, n)
	for i := range n {
		tk, _, err := svc.Submit(t.Context(), "Refund", "Charged twice, please refund.")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		ids[i] = tk.ID.String()
	}
	return ids
}

// consume reads records from tickets.submitted until it has want of them,
// then reads for a short while more to catch duplicates.
func consume(t *testing.T, cluster *kfake.Cluster, want int) []*kgo.Record {
	t.Helper()
	cl, err := kafka.NewClient(cluster.ListenAddrs(), "outbox-test-consumer",
		kgo.ConsumeTopics(kafka.TopicSubmitted), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("NewClient consumer: %v", err)
	}
	defer cl.Close()

	var recs []*kgo.Record
	deadline := time.Now().Add(30 * time.Second)
	var grace time.Time
	for {
		if len(recs) >= want && grace.IsZero() {
			grace = time.Now().Add(500 * time.Millisecond)
		}
		if !grace.IsZero() && time.Now().After(grace) {
			return recs
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d records on %s, want %d", len(recs), kafka.TopicSubmitted, want)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		fetches := cl.PollFetches(ctx)
		cancel()
		fetches.EachError(func(_ string, _ int32, err error) {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("fetch: %v", err)
			}
		})
		recs = append(recs, fetches.Records()...)
	}
}

// waitPublished polls until no outbox row is left unpublished.
func waitPublished(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var left int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&left); err != nil {
			t.Fatalf("count unpublished: %v", err)
		}
		if left == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d outbox rows still unpublished", left)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSubmittedTicketReachesKafka(t *testing.T) {
	t.Parallel()
	pool, cluster := newPool(t), newCluster(t)
	startRelay(t, pool, cluster, outbox.Config{PollInterval: 20 * time.Millisecond})

	id := submit(t, pool, 1)[0]
	recs := consume(t, cluster, 1)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	r := recs[0]
	if string(r.Key) != id {
		t.Errorf("record key = %q, want ticket id %s", r.Key, id)
	}
	ev, err := ticket.UnmarshalSubmitted(r.Value)
	if err != nil {
		t.Fatalf("record value %s: %v", r.Value, err)
	}
	if ev.TicketID.String() != id {
		t.Errorf("event ticket id = %s, want %s", ev.TicketID, id)
	}
	waitPublished(t, pool)
}

func TestTwoRelaysDoNotDuplicate(t *testing.T) {
	t.Parallel()
	pool, cluster := newPool(t), newCluster(t)
	const n = 200
	ids := submit(t, pool, n)

	// Small batches make the relays contend for rows many times.
	cfg := outbox.Config{PollInterval: 10 * time.Millisecond, BatchSize: 7}
	startRelay(t, pool, cluster, cfg)
	startRelay(t, pool, cluster, cfg)

	waitPublished(t, pool)
	recs := consume(t, cluster, n)
	seen := map[string]int{}
	for _, r := range recs {
		seen[string(r.Key)]++
	}
	if len(recs) != n {
		t.Errorf("got %d records for %d tickets", len(recs), n)
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Errorf("ticket %s published %d times, want once", id, seen[id])
		}
	}
}

// failProduce makes cluster answer every produce request with a
// non-retriable error while failing is true.
func failProduce(cluster *kfake.Cluster, failing *atomic.Bool) {
	cluster.ControlKey(int16(kmsg.Produce), func(req kmsg.Request) (kmsg.Response, error, bool) {
		cluster.KeepControl()
		if !failing.Load() {
			return nil, nil, false
		}
		pr, ok := req.(*kmsg.ProduceRequest)
		if !ok {
			return nil, nil, false
		}
		resp := kmsg.NewPtrProduceResponse()
		resp.Version = pr.Version
		for _, rt := range pr.Topics {
			st := kmsg.NewProduceResponseTopic()
			st.Topic, st.TopicID = rt.Topic, rt.TopicID
			for _, rp := range rt.Partitions {
				sp := kmsg.NewProduceResponseTopicPartition()
				sp.Partition = rp.Partition
				sp.ErrorCode = kerr.InvalidRecord.Code
				st.Partitions = append(st.Partitions, sp)
			}
			resp.Topics = append(resp.Topics, st)
		}
		return resp, nil, true
	})
}

func TestFailedProduceIsRetried(t *testing.T) {
	t.Parallel()
	pool, cluster := newPool(t), newCluster(t)
	var failing atomic.Bool
	failing.Store(true)
	failProduce(cluster, &failing)

	id := submit(t, pool, 1)[0]
	startRelay(t, pool, cluster, outbox.Config{PollInterval: 20 * time.Millisecond, PublishTimeout: time.Second})

	// Wait until the relay has tried and failed at least twice.
	deadline := time.Now().Add(30 * time.Second)
	for {
		var (
			published bool
			attempts  int
			lastError *string
		)
		err := pool.QueryRow(t.Context(),
			`SELECT published_at IS NOT NULL, attempts, last_error FROM outbox`).Scan(&published, &attempts, &lastError)
		if err != nil {
			t.Fatalf("read outbox row: %v", err)
		}
		if published {
			t.Fatal("outbox row marked published while every produce fails")
		}
		if attempts >= 2 {
			if lastError == nil || *lastError == "" {
				t.Error("failed attempts recorded without last_error")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay made %d attempts, want at least 2", attempts)
		}
		time.Sleep(20 * time.Millisecond)
	}

	failing.Store(false)
	waitPublished(t, pool)
	recs := consume(t, cluster, 1)
	if len(recs) != 1 || string(recs[0].Key) != id {
		t.Fatalf("records after recovery = %d (first key %q), want one keyed %s", len(recs), firstKey(recs), id)
	}
}

func firstKey(recs []*kgo.Record) string {
	if len(recs) == 0 {
		return ""
	}
	return string(recs[0].Key)
}
