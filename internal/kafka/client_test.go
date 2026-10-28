package kafka_test

import (
	"testing"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
)

func TestNewClientRejects(t *testing.T) {
	tests := []struct {
		name     string
		brokers  []string
		clientID string
	}{
		{"no brokers", nil, "test"},
		{"empty client id", []string{"localhost:9092"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl, err := kafka.NewClient(tt.brokers, tt.clientID)
			if err == nil {
				cl.Close()
				t.Fatal("NewClient succeeded, want an error")
			}
		})
	}
}

func TestNewClientProducer(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(6, kafka.TopicSubmitted))
	if err != nil {
		t.Fatalf("start kfake cluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	cl, err := kafka.NewClient(cluster.ListenAddrs(), "ticket-triage-test")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(cl.Close)

	if got := cl.OptValue(kgo.ClientID); got != "ticket-triage-test" {
		t.Errorf("ClientID = %v, want ticket-triage-test", got)
	}
	if got := cl.OptValue(kgo.DisableIdempotentWrite); got != false {
		t.Errorf("DisableIdempotentWrite = %v, want false", got)
	}
	if got := cl.OptValue(kgo.RequiredAcks); got != kgo.AllISRAcks() {
		t.Errorf("RequiredAcks = %v, want AllISRAcks", got)
	}

	// Records with the same key land in the same partition.
	key := []byte("0190a5c4-0000-7000-8000-000000000001")
	var partitions []int32
	for range 3 {
		rec := &kgo.Record{Topic: kafka.TopicSubmitted, Key: key, Value: []byte(`{}`)}
		res := cl.ProduceSync(t.Context(), rec)
		if err := res.FirstErr(); err != nil {
			t.Fatalf("ProduceSync: %v", err)
		}
		partitions = append(partitions, rec.Partition)
	}
	for _, p := range partitions[1:] {
		if p != partitions[0] {
			t.Errorf("same key went to partitions %v, want one partition", partitions)
			break
		}
	}
}
