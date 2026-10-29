package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// ReplayProcessor re-classifies the ticket of one replayed record;
// *shadow.Processor satisfies it. A non-nil error means the record must not
// be committed and stops the replay.
type ReplayProcessor interface {
	Replay(ctx context.Context, ticketID string) error
}

// ReplayGroupPrefix starts the consumer group of every replay run, so a
// replay never shares a group, or its committed offsets, with the workers.
const ReplayGroupPrefix = "triage-shadow-"

// ReplayGroup returns the consumer group of the replay run runID.
func ReplayGroup(runID string) string {
	return ReplayGroupPrefix + runID
}

// ReplayOpts returns the client options of a replay in group: it reads
// tickets.submitted from the earliest offset, or from the first record at or
// after from when from is not zero, for a new group, and from the group's
// committed offsets for a resumed one. It commits only by hand and holds
// rebalances between a poll and its commit, like the classifier consumer.
func ReplayOpts(group string, from time.Time) []kgo.Opt {
	start := kgo.NewOffset().AtStart()
	if !from.IsZero() {
		start = kgo.NewOffset().AfterMilli(from.UnixMilli())
	}
	return []kgo.Opt{
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(TopicSubmitted),
		kgo.ConsumeResetOffset(start),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	}
}

// ReplayConfig configures a Replayer. Zero fields take the defaults of
// ConsumerConfig.
type ReplayConfig struct {
	Concurrency int
	BatchSize   int
	// From is the start of a new group, as passed to ReplayOpts; zero means
	// the earliest offset.
	From time.Time
	// MaxRecords bounds the records one Run handles; zero means no bound.
	MaxRecords int
	// NoCommit leaves the group's offsets untouched, for a dry run.
	NoCommit bool
}

// ReplayStop says why Run returned without an error.
type ReplayStop int

const (
	// ReplayComplete means every record up to the end offsets taken when Run
	// started was handled.
	ReplayComplete ReplayStop = iota + 1
	// ReplayLimited means MaxRecords records were handled.
	ReplayLimited
	// ReplayCancelled means ctx was done; the batch in flight is not
	// committed and is replayed by a resumed run.
	ReplayCancelled
)

func (s ReplayStop) String() string {
	switch s {
	case ReplayComplete:
		return "complete"
	case ReplayLimited:
		return "max records reached"
	case ReplayCancelled:
		return "cancelled"
	}
	return fmt.Sprintf("ReplayStop(%d)", int(s))
}

// ReplayResult is what one Run did.
type ReplayResult struct {
	Stop ReplayStop
	// Records counts the records handled and committed (or, with NoCommit,
	// that would have been).
	Records int
}

// Replayer reads the history of tickets.submitted in its own consumer group
// and hands each ticket to a ReplayProcessor (README §6.1). It only consumes:
// it never produces, and commits only its own group's offsets. Unlike the
// classifier consumer it stops by itself at the end of the history.
type Replayer struct {
	cl   *kgo.Client
	adm  *kadm.Client
	proc ReplayProcessor
	cfg  ReplayConfig
	log  *slog.Logger
}

// NewReplayer returns a Replayer that polls with cl and replays each record
// with proc. cl must be a group client built with ReplayOpts for the same
// From as cfg.
func NewReplayer(cl *kgo.Client, proc ReplayProcessor, cfg ReplayConfig, log *slog.Logger) (*Replayer, error) {
	if g, _ := cl.OptValue(kgo.ConsumerGroup).(string); g == "" {
		return nil, errors.New("kafka: replay: client has no consumer group")
	}
	for _, opt := range []any{kgo.DisableAutoCommit, kgo.BlockRebalanceOnPoll} {
		if on, _ := cl.OptValue(opt).(bool); !on {
			return nil, errors.New("kafka: replay: client must be built with ReplayOpts")
		}
	}
	if cfg.MaxRecords < 0 {
		return nil, errors.New("kafka: replay: negative MaxRecords")
	}
	c := ConsumerConfig{Concurrency: cfg.Concurrency, BatchSize: cfg.BatchSize}.withDefaults()
	cfg.Concurrency, cfg.BatchSize = c.Concurrency, c.BatchSize
	return &Replayer{cl: cl, adm: kadm.NewClient(cl), proc: proc, cfg: cfg, log: log}, nil
}

// Run replays the records of tickets.submitted up to the end offsets taken
// when it starts, in batches processed by a bounded pool, committing each
// batch once all of it is processed. It returns when the end is reached,
// MaxRecords records were handled, or ctx is done, with the reason, or with
// an error when a record failed or the offsets could not be listed; the
// batch of a failed record is not committed. The error wraps
// classifier.ErrAuth for a classifier configuration failure. Records
// produced after Run started are left for a later run. Run leaves the group
// to the caller: LeaveGroupContext, then Close the client.
//
// A Replayer expects all partitions: run one process per replay group.
func (r *Replayer) Run(ctx context.Context) (ReplayResult, error) {
	ends, remaining, err := r.targets(ctx)
	if err != nil {
		return ReplayResult{}, err
	}
	res := ReplayResult{Stop: ReplayComplete}
	for len(remaining) > 0 {
		n := r.cfg.BatchSize
		if r.cfg.MaxRecords > 0 {
			if res.Records >= r.cfg.MaxRecords {
				res.Stop = ReplayLimited
				return res, nil
			}
			n = min(n, r.cfg.MaxRecords-res.Records)
		}
		fetches := r.cl.PollRecords(ctx, n)
		if isDone(ctx) || fetches.IsClientClosed() {
			r.cl.AllowRebalance()
			res.Stop = ReplayCancelled
			return res, nil
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			r.log.WarnContext(ctx, "kafka fetch error",
				slog.String("topic", topic), slog.Int("partition", int(partition)), errorAttr(err))
		})
		// Records past the end are newer than the run; they stay uncommitted.
		var recs []*kgo.Record
		for _, rec := range fetches.Records() {
			if rec.Offset < ends[rec.Partition] {
				recs = append(recs, rec)
			}
		}
		err := r.batch(ctx, recs)
		r.cl.AllowRebalance()
		if err != nil && isDone(ctx) {
			// The batch was cut off by the stop, not failed.
			res.Stop = ReplayCancelled
			return res, nil
		}
		if err != nil {
			return res, err
		}
		res.Records += len(recs)
		for _, rec := range recs {
			if rec.Offset+1 >= ends[rec.Partition] {
				delete(remaining, rec.Partition)
			}
		}
	}
	return res, nil
}

// isDone reports whether ctx is done.
func isDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// targets returns the end offset of every partition of tickets.submitted,
// and the set of partitions that have records left before their end: from
// the group's committed offset, or from the start of a new group.
func (r *Replayer) targets(ctx context.Context) (ends map[int32]int64, remaining map[int32]bool, err error) {
	listedEnds, err := r.adm.ListEndOffsets(ctx, TopicSubmitted)
	if err == nil {
		err = listedEnds.Error()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("kafka: replay: list end offsets of %s: %w", TopicSubmitted, err)
	}
	var starts kadm.ListedOffsets
	if r.cfg.From.IsZero() {
		starts, err = r.adm.ListStartOffsets(ctx, TopicSubmitted)
	} else {
		starts, err = r.adm.ListOffsetsAfterMilli(ctx, r.cfg.From.UnixMilli(), TopicSubmitted)
	}
	if err == nil {
		err = starts.Error()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("kafka: replay: list start offsets of %s: %w", TopicSubmitted, err)
	}
	group, _ := r.cl.OptValue(kgo.ConsumerGroup).(string)
	committed, err := r.adm.FetchOffsets(ctx, group)
	if err == nil {
		err = committed.Error()
	}
	if errors.Is(err, kerr.GroupIDNotFound) {
		// A new group that has not joined yet: nothing is committed.
		committed, err = nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("kafka: replay: fetch offsets of group %s: %w", group, err)
	}

	ends, remaining = make(map[int32]int64), make(map[int32]bool)
	listedEnds.Each(func(e kadm.ListedOffset) {
		ends[e.Partition] = e.Offset
		from := int64(0)
		if s, ok := starts.Lookup(e.Topic, e.Partition); ok && s.Offset >= 0 {
			from = s.Offset
		}
		if c, ok := committed.Lookup(e.Topic, e.Partition); ok && c.At >= 0 {
			from = c.At
		}
		if from < e.Offset {
			remaining[e.Partition] = true
		}
	})
	return ends, remaining, nil
}

// batch processes recs in the bounded pool and, unless NoCommit, commits them
// once all are processed. A failed commit is only logged: a resumed run
// replays the batch, and the processor skips tickets it already answered.
func (r *Replayer) batch(ctx context.Context, recs []*kgo.Record) error {
	if len(recs) == 0 {
		return nil
	}
	if err := runPool(ctx, r.cfg.Concurrency, recs, r.handle); err != nil {
		return fmt.Errorf("kafka: replay batch of %d records not committed: %w", len(recs), err)
	}
	if r.cfg.NoCommit {
		return nil
	}
	if err := r.cl.CommitRecords(ctx, recs...); err != nil {
		r.log.ErrorContext(ctx, "kafka replay commit failed", slog.Int("records", len(recs)), errorAttr(err))
	}
	return nil
}

// handle replays one record. A record whose payload does not decode is
// skipped: there is no ticket to replay.
func (r *Replayer) handle(ctx context.Context, rec *kgo.Record) error {
	attrs := []slog.Attr{
		slog.String("topic", rec.Topic),
		slog.Int("partition", int(rec.Partition)),
		slog.Int64("offset", rec.Offset),
	}
	msg, err := ticket.UnmarshalSubmitted(rec.Value)
	if err != nil {
		r.log.LogAttrs(ctx, slog.LevelWarn, "replay: poison record skipped", append(attrs, errorAttr(err))...)
		return nil
	}
	id := msg.TicketID.String()
	if err := r.proc.Replay(ctx, id); err != nil {
		if errors.Is(err, classifier.ErrAuth) {
			r.log.LogAttrs(ctx, slog.LevelError, "classifier auth failed, stopping replay",
				append(attrs, slog.String("ticket_id", id), errorAttr(err))...)
		}
		return fmt.Errorf("replay ticket %s: %w", id, err)
	}
	return nil
}
