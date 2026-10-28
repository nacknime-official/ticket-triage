package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/outbox"
)

func TestPublisher(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(6, kafka.TopicSubmitted))
	if err != nil {
		t.Fatalf("start kfake cluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	cl, err := kafka.NewClient(cluster.ListenAddrs(), "publisher-test")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(cl.Close)

	msgs := []outbox.Message{
		{ID: 1, Topic: kafka.TopicSubmitted, Key: []byte("a"), Payload: []byte(`{"ticket_id":"a"}`),
			Headers: map[string]string{"b": "2", "a": "1"}},
		// A topic that does not exist fails without failing the others.
		{ID: 2, Topic: "missing", Key: []byte("b"), Payload: []byte(`{}`)},
		{ID: 3, Topic: kafka.TopicSubmitted, Key: []byte("c"), Payload: []byte(`{"ticket_id":"c"}`)},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	errs := kafka.NewPublisher(cl).Publish(ctx, msgs)
	if len(errs) != len(msgs) {
		t.Fatalf("Publish returned %d errors for %d messages", len(errs), len(msgs))
	}
	if errs[0] != nil || errs[2] != nil {
		t.Errorf("errors for existing topic = %v, %v; want nil", errs[0], errs[2])
	}
	if errs[1] == nil {
		t.Error("publish to a missing topic succeeded, want an error")
	}

	consumer, err := kafka.NewClient(cluster.ListenAddrs(), "publisher-test-consumer",
		kgo.ConsumeTopics(kafka.TopicSubmitted), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("NewClient consumer: %v", err)
	}
	t.Cleanup(consumer.Close)
	got := map[string]*kgo.Record{}
	for len(got) < 2 {
		fetches := consumer.PollFetches(t.Context())
		if err := fetches.Err(); err != nil {
			t.Fatalf("PollFetches: %v", err)
		}
		fetches.EachRecord(func(r *kgo.Record) { got[string(r.Key)] = r })
	}
	for _, m := range []outbox.Message{msgs[0], msgs[2]} {
		r := got[string(m.Key)]
		if r == nil || string(r.Value) != string(m.Payload) {
			t.Errorf("record for key %s = %v, want payload %s", m.Key, r, m.Payload)
		}
	}
	h := got["a"].Headers
	if len(h) != 2 || h[0].Key != "a" || string(h[0].Value) != "1" || h[1].Key != "b" || string(h[1].Value) != "2" {
		t.Errorf("headers = %+v, want a=1, b=2 in key order", h)
	}
}
