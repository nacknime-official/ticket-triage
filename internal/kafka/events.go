package kafka

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// EventSink receives the status events read from tickets.events;
// *notify.Hub implements it. Publish must not block.
type EventSink interface {
	Publish(e ticket.StatusChanged)
}

// EventsOpts returns the client options of a tickets.events reader: no
// consumer group, so every API instance reads every event (fan-out, README
// §3.4), starting at the end of each partition. Events from before the start
// are not needed: an SSE stream reads the current state from Postgres after
// subscribing.
func EventsOpts() []kgo.Opt {
	return []kgo.Opt{
		kgo.ConsumeTopics(TopicEvents),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
	}
}

// EventsReader passes the ticket.StatusChanged events of tickets.events to an
// EventSink. It commits nothing: without a group there is no offset to keep,
// and a restarted instance has no subscribers waiting for old events.
type EventsReader struct {
	cl   *kgo.Client
	sink EventSink
	log  *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewEventsReader returns a reader that polls with cl and publishes to sink.
// cl must be a group-less client built with EventsOpts.
func NewEventsReader(cl *kgo.Client, sink EventSink, log *slog.Logger) (*EventsReader, error) {
	if g, _ := cl.OptValue(kgo.ConsumerGroup).(string); g != "" {
		return nil, errors.New("kafka: events reader: client must not have a consumer group")
	}
	return &EventsReader{cl: cl, sink: sink, log: log}, nil
}

// Start runs the reader in a new goroutine until ctx is done or Stop is
// called. It panics if the reader is already started.
func (r *EventsReader) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done != nil {
		panic("kafka: EventsReader started twice")
	}
	ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		r.Run(ctx)
	}()
}

// Stop cancels a started reader and waits for it to return, or for ctx to be
// done. Its signature fits lifecycle.Step. Stopping a reader that was never
// started is a no-op. The caller closes the client afterwards.
func (r *EventsReader) Stop(ctx context.Context) error {
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run reads until ctx is done or the client is closed. Fetch errors and
// records that are not a valid status event are logged and skipped: a lost
// event only delays a stream until its client reconnects and rereads the
// state.
func (r *EventsReader) Run(ctx context.Context) {
	for {
		fetches := r.cl.PollFetches(ctx)
		if ctx.Err() != nil || fetches.IsClientClosed() {
			return
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			r.log.WarnContext(ctx, "kafka fetch error",
				slog.String("topic", topic), slog.Int("partition", int(partition)), errorAttr(err))
		})
		fetches.EachRecord(func(rec *kgo.Record) { r.handle(ctx, rec) })
	}
}

// handle publishes the event of one record. A record whose type header names
// another event is skipped; the payload is never logged.
func (r *EventsReader) handle(ctx context.Context, rec *kgo.Record) {
	attrs := []slog.Attr{
		slog.String("topic", rec.Topic),
		slog.Int("partition", int(rec.Partition)),
		slog.Int64("offset", rec.Offset),
	}
	for _, h := range rec.Headers {
		if h.Key == "type" && string(h.Value) != ticket.StatusChangedType {
			r.log.LogAttrs(ctx, slog.LevelDebug, "skipped event of another type",
				append(attrs, slog.String("type", string(h.Value)))...)
			return
		}
	}
	e, err := ticket.UnmarshalStatusChanged(rec.Value)
	if err != nil {
		r.log.LogAttrs(ctx, slog.LevelWarn, "skipped invalid status event", append(attrs, errorAttr(err))...)
		return
	}
	r.sink.Publish(e)
}
