//go:build integration

package outbox_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/outbox"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

// TestStatusEventReachesKafka classifies one ticket and leaves another
// pending: only the classification reaches tickets.events, once, keyed by the
// ticket id, and a second Process of the classified ticket skips without an
// event.
func TestStatusEventReachesKafka(t *testing.T) {
	t.Parallel()
	pool, cluster := newPool(t), newCluster(t)
	startRelay(t, pool, cluster, outbox.Config{PollInterval: 20 * time.Millisecond})
	ids := submit(t, pool, 2)
	classifiedID, pendingID := ids[0], ids[1]

	store := postgres.NewTriageStore(pool, kafka.TopicEvents)
	cfg := triage.Config{Thresholds: classifier.DefaultThresholds(), DailyBudgetUSD: 1, MaxAttempts: 1}
	p := triage.NewProcessor(store, &classifier.Fake{}, cfg, discard)
	for i, want := range []triage.Kind{triage.Done, triage.Skipped} {
		out, err := p.Process(t.Context(), classifiedID)
		if err != nil || out.Kind != want {
			t.Fatalf("Process %d = %v, %v; want %v", i+1, out.Kind, err, want)
		}
	}
	failing := &classifier.Fake{Errs: []error{fmt.Errorf("%w: 503", classifier.ErrTransient)}}
	out, err := triage.NewProcessor(store, failing, cfg, discard).Process(t.Context(), pendingID)
	if err != nil || out.Kind != triage.Retryable {
		t.Fatalf("Process of a failing ticket = %v, %v; want %v", out.Kind, err, triage.Retryable)
	}

	waitPublished(t, pool)
	recs := consume(t, cluster, kafka.TopicEvents, 1)
	if len(recs) != 1 {
		t.Fatalf("got %d records on %s, want 1", len(recs), kafka.TopicEvents)
	}
	r := recs[0]
	if string(r.Key) != classifiedID {
		t.Errorf("record key = %q, want ticket id %s", r.Key, classifiedID)
	}
	if len(r.Headers) != 1 || r.Headers[0].Key != "type" || string(r.Headers[0].Value) != ticket.StatusChangedType {
		t.Errorf("record headers = %v, want type=%s", r.Headers, ticket.StatusChangedType)
	}
	// submit's ticket text must not reach Kafka.
	for _, text := range []string{"Refund", "Charged twice"} {
		if strings.Contains(string(r.Value), text) {
			t.Errorf("record value %s carries ticket text %q", r.Value, text)
		}
	}
	ev, err := ticket.UnmarshalStatusChanged(r.Value)
	if err != nil {
		t.Fatalf("record value %s: %v", r.Value, err)
	}
	if ev.TicketID.String() != classifiedID || ev.Status != ticket.StatusClassified || ev.StatusReason != nil ||
		ev.Category == nil || *ev.Category != classifier.CategoryBilling || ev.Priority == nil ||
		ev.Message == "" || ev.At.IsZero() {
		t.Errorf("status event = %s", r.Value)
	}
}
