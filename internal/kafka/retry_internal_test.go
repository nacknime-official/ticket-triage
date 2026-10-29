package kafka

import (
	"fmt"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

func TestRetryPolicyDelay(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 5, BackoffBase: 30 * time.Second, BackoffMax: 10 * time.Minute}
	tests := []struct {
		n         int
		suggested time.Duration
		want      time.Duration
	}{
		{1, 0, 30 * time.Second},
		{2, 0, time.Minute},
		{3, 0, 2 * time.Minute},
		{5, 0, 8 * time.Minute},
		{6, 0, 10 * time.Minute},
		{64, 0, 10 * time.Minute},
		{1000, 0, 10 * time.Minute},
		{1, 45 * time.Second, 45 * time.Second},
		{3, 45 * time.Second, 2 * time.Minute},
		{1, time.Hour, 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt %d suggested %v", tt.n, tt.suggested), func(t *testing.T) {
			if got := p.delay(tt.n, tt.suggested); got != tt.want {
				t.Errorf("delay = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryPolicyDefaults(t *testing.T) {
	got := RetryPolicy{}.withDefaults()
	want := RetryPolicy{MaxAttempts: DefaultRetryMaxAttempts, BackoffBase: DefaultRetryBackoffBase, BackoffMax: DefaultRetryBackoffMax}
	if got != want {
		t.Errorf("withDefaults = %+v, want %+v", got, want)
	}
}

func TestReadMeta(t *testing.T) {
	at := time.Date(2026, 10, 29, 12, 0, 0, 500, time.UTC)
	tests := []struct {
		name    string
		headers []kgo.RecordHeader
		want    meta
		wantErr bool
	}{
		{"none", nil, meta{}, false},
		{"both", headers(HeaderAttempt, "2", HeaderNotBefore, at.Format(time.RFC3339Nano)), meta{2, at}, false},
		{"last wins", headers(HeaderAttempt, "1", HeaderAttempt, "3"), meta{attempt: 3}, false},
		{"bad attempt", headers(HeaderAttempt, "two"), meta{}, true},
		{"negative attempt", headers(HeaderAttempt, "-1"), meta{}, true},
		{"bad not_before", headers(HeaderNotBefore, "tomorrow"), meta{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readMeta(&kgo.Record{Headers: tt.headers})
			if (err != nil) != tt.wantErr || !got.notBefore.Equal(tt.want.notBefore) || got.attempt != tt.want.attempt {
				t.Errorf("readMeta = %+v, %v; want %+v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestErrorClass(t *testing.T) {
	tests := []struct {
		err  error
		want ErrorClass
	}{
		{fmt.Errorf("x: %w", triage.ErrBadID), ClassBadID},
		{fmt.Errorf("x: %w", ticket.ErrNotFound), ClassNotFound},
		{fmt.Errorf("x: %w", classifier.ErrInvalidAnswer), ClassInvalidAnswer},
		{fmt.Errorf("x: %w", classifier.ErrPermanent), ClassPermanent},
		{fmt.Errorf("x: %w", classifier.ErrTransient), ClassTransient},
		{fmt.Errorf("x"), ClassUnknown},
		{nil, ClassUnknown},
	}
	for _, tt := range tests {
		if got := errorClass(tt.err); got != tt.want {
			t.Errorf("errorClass(%v) = %s, want %s", tt.err, got, tt.want)
		}
	}
}

// Retries and DLQ records keep pointing at the record the ticket first
// arrived in.
func TestDerivedRecordsKeepOrigin(t *testing.T) {
	src := &kgo.Record{Topic: TopicSubmitted, Partition: 4, Offset: 17, Key: []byte("k"), Value: []byte("v")}
	notBefore := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	first := retryRecord(src, 1, notBefore)
	second := retryRecord(first, 2, notBefore)
	dead := dlqRecord(second, 2, ClassTransient, notBefore)
	for _, r := range []*kgo.Record{first, second, dead} {
		for key, want := range map[string]string{
			HeaderOriginalTopic: TopicSubmitted, HeaderOriginalPartition: "4", HeaderOriginalOffset: "17",
		} {
			if got, _ := header(r, key); got != want {
				t.Errorf("%s record %s = %q, want %q", r.Topic, key, got, want)
			}
		}
		if string(r.Key) != "k" || string(r.Value) != "v" {
			t.Errorf("%s record = %s/%s, want k/v", r.Topic, r.Key, r.Value)
		}
	}
	if got, _ := header(second, HeaderAttempt); got != "2" {
		t.Errorf("second retry attempt = %q, want 2", got)
	}
}
