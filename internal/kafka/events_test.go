package kafka_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/notify"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

var _ kafka.EventSink = (*notify.Hub)(nil)

// eventSink records the events it receives.
type eventSink struct {
	mu     sync.Mutex
	events []ticket.StatusChanged
}

func (s *eventSink) Publish(e ticket.StatusChanged) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

// count returns how many events of the ticket arrived.
func (s *eventSink) count(id uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.TicketID == id {
			n++
		}
	}
	return n
}

func (s *eventSink) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// statusRecord is a tickets.events record as the outbox relay writes it.
func statusRecord(t *testing.T, id uuid.UUID, status ticket.Status) *kgo.Record {
	t.Helper()
	value, err := ticket.StatusChanged{TicketID: id, Status: status, At: time.Now().UTC()}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{
		Topic: kafka.TopicEvents, Key: []byte(id.String()), Value: value,
		Headers: []kgo.RecordHeader{{Key: "type", Value: []byte(ticket.StatusChangedType)}},
	}
}

func TestEventsReader(t *testing.T) {
	// One partition, so once a probe event arrives the reader is positioned
	// for every later record.
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, kafka.TopicEvents))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	addrs := cluster.ListenAddrs()
	producer, err := kafka.NewClient(addrs, "kafka-test-producer")
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	before := uuid.NewV7()
	produceRecords(t, producer, statusRecord(t, before, ticket.StatusClassified))

	cl, err := kafka.NewClient(addrs, "kafka-test-events",
		append(kafka.EventsOpts(), kgo.FetchMaxWait(100*time.Millisecond))...)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	sink := &eventSink{}
	r, err := kafka.NewEventsReader(cl, sink, slog.New(slog.NewTextHandler(t.Output(), nil)))
	if err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())

	// The reader starts at the end, at a time the test cannot observe:
	// produce probes until one arrives.
	probe := uuid.NewV7()
	waitFor(t, "a probe event", func() bool {
		produceRecords(t, producer, statusRecord(t, probe, ticket.StatusClassified))
		time.Sleep(50 * time.Millisecond)
		return sink.count(probe) > 0
	})

	after, noHeader := uuid.NewV7(), uuid.NewV7()
	otherType := statusRecord(t, uuid.NewV7(), ticket.StatusClassified)
	otherType.Headers = []kgo.RecordHeader{{Key: "type", Value: []byte("ticket.something_else")}}
	bare := statusRecord(t, noHeader, ticket.StatusNeedsHuman)
	bare.Headers = nil
	produceRecords(t, producer,
		&kgo.Record{Topic: kafka.TopicEvents, Key: []byte("x"), Value: []byte(`{"ticket_id":`)},
		&kgo.Record{Topic: kafka.TopicEvents, Key: []byte("x"), Value: []byte(`{"ticket_id":"` +
			uuid.NewV7().String() + `","status":"exploded","message":"","at":"2026-10-29T00:00:00Z"}`)},
		otherType,
		statusRecord(t, after, ticket.StatusClassified),
		bare,
	)
	waitFor(t, "the events after the start", func() bool { return sink.count(after) == 1 && sink.count(noHeader) == 1 })

	if n := sink.count(before); n != 0 {
		t.Errorf("got %d events produced before the start, want 0: the reader must start at the end", n)
	}
	if got, want := sink.len(), sink.count(probe)+2; got != want {
		t.Errorf("sink got %d events, want %d: invalid or foreign records were passed on", got, want)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestEventsReaderStopsOnClientClose(t *testing.T) {
	addrs := newCluster(t, 1)
	cl, err := kafka.NewClient(addrs, "kafka-test-events", kafka.EventsOpts()...)
	if err != nil {
		t.Fatal(err)
	}
	r, err := kafka.NewEventsReader(cl, &eventSink{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(context.WithoutCancel(t.Context()))
	}()
	cl.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the client closed")
	}
}

func TestNewEventsReaderRejectsGroupClient(t *testing.T) {
	addrs := newCluster(t, 1)
	cl, err := kafka.NewClient(addrs, "kafka-test-events", kafka.ConsumerOpts(testGroup)...)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if _, err := kafka.NewEventsReader(cl, &eventSink{}, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("NewEventsReader accepted a group client")
	}
	// Stop on a reader that never started is a no-op.
	var r kafka.EventsReader
	if err := r.Stop(t.Context()); err != nil {
		t.Errorf("Stop of an unstarted reader: %v", err)
	}
}
