package kafka

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
)

// Topic names. Every topic is keyed by ticket id, so all records of one ticket
// land in one partition in order.
const (
	// TopicSubmitted carries new tickets from the outbox relay to the workers.
	TopicSubmitted = "tickets.submitted"
	// TopicSubmittedRetry holds tickets whose classification failed
	// transiently, until their not_before header is due.
	TopicSubmittedRetry = "tickets.submitted.retry"
	// TopicSubmittedDLQ holds tickets that ran out of attempts or were poison,
	// for audit and manual re-drive.
	TopicSubmittedDLQ = "tickets.submitted.dlq"
	// TopicEvents carries ticket status changes to the API instances.
	TopicEvents = "tickets.events"
)

// TopicSpec is the configuration a topic is created with.
type TopicSpec struct {
	Name       string
	Partitions int32
	Retention  time.Duration // retention.ms
}

// Topics returns the specs of every topic the service uses.
//
// Partitions bound consumer parallelism: tickets.submitted has the most
// because it feeds the worker group. Retention covers what each topic is read
// for: replaying tickets.submitted to re-classify history (PRD §6.1),
// re-driving the DLQ by hand, and live status events, which Postgres also
// keeps.
func Topics() []TopicSpec {
	const day = 24 * time.Hour
	return []TopicSpec{
		{Name: TopicSubmitted, Partitions: 6, Retention: 7 * day},
		{Name: TopicSubmittedRetry, Partitions: 3, Retention: 7 * day},
		{Name: TopicSubmittedDLQ, Partitions: 1, Retention: 30 * day},
		{Name: TopicEvents, Partitions: 3, Retention: day},
	}
}

// EnsureTopics creates every topic in specs that does not exist yet, with the
// broker's default replication factor, and returns the names of the topics it
// created. A topic that already exists counts as success and is left
// unchanged, even if its partitions or retention differ from its spec, so
// running EnsureTopics again is a no-op.
//
// It tries every spec and joins the errors of those that failed.
func EnsureTopics(ctx context.Context, adm *kadm.Client, specs ...TopicSpec) (created []string, err error) {
	var errs []error
	for _, s := range specs {
		configs := map[string]*string{
			"retention.ms": kadm.StringPtr(strconv.FormatInt(s.Retention.Milliseconds(), 10)),
		}
		resp, err := adm.CreateTopic(ctx, s.Partitions, -1, configs, s.Name)
		switch {
		case err == nil:
			created = append(created, s.Name)
		case errors.Is(err, kerr.TopicAlreadyExists):
		case resp.ErrMessage != "":
			errs = append(errs, fmt.Errorf("kafka: create topic %s: %w: %s", s.Name, err, resp.ErrMessage))
		default:
			errs = append(errs, fmt.Errorf("kafka: create topic %s: %w", s.Name, err))
		}
	}
	return created, errors.Join(errs...)
}
