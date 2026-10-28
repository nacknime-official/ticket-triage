package kafka

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/outbox"
)

var _ outbox.Publisher = (*Publisher)(nil)

// Publisher produces outbox messages with a franz-go client.
type Publisher struct {
	cl *kgo.Client
}

// NewPublisher returns a Publisher that produces with cl, usually a client
// from NewClient. Publish waits until ctx is done for a record to be
// acknowledged; give cl kgo.RecordDeliveryTimeout as well to bound the wait
// independently of ctx.
func NewPublisher(cl *kgo.Client) *Publisher {
	return &Publisher{cl: cl}
}

// Publish implements outbox.Publisher. Each message becomes one record on
// its topic with its key and headers; ProduceSync waits for every record.
func (p *Publisher) Publish(ctx context.Context, msgs []outbox.Message) []error {
	recs := make([]*kgo.Record, len(msgs))
	index := make(map[*kgo.Record]int, len(msgs))
	for i, m := range msgs {
		r := &kgo.Record{Topic: m.Topic, Key: m.Key, Value: m.Payload}
		for _, k := range slices.Sorted(maps.Keys(m.Headers)) {
			r.Headers = append(r.Headers, kgo.RecordHeader{Key: k, Value: []byte(m.Headers[k])})
		}
		recs[i] = r
		index[r] = i
	}
	errs := make([]error, len(msgs))
	// Results arrive in completion order, not in the order of recs.
	results := p.cl.ProduceSync(ctx, recs...)
	if len(results) != len(recs) {
		err := errors.New("kafka: produce returned fewer results than records")
		for i := range errs {
			errs[i] = err
		}
		return errs
	}
	for _, res := range results {
		errs[index[res.Record]] = res.Err
	}
	return errs
}
