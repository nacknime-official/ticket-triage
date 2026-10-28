package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

// Processor processes the ticket of one record; *triage.Processor satisfies
// it. A non-nil error means the record must not be committed.
type Processor interface {
	Process(ctx context.Context, ticketID string) (triage.Outcome, error)
}

// ErrDrainTimeout is the cause of the cancellation of in-flight work when
// Stop gives up waiting for it.
var ErrDrainTimeout = errors.New("kafka: drain timed out")

// Defaults of ConsumerConfig.
const (
	DefaultConcurrency = 8
	DefaultBatchSize   = 64
)

// ConsumerConfig configures a Consumer. Zero fields take the defaults.
type ConsumerConfig struct {
	// Concurrency bounds the records processed at once.
	Concurrency int
	// BatchSize bounds the records of one poll, and so of one commit.
	BatchSize int
}

func (c ConsumerConfig) withDefaults() ConsumerConfig {
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	return c
}

// ConsumerOpts returns the client options of a classifier consumer in group:
// it reads tickets.submitted from the start for a new group, commits only by
// hand, and holds rebalances between a poll and its commit, so a batch is
// committed by the member that processed it.
func ConsumerOpts(group string) []kgo.Opt {
	return []kgo.Opt{
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(TopicSubmitted),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	}
}

// Consumer is the classifier consumer loop of README §4.2 and §5: poll a
// batch, process its records in a bounded pool, and commit the batch only
// once every record is processed. Call Run once, and Stop to drain it.
type Consumer struct {
	cl   *kgo.Client
	proc Processor
	cfg  ConsumerConfig
	log  *slog.Logger

	stopOnce, abortOnce sync.Once
	stop                chan struct{} // closed by Stop: stop polling
	abort               chan struct{} // closed by Stop at its deadline: cancel in-flight work
	done                chan struct{} // closed when Run returns
}

// NewConsumer returns a Consumer that polls with cl and processes each
// record with proc. cl must be a group client built with ConsumerOpts.
func NewConsumer(cl *kgo.Client, proc Processor, cfg ConsumerConfig, log *slog.Logger) (*Consumer, error) {
	if g, _ := cl.OptValue(kgo.ConsumerGroup).(string); g == "" {
		return nil, errors.New("kafka: consumer: client has no consumer group")
	}
	for _, opt := range []any{kgo.DisableAutoCommit, kgo.BlockRebalanceOnPoll} {
		if on, _ := cl.OptValue(opt).(bool); !on {
			return nil, errors.New("kafka: consumer: client must be built with ConsumerOpts")
		}
	}
	return &Consumer{
		cl: cl, proc: proc, cfg: cfg.withDefaults(), log: log,
		stop:  make(chan struct{}),
		abort: make(chan struct{}),
		done:  make(chan struct{}),
	}, nil
}

// Run consumes until ctx is done, Stop is called, or a record fails.
//
// Once ctx is done or Stop is called, Run stops polling, lets the batch in
// flight finish and commits it, and returns nil. In-flight work is not
// cancelled by ctx, only by Stop at its deadline.
//
// A record whose Process returned an error stops consumption without
// committing its batch, so the batch is redelivered after a restart. The
// error wraps classifier.ErrAuth for a classifier configuration failure, and
// ErrDrainTimeout when Stop cut the batch off. Run leaves the group to the
// caller: LeaveGroupContext, then Close the client.
func (c *Consumer) Run(ctx context.Context) error {
	defer close(c.done)

	pollCtx, stopPolling := context.WithCancel(ctx)
	work, cancelWork := context.WithCancelCause(context.WithoutCancel(ctx))
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancelWork(nil)
	defer stopPolling()
	wg.Go(func() {
		select {
		case <-c.stop:
			stopPolling()
		case <-pollCtx.Done():
		}
		select {
		case <-c.abort:
			cancelWork(ErrDrainTimeout)
		case <-work.Done():
		}
	})

	for pollCtx.Err() == nil {
		fetches := c.cl.PollRecords(pollCtx, c.cfg.BatchSize)
		if pollCtx.Err() != nil || fetches.IsClientClosed() {
			// Records polled with the stop are left for redelivery.
			c.cl.AllowRebalance()
			return nil
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			c.log.WarnContext(ctx, "kafka fetch error",
				slog.String("topic", topic), slog.Int("partition", int(partition)), errorAttr(err))
		})
		err := c.batch(work, fetches.Records())
		c.cl.AllowRebalance()
		if err != nil {
			return err
		}
	}
	return nil
}

// Stop stops polling and waits for Run to commit the batch in flight and
// return. When ctx is done first, Stop cancels the in-flight work, so Run
// returns without committing it, and returns the cause of ctx. Stop may be
// called more than once and from any goroutine.
func (c *Consumer) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() { close(c.stop) })
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		c.abortOnce.Do(func() { close(c.abort) })
		return fmt.Errorf("kafka: consumer drain: %w", context.Cause(ctx))
	}
}

// batch processes recs in the bounded pool and commits them once all are
// processed. A failed commit is only logged: every record is processed, the
// next commit of the same partitions covers it, and a restart or rebalance
// before that redelivers the batch, which the idempotent Processor absorbs.
func (c *Consumer) batch(ctx context.Context, recs []*kgo.Record) error {
	if len(recs) == 0 {
		return nil
	}
	if err := runPool(ctx, c.cfg.Concurrency, recs, c.handle); err != nil {
		return fmt.Errorf("kafka: batch of %d records not committed: %w", len(recs), err)
	}
	start := time.Now()
	if err := c.cl.CommitRecords(ctx, recs...); err != nil {
		c.log.ErrorContext(ctx, "kafka commit failed",
			slog.Int("records", len(recs)), errorAttr(err))
		return nil
	}
	c.log.DebugContext(ctx, "kafka batch committed",
		slog.Int("records", len(recs)), slog.Int64("commit_ms", time.Since(start).Milliseconds()))
	return nil
}

// handle processes one record. It returns an error only when the record must
// not be committed; every outcome of the Processor is committed.
func (c *Consumer) handle(ctx context.Context, r *kgo.Record) error {
	attrs := []slog.Attr{
		slog.String("topic", r.Topic),
		slog.Int("partition", int(r.Partition)),
		slog.Int64("offset", r.Offset),
	}
	msg, err := ticket.UnmarshalSubmitted(r.Value)
	if err != nil {
		// T17: poison records go to the DLQ here.
		c.log.LogAttrs(ctx, slog.LevelWarn, "poison record committed",
			append(attrs, slog.String("outcome", triage.Poison.String()), errorAttr(err))...)
		return nil
	}
	id := msg.TicketID.String()
	attrs = append(attrs, slog.String("ticket_id", id))

	out, err := c.proc.Process(ctx, id)
	if err != nil {
		if errors.Is(err, classifier.ErrAuth) {
			c.log.LogAttrs(ctx, slog.LevelError, "classifier auth failed, stopping consumption",
				append(attrs, errorAttr(err))...)
		}
		return fmt.Errorf("process ticket %s: %w", id, err)
	}

	attrs = append(attrs, slog.String("outcome", out.Kind.String()))
	if out.Status != "" {
		attrs = append(attrs, slog.String("status", string(out.Status)))
	}
	if out.Reason != "" {
		attrs = append(attrs, slog.String("status_reason", string(out.Reason)))
	}
	switch out.Kind {
	case triage.Done, triage.Skipped:
		c.log.LogAttrs(ctx, slog.LevelInfo, "ticket processed", attrs...)
	case triage.Retryable:
		// T17: the record goes to the retry topic here, due after
		// out.RetryAfter. Until then the ticket stays pending.
		c.log.LogAttrs(ctx, slog.LevelWarn, "retryable ticket committed, left pending", append(attrs,
			slog.Duration("retry_after", out.RetryAfter), errorAttr(out.Err))...)
	case triage.Poison:
		// T17: poison records go to the DLQ here.
		c.log.LogAttrs(ctx, slog.LevelWarn, "poison record committed",
			append(attrs, errorAttr(out.Err))...)
	default:
		return fmt.Errorf("process ticket %s: unknown outcome %s", id, out.Kind)
	}
	return nil
}

func errorAttr(err error) slog.Attr {
	if err == nil {
		return slog.String("error", "")
	}
	return slog.String("error", err.Error())
}

// runPool calls fn for every item, at most n at once. After the first error
// it starts no more items and cancels the ctx of those running. It returns
// once every started call has returned: nil when every item succeeded,
// otherwise the first error, or the cause of ctx when ctx was done first.
func runPool[T any](ctx context.Context, n int, items []T, fn func(context.Context, T) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	sem := make(chan struct{}, n)
	var (
		wg     sync.WaitGroup
		failed atomic.Bool
	)
	for _, item := range items {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			failed.Store(true)
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := fn(ctx, item); err != nil {
				failed.Store(true)
				cancel(err)
			}
		})
	}
	wg.Wait()
	if failed.Load() {
		return context.Cause(ctx)
	}
	return nil
}
