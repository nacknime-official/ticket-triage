package kafka

import (
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// NewClient returns a client that dials brokers and identifies itself as
// clientID, for example "ticket-triage-worker", so broker logs and quotas
// tell the binaries apart.
//
// Produce is idempotent (franz-go's default, kept on here) and waits for all
// in-sync replicas, so a produce retried by the client never writes a
// duplicate record within one producer session. Records are partitioned by
// key, and every topic is keyed by ticket id. opts are applied after these
// settings, so callers add consumer options or override them.
//
// The client connects lazily; the first request reports unreachable brokers.
func NewClient(brokers []string, clientID string, opts ...kgo.Opt) (*kgo.Client, error) {
	if len(brokers) == 0 {
		return nil, errors.New("kafka: no brokers")
	}
	if clientID == "" {
		return nil, errors.New("kafka: empty client id")
	}
	base := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
	}
	cl, err := kgo.NewClient(append(base, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("kafka: new client: %w", err)
	}
	return cl, nil
}
