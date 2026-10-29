package kafka_test

import (
	"slices"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
)

// seedDLQ writes one DLQ record per id with class and returns them.
func seedDLQ(t *testing.T, addrs []string, class kafka.ErrorClass, ids ...string) []*kgo.Record {
	t.Helper()
	cl, err := kafka.NewClient(addrs, "kafka-test-producer")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	recs := make([]*kgo.Record, len(ids))
	for i, id := range ids {
		recs[i] = &kgo.Record{Topic: kafka.TopicSubmittedDLQ, Key: []byte(id), Value: []byte(`{"ticket_id":"` + id + `"}`),
			Headers: []kgo.RecordHeader{
				{Key: kafka.HeaderOriginalTopic, Value: []byte(kafka.TopicSubmitted)},
				{Key: kafka.HeaderOriginalPartition, Value: []byte("2")},
				{Key: kafka.HeaderOriginalOffset, Value: []byte("40")},
				{Key: kafka.HeaderAttempt, Value: []byte("3")},
				{Key: kafka.HeaderErrorClass, Value: []byte(class)},
				{Key: kafka.HeaderFailedAt, Value: []byte("2026-10-29T12:00:00Z")},
			}}
	}
	produceRecords(t, cl, recs...)
	return recs
}

// redrive runs kafka.Redrive with a fresh client and returns what it
// reported.
func redrive(t *testing.T, addrs []string, f kafka.RedriveFilter, dryRun bool) []kafka.DeadLetter {
	t.Helper()
	cl, err := kafka.NewClient(addrs, "kafka-test-redrive", kafka.RedriveOpts()...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()
	var found []kafka.DeadLetter
	if err := kafka.Redrive(t.Context(), cl, f, dryRun, func(d kafka.DeadLetter) error {
		found = append(found, d)
		return nil
	}); err != nil {
		t.Fatalf("Redrive: %v", err)
	}
	return found
}

func ticketIDs(ds []kafka.DeadLetter) []string {
	ids := make([]string, len(ds))
	for i, d := range ds {
		ids[i] = d.TicketID
	}
	return slices.Sorted(slices.Values(ids))
}

func TestRedrive(t *testing.T) {
	transient, poison := newIDs(t, 2), newIDs(t, 1)
	sorted := func(ids ...string) []string { return slices.Sorted(slices.Values(ids)) }
	tests := []struct {
		name   string
		filter kafka.RedriveFilter
		dryRun bool
		want   []string
	}{
		{"dry run", kafka.RedriveFilter{}, true, sorted(append(transient, poison...)...)},
		{"all", kafka.RedriveFilter{}, false, sorted(append(transient, poison...)...)},
		{"by class", kafka.RedriveFilter{Classes: []kafka.ErrorClass{kafka.ClassTransient}}, false, sorted(transient...)},
		{"by ticket", kafka.RedriveFilter{TicketIDs: []string{transient[1], "unknown"}}, false, sorted(transient[1])},
		{"no match", kafka.RedriveFilter{Classes: []kafka.ErrorClass{kafka.ClassNotFound}}, false, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addrs := newCluster(t, 1)
			seedDLQ(t, addrs, kafka.ClassTransient, transient...)
			seedDLQ(t, addrs, kafka.ClassPermanent, poison...)

			found := redrive(t, addrs, tt.filter, tt.dryRun)
			if got := ticketIDs(found); !slices.Equal(got, tt.want) {
				t.Errorf("found %v, want %v", got, tt.want)
			}
			wantProduced := int64(len(tt.want))
			if tt.dryRun {
				wantProduced = 0
			}
			if n := endOffset(t, addrs, kafka.TopicSubmitted); n != wantProduced {
				t.Errorf("tickets.submitted has %d records, want %d", n, wantProduced)
			}
			if n := endOffset(t, addrs, kafka.TopicSubmittedDLQ); n != 3 {
				t.Errorf("DLQ has %d records, want the 3 it had", n)
			}
			if wantProduced == 0 {
				return
			}
			for _, r := range readTopic(t, addrs, kafka.TopicSubmitted, int(wantProduced)) {
				if !slices.Contains(tt.want, string(r.Key)) || string(r.Value) != `{"ticket_id":"`+string(r.Key)+`"}` {
					t.Errorf("re-driven record %s = %s, want a wanted ticket with its value", r.Key, r.Value)
				}
				// A fresh first attempt that still points at the first record.
				checkHeaders(t, r, map[string]string{
					kafka.HeaderOriginalTopic:     kafka.TopicSubmitted,
					kafka.HeaderOriginalPartition: "2",
					kafka.HeaderOriginalOffset:    "40",
				})
			}
		})
	}
}

func TestRedriveReportsMetadata(t *testing.T) {
	addrs := newCluster(t, 1)
	ids := newIDs(t, 1)
	seedDLQ(t, addrs, kafka.ClassTransient, ids...)
	found := redrive(t, addrs, kafka.RedriveFilter{}, true)
	want := kafka.DeadLetter{
		Partition: 0, Offset: 0, TicketID: ids[0], Class: kafka.ClassTransient, Attempt: 3,
		FailedAt: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), Origin: "tickets.submitted/2/40",
	}
	if len(found) != 1 || found[0] != want {
		t.Errorf("found %+v, want [%+v]", found, want)
	}
}

// An empty DLQ is read to its end at once.
func TestRedriveEmpty(t *testing.T) {
	addrs := newCluster(t, 1)
	if found := redrive(t, addrs, kafka.RedriveFilter{}, false); len(found) != 0 {
		t.Errorf("found %v in an empty DLQ", found)
	}
}
