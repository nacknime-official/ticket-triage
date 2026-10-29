package kafka

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// RedriveOpts returns the client options of a re-drive run: a group-less
// reader of tickets.submitted.dlq from the start, so a run neither commits
// nor depends on what an earlier run read.
func RedriveOpts() []kgo.Opt {
	return []kgo.Opt{
		kgo.ConsumeTopics(TopicSubmittedDLQ),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	}
}

// RedriveFilter selects DLQ records. An empty field matches every record.
type RedriveFilter struct {
	Classes   []ErrorClass
	TicketIDs []string
}

func (f RedriveFilter) match(d DeadLetter) bool {
	return (len(f.Classes) == 0 || slices.Contains(f.Classes, d.Class)) &&
		(len(f.TicketIDs) == 0 || slices.Contains(f.TicketIDs, d.TicketID))
}

// DeadLetter is the metadata of one DLQ record; never its payload.
type DeadLetter struct {
	Partition int32
	Offset    int64
	TicketID  string // the record key
	Class     ErrorClass
	Attempt   int
	FailedAt  time.Time // zero when the header is missing or does not parse
	// Origin locates the record the ticket first arrived in, as
	// topic/partition/offset.
	Origin string
}

func deadLetter(r *kgo.Record) DeadLetter {
	d := DeadLetter{Partition: r.Partition, Offset: r.Offset, TicketID: string(r.Key)}
	if v, ok := header(r, HeaderErrorClass); ok {
		d.Class = ErrorClass(v)
	}
	if v, ok := header(r, HeaderAttempt); ok {
		d.Attempt, _ = strconv.Atoi(v)
	}
	if v, ok := header(r, HeaderFailedAt); ok {
		d.FailedAt, _ = time.Parse(time.RFC3339Nano, v)
	}
	topic, _ := header(r, HeaderOriginalTopic)
	partition, _ := header(r, HeaderOriginalPartition)
	offset, _ := header(r, HeaderOriginalOffset)
	d.Origin = topic + "/" + partition + "/" + offset
	return d
}

// Redrive reads tickets.submitted.dlq up to its end when the call starts and
// calls found with every record that f matches. Unless dryRun, it first
// produces each match to tickets.submitted, with its key, value and
// original_* headers, so the ticket starts over at attempt 0; found then
// reports a record that was re-driven. cl must be built with RedriveOpts.
//
// A re-driven ticket that went to needs_human is skipped by the worker, as
// it is no longer pending; set it back to pending first to classify it again.
// The DLQ itself is left as it is, so a later run finds the record again.
func Redrive(ctx context.Context, cl *kgo.Client, f RedriveFilter, dryRun bool, found func(DeadLetter) error) error {
	ends, err := kadm.NewClient(cl).ListEndOffsets(ctx, TopicSubmittedDLQ)
	if err != nil {
		return fmt.Errorf("kafka: redrive: list end offsets: %w", err)
	}
	if err := ends.Error(); err != nil {
		return fmt.Errorf("kafka: redrive: list end offsets: %w", err)
	}
	// left holds the end offset of every partition not read to its end yet.
	left := make(map[int32]int64)
	ends.Each(func(o kadm.ListedOffset) {
		if o.Offset > 0 {
			left[o.Partition] = o.Offset
		}
	})

	for len(left) > 0 {
		fetches := cl.PollFetches(ctx)
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("kafka: redrive: %w", err)
		}
		if fetches.IsClientClosed() {
			return errors.New("kafka: redrive: client closed")
		}
		if err := fetches.Err(); err != nil {
			return fmt.Errorf("kafka: redrive: fetch: %w", err)
		}
		var matched []DeadLetter
		var out []*kgo.Record
		for _, r := range fetches.Records() {
			end, ok := left[r.Partition]
			if !ok || r.Offset >= end {
				continue // produced after the run started
			}
			if r.Offset == end-1 {
				delete(left, r.Partition)
			}
			d := deadLetter(r)
			if !f.match(d) {
				continue
			}
			matched = append(matched, d)
			out = append(out, &kgo.Record{Topic: TopicSubmitted, Key: r.Key, Value: r.Value, Headers: origin(r)})
		}
		if !dryRun && len(out) > 0 {
			if err := cl.ProduceSync(ctx, out...).FirstErr(); err != nil {
				return fmt.Errorf("kafka: redrive: produce to %s: %w", TopicSubmitted, err)
			}
		}
		for _, d := range matched {
			if err := found(d); err != nil {
				return err
			}
		}
	}
	return nil
}
