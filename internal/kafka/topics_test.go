package kafka_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
)

// newAdmin starts an in-process broker and returns an admin client for it.
func newAdmin(t *testing.T, opts ...kfake.Opt) *kadm.Client {
	t.Helper()
	cluster, err := kfake.NewCluster(append([]kfake.Opt{kfake.NumBrokers(1)}, opts...)...)
	if err != nil {
		t.Fatalf("start kfake cluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	cl, err := kafka.NewClient(cluster.ListenAddrs(), "kafka-test")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(cl.Close)
	return kadm.NewClient(cl)
}

func names(specs []kafka.TopicSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

func TestTopics(t *testing.T) {
	const day = 24 * time.Hour
	want := []kafka.TopicSpec{
		{Name: "tickets.submitted", Partitions: 6, Retention: 7 * day},
		{Name: "tickets.submitted.retry", Partitions: 3, Retention: 7 * day},
		{Name: "tickets.submitted.dlq", Partitions: 1, Retention: 30 * day},
		{Name: "tickets.events", Partitions: 3, Retention: day},
	}
	if got := kafka.Topics(); !slices.Equal(got, want) {
		t.Errorf("Topics() = %+v, want %+v", got, want)
	}
}

// checkTopics fails unless every spec's topic exists with its partitions and
// retention.
func checkTopics(t *testing.T, adm *kadm.Client, specs []kafka.TopicSpec) {
	t.Helper()
	ctx := t.Context()
	details, err := adm.ListTopics(ctx, names(specs)...)
	if err != nil {
		t.Fatalf("ListTopics: %v", err)
	}
	configs, err := adm.DescribeTopicConfigs(ctx, names(specs)...)
	if err != nil {
		t.Fatalf("DescribeTopicConfigs: %v", err)
	}
	for _, s := range specs {
		d, ok := details[s.Name]
		if !ok || d.Err != nil {
			t.Errorf("topic %s: missing (err %v)", s.Name, d.Err)
			continue
		}
		if got := int32(len(d.Partitions)); got != s.Partitions {
			t.Errorf("topic %s: %d partitions, want %d", s.Name, got, s.Partitions)
		}
		rc, err := configs.On(s.Name, nil)
		if err != nil {
			t.Errorf("topic %s: describe configs: %v", s.Name, err)
			continue
		}
		want := strconv.FormatInt(s.Retention.Milliseconds(), 10)
		var got string
		for _, c := range rc.Configs {
			if c.Key == "retention.ms" {
				got = c.MaybeValue()
			}
		}
		if got != want {
			t.Errorf("topic %s: retention.ms = %q, want %q", s.Name, got, want)
		}
	}
}

func TestEnsureTopics(t *testing.T) {
	adm := newAdmin(t)
	specs := kafka.Topics()

	created, err := kafka.EnsureTopics(t.Context(), adm, specs...)
	if err != nil {
		t.Fatalf("first EnsureTopics: %v", err)
	}
	if want := names(specs); !slices.Equal(created, want) {
		t.Errorf("first EnsureTopics created %v, want %v", created, want)
	}
	checkTopics(t, adm, specs)

	created, err = kafka.EnsureTopics(t.Context(), adm, specs...)
	if err != nil {
		t.Fatalf("second EnsureTopics: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("second EnsureTopics created %v, want none", created)
	}
	checkTopics(t, adm, specs)
}

func TestEnsureTopicsKeepsExisting(t *testing.T) {
	adm := newAdmin(t, kfake.SeedTopics(1, kafka.TopicEvents))

	created, err := kafka.EnsureTopics(t.Context(), adm, kafka.Topics()...)
	if err != nil {
		t.Fatalf("EnsureTopics: %v", err)
	}
	want := []string{kafka.TopicSubmitted, kafka.TopicSubmittedRetry, kafka.TopicSubmittedDLQ}
	if !slices.Equal(created, want) {
		t.Errorf("created %v, want %v", created, want)
	}
	details, err := adm.ListTopics(t.Context(), kafka.TopicEvents)
	if err != nil {
		t.Fatalf("ListTopics: %v", err)
	}
	if got := len(details[kafka.TopicEvents].Partitions); got != 1 {
		t.Errorf("existing %s has %d partitions, want 1 (unchanged)", kafka.TopicEvents, got)
	}
}

func TestEnsureTopicsJoinsErrors(t *testing.T) {
	adm := newAdmin(t)
	specs := []kafka.TopicSpec{
		{Name: "bad.one", Partitions: 0, Retention: time.Hour},
		{Name: "good", Partitions: 2, Retention: time.Hour},
		{Name: "bad.two", Partitions: 0, Retention: time.Hour},
	}

	created, err := kafka.EnsureTopics(t.Context(), adm, specs...)
	if !errors.Is(err, kerr.InvalidPartitions) {
		t.Fatalf("EnsureTopics error = %v, want %v", err, kerr.InvalidPartitions)
	}
	for _, name := range []string{"bad.one", "bad.two"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name topic %s", err, name)
		}
	}
	if want := []string{"good"}; !slices.Equal(created, want) {
		t.Errorf("created %v, want %v", created, want)
	}
	checkTopics(t, adm, specs[1:2])
}
