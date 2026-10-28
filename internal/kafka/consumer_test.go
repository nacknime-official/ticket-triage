package kafka_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

const testGroup = "triage-workers-test"

// fakeProcessor runs fn for every Process call and records the ids.
type fakeProcessor struct {
	fn func(ctx context.Context, id string) (triage.Outcome, error)

	mu  sync.Mutex
	ids []string
}

func (p *fakeProcessor) Process(ctx context.Context, id string) (triage.Outcome, error) {
	p.mu.Lock()
	p.ids = append(p.ids, id)
	p.mu.Unlock()
	if p.fn == nil {
		return triage.Outcome{Kind: triage.Done}, nil
	}
	return p.fn(ctx, id)
}

// seen returns the ids processed so far, sorted.
func (p *fakeProcessor) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Sorted(slices.Values(p.ids))
}

func newCluster(t *testing.T, partitions int32) []string {
	t.Helper()
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(partitions, kafka.TopicSubmitted))
	if err != nil {
		t.Fatalf("start kfake cluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	return cluster.ListenAddrs()
}

func newIDs(t *testing.T, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = uuid.NewV7().String()
	}
	return ids
}

// produce writes one tickets.submitted record per id, keyed by the id, and
// returns the records in order.
func produce(t *testing.T, addrs []string, ids ...string) []*kgo.Record {
	t.Helper()
	cl, err := kafka.NewClient(addrs, "kafka-test-producer")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	recs := make([]*kgo.Record, len(ids))
	for i, id := range ids {
		value, err := ticket.Submitted{TicketID: uuid.MustParse(id), SubmittedAt: time.Now()}.Marshal()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		recs[i] = &kgo.Record{Topic: kafka.TopicSubmitted, Key: []byte(id), Value: value}
	}
	produceRecords(t, cl, recs...)
	return recs
}

func produceRecords(t *testing.T, cl *kgo.Client, recs ...*kgo.Record) {
	t.Helper()
	if err := cl.ProduceSync(t.Context(), recs...).FirstErr(); err != nil {
		t.Fatalf("ProduceSync: %v", err)
	}
}

// running is a Consumer whose Run executes in the background.
type running struct {
	c      *kafka.Consumer
	cl     *kgo.Client
	cancel context.CancelFunc
	errc   chan error
}

// start builds a consumer in testGroup and starts Run. Cleanup stops it,
// leaves the group and closes the client.
func start(t *testing.T, addrs []string, proc kafka.Processor, cfg kafka.ConsumerConfig) *running {
	t.Helper()
	opts := append(kafka.ConsumerOpts(testGroup), kgo.FetchMaxWait(100*time.Millisecond))
	cl, err := kafka.NewClient(addrs, "kafka-test-consumer", opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c, err := kafka.NewConsumer(cl, proc, cfg, slog.New(slog.NewTextHandler(t.Output(), nil)))
	if err != nil {
		cl.Close()
		t.Fatalf("NewConsumer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{c: c, cl: cl, cancel: cancel, errc: make(chan error, 1)}
	go func() { r.errc <- c.Run(ctx) }()
	t.Cleanup(func() {
		r.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.c.Stop(ctx)
		_ = r.cl.LeaveGroupContext(ctx)
		r.cl.Close()
	})
	return r
}

// wait returns the error of Run, failing the test if Run does not return.
func (r *running) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.errc:
		r.errc <- err // later waits see it too
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func newAdminFor(t *testing.T, addrs []string) *kadm.Client {
	t.Helper()
	cl, err := kafka.NewClient(addrs, "kafka-test-admin")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(cl.Close)
	return kadm.NewClient(cl)
}

// committed returns the number of tickets.submitted records testGroup has
// committed: the sum of its committed offsets, as every partition starts at 0.
func committed(t *testing.T, adm *kadm.Client) int64 {
	t.Helper()
	offs, err := adm.FetchOffsets(t.Context(), testGroup)
	if errors.Is(err, kerr.GroupIDNotFound) {
		return 0
	}
	if err != nil {
		t.Fatalf("FetchOffsets: %v", err)
	}
	var n int64
	offs.Each(func(o kadm.OffsetResponse) {
		if o.Topic == kafka.TopicSubmitted && o.Err == nil && o.At > 0 {
			n += o.At
		}
	})
	return n
}

// waitFor polls cond until it holds, failing the test after 10s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNewConsumerRejectsClient(t *testing.T) {
	addrs := newCluster(t, 1)
	tests := []struct {
		name string
		opts []kgo.Opt
	}{
		{"no group", nil},
		{"auto commit", []kgo.Opt{kgo.ConsumerGroup(testGroup), kgo.ConsumeTopics(kafka.TopicSubmitted), kgo.BlockRebalanceOnPoll()}},
		{"rebalance on poll", []kgo.Opt{kgo.ConsumerGroup(testGroup), kgo.ConsumeTopics(kafka.TopicSubmitted), kgo.DisableAutoCommit()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl, err := kafka.NewClient(addrs, "kafka-test", tt.opts...)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			defer cl.Close()
			if _, err := kafka.NewConsumer(cl, &fakeProcessor{}, kafka.ConsumerConfig{}, slog.New(slog.DiscardHandler)); err == nil {
				t.Error("NewConsumer succeeded, want an error")
			}
		})
	}
}

// The batch is committed only once every record in it is processed.
func TestConsumerCommitsAfterBatch(t *testing.T) {
	addrs := newCluster(t, 1)
	adm := newAdminFor(t, addrs)
	ids := newIDs(t, 6)
	produce(t, addrs, ids...) // one partition, one poll: one batch

	slow := ids[0] // offset 0, so no commit can cover the batch before it ends
	release := make(chan struct{})
	var mu sync.Mutex
	finished := 0
	proc := &fakeProcessor{fn: func(ctx context.Context, id string) (triage.Outcome, error) {
		if id == slow {
			<-release
		}
		mu.Lock()
		finished++
		mu.Unlock()
		return triage.Outcome{Kind: triage.Done}, nil
	}}
	r := start(t, addrs, proc, kafka.ConsumerConfig{Concurrency: len(ids), BatchSize: 64})

	waitFor(t, "every record but the slow one to finish", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return finished == len(ids)-1
	})
	if got := committed(t, adm); got != 0 {
		t.Errorf("committed %d records while one of the batch was in flight, want 0", got)
	}

	close(release)
	waitFor(t, "the batch to be committed", func() bool { return committed(t, adm) == int64(len(ids)) })
	if got, want := proc.seen(), slices.Sorted(slices.Values(ids)); !slices.Equal(got, want) {
		t.Errorf("processed %v, want %v", got, want)
	}

	r.cancel()
	if err := r.wait(t); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

// Every outcome of the Processor is committed, and so is a payload that does
// not decode or has no id; consumption goes on across partitions.
func TestConsumerCommitsEveryOutcome(t *testing.T) {
	addrs := newCluster(t, 3)
	adm := newAdminFor(t, addrs)
	ids := newIDs(t, 4)
	outcomes := map[string]triage.Outcome{
		ids[0]: {Kind: triage.Done, Status: ticket.StatusClassified},
		ids[1]: {Kind: triage.Skipped},
		ids[2]: {Kind: triage.Retryable, RetryAfter: time.Second, Err: classifier.ErrTransient},
		ids[3]: {Kind: triage.Poison, Status: ticket.StatusNeedsHuman, Reason: ticket.ReasonClassifierUnavailable,
			Err: classifier.ErrPermanent},
	}
	proc := &fakeProcessor{fn: func(_ context.Context, id string) (triage.Outcome, error) {
		return outcomes[id], nil
	}}
	produce(t, addrs, ids...)
	cl, err := kafka.NewClient(addrs, "kafka-test-producer")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	produceRecords(t, cl,
		&kgo.Record{Topic: kafka.TopicSubmitted, Key: []byte("bad"), Value: []byte("not json")},
		&kgo.Record{Topic: kafka.TopicSubmitted, Key: []byte("no-id"), Value: []byte(`{"submitted_at":"2026-10-28T12:00:00Z"}`)})

	r := start(t, addrs, proc, kafka.ConsumerConfig{Concurrency: 2})
	waitFor(t, "every record to be committed", func() bool { return committed(t, adm) == int64(len(ids)+2) })
	if got, want := proc.seen(), slices.Sorted(slices.Values(ids)); !slices.Equal(got, want) {
		t.Errorf("processed %v, want %v (bad payloads are never processed)", got, want)
	}
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

// An ErrAuth stops consumption without committing the batch.
func TestConsumerStopsOnErrAuth(t *testing.T) {
	addrs := newCluster(t, 1)
	adm := newAdminFor(t, addrs)
	ids := newIDs(t, 3)
	produce(t, addrs, ids...)
	proc := &fakeProcessor{fn: func(_ context.Context, id string) (triage.Outcome, error) {
		if id == ids[1] {
			return triage.Outcome{}, fmt.Errorf("triage: classify ticket %s: %w", id, classifier.ErrAuth)
		}
		return triage.Outcome{Kind: triage.Done}, nil
	}}
	r := start(t, addrs, proc, kafka.ConsumerConfig{Concurrency: 1})

	err := r.wait(t)
	if !errors.Is(err, classifier.ErrAuth) {
		t.Fatalf("Run = %v, want an error wrapping classifier.ErrAuth", err)
	}
	if got := committed(t, adm); got != 0 {
		t.Errorf("committed %d records after ErrAuth, want 0", got)
	}
	if !slices.Contains(proc.seen(), ids[1]) {
		t.Errorf("processed %v, want %s among them", proc.seen(), ids[1])
	}
	// Stop after Run returned on its own is a no-op.
	if err := r.c.Stop(t.Context()); err != nil {
		t.Errorf("Stop = %v, want nil", err)
	}
}

// Cancelling Run stops polling but lets the batch in flight finish, with a
// live ctx, and commits it.
func TestConsumerDrainsOnCancel(t *testing.T) {
	addrs := newCluster(t, 1)
	adm := newAdminFor(t, addrs)
	ids := newIDs(t, 3)
	produce(t, addrs, ids...)

	release := make(chan struct{})
	var (
		mu      sync.Mutex
		started int
		ctxErrs []error
	)
	proc := &fakeProcessor{fn: func(ctx context.Context, id string) (triage.Outcome, error) {
		mu.Lock()
		started++
		mu.Unlock()
		<-release
		mu.Lock()
		ctxErrs = append(ctxErrs, ctx.Err())
		mu.Unlock()
		return triage.Outcome{Kind: triage.Done}, nil
	}}
	r := start(t, addrs, proc, kafka.ConsumerConfig{Concurrency: len(ids)})
	waitFor(t, "the batch to be in flight", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return started == len(ids)
	})

	r.cancel()
	stopc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stopc <- r.c.Stop(ctx)
	}()
	// Records produced after the cancel are left for the next consumer.
	late := newIDs(t, 1)
	produce(t, addrs, late...)
	close(release)

	if err := r.wait(t); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
	if err := <-stopc; err != nil {
		t.Errorf("Stop = %v, want nil", err)
	}
	if got := committed(t, adm); got != int64(len(ids)) {
		t.Errorf("committed %d records, want the %d of the drained batch", got, len(ids))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, err := range ctxErrs {
		if err != nil {
			t.Errorf("in-flight ctx was done during the drain: %v", err)
		}
	}
	if got, want := proc.seen(), slices.Sorted(slices.Values(ids)); !slices.Equal(got, want) {
		t.Errorf("processed %v, want only the drained batch %v (not %s, produced after the cancel)", got, want, late[0])
	}
}

// A batch cut off by Stop's deadline is not committed, and a restarted
// consumer in the group gets exactly the uncommitted records.
func TestConsumerRestartRedeliversUncommitted(t *testing.T) {
	addrs := newCluster(t, 1)
	adm := newAdminFor(t, addrs)
	first, second := newIDs(t, 3), newIDs(t, 3)

	var mu sync.Mutex
	blocked := 0
	proc := &fakeProcessor{fn: func(ctx context.Context, id string) (triage.Outcome, error) {
		if slices.Contains(second, id) {
			mu.Lock()
			blocked++
			mu.Unlock()
			<-ctx.Done()
			return triage.Outcome{}, fmt.Errorf("triage: classify ticket %s: %w", id, context.Cause(ctx))
		}
		return triage.Outcome{Kind: triage.Done}, nil
	}}
	r := start(t, addrs, proc, kafka.ConsumerConfig{Concurrency: 3})
	produce(t, addrs, first...)
	waitFor(t, "the first batch to be committed", func() bool { return committed(t, adm) == int64(len(first)) })
	produce(t, addrs, second...)
	waitFor(t, "the second batch to be in flight", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return blocked == len(second)
	})

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.c.Stop(expired); !errors.Is(err, context.Canceled) {
		t.Errorf("Stop = %v, want context.Canceled", err)
	}
	if err := r.wait(t); !errors.Is(err, kafka.ErrDrainTimeout) {
		t.Errorf("Run = %v, want an error wrapping ErrDrainTimeout", err)
	}
	if got := committed(t, adm); got != int64(len(first)) {
		t.Fatalf("committed %d records, want %d", got, len(first))
	}
	if err := r.cl.LeaveGroupContext(t.Context()); err != nil {
		t.Fatalf("LeaveGroupContext: %v", err)
	}
	r.cl.Close()

	restarted := &fakeProcessor{}
	r2 := start(t, addrs, restarted, kafka.ConsumerConfig{})
	waitFor(t, "the second batch to be committed", func() bool {
		return committed(t, adm) == int64(len(first)+len(second))
	})
	if got, want := restarted.seen(), slices.Sorted(slices.Values(second)); !slices.Equal(got, want) {
		t.Errorf("restarted consumer processed %v, want only the uncommitted %v", got, want)
	}
	r2.cancel()
	if err := r2.wait(t); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}
