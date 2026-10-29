package kafka_test

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

const testRetryGroup = "triage-workers-retry-test"

// fastRetry is a retry policy short enough for tests.
var fastRetry = kafka.RetryPolicy{MaxAttempts: 2, BackoffBase: 100 * time.Millisecond, BackoffMax: time.Second}

// startRetry builds a retry consumer in testRetryGroup and starts Run, like
// start.
func startRetry(t *testing.T, addrs []string, proc kafka.Processor, cfg kafka.ConsumerConfig) *running {
	t.Helper()
	return startWith(t, addrs, proc, cfg, kafka.RetryConsumerOpts(testRetryGroup))
}

// readTopic reads topic from the start until it has n records, failing the
// test after 10s, and returns them.
func readTopic(t *testing.T, addrs []string, topic string, n int) []*kgo.Record {
	t.Helper()
	cl, err := kafka.NewClient(addrs, "kafka-test-reader",
		kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.FetchMaxWait(100*time.Millisecond))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var recs []*kgo.Record
	for len(recs) < n {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			t.Fatalf("read %d records of %s, want %d", len(recs), topic, n)
		}
		recs = append(recs, fetches.Records()...)
	}
	return recs
}

// endOffset returns the number of records in topic, over all partitions.
func endOffset(t *testing.T, addrs []string, topic string) int64 {
	t.Helper()
	ends, err := newAdminFor(t, addrs).ListEndOffsets(t.Context(), topic)
	if err != nil {
		t.Fatalf("ListEndOffsets: %v", err)
	}
	var n int64
	ends.Each(func(o kadm.ListedOffset) { n += o.Offset })
	return n
}

// headerMap returns the headers of r by key; a test fails on a repeated key.
func headerMap(t *testing.T, r *kgo.Record) map[string]string {
	t.Helper()
	m := make(map[string]string, len(r.Headers))
	for _, h := range r.Headers {
		if _, ok := m[h.Key]; ok {
			t.Errorf("header %s repeated", h.Key)
		}
		m[h.Key] = string(h.Value)
	}
	return m
}

// checkHeaders checks that r has exactly the header keys of want, with the
// values of the non-empty ones; an empty want value only needs to be set.
func checkHeaders(t *testing.T, r *kgo.Record, want map[string]string) map[string]string {
	t.Helper()
	got := headerMap(t, r)
	for k, v := range want {
		g, ok := got[k]
		switch {
		case !ok:
			t.Errorf("%s record has no %s header", r.Topic, k)
		case v != "" && g != v:
			t.Errorf("%s record header %s = %q, want %q", r.Topic, k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s record has unexpected header %s", r.Topic, k)
		}
	}
	return got
}

// A transient failure goes to the retry topic and the original is
// committed; the retry consumer processes it once due, and it succeeds.
func TestRetryThenSuccess(t *testing.T) {
	addrs := newCluster(t, 1)
	adm := newAdminFor(t, addrs)
	ids := newIDs(t, 2)
	src := produce(t, addrs, ids...)

	var mu sync.Mutex
	calls := map[string]int{}
	proc := &fakeProcessor{fn: func(_ context.Context, id string) (triage.Outcome, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[id]++
		if id == ids[1] && calls[id] == 1 {
			return triage.Outcome{Kind: triage.Retryable, Err: classifier.ErrTransient}, nil
		}
		return triage.Outcome{Kind: triage.Done, Status: ticket.StatusClassified}, nil
	}}
	cfg := kafka.ConsumerConfig{Retry: fastRetry}
	start(t, addrs, proc, cfg)
	startRetry(t, addrs, proc, cfg)

	waitFor(t, "both tickets to be processed twice in all", func() bool { return len(proc.seen()) == 3 })
	waitFor(t, "tickets.submitted to be committed", func() bool { return committed(t, adm) == 2 })

	retried := readTopic(t, addrs, kafka.TopicSubmittedRetry, 1)[0]
	if string(retried.Key) != ids[1] || string(retried.Value) != string(src[1].Value) {
		t.Errorf("retry record = %s/%s, want the record of %s", retried.Key, retried.Value, ids[1])
	}
	checkHeaders(t, retried, map[string]string{
		kafka.HeaderAttempt:           "1",
		kafka.HeaderNotBefore:         "",
		kafka.HeaderOriginalTopic:     kafka.TopicSubmitted,
		kafka.HeaderOriginalPartition: "0",
		kafka.HeaderOriginalOffset:    "1",
	})
	if n := endOffset(t, addrs, kafka.TopicSubmittedRetry); n != 1 {
		t.Errorf("retry topic has %d records, want 1", n)
	}
	if n := endOffset(t, addrs, kafka.TopicSubmittedDLQ); n != 0 {
		t.Errorf("DLQ has %d records, want 0", n)
	}
	if got := proc.gaveUp(); len(got) != 0 {
		t.Errorf("gave up on %v, want none", got)
	}
}

// A ticket still retryable after its last retry attempt is failed open and
// goes to the DLQ with its metadata.
func TestRetryExhaustedGoesToDLQ(t *testing.T) {
	addrs := newCluster(t, 1)
	ids := newIDs(t, 1)
	produce(t, addrs, ids...)

	proc := &fakeProcessor{fn: func(context.Context, string) (triage.Outcome, error) {
		return triage.Outcome{Kind: triage.Retryable, RetryAfter: time.Millisecond, Err: classifier.ErrTransient}, nil
	}}
	cfg := kafka.ConsumerConfig{Retry: fastRetry}
	start(t, addrs, proc, cfg)
	startRetry(t, addrs, proc, cfg)

	before := time.Now()
	dead := readTopic(t, addrs, kafka.TopicSubmittedDLQ, 1)[0]
	got := checkHeaders(t, dead, map[string]string{
		kafka.HeaderAttempt:           strconv.Itoa(fastRetry.MaxAttempts),
		kafka.HeaderErrorClass:        string(kafka.ClassTransient),
		kafka.HeaderFailedAt:          "",
		kafka.HeaderOriginalTopic:     kafka.TopicSubmitted,
		kafka.HeaderOriginalPartition: "0",
		kafka.HeaderOriginalOffset:    "0",
	})
	if at, err := time.Parse(time.RFC3339Nano, got[kafka.HeaderFailedAt]); err != nil || at.Before(before.Add(-time.Minute)) {
		t.Errorf("failed_at = %q (%v), want a recent RFC 3339 time", got[kafka.HeaderFailedAt], err)
	}
	if string(dead.Key) != ids[0] {
		t.Errorf("DLQ record key = %s, want %s", dead.Key, ids[0])
	}
	waitFor(t, "the ticket to be given up on", func() bool { return len(proc.gaveUp()) == 1 })
	if got := proc.gaveUp(); !slices.Equal(got, ids) {
		t.Errorf("gave up on %v, want %v", got, ids)
	}
	// The first attempt and one per trip through the retry topic.
	if n := len(proc.seen()); n != 1+fastRetry.MaxAttempts {
		t.Errorf("processed %d times, want %d", n, 1+fastRetry.MaxAttempts)
	}
	if n := endOffset(t, addrs, kafka.TopicSubmittedRetry); n != int64(fastRetry.MaxAttempts) {
		t.Errorf("retry topic has %d records, want %d", n, fastRetry.MaxAttempts)
	}
}

// Poison records go to the DLQ at once, without a retry; the Processor has
// already failed their ticket open, so the consumer does not give up on it.
func TestPoisonGoesToDLQ(t *testing.T) {
	addrs := newCluster(t, 1)
	adm := newAdminFor(t, addrs)
	ids := newIDs(t, 1)
	produce(t, addrs, ids...)
	cl, err := kafka.NewClient(addrs, "kafka-test-producer")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	produceRecords(t, cl, &kgo.Record{Topic: kafka.TopicSubmitted, Key: []byte("bad"), Value: []byte("not json")})

	proc := &fakeProcessor{fn: func(context.Context, string) (triage.Outcome, error) {
		return triage.Outcome{Kind: triage.Poison, Status: ticket.StatusNeedsHuman,
			Reason: ticket.ReasonClassifierUnavailable, Err: classifier.ErrPermanent}, nil
	}}
	start(t, addrs, proc, kafka.ConsumerConfig{Retry: fastRetry})

	dead := readTopic(t, addrs, kafka.TopicSubmittedDLQ, 2)
	waitFor(t, "tickets.submitted to be committed", func() bool { return committed(t, adm) == 2 })
	byKey := map[string]*kgo.Record{}
	for _, r := range dead {
		byKey[string(r.Key)] = r
	}
	tests := []struct {
		key, class, offset string
	}{
		{ids[0], string(kafka.ClassPermanent), "0"},
		{"bad", string(kafka.ClassBadPayload), "1"},
	}
	for _, tt := range tests {
		r, ok := byKey[tt.key]
		if !ok {
			t.Errorf("no DLQ record for %s", tt.key)
			continue
		}
		checkHeaders(t, r, map[string]string{
			kafka.HeaderAttempt:           "0",
			kafka.HeaderErrorClass:        tt.class,
			kafka.HeaderFailedAt:          "",
			kafka.HeaderOriginalTopic:     kafka.TopicSubmitted,
			kafka.HeaderOriginalPartition: "0",
			kafka.HeaderOriginalOffset:    tt.offset,
		})
	}
	if n := endOffset(t, addrs, kafka.TopicSubmittedRetry); n != 0 {
		t.Errorf("retry topic has %d records, want 0", n)
	}
	if got := proc.gaveUp(); len(got) != 0 {
		t.Errorf("gave up on %v, want none", got)
	}
}

// A retry record is not processed before its not_before, and a record behind
// it in its partition waits for it, even when it is due.
func TestRetryWaitsForNotBefore(t *testing.T) {
	addrs := newCluster(t, 1)
	adm := newAdminFor(t, addrs)
	ids := newIDs(t, 2)
	src := produce(t, addrs, ids...)

	notBefore := time.Now().Add(time.Second)
	due := time.Now().Add(-time.Minute)
	retries := make([]*kgo.Record, len(src))
	for i, r := range src {
		at := notBefore
		if i == 1 {
			at = due
		}
		retries[i] = &kgo.Record{Topic: kafka.TopicSubmittedRetry, Key: r.Key, Value: r.Value, Headers: []kgo.RecordHeader{
			{Key: kafka.HeaderAttempt, Value: []byte("1")},
			{Key: kafka.HeaderNotBefore, Value: []byte(at.UTC().Format(time.RFC3339Nano))},
		}}
	}
	cl, err := kafka.NewClient(addrs, "kafka-test-producer")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	produceRecords(t, cl, retries...)

	var mu sync.Mutex
	processedAt := map[string]time.Time{}
	proc := &fakeProcessor{fn: func(_ context.Context, id string) (triage.Outcome, error) {
		mu.Lock()
		defer mu.Unlock()
		processedAt[id] = time.Now()
		return triage.Outcome{Kind: triage.Done}, nil
	}}
	startRetry(t, addrs, proc, kafka.ConsumerConfig{Retry: fastRetry})

	waitFor(t, "both retries to be processed", func() bool { return len(proc.seen()) == 2 })
	mu.Lock()
	defer mu.Unlock()
	for _, id := range ids {
		if at := processedAt[id]; at.Before(notBefore) {
			t.Errorf("%s processed %v before the head's not_before", id, notBefore.Sub(at))
		}
	}
	waitFor(t, "the retry topic to be committed", func() bool {
		offs, err := adm.FetchOffsets(t.Context(), testRetryGroup)
		if err != nil {
			return false
		}
		o, ok := offs.Lookup(kafka.TopicSubmittedRetry, 0)
		return ok && o.At == 2
	})
}
