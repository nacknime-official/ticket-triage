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
	// GiveUp moves a still-pending ticket to needs_human before its record
	// goes to the DLQ without a final outcome.
	GiveUp(ctx context.Context, ticketID string) (triage.Outcome, error)
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
	// Retry configures the retry topic and when a record goes to the DLQ.
	Retry RetryPolicy
}

func (c ConsumerConfig) withDefaults() ConsumerConfig {
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	c.Retry = c.Retry.withDefaults()
	return c
}

// ConsumerOpts returns the client options of a classifier consumer in group:
// it reads tickets.submitted from the start for a new group, commits only by
// hand, and holds rebalances between a poll and its commit, so a batch is
// committed by the member that processed it.
func ConsumerOpts(group string) []kgo.Opt {
	return consumerOpts(group, TopicSubmitted)
}

// RetryConsumerOpts returns the client options of a retry consumer in group:
// ConsumerOpts for tickets.submitted.retry. Give it its own group, so
// waiting retries never hold up tickets.submitted.
func RetryConsumerOpts(group string) []kgo.Opt {
	return consumerOpts(group, TopicSubmittedRetry)
}

func consumerOpts(group, topic string) []kgo.Opt {
	return []kgo.Opt{
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	}
}

// Consumer is the classifier consumer loop of README §4.2 and §5: poll a
// batch, process its records in a bounded pool, and commit the batch only
// once every record is processed. Call Run once, and Stop to drain it.
//
// The same loop serves tickets.submitted and tickets.submitted.retry. A
// Retryable ticket is produced to the retry topic, due after a backoff; once
// its attempts run out, the ticket is failed open to needs_human and the
// record goes to the DLQ, as does a Poison record at once. Both produces
// finish before the batch is committed, so a crash in between duplicates a
// record but never loses one. A record that is not due yet pauses its
// partition until it is, and is fetched again from there.
type Consumer struct {
	cl   *kgo.Client
	proc Processor
	cfg  ConsumerConfig
	log  *slog.Logger

	// paused holds the partitions paused for a record that is not due yet,
	// with the time it is due. Only Run uses it.
	paused map[topicPartition]time.Time

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
		paused: make(map[topicPartition]time.Time),
		stop:   make(chan struct{}),
		abort:  make(chan struct{}),
		done:   make(chan struct{}),
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
		c.resumeDue(time.Now())
		fetches := c.poll(pollCtx)
		if pollCtx.Err() != nil || fetches.IsClientClosed() {
			// Records polled with the stop are left for redelivery.
			c.cl.AllowRebalance()
			return nil
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			if topic == "" && errors.Is(err, context.DeadlineExceeded) {
				return // the wait for a paused partition ended
			}
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

// poll polls a batch, waiting no longer than until the first paused
// partition is due.
func (c *Consumer) poll(ctx context.Context) kgo.Fetches {
	var next time.Time
	for _, due := range c.paused {
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	if !next.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, next)
		defer cancel()
	}
	return c.cl.PollRecords(ctx, c.cfg.BatchSize)
}

// resumeDue resumes the paused partitions that are due at now.
func (c *Consumer) resumeDue(now time.Time) {
	resume := make(map[string][]int32)
	for tp, due := range c.paused {
		if !due.After(now) {
			resume[tp.topic] = append(resume[tp.topic], tp.partition)
			delete(c.paused, tp)
		}
	}
	if len(resume) > 0 {
		c.cl.ResumeFetchPartitions(resume)
	}
}

// batch processes the due records of recs in the bounded pool and commits
// them once all are processed. A failed commit is only logged: every record
// is processed, the next commit of the same partitions covers it, and a
// restart or rebalance before that redelivers the batch, which the
// idempotent Processor absorbs.
//
// In a partition whose next record is not due yet, batch processes the
// records before it, pauses the partition, and rewinds it to that record.
func (c *Consumer) batch(ctx context.Context, recs []*kgo.Record) error {
	due, rewind := c.deferNotDue(recs, time.Now())
	if len(due) > 0 {
		if err := runPool(ctx, c.cfg.Concurrency, due, c.handle); err != nil {
			return fmt.Errorf("kafka: batch of %d records not committed: %w", len(due), err)
		}
		start := time.Now()
		if err := c.cl.CommitRecords(ctx, due...); err != nil {
			c.log.ErrorContext(ctx, "kafka commit failed",
				slog.Int("records", len(due)), errorAttr(err))
		} else {
			c.log.DebugContext(ctx, "kafka batch committed",
				slog.Int("records", len(due)), slog.Int64("commit_ms", time.Since(start).Milliseconds()))
		}
	}
	// After the commit, as SetOffsets must not run concurrently with one.
	// Rebalances are still blocked, so the partitions are still ours.
	if len(rewind) > 0 {
		c.cl.SetOffsets(rewind)
	}
	return nil
}

// deferNotDue splits recs at the first record of each partition that is due
// after now. It pauses those partitions, notes when each is due, and returns
// the records before the split and the offsets to rewind the partitions to.
// A record whose headers do not parse is due, so handle sends it to the DLQ.
func (c *Consumer) deferNotDue(recs []*kgo.Record, now time.Time) ([]*kgo.Record, map[string]map[int32]kgo.EpochOffset) {
	var (
		due    = make([]*kgo.Record, 0, len(recs))
		rewind map[string]map[int32]kgo.EpochOffset
		pause  map[string][]int32
	)
	for _, r := range recs {
		tp := topicPartition{r.Topic, r.Partition}
		if _, ok := rewind[r.Topic][r.Partition]; ok {
			continue // behind a record that is not due; fetched again on resume
		}
		m, err := readMeta(r)
		if err != nil || !m.notBefore.After(now) {
			due = append(due, r)
			continue
		}
		if rewind == nil {
			rewind = make(map[string]map[int32]kgo.EpochOffset)
			pause = make(map[string][]int32)
		}
		if rewind[r.Topic] == nil {
			rewind[r.Topic] = make(map[int32]kgo.EpochOffset)
		}
		rewind[r.Topic][r.Partition] = kgo.EpochOffset{Epoch: r.LeaderEpoch, Offset: r.Offset}
		pause[r.Topic] = append(pause[r.Topic], r.Partition)
		c.paused[tp] = m.notBefore
		c.log.Debug("kafka partition paused until its next record is due",
			slog.String("topic", r.Topic), slog.Int("partition", int(r.Partition)),
			slog.Int64("offset", r.Offset), slog.Time("not_before", m.notBefore))
	}
	if len(pause) > 0 {
		c.cl.PauseFetchPartitions(pause)
	}
	return due, rewind
}

// handle processes one record. It returns an error only when the record must
// not be committed; every outcome of the Processor is committed, after any
// produce to the retry topic or the DLQ has succeeded.
func (c *Consumer) handle(ctx context.Context, r *kgo.Record) error {
	attrs := []slog.Attr{
		slog.String("topic", r.Topic),
		slog.Int("partition", int(r.Partition)),
		slog.Int64("offset", r.Offset),
	}
	m, metaErr := readMeta(r)
	msg, err := ticket.UnmarshalSubmitted(r.Value)
	if err != nil {
		return c.deadLetter(ctx, r, m.attempt, ClassBadPayload, err, attrs)
	}
	id := msg.TicketID.String()
	attrs = append(attrs, slog.String("ticket_id", id))
	if metaErr != nil {
		return c.giveUp(ctx, r, id, 0, ClassBadHeader, metaErr, attrs)
	}
	if m.attempt > 0 {
		attrs = append(attrs, slog.Int("retry_attempt", m.attempt))
	}

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
		if m.attempt >= c.cfg.Retry.MaxAttempts {
			return c.giveUp(ctx, r, id, m.attempt, errorClass(out.Err), out.Err, attrs)
		}
		return c.retry(ctx, r, m.attempt+1, out, attrs)
	case triage.Poison:
		// The Processor already failed the ticket open if it exists.
		return c.deadLetter(ctx, r, m.attempt, errorClass(out.Err), out.Err, attrs)
	default:
		return fmt.Errorf("process ticket %s: unknown outcome %s", id, out.Kind)
	}
	return nil
}

// retry produces r to the retry topic as attempt n, due after the backoff.
func (c *Consumer) retry(ctx context.Context, r *kgo.Record, n int, out triage.Outcome, attrs []slog.Attr) error {
	notBefore := time.Now().Add(c.cfg.Retry.delay(n, out.RetryAfter))
	if err := c.produce(ctx, retryRecord(r, n, notBefore)); err != nil {
		return err
	}
	c.log.LogAttrs(ctx, slog.LevelWarn, "ticket left pending, sent to the retry topic", append(attrs,
		slog.Int("next_retry_attempt", n), slog.Time("not_before", notBefore.UTC()), errorAttr(out.Err))...)
	return nil
}

// giveUp fails the ticket with the id open, then sends r to the DLQ.
func (c *Consumer) giveUp(ctx context.Context, r *kgo.Record, id string, attempt int, class ErrorClass, cause error, attrs []slog.Attr) error {
	out, err := c.proc.GiveUp(ctx, id)
	if err != nil {
		return fmt.Errorf("give up on ticket %s: %w", id, err)
	}
	if out.Kind == triage.Done {
		c.log.LogAttrs(ctx, slog.LevelWarn, "ticket failed open before the DLQ", append(attrs,
			slog.String("status", string(out.Status)), slog.String("status_reason", string(out.Reason)))...)
	}
	return c.deadLetter(ctx, r, attempt, class, cause, attrs)
}

// deadLetter produces r to the DLQ with its failure metadata.
func (c *Consumer) deadLetter(ctx context.Context, r *kgo.Record, attempt int, class ErrorClass, cause error, attrs []slog.Attr) error {
	if err := c.produce(ctx, dlqRecord(r, attempt, class, time.Now())); err != nil {
		return err
	}
	c.log.LogAttrs(ctx, slog.LevelWarn, "record sent to the DLQ", append(attrs,
		slog.String("error_class", string(class)), errorAttr(cause))...)
	return nil
}

// produce writes rec and waits for the broker to acknowledge it.
func (c *Consumer) produce(ctx context.Context, rec *kgo.Record) error {
	if err := c.cl.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return fmt.Errorf("kafka: produce to %s: %w", rec.Topic, err)
	}
	return nil
}

type topicPartition struct {
	topic     string
	partition int32
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
