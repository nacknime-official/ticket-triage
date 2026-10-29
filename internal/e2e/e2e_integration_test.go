//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/httpapi"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/lifecycle"
	"github.com/nacknime-official/ticket-triage/internal/logging"
	"github.com/nacknime-official/ticket-triage/internal/outbox"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
	"github.com/nacknime-official/ticket-triage/migrations"
)

// Images match the services in compose.yaml.
const (
	postgresImage = "pgvector/pgvector:0.8.7-pg16"
	kafkaImage    = "apache/kafka:4.3.2"
)

const (
	// classifyWithin is how long a submitted ticket may stay pending.
	classifyWithin = 10 * time.Second
	// shutdownTimeout bounds the drain of the whole stack.
	shutdownTimeout = 30 * time.Second
)

// TestEndToEnd runs the API and the worker in-process against one Postgres
// and one Kafka container: submit → outbox → relay → Kafka → consumer →
// triage with the fake classifier → final status read back over HTTP. The
// subtests share the stack and run in order; the last one shuts it down.
func TestEndToEnd(t *testing.T) {
	dsn, brokers := startPostgres(t), startKafka(t)
	s := startStack(t, dsn, brokers)

	t.Run("submitted ticket is classified", func(t *testing.T) {
		const subject, body = "Refund", "I was charged twice for my subscription, please refund."
		want := fakeResult(t, subject, body)
		if want.Category == classifier.CategoryOther {
			t.Fatalf("fake classifies the ticket as %s; pick a text it routes to a real category", want.Category)
		}

		id, token := s.submit(t, subject, body)
		got := s.waitFinal(t, id, token)
		if got.Status != ticket.StatusClassified {
			t.Fatalf("status = %s (reason %v), want %s", got.Status, deref(got.StatusReason), ticket.StatusClassified)
		}
		if deref(got.Category) != want.Category {
			t.Errorf("category = %q, want %q from the fake", deref(got.Category), want.Category)
		}
		if wantPriority := ticket.PriorityFromUrgency(want.Urgency).String(); deref(got.Priority) != wantPriority {
			t.Errorf("priority = %q, want %q from the fake", deref(got.Priority), wantPriority)
		}
		if outs := s.proc.wait(t, id, 1); outs[0].Kind != triage.Done {
			t.Errorf("outcome = %s, want %s", outs[0].Kind, triage.Done)
		}
	})

	t.Run("category other goes to needs_human", func(t *testing.T) {
		const subject, body = "Hello", "Just saying hi to the team."
		if want := fakeResult(t, subject, body); want.Category != classifier.CategoryOther {
			t.Fatalf("fake classifies the ticket as %s, want %s", want.Category, classifier.CategoryOther)
		}

		id, token := s.submit(t, subject, body)
		got := s.waitFinal(t, id, token)
		if got.Status != ticket.StatusNeedsHuman || deref(got.StatusReason) != ticket.ReasonCategoryOther {
			t.Errorf("status = %s (reason %q), want %s (reason %q)",
				got.Status, deref(got.StatusReason), ticket.StatusNeedsHuman, ticket.ReasonCategoryOther)
		}
	})

	t.Run("duplicate delivery changes nothing", func(t *testing.T) {
		id, token := s.submit(t, "Invoice", "Please send the invoice for my last payment.")
		s.waitFinal(t, id, token)
		s.proc.wait(t, id, 1)
		before := s.snapshot(t, id)
		if before.audit != 1 || before.okAudit != 1 {
			t.Fatalf("after the first delivery: %d audit rows, %d ok, want 1 and 1", before.audit, before.okAudit)
		}
		calls := s.fake.Calls()

		// Publish the outbox row again, as a relay that died between the
		// broker's acknowledgement and its commit does.
		s.redeliver(t, id)
		outs := s.proc.wait(t, id, 2)
		if outs[1].Kind != triage.Skipped {
			t.Errorf("second outcome = %s, want %s", outs[1].Kind, triage.Skipped)
		}
		if after := s.snapshot(t, id); after != before {
			t.Errorf("redelivery changed the ticket:\nbefore %+v\nafter  %+v", before, after)
		}
		if n := s.fake.Calls(); n != calls {
			t.Errorf("classifier called %d more times on redelivery, want 0", n-calls)
		}
	})

	t.Run("graceful shutdown leaves no goroutines", func(t *testing.T) {
		if err := s.shutdown(); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
		assertNoLeaks(t)
	})
}

// startPostgres starts a Postgres container, applies every migration and
// returns its connection string.
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
	return dsn
}

// Kafka container layout: the PLAINTEXT listener serves the broker's own
// container, PLAINTEXT_HOST the test through a mapped port, as in compose.yaml.
const (
	kafkaHostPort = "29092/tcp"
	// kafkaStarter is copied in once the mapped port is known; the container
	// waits for it, because the advertised listener must name that port.
	kafkaStarter = "/tmp/e2e-start.sh"
)

// startKafka starts a single-node KRaft Kafka from the compose image, creates
// the topics of the service and returns the bootstrap address.
func startKafka(t *testing.T) []string {
	t.Helper()
	ctx := t.Context()
	ctr, err := testcontainers.Run(ctx, kafkaImage,
		testcontainers.WithExposedPorts(kafkaHostPort),
		// The image applies no defaults once any KAFKA_* variable is set, so
		// this is the full broker configuration of compose.yaml.
		testcontainers.WithEnv(map[string]string{
			"CLUSTER_ID":                                             "5hl3cknzQ22P3WsmQPy2cg",
			"KAFKA_NODE_ID":                                          "1",
			"KAFKA_PROCESS_ROLES":                                    "broker,controller",
			"KAFKA_CONTROLLER_QUORUM_VOTERS":                         "1@localhost:9093",
			"KAFKA_CONTROLLER_LISTENER_NAMES":                        "CONTROLLER",
			"KAFKA_LISTENERS":                                        "PLAINTEXT://:9092,CONTROLLER://:9093,PLAINTEXT_HOST://:29092",
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":                   "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,PLAINTEXT_HOST:PLAINTEXT",
			"KAFKA_INTER_BROKER_LISTENER_NAME":                       "PLAINTEXT",
			"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR":                 "1",
			"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR":         "1",
			"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR":                    "1",
			"KAFKA_SHARE_COORDINATOR_STATE_TOPIC_REPLICATION_FACTOR": "1",
			"KAFKA_SHARE_COORDINATOR_STATE_TOPIC_MIN_ISR":            "1",
			"KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS":                 "0",
			"KAFKA_AUTO_CREATE_TOPICS_ENABLE":                        "false",
		}),
		testcontainers.WithEntrypoint("sh"),
		testcontainers.WithCmd("-c", "while [ ! -f "+kafkaStarter+" ]; do sleep 0.1; done; exec bash "+kafkaStarter),
		testcontainers.WithLifecycleHooks(testcontainers.ContainerLifecycleHooks{
			PostStarts: []testcontainers.ContainerHook{startKafkaBroker},
		}),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start kafka: %v", err)
	}
	addr, err := ctr.PortEndpoint(ctx, kafkaHostPort, "")
	if err != nil {
		t.Fatalf("kafka endpoint: %v", err)
	}
	brokers := []string{addr}

	cl, err := kafka.NewClient(brokers, "ticket-triage-e2e-setup")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	if _, err := kafka.EnsureTopics(ctx, kadm.NewClient(cl), kafka.Topics()...); err != nil {
		t.Fatalf("EnsureTopics: %v", err)
	}
	return brokers
}

// startKafkaBroker writes the starter script with the advertised listeners
// and waits until the broker runs.
func startKafkaBroker(ctx context.Context, c testcontainers.Container) error {
	if err := wait.ForMappedPort(kafkaHostPort).WaitUntilReady(ctx, c); err != nil {
		return fmt.Errorf("wait for mapped port: %w", err)
	}
	hostAddr, err := c.PortEndpoint(ctx, kafkaHostPort, "")
	if err != nil {
		return fmt.Errorf("port endpoint: %w", err)
	}
	inspect, err := c.Inspect(ctx)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	script := fmt.Sprintf("export KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://%s:9092,PLAINTEXT_HOST://%s\n"+
		"exec /etc/kafka/docker/run\n", inspect.Config.Hostname, hostAddr)
	if err := c.CopyToContainer(ctx, []byte(script), kafkaStarter, 0o755); err != nil {
		return fmt.Errorf("copy starter script: %w", err)
	}
	if err := wait.ForLog("Kafka Server started").WaitUntilReady(ctx, c); err != nil {
		return fmt.Errorf("wait for broker: %w", err)
	}
	return nil
}

// stack is the API (HTTP handler and outbox relay) and the worker (classifier
// consumer), each with its own pool and Kafka client as in cmd/api and
// cmd/worker.
type stack struct {
	baseURL string
	client  *http.Client
	brokers []string
	db      *pgxpool.Pool // the worker's pool, also used for assertions
	fake    *classifier.Fake
	proc    *recorder

	shutdownOnce sync.Once
	shutdownErr  error
	stop         func() error
}

// stackEnv is the configuration of the stack beyond the defaults of
// internal/config, which cmd/api and cmd/worker load too.
func stackEnv(dsn string, brokers []string) map[string]string {
	return map[string]string{
		"DATABASE_URL":         dsn,
		"KAFKA_BROKERS":        strings.Join(brokers, ","),
		"KAFKA_CONSUMER_GROUP": "ticket-triage-e2e",
		// A ticket reaches Kafka within a poll; the default 500ms is slow
		// for a test that waits on every ticket.
		"OUTBOX_POLL_INTERVAL": "50ms",
		// Every subtest submits from 127.0.0.1; the default burst of 3
		// would cap the test at three tickets.
		"SUBMIT_BURST": "100",
		"CLASSIFIER":   config.ClassifierFake,
	}
}

// startStack composes the API and the worker as cmd/api and cmd/worker do,
// with the fake classifier, and stops them at test cleanup unless a subtest
// shut them down first.
func startStack(t *testing.T, dsn string, brokers []string) *stack {
	t.Helper()
	ctx := t.Context()
	env := stackEnv(dsn, brokers)
	l := config.NewLoader(func(name string) string { return env[name] })
	httpCfg, pgCfg, kafkaCfg, outboxCfg, limits := l.HTTP(), l.Postgres(), l.Kafka(), l.Outbox(), l.Limits()
	clsCfg, thrCfg, workerCfg := l.Classifier(), l.Thresholds(), l.Worker()
	if err := l.Err(); err != nil {
		t.Fatalf("load config: %v", err)
	}
	log := logging.New(t.Output(), slog.LevelInfo)
	// The stack outlives t.Context, which is canceled before cleanups run;
	// it stops through its shutdown steps.
	runCtx := context.WithoutCancel(ctx)

	// Everything that can fail is built first; until the stack starts, a
	// failure closes what was built.
	started := false
	var undo []func()
	defer func() {
		if !started {
			for _, f := range slices.Backward(undo) {
				f()
			}
		}
	}()

	// API: the handler stack and the relay of cmd/api.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	undo = append(undo, func() { _ = ln.Close() })
	pubClient, err := kafka.NewClient(kafkaCfg.Brokers, "ticket-triage-e2e-api",
		kgo.RecordDeliveryTimeout(outboxCfg.PublishTimeout))
	if err != nil {
		t.Fatalf("api NewClient: %v", err)
	}
	undo = append(undo, pubClient.Close)
	apiPool, err := postgres.NewPool(ctx, pgCfg.URL.Reveal())
	if err != nil {
		t.Fatalf("api NewPool: %v", err)
	}
	undo = append(undo, apiPool.Close)
	svc := ticket.NewService(postgres.NewTickets(apiPool, kafka.TopicSubmitted),
		ticket.Config{MaxPending: limits.PendingCap})
	submitLimiter := httpapi.NewRateLimiter(httpapi.RateLimit{
		PerMinute: limits.SubmitRatePerMin,
		Burst:     limits.SubmitBurst,
	})
	handler := httpapi.NewHandler(svc, apiPool, log,
		httpapi.NewClientIP(httpCfg.TrustedProxies).Middleware,
		httpapi.SecurityHeaders,
		httpapi.ForRoute("POST /tickets", submitLimiter.Middleware),
		httpapi.LimitBody(limits.MaxBodyBytes),
		httpapi.WriteDeadline(httpCfg.WriteTimeout, log),
	)
	srv := httpapi.NewServer(ln.Addr().String(), handler, httpapi.ServerTimeouts{
		ReadHeader: httpCfg.ReadHeaderTimeout,
		Read:       httpCfg.ReadTimeout,
		Idle:       httpCfg.IdleTimeout,
	}, log)
	relay := outbox.NewRelay(postgres.NewOutbox(apiPool), kafka.NewPublisher(pubClient), outbox.Config{
		PollInterval:    outboxCfg.PollInterval,
		BatchSize:       outboxCfg.BatchSize,
		PublishTimeout:  outboxCfg.PublishTimeout,
		Retention:       outboxCfg.Retention,
		CleanupInterval: outboxCfg.CleanupInterval,
	}, log)

	// Worker: the processor of cmd/worker, with the fake classifier, behind
	// the consumer.
	workerPool, err := postgres.NewPool(ctx, pgCfg.URL.Reveal())
	if err != nil {
		t.Fatalf("worker NewPool: %v", err)
	}
	undo = append(undo, workerPool.Close)
	fake := &classifier.Fake{}
	proc := &recorder{next: triage.NewProcessor(postgres.NewTriageStore(workerPool), fake, triage.Config{
		Thresholds: classifier.Thresholds{
			CategoryMinConfidence:  thrCfg.CategoryMinConfidence,
			UrgencyMinConfidence:   thrCfg.UrgencyMinConfidence,
			ComplaintUncertainLow:  thrCfg.ComplaintUncertainLow,
			ComplaintUncertainHigh: thrCfg.ComplaintUncertainHigh,
			RouteOtherToHuman:      thrCfg.RouteOtherToHuman,
		},
		DailyBudgetUSD: clsCfg.DailyBudgetUSD,
		CallTimeout:    clsCfg.Jev.Timeout,
	}, log)}
	consumerClient, err := kafka.NewClient(kafkaCfg.Brokers, "ticket-triage-e2e-worker",
		kafka.ConsumerOpts(kafkaCfg.ConsumerGroup)...)
	if err != nil {
		t.Fatalf("worker NewClient: %v", err)
	}
	undo = append(undo, consumerClient.Close)
	consumer, err := kafka.NewConsumer(consumerClient, proc, kafka.ConsumerConfig{
		Concurrency: workerCfg.Concurrency,
		BatchSize:   workerCfg.BatchSize,
	}, log)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	started = true
	serveErr := make(chan error, 1)
	var serving sync.WaitGroup
	serving.Go(func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	})
	relay.Start(runCtx)
	runErr := make(chan error, 1)
	go func() { runErr <- consumer.Run(runCtx) }()

	httpClient := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	s := &stack{
		baseURL: "http://" + ln.Addr().String(),
		client:  httpClient,
		brokers: kafkaCfg.Brokers,
		db:      workerPool,
		fake:    fake,
		proc:    proc,
	}
	s.stop = func() error {
		httpClient.CloseIdleConnections()
		// The steps of cmd/api, then those of cmd/worker.
		err := lifecycle.Shutdown(runCtx, shutdownTimeout,
			lifecycle.Step{Name: "http server", Stop: srv.Shutdown},
			lifecycle.Step{Name: "outbox relay", Stop: relay.Stop},
			lifecycle.Step{Name: "api kafka client", Stop: func(context.Context) error {
				pubClient.Close()
				return nil
			}},
			lifecycle.Step{Name: "api postgres pool", Stop: func(context.Context) error {
				apiPool.Close()
				return nil
			}},
			lifecycle.Step{Name: "consumer", Stop: consumer.Stop},
			lifecycle.Step{Name: "worker kafka client", Stop: func(ctx context.Context) error {
				defer consumerClient.Close()
				return consumerClient.LeaveGroupContext(ctx)
			}},
			lifecycle.Step{Name: "worker postgres pool", Stop: func(context.Context) error {
				workerPool.Close()
				return nil
			}},
		)
		errs := []error{err}
		// Close stops a server that Shutdown gave up on; on a stopped one it
		// is a no-op.
		_ = srv.Close()
		serving.Wait()
		select {
		case err := <-serveErr:
			errs = append(errs, fmt.Errorf("serve: %w", err))
		default:
		}
		// Run has returned unless the drain was cut off.
		select {
		case err := <-runErr:
			errs = append(errs, err)
		default:
			errs = append(errs, errors.New("consumer still running after shutdown"))
		}
		return errors.Join(errs...)
	}
	t.Cleanup(func() {
		if err := s.shutdown(); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return s
}

// shutdown stops the stack once; later calls return the first result.
func (s *stack) shutdown() error {
	s.shutdownOnce.Do(func() { s.shutdownErr = s.stop() })
	return s.shutdownErr
}

// ticketView is the part of the GET /tickets/{id} response the test checks.
type ticketView struct {
	ID           string         `json:"id"`
	Status       ticket.Status  `json:"status"`
	StatusReason *ticket.Reason `json:"status_reason"`
	Category     *string        `json:"category"`
	Priority     *string        `json:"priority"`
}

// submit posts a ticket and returns its id and access token.
func (s *stack) submit(t *testing.T, subject, body string) (id, token string) {
	t.Helper()
	reqBody, err := json.Marshal(map[string]string{"subject": subject, "body": body})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.baseURL+"/tickets", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	status, data := s.do(t, req)
	if status != http.StatusAccepted {
		t.Fatalf("POST /tickets = %d %s, want %d", status, data, http.StatusAccepted)
	}
	var resp struct {
		ID          string        `json:"id"`
		Status      ticket.Status `json:"status"`
		AccessToken string        `json:"access_token"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("decode POST /tickets response %s: %v", data, err)
	}
	if resp.Status != ticket.StatusPending || resp.ID == "" || resp.AccessToken == "" {
		t.Fatalf("POST /tickets response = %s, want a pending ticket with an id and a token", data)
	}
	return resp.ID, resp.AccessToken
}

// waitFinal polls GET /tickets/{id} with the access token until the ticket
// leaves pending, for at most classifyWithin.
func (s *stack) waitFinal(t *testing.T, id, token string) ticketView {
	t.Helper()
	deadline := time.Now().Add(classifyWithin)
	for {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.baseURL+"/tickets/"+id, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		status, data := s.do(t, req)
		if status != http.StatusOK {
			t.Fatalf("GET /tickets/%s = %d %s, want %d", id, status, data, http.StatusOK)
		}
		var v ticketView
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatalf("decode GET /tickets/%s response %s: %v", id, data, err)
		}
		if v.ID != id {
			t.Fatalf("GET /tickets/%s returned ticket %s", id, v.ID)
		}
		if v.Status != ticket.StatusPending {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("ticket %s still %s after %s", id, v.Status, classifyWithin)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *stack) do(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	}()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", req.Method, req.URL.Path, err)
	}
	return resp.StatusCode, data
}

// redeliver publishes the ticket's tickets.submitted outbox row once more.
func (s *stack) redeliver(t *testing.T, id string) {
	t.Helper()
	ctx := t.Context()
	var msg outbox.Message
	err := s.db.QueryRow(ctx, `SELECT topic, key, payload FROM outbox WHERE topic = $1 AND key = $2`,
		kafka.TopicSubmitted, []byte(id)).Scan(&msg.Topic, &msg.Key, &msg.Payload)
	if err != nil {
		t.Fatalf("read outbox row of ticket %s: %v", id, err)
	}
	cl, err := kafka.NewClient(s.brokers, "ticket-triage-e2e-redeliver")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	if err := kafka.NewPublisher(cl).Publish(ctx, []outbox.Message{msg})[0]; err != nil {
		t.Fatalf("publish ticket %s again: %v", id, err)
	}
}

// ticketState is everything a Process run can change about a ticket.
type ticketState struct {
	row            string // the tickets row as JSON
	audit, okAudit int    // classifications rows, and those with outcome ok
}

func (s *stack) snapshot(t *testing.T, id string) ticketState {
	t.Helper()
	var st ticketState
	err := s.db.QueryRow(t.Context(), `
SELECT (SELECT to_jsonb(t)::text FROM tickets t WHERE id = $1),
       count(*), count(*) FILTER (WHERE outcome = 'ok')
FROM classifications WHERE ticket_id = $1`, id).Scan(&st.row, &st.audit, &st.okAudit)
	if err != nil {
		t.Fatalf("snapshot ticket %s: %v", id, err)
	}
	return st
}

// fakeResult is what the fake classifier answers for the text.
func fakeResult(t *testing.T, subject, body string) classifier.Result {
	t.Helper()
	r, err := (&classifier.Fake{}).Classify(t.Context(), classifier.Input{Subject: subject, Body: body})
	if err != nil {
		t.Fatalf("fake Classify: %v", err)
	}
	return r
}

// recorder records the outcome of every Process call by ticket id.
type recorder struct {
	next kafka.Processor

	mu       sync.Mutex
	outcomes map[string][]triage.Outcome
}

func (r *recorder) Process(ctx context.Context, id string) (triage.Outcome, error) {
	out, err := r.next.Process(ctx, id)
	if err != nil {
		// An error stops the consumer; record it so the wait fails at once.
		out = triage.Outcome{Err: err}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.outcomes == nil {
		r.outcomes = make(map[string][]triage.Outcome)
	}
	r.outcomes[id] = append(r.outcomes[id], out)
	return out, err
}

// wait returns the outcomes of the ticket once there are n, for at most
// classifyWithin. A Process error fails the test.
func (r *recorder) wait(t *testing.T, id string, n int) []triage.Outcome {
	t.Helper()
	deadline := time.Now().Add(classifyWithin)
	for {
		r.mu.Lock()
		outs := slices.Clone(r.outcomes[id])
		r.mu.Unlock()
		for _, o := range outs {
			if o.Kind == 0 {
				t.Fatalf("Process(%s): %v", id, o.Err)
			}
		}
		if len(outs) >= n {
			return outs
		}
		if time.Now().After(deadline) {
			t.Fatalf("ticket %s processed %d times after %s, want %d", id, len(outs), classifyWithin, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stackCode names the code the stack runs. After shutdown no goroutine may
// still be in any of it.
var stackCode = []string{
	"github.com/nacknime-official/ticket-triage/internal/classifier.",
	"github.com/nacknime-official/ticket-triage/internal/httpapi.",
	"github.com/nacknime-official/ticket-triage/internal/kafka.",
	"github.com/nacknime-official/ticket-triage/internal/lifecycle.",
	"github.com/nacknime-official/ticket-triage/internal/outbox.",
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres.",
	"github.com/nacknime-official/ticket-triage/internal/triage.",
	"github.com/twmb/franz-go/",
	"github.com/jackc/",
	"net/http.(*Server).Serve",
	"net/http.(*conn).serve",
}

// assertNoLeaks fails the test if a goroutine of the stack is still around
// shortly after shutdown, blocked forever or not.
func assertNoLeaks(t *testing.T) {
	t.Helper()
	// Goroutines that a stopped component has signaled may take a moment
	// to return.
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := stackGoroutines(t)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines of the stack outlived shutdown:\n\n%s", len(left), strings.Join(left, "\n\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stackGoroutines returns the stacks of the goroutines in stackCode. It reads
// the goroutineleak profile, whose collection first runs the leak-detecting
// GC, so a goroutine blocked forever shows as leaked in its header.
func stackGoroutines(t *testing.T) []string {
	t.Helper()
	var b strings.Builder
	if err := pprof.Lookup("goroutineleak").WriteTo(&b, 2); err != nil {
		t.Fatalf("goroutineleak profile: %v", err)
	}
	var left []string
	for g := range strings.SplitSeq(b.String(), "\n\n") {
		if slices.ContainsFunc(stackCode, func(code string) bool { return strings.Contains(g, code) }) {
			left = append(left, g)
		}
	}
	return left
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
