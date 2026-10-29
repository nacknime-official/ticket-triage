//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/migrations"
)

// postgresImage matches the Postgres service in compose.yaml.
const postgresImage = "pgvector/pgvector:0.8.7-pg16"

const workersGroup = "triage-workers"

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

// history is the database and topic a replay reads, with what the live flow
// already did to it.
type history struct {
	pool  *pgxpool.Pool
	adm   *kadm.Client
	env   map[string]string
	ids   map[string]uuid.UUID // by name
	ticks []ticket.Ticket
}

// newHistory stores tickets in every live state, publishes their submitted
// records plus one for a ticket that is gone, and commits part of the topic
// for the workers' group.
func newHistory(t *testing.T) *history {
	t.Helper()
	ctx := t.Context()
	dsn := startPostgres(t)
	cluster := startKafka(t)
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	cl, err := kafka.NewClient(cluster.ListenAddrs(), "shadow-test")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(cl.Close)

	h := &history{
		pool: pool,
		adm:  kadm.NewClient(cl),
		ids:  make(map[string]uuid.UUID),
		env: map[string]string{
			"DATABASE_URL":  dsn,
			"KAFKA_BROKERS": strings.Join(cluster.ListenAddrs(), ","),
			"CLASSIFIER":    "fake",
			// Stricter than live: the fake's default urgency confidence
			// (0.6) is no longer enough.
			"URGENCY_MIN_CONFIDENCE":  "0.65",
			"WORKER_SHUTDOWN_TIMEOUT": "5s",
		},
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	add := func(name, subject, body string, status ticket.Status, reason ticket.Reason, c *ticket.Classification) {
		_, hash := ticket.NewToken()
		tk := ticket.Ticket{
			ID: uuid.NewV7(), Subject: subject, Body: body, Status: status, StatusReason: reason,
			Classification: c, AccessTokenHash: hash, CreatedAt: now, UpdatedAt: now,
		}
		if status.Final() {
			tk.Attempts, tk.ClassifiedAt = 1, now
		}
		if err := postgres.NewTickets(pool, kafka.TopicSubmitted).Create(ctx, tk); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		h.ids[name] = tk.ID
		h.ticks = append(h.ticks, tk)
	}
	billing := &ticket.Classification{
		Category: "billing", CategoryConfidence: 0.9, ComplaintP: 0.1, Urgency: 1, UrgencyConfidence: 0.7,
	}
	// The fake answers billing with no urgency keyword: stricter thresholds
	// send it to a person.
	add("invoice", "Invoice", "Please send the invoice.", ticket.StatusClassified, "", billing)
	// An urgency keyword keeps the fake confident: still classified.
	add("urgent", "Refund", "Charged twice, refund asap.", ticket.StatusClassified, "",
		&ticket.Classification{Category: "billing", CategoryConfidence: 0.9, ComplaintP: 0.1, Urgency: 2,
			UrgencyConfidence: 0.8})
	add("budget", "Login", "Cannot log in, urgent.", ticket.StatusNeedsHuman, ticket.ReasonBudgetExhausted, nil)
	add("pending", "Dark mode", "Please add dark mode.", ticket.StatusPending, "", nil)

	var recs []*kgo.Record
	for _, id := range append(slicesOf(h.ids), uuid.NewV7()) {
		v, err := ticket.Submitted{TicketID: id, SubmittedAt: now}.Marshal()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		recs = append(recs, &kgo.Record{Topic: kafka.TopicSubmitted, Key: []byte(id.String()), Value: v})
	}
	if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		t.Fatalf("ProduceSync: %v", err)
	}
	commit := kadm.Offsets{}
	commit.Add(kadm.Offset{Topic: kafka.TopicSubmitted, Partition: recs[0].Partition, At: recs[0].Offset + 1, LeaderEpoch: -1})
	if resp, err := h.adm.CommitOffsets(ctx, workersGroup, commit); err != nil || resp.Error() != nil {
		t.Fatalf("CommitOffsets: %v, %v", err, resp.Error())
	}
	return h
}

func slicesOf(m map[string]uuid.UUID) []uuid.UUID {
	var ids []uuid.UUID
	for _, id := range m {
		ids = append(ids, id)
	}
	return ids
}

// state is everything a replay must not change.
type state struct {
	Tickets          []string
	Classifications  int
	Outbox           int
	WorkerOffsets    map[int32]int64
	OtherTopicCounts map[string]int64
}

func (h *history) state(t *testing.T) state {
	t.Helper()
	ctx := t.Context()
	s := state{WorkerOffsets: make(map[int32]int64), OtherTopicCounts: make(map[string]int64)}
	rows, err := h.pool.Query(ctx, `SELECT row_to_json(t)::text FROM tickets t ORDER BY id`)
	if err != nil {
		t.Fatalf("read tickets: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatalf("scan ticket: %v", err)
		}
		s.Tickets = append(s.Tickets, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read tickets: %v", err)
	}
	err = h.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM classifications), (SELECT count(*) FROM outbox)`).Scan(&s.Classifications, &s.Outbox)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}
	offsets, err := h.adm.FetchOffsets(ctx, workersGroup)
	if err != nil {
		t.Fatalf("FetchOffsets: %v", err)
	}
	offsets.Each(func(o kadm.OffsetResponse) { s.WorkerOffsets[o.Partition] = o.At })
	for _, topic := range []string{kafka.TopicSubmittedRetry, kafka.TopicSubmittedDLQ, kafka.TopicEvents} {
		ends, err := h.adm.ListEndOffsets(ctx, topic)
		if err != nil {
			t.Fatalf("ListEndOffsets: %v", err)
		}
		ends.Each(func(o kadm.ListedOffset) { s.OtherTopicCounts[topic] += o.Offset })
	}
	return s
}

// shadowRows returns the status of each shadow row of run, by ticket id.
func (h *history) shadowRows(t *testing.T, run string) map[uuid.UUID]string {
	t.Helper()
	rows, err := h.pool.Query(t.Context(),
		`SELECT ticket_id, outcome || ':' || coalesce(status, '') || ':' || coalesce(status_reason, '')
		 FROM shadow_classifications WHERE run_id = $1`, run)
	if err != nil {
		t.Fatalf("read shadow rows: %v", err)
	}
	defer rows.Close()
	got := make(map[uuid.UUID]string)
	for rows.Next() {
		var id uuid.UUID
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatalf("scan shadow row: %v", err)
		}
		got[id] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read shadow rows: %v", err)
	}
	return got
}

func (h *history) run(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	merged := map[string]string{}
	for k, v := range h.env {
		merged[k] = v
	}
	for k, v := range env {
		merged[k] = v
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var out, logs bytes.Buffer
	err := runShadow(ctx, args, func(k string) string { return merged[k] }, &out, &logs)
	if ctx.Err() != nil {
		t.Fatalf("runShadow did not stop by itself:\n%s\n%s", out.String(), logs.String())
	}
	for _, tk := range h.ticks {
		if strings.Contains(logs.String(), tk.Body) || strings.Contains(out.String(), tk.Body) {
			t.Errorf("output leaks the body of ticket %s", tk.ID)
		}
	}
	return out.String(), err
}

func TestShadowReplayLeavesLiveStateUntouched(t *testing.T) {
	h := newHistory(t)
	before := h.state(t)

	out, err := h.run(t, nil, "-run-id", "strict-urgency")
	if err != nil {
		t.Fatalf("runShadow: %v\n%s", err, out)
	}
	if got := h.state(t); !reflect.DeepEqual(got, before) {
		t.Errorf("live state changed by the replay:\n got %+v\nwant %+v", got, before)
	}

	want := map[uuid.UUID]string{
		h.ids["invoice"]: "ok:needs_human:low_confidence",
		h.ids["urgent"]:  "ok:classified:",
		h.ids["budget"]:  "ok:classified:",
		h.ids["pending"]: "ok:needs_human:low_confidence",
	}
	if got := h.shadowRows(t, "strict-urgency"); !reflect.DeepEqual(got, want) {
		t.Errorf("shadow rows = %v, want %v", got, want)
	}
	for _, line := range []string{
		"Stopped: complete after 5 records (group triage-shadow-strict-urgency)\n",
		"Shadow run strict-urgency\n",
		"Skipped, bad id or missing ticket      1\n",
		"Still pending live                     1\n",
		"Tickets that would change status: 2\n",
		h.ids["invoice"].String() + "  classified -> needs_human (low_confidence)\n",
		h.ids["budget"].String() + "  needs_human (budget_exhausted) -> classified\n",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("report misses %q:\n%s", line, out)
		}
	}

	// The finished run resumes to nothing.
	out, err = h.run(t, nil, "-run-id", "strict-urgency")
	if err != nil || !strings.Contains(out, "Stopped: complete after 0 records") {
		t.Errorf("resumed run: %v\n%s", err, out)
	}
}

func TestShadowReplayDryRunAndLimits(t *testing.T) {
	h := newHistory(t)
	before := h.state(t)

	// A dry run classifies with the fake whatever CLASSIFIER says, writes
	// nothing and commits nothing.
	out, err := h.run(t, map[string]string{"CLASSIFIER": "jev", "OPENROUTER_API_KEY": "sk-or-unused"},
		"-run-id", "dry", "-dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "(dry run: fake classifier, nothing written)") ||
		!strings.Contains(out, "Tickets that would change status: 2\n") {
		t.Errorf("dry-run report:\n%s", out)
	}
	if got := h.shadowRows(t, "dry"); len(got) != 0 {
		t.Errorf("dry run wrote %v", got)
	}
	offsets, err := h.adm.FetchOffsets(t.Context(), kafka.ReplayGroup("dry"))
	if err != nil {
		t.Fatalf("FetchOffsets: %v", err)
	}
	if n := len(offsets[kafka.TopicSubmitted]); n != 0 {
		t.Errorf("dry run committed %v", offsets)
	}

	// A spent budget stops a real run before any call.
	out, err = h.run(t, map[string]string{"DAILY_BUDGET_USD": "0"}, "-run-id", "broke")
	// Records whose ticket is gone need no budget, so the count may not be 0.
	if err != nil || !strings.Contains(out, "Stopped: daily budget exhausted after") {
		t.Errorf("run without budget: %v\n%s", err, out)
	}
	if got := h.shadowRows(t, "broke"); len(got) != 0 {
		t.Errorf("run without budget wrote %v", got)
	}

	// -max-tickets bounds one invocation; the next one resumes.
	out, err = h.run(t, nil, "-run-id", "small", "-max-tickets", "2")
	if err != nil || !strings.Contains(out, "Stopped: max records reached after 2 records") {
		t.Errorf("limited run: %v\n%s", err, out)
	}
	out, err = h.run(t, nil, "-run-id", "small")
	if err != nil || !strings.Contains(out, "Stopped: complete after 3 records") {
		t.Errorf("resumed run: %v\n%s", err, out)
	}
	if got := h.shadowRows(t, "small"); len(got) != 4 {
		t.Errorf("limited and resumed runs wrote %d rows, want 4: %v", len(got), got)
	}

	if got := h.state(t); !reflect.DeepEqual(got, before) {
		t.Errorf("live state changed:\n got %+v\nwant %+v", got, before)
	}
}
