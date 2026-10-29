package kafka_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// fakeReplay records the replayed ids and runs fn, if set, for each.
type fakeReplay struct {
	fn func(ctx context.Context, id string) error

	mu  sync.Mutex
	ids []string
}

func (p *fakeReplay) Replay(ctx context.Context, id string) error {
	p.mu.Lock()
	p.ids = append(p.ids, id)
	p.mu.Unlock()
	if p.fn == nil {
		return nil
	}
	return p.fn(ctx, id)
}

func (p *fakeReplay) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Sorted(slices.Values(p.ids))
}

// newFullCluster starts a broker with every topic of the service.
func newFullCluster(t *testing.T, partitions int32) []string {
	t.Helper()
	var names []string
	for _, s := range kafka.Topics() {
		names = append(names, s.Name)
	}
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(partitions, names...))
	if err != nil {
		t.Fatalf("start kfake cluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	return cluster.ListenAddrs()
}

func replayAdmin(t *testing.T, addrs []string) *kadm.Client {
	t.Helper()
	cl, err := kafka.NewClient(addrs, "kafka-test-admin")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(cl.Close)
	return kadm.NewClient(cl)
}

// groupOffsets returns the committed offset of each partition of
// tickets.submitted in group.
func groupOffsets(t *testing.T, adm *kadm.Client, group string) map[int32]int64 {
	t.Helper()
	resp, err := adm.FetchOffsets(t.Context(), group)
	if err == nil {
		err = resp.Error()
	}
	if err != nil {
		t.Fatalf("FetchOffsets(%s): %v", group, err)
	}
	got := make(map[int32]int64)
	resp.Each(func(o kadm.OffsetResponse) {
		if o.Topic == kafka.TopicSubmitted {
			got[o.Partition] = o.At
		}
	})
	return got
}

// endOffsets returns the end offset of every partition of topic.
func endOffsets(t *testing.T, adm *kadm.Client, topic string) map[int32]int64 {
	t.Helper()
	listed, err := adm.ListEndOffsets(t.Context(), topic)
	if err == nil {
		err = listed.Error()
	}
	if err != nil {
		t.Fatalf("ListEndOffsets(%s): %v", topic, err)
	}
	got := make(map[int32]int64)
	listed.Each(func(o kadm.ListedOffset) { got[o.Partition] = o.Offset })
	return got
}

// replay runs one Replayer in group to completion and returns its result.
func replay(t *testing.T, addrs []string, group string, proc kafka.ReplayProcessor, cfg kafka.ReplayConfig) (kafka.ReplayResult, error) {
	t.Helper()
	return replayCtx(t.Context(), t, addrs, group, proc, cfg)
}

func replayCtx(ctx context.Context, t *testing.T, addrs []string, group string, proc kafka.ReplayProcessor,
	cfg kafka.ReplayConfig) (kafka.ReplayResult, error) {
	t.Helper()
	opts := append(kafka.ReplayOpts(group, cfg.From), kgo.FetchMaxWait(100*time.Millisecond))
	cl, err := kafka.NewClient(addrs, "kafka-test-replay", opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	r, err := kafka.NewReplayer(cl, proc, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewReplayer: %v", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := r.Run(runCtx)
	if runCtx.Err() != nil && ctx.Err() == nil {
		t.Fatalf("Run did not stop by itself: %+v, %v", res, err)
	}
	leaveCtx, cancelLeave := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelLeave()
	if err := cl.LeaveGroupContext(leaveCtx); err != nil {
		t.Errorf("LeaveGroupContext: %v", err)
	}
	return res, err
}

func TestReplayReadsHistoryInItsOwnGroup(t *testing.T) {
	t.Parallel()
	addrs := newFullCluster(t, 3)
	adm := replayAdmin(t, addrs)
	ids := newIDs(t, 12)
	produce(t, addrs, ids...)

	// The workers' group has committed part of the history.
	mainOffsets := kadm.Offsets{}
	mainOffsets.Add(kadm.Offset{Topic: kafka.TopicSubmitted, Partition: 0, At: 1, LeaderEpoch: -1})
	if resp, err := adm.CommitOffsets(t.Context(), testGroup, mainOffsets); err != nil || resp.Error() != nil {
		t.Fatalf("CommitOffsets(%s): %v, %v", testGroup, err, resp.Error())
	}
	mainBefore := groupOffsets(t, adm, testGroup)
	otherEnds := map[string]map[int32]int64{}
	for _, topic := range []string{kafka.TopicSubmittedRetry, kafka.TopicSubmittedDLQ, kafka.TopicEvents} {
		otherEnds[topic] = endOffsets(t, adm, topic)
	}

	proc := &fakeReplay{}
	group := kafka.ReplayGroup("t1")
	res, err := replay(t, addrs, group, proc, kafka.ReplayConfig{BatchSize: 5, Concurrency: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res != (kafka.ReplayResult{Stop: kafka.ReplayComplete, Records: len(ids)}) {
		t.Errorf("Run = %+v, want complete after %d records", res, len(ids))
	}
	if got, want := proc.seen(), slices.Sorted(slices.Values(ids)); !reflect.DeepEqual(got, want) {
		t.Errorf("replayed %v, want %v", got, want)
	}
	if got, want := groupOffsets(t, adm, group), endOffsets(t, adm, kafka.TopicSubmitted); !reflect.DeepEqual(got, want) {
		t.Errorf("replay group offsets = %v, want the end offsets %v", got, want)
	}
	if got := groupOffsets(t, adm, testGroup); !reflect.DeepEqual(got, mainBefore) {
		t.Errorf("workers' group offsets = %v, want them untouched: %v", got, mainBefore)
	}
	for topic, before := range otherEnds {
		if got := endOffsets(t, adm, topic); !reflect.DeepEqual(got, before) {
			t.Errorf("%s end offsets = %v, want nothing produced: %v", topic, got, before)
		}
	}
}

func TestReplayStopsAtTheEndTakenAtStart(t *testing.T) {
	t.Parallel()
	addrs := newCluster(t, 1)
	ids := newIDs(t, 3)
	produce(t, addrs, ids...)
	late := newIDs(t, 2)
	var once sync.Once
	proc := &fakeReplay{fn: func(ctx context.Context, _ string) error {
		once.Do(func() {
			cl, err := kafka.NewClient(addrs, "kafka-test-late-producer")
			if err != nil {
				t.Errorf("NewClient: %v", err)
				return
			}
			defer cl.Close()
			for _, id := range late {
				v, err := ticket.Submitted{TicketID: uuid.MustParse(id), SubmittedAt: time.Now()}.Marshal()
				if err != nil {
					t.Errorf("marshal: %v", err)
					return
				}
				rec := &kgo.Record{Topic: kafka.TopicSubmitted, Key: []byte(id), Value: v}
				if err := cl.ProduceSync(ctx, rec).FirstErr(); err != nil {
					t.Errorf("ProduceSync: %v", err)
				}
			}
		})
		return nil
	}}
	res, err := replay(t, addrs, kafka.ReplayGroup("t2"), proc, kafka.ReplayConfig{BatchSize: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Records != len(ids) {
		t.Errorf("Run handled %d records, want %d", res.Records, len(ids))
	}
	if got, want := proc.seen(), slices.Sorted(slices.Values(ids)); !reflect.DeepEqual(got, want) {
		t.Errorf("replayed %v, want only the history %v", got, want)
	}
}

func TestReplayMaxRecordsThenResume(t *testing.T) {
	t.Parallel()
	addrs := newCluster(t, 1)
	adm := replayAdmin(t, addrs)
	ids := newIDs(t, 10)
	produce(t, addrs, ids...)
	group := kafka.ReplayGroup("t3")

	first := &fakeReplay{}
	res, err := replay(t, addrs, group, first, kafka.ReplayConfig{BatchSize: 3, MaxRecords: 4})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if res != (kafka.ReplayResult{Stop: kafka.ReplayLimited, Records: 4}) {
		t.Errorf("first Run = %+v, want limited after 4", res)
	}
	if got, want := first.seen(), slices.Sorted(slices.Values(ids[:4])); !reflect.DeepEqual(got, want) {
		t.Errorf("first run replayed %v, want %v", got, want)
	}
	if got := groupOffsets(t, adm, group); got[0] != 4 {
		t.Errorf("committed offset after the first run = %v, want 4", got)
	}

	second := &fakeReplay{}
	res, err = replay(t, addrs, group, second, kafka.ReplayConfig{BatchSize: 3})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res != (kafka.ReplayResult{Stop: kafka.ReplayComplete, Records: 6}) {
		t.Errorf("second Run = %+v, want complete after 6", res)
	}
	if got, want := second.seen(), slices.Sorted(slices.Values(ids[4:])); !reflect.DeepEqual(got, want) {
		t.Errorf("resumed run replayed %v, want %v", got, want)
	}

	// A third run of the finished group has nothing to do.
	res, err = replay(t, addrs, group, &fakeReplay{}, kafka.ReplayConfig{})
	if err != nil || res != (kafka.ReplayResult{Stop: kafka.ReplayComplete}) {
		t.Errorf("Run of a finished group = %+v, %v; want complete after 0", res, err)
	}
}

func TestReplayNoCommit(t *testing.T) {
	t.Parallel()
	addrs := newCluster(t, 2)
	adm := replayAdmin(t, addrs)
	ids := newIDs(t, 5)
	produce(t, addrs, ids...)
	group := kafka.ReplayGroup("dry")
	for range 2 {
		proc := &fakeReplay{}
		res, err := replay(t, addrs, group, proc, kafka.ReplayConfig{NoCommit: true})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Records != len(ids) || len(proc.seen()) != len(ids) {
			t.Errorf("dry Run = %+v, replayed %d; want all %d", res, len(proc.seen()), len(ids))
		}
	}
	if got := groupOffsets(t, adm, group); len(got) != 0 {
		t.Errorf("dry runs committed %v, want nothing", got)
	}
}

func TestReplayFromTimestamp(t *testing.T) {
	t.Parallel()
	addrs := newCluster(t, 2)
	cl, err := kafka.NewClient(addrs, "kafka-test-producer")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	cut := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	record := func(id string, at time.Time) *kgo.Record {
		v, err := ticket.Submitted{TicketID: uuid.MustParse(id), SubmittedAt: at}.Marshal()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return &kgo.Record{Topic: kafka.TopicSubmitted, Key: []byte(id), Value: v, Timestamp: at}
	}
	old, recent := newIDs(t, 4), newIDs(t, 3)
	var recs []*kgo.Record
	for _, id := range old {
		recs = append(recs, record(id, cut.Add(-time.Hour)))
	}
	for _, id := range recent {
		recs = append(recs, record(id, cut.Add(time.Minute)))
	}
	produceRecords(t, cl, recs...)

	proc := &fakeReplay{}
	res, err := replay(t, addrs, kafka.ReplayGroup("t4"), proc, kafka.ReplayConfig{From: cut})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res != (kafka.ReplayResult{Stop: kafka.ReplayComplete, Records: len(recent)}) {
		t.Errorf("Run = %+v, want complete after %d", res, len(recent))
	}
	if got, want := proc.seen(), slices.Sorted(slices.Values(recent)); !reflect.DeepEqual(got, want) {
		t.Errorf("replayed %v, want the records after the cut %v", got, want)
	}

	// A start after every record leaves nothing to replay.
	res, err = replay(t, addrs, kafka.ReplayGroup("t5"), &fakeReplay{}, kafka.ReplayConfig{From: time.Now().Add(time.Hour)})
	if err != nil || res != (kafka.ReplayResult{Stop: kafka.ReplayComplete}) {
		t.Errorf("Run from the future = %+v, %v; want complete after 0", res, err)
	}
}

func TestReplayFailureIsNotCommitted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"auth", fmt.Errorf("%w: 401", classifier.ErrAuth)},
		{"storage", errors.New("db down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			addrs := newCluster(t, 1)
			adm := replayAdmin(t, addrs)
			ids := newIDs(t, 4)
			produce(t, addrs, ids...)
			proc := &fakeReplay{fn: func(_ context.Context, id string) error {
				if id == ids[2] {
					return tt.err
				}
				return nil
			}}
			group := kafka.ReplayGroup("fail-" + tt.name)
			_, err := replay(t, addrs, group, proc, kafka.ReplayConfig{BatchSize: 10, Concurrency: 1})
			if !errors.Is(err, tt.err) {
				t.Errorf("Run error = %v, want %v", err, tt.err)
			}
			if got := groupOffsets(t, adm, group); len(got) != 0 {
				t.Errorf("failed batch committed %v, want nothing", got)
			}
		})
	}
}

func TestReplaySkipsPoisonRecords(t *testing.T) {
	t.Parallel()
	addrs := newCluster(t, 1)
	cl, err := kafka.NewClient(addrs, "kafka-test-producer")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	produceRecords(t, cl, &kgo.Record{Topic: kafka.TopicSubmitted, Value: []byte(`{"bad":`)})
	ids := newIDs(t, 1)
	produce(t, addrs, ids...)
	proc := &fakeReplay{}
	res, err := replay(t, addrs, kafka.ReplayGroup("t6"), proc, kafka.ReplayConfig{})
	if err != nil || res.Records != 2 {
		t.Fatalf("Run = %+v, %v; want 2 records", res, err)
	}
	if got := proc.seen(); !reflect.DeepEqual(got, ids) {
		t.Errorf("replayed %v, want %v", got, ids)
	}
}

func TestReplayCancelled(t *testing.T) {
	t.Parallel()
	addrs := newCluster(t, 1)
	adm := replayAdmin(t, addrs)
	produce(t, addrs, newIDs(t, 3)...)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	proc := &fakeReplay{fn: func(ctx context.Context, _ string) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}}
	group := kafka.ReplayGroup("t7")
	res, err := replayCtx(ctx, t, addrs, group, proc, kafka.ReplayConfig{})
	if err != nil || res != (kafka.ReplayResult{Stop: kafka.ReplayCancelled}) {
		t.Errorf("cancelled Run = %+v, %v; want cancelled after 0", res, err)
	}
	if got := groupOffsets(t, adm, group); len(got) != 0 {
		t.Errorf("cancelled batch committed %v, want nothing", got)
	}
}

func TestNewReplayerRejects(t *testing.T) {
	addrs := newCluster(t, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name string
		opts []kgo.Opt
		cfg  kafka.ReplayConfig
	}{
		{"no group", nil, kafka.ReplayConfig{}},
		{"auto commit", []kgo.Opt{kgo.ConsumerGroup("g"), kgo.ConsumeTopics(kafka.TopicSubmitted)}, kafka.ReplayConfig{}},
		{"negative max", kafka.ReplayOpts("g", time.Time{}), kafka.ReplayConfig{MaxRecords: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl, err := kafka.NewClient(addrs, "kafka-test", tt.opts...)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			defer cl.Close()
			if _, err := kafka.NewReplayer(cl, &fakeReplay{}, tt.cfg, log); err == nil {
				t.Error("NewReplayer succeeded, want an error")
			}
		})
	}
	if got := kafka.ReplayGroup("x"); got != "triage-shadow-x" {
		t.Errorf("ReplayGroup(x) = %q", got)
	}
}
