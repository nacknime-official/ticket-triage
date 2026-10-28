package outbox

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Defaults for the zero fields of Config.
const (
	DefaultPollInterval    = 500 * time.Millisecond
	DefaultBatchSize       = 100
	DefaultPublishTimeout  = 10 * time.Second
	DefaultRetention       = 7 * 24 * time.Hour
	DefaultCleanupInterval = time.Hour
)

// Message is one outbox row: a record to publish to Topic, keyed by Key (the
// ticket id). Payload carries only ids and status fields, never ticket text,
// so a Message is safe to log by ID, Topic and Key.
type Message struct {
	ID        int64
	Topic     string
	Key       []byte
	Payload   []byte
	Headers   map[string]string
	CreatedAt time.Time
}

// PublishFunc publishes msgs and returns one error per message, in order:
// nil when the broker acknowledged it.
type PublishFunc func(ctx context.Context, msgs []Message) []error

// Store holds the outbox rows.
type Store interface {
	// PublishPending claims up to limit unpublished messages, oldest first,
	// skipping those another relay has claimed, and passes them to publish
	// while it holds the claim. It marks each message published whose error
	// is nil and leaves the others unpublished, recording the error, all in
	// one transaction. It returns the number of messages claimed.
	PublishPending(ctx context.Context, limit int, publish PublishFunc) (claimed int, err error)
	// DeletePublished deletes the messages published before t and returns
	// how many it deleted.
	DeletePublished(ctx context.Context, before time.Time) (int64, error)
}

// Publisher sends messages to the broker.
type Publisher interface {
	// Publish sends msgs and waits for the broker's acknowledgement. It
	// returns one error per message, in order: nil when it was acknowledged.
	Publish(ctx context.Context, msgs []Message) []error
}

// Config tunes the Relay. Zero fields take the defaults.
type Config struct {
	// PollInterval is the wait between polls once the outbox is drained.
	PollInterval time.Duration
	// BatchSize is the maximum number of messages claimed per transaction.
	BatchSize int
	// PublishTimeout bounds one batch's publish, so a broker outage does not
	// hold the claimed rows locked indefinitely.
	PublishTimeout time.Duration
	// Retention is how long published messages are kept before cleanup.
	Retention time.Duration
	// CleanupInterval is the wait between cleanups of published messages.
	CleanupInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.PublishTimeout <= 0 {
		c.PublishTimeout = DefaultPublishTimeout
	}
	if c.Retention <= 0 {
		c.Retention = DefaultRetention
	}
	if c.CleanupInterval <= 0 {
		c.CleanupInterval = DefaultCleanupInterval
	}
	return c
}

// Relay moves outbox messages to the broker: it polls the Store, publishes
// what it claims, and periodically deletes old published messages.
//
// Delivery is at least once. Several relays may run against one Store: a
// claim excludes the other relays, so in normal operation each message is
// published once, but a relay that dies between the broker's acknowledgement
// and its commit leaves the message to be published again.
type Relay struct {
	store Store
	pub   Publisher
	cfg   Config
	log   *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewRelay returns a Relay that publishes the messages of store with pub.
func NewRelay(store Store, pub Publisher, cfg Config, log *slog.Logger) *Relay {
	return &Relay{store: store, pub: pub, cfg: cfg.withDefaults(), log: log}
}

// Start runs the Relay in a new goroutine until ctx is done or Stop is
// called. It panics if the Relay is already started.
func (r *Relay) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done != nil {
		panic("outbox: Relay started twice")
	}
	ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		r.Run(ctx)
	}()
}

// Stop cancels a started Relay and waits for its current batch to finish,
// or for ctx to be done, whichever comes first. Its signature fits
// lifecycle.Step. Stopping a Relay that was never started is a no-op.
func (r *Relay) Stop(ctx context.Context) error {
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

// Run publishes and cleans up until ctx is done. Store errors are logged and
// retried at the next poll, so Run returns only on cancellation.
func (r *Relay) Run(ctx context.Context) {
	poll := time.NewTicker(r.cfg.PollInterval)
	defer poll.Stop()
	cleanup := time.NewTicker(r.cfg.CleanupInterval)
	defer cleanup.Stop()

	for {
		r.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-cleanup.C:
			r.cleanup(ctx)
		}
	}
}

// drain publishes batches until a batch comes back short or has a failure,
// so a backlog does not wait one poll interval per batch, while a broker
// outage is retried once per poll instead of in a hot loop. A batch that has
// started runs to completion even if ctx is canceled meanwhile (its publish
// is bounded by PublishTimeout), so stopping does not abandon messages the
// broker already acknowledged.
func (r *Relay) drain(ctx context.Context) {
	for ctx.Err() == nil {
		failed := false
		claimed, err := r.store.PublishPending(context.WithoutCancel(ctx), r.cfg.BatchSize,
			func(ctx context.Context, msgs []Message) []error {
				errs := r.publish(ctx, msgs)
				failed = slices.ContainsFunc(errs, func(err error) bool { return err != nil })
				return errs
			})
		if err != nil {
			r.log.ErrorContext(ctx, "outbox: publish pending", slog.Any("err", err))
			return
		}
		if claimed < r.cfg.BatchSize || failed {
			return
		}
	}
}

// publish is the PublishFunc the Relay hands to the Store.
func (r *Relay) publish(ctx context.Context, msgs []Message) []error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.PublishTimeout)
	defer cancel()
	errs := r.pub.Publish(ctx, msgs)
	if len(errs) != len(msgs) {
		// A broken Publisher must not mark anything published.
		err := errors.New("outbox: publisher returned a wrong number of results")
		errs = make([]error, len(msgs))
		for i := range errs {
			errs[i] = err
		}
	}
	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			if failed == 1 {
				r.log.WarnContext(ctx, "outbox: publish failed",
					slog.Int64("outbox_id", msgs[i].ID), slog.String("topic", msgs[i].Topic), slog.Any("err", err))
			}
		}
	}
	if failed > 1 {
		r.log.WarnContext(ctx, "outbox: messages left unpublished", slog.Int("failed", failed), slog.Int("batch", len(msgs)))
	}
	return errs
}

func (r *Relay) cleanup(ctx context.Context) {
	n, err := r.store.DeletePublished(ctx, time.Now().Add(-r.cfg.Retention))
	if err != nil {
		if ctx.Err() == nil {
			r.log.ErrorContext(ctx, "outbox: delete published", slog.Any("err", err))
		}
		return
	}
	if n > 0 {
		r.log.InfoContext(ctx, "outbox: deleted published messages", slog.Int64("deleted", n))
	}
}
