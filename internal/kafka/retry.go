package kafka

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

// Record headers of the retry topic and the DLQ (PRD §4.2). They carry
// only delivery metadata, never ticket text.
const (
	// HeaderAttempt is the number of retry-topic attempts the record has
	// had: 1 on its first trip through tickets.submitted.retry. A record
	// without it is on its first, in-process attempt.
	HeaderAttempt = "attempt"
	// HeaderNotBefore is the time, in RFC 3339 UTC, before which a retry
	// record must not be processed.
	HeaderNotBefore = "not_before"
	// HeaderOriginalTopic, HeaderOriginalPartition and HeaderOriginalOffset
	// locate the record the ticket first arrived in; retries keep them.
	HeaderOriginalTopic     = "original_topic"
	HeaderOriginalPartition = "original_partition"
	HeaderOriginalOffset    = "original_offset"
	// HeaderErrorClass is the ErrorClass of a DLQ record.
	HeaderErrorClass = "error_class"
	// HeaderFailedAt is the time, in RFC 3339 UTC, a record went to the DLQ.
	HeaderFailedAt = "failed_at"
)

// ErrorClass says why a record went to the DLQ.
type ErrorClass string

const (
	// ClassBadPayload: the value is not a Submitted event.
	ClassBadPayload ErrorClass = "bad_payload"
	// ClassBadHeader: the attempt or not_before header does not parse.
	ClassBadHeader ErrorClass = "bad_header"
	// ClassBadID: the ticket id does not parse.
	ClassBadID ErrorClass = "bad_id"
	// ClassNotFound: no ticket has the id.
	ClassNotFound ErrorClass = "not_found"
	// ClassInvalidAnswer: the classifier answered outside the taxonomy.
	ClassInvalidAnswer ErrorClass = "invalid_answer"
	// ClassPermanent: the classifier rejected the request for good.
	ClassPermanent ErrorClass = "permanent"
	// ClassTransient: transient classifier failures outlasted every retry.
	ClassTransient ErrorClass = "transient"
	// ClassUnknown: any other cause.
	ClassUnknown ErrorClass = "unknown"
)

// ErrorClasses returns every ErrorClass.
func ErrorClasses() []ErrorClass {
	return []ErrorClass{
		ClassBadPayload, ClassBadHeader, ClassBadID, ClassNotFound,
		ClassInvalidAnswer, ClassPermanent, ClassTransient, ClassUnknown,
	}
}

// errorClass returns the class of the cause of a Poison or Retryable outcome.
func errorClass(err error) ErrorClass {
	switch {
	case errors.Is(err, triage.ErrBadID):
		return ClassBadID
	case errors.Is(err, ticket.ErrNotFound):
		return ClassNotFound
	case errors.Is(err, classifier.ErrInvalidAnswer):
		return ClassInvalidAnswer
	case errors.Is(err, classifier.ErrPermanent):
		return ClassPermanent
	case errors.Is(err, classifier.ErrTransient):
		return ClassTransient
	}
	return ClassUnknown
}

// Defaults of RetryPolicy.
const (
	DefaultRetryMaxAttempts = 3
	DefaultRetryBackoffBase = 30 * time.Second
	DefaultRetryBackoffMax  = 10 * time.Minute
)

// RetryPolicy configures level 2 of PRD §4.2, the retry topic. Zero
// fields take the defaults.
type RetryPolicy struct {
	// MaxAttempts is the number of trips through the retry topic; a record
	// still retryable after the last one goes to the DLQ.
	MaxAttempts int
	// BackoffBase is the delay before retry attempt 1; each later attempt
	// doubles it, up to BackoffMax.
	BackoffBase time.Duration
	BackoffMax  time.Duration
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = DefaultRetryMaxAttempts
	}
	if p.BackoffBase <= 0 {
		p.BackoffBase = DefaultRetryBackoffBase
	}
	if p.BackoffMax <= 0 {
		p.BackoffMax = DefaultRetryBackoffMax
	}
	return p
}

// delay returns the wait before retry attempt n (from 1): the scheduled
// backoff, or the delay the classifier suggested if that is longer, capped
// at BackoffMax so one record cannot hold its partition for long.
func (p RetryPolicy) delay(n int, suggested time.Duration) time.Duration {
	d := p.BackoffMax
	if n-1 < 63 {
		if s := p.BackoffBase << (n - 1); s > 0 && s < d {
			d = s
		}
	}
	return min(max(d, suggested), p.BackoffMax)
}

// meta is the delivery metadata a record carries in its headers.
type meta struct {
	attempt   int
	notBefore time.Time // zero when the record is due at once
}

// readMeta parses the attempt and not_before headers of r.
func readMeta(r *kgo.Record) (meta, error) {
	var m meta
	if v, ok := header(r, HeaderAttempt); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return meta{}, fmt.Errorf("kafka: bad %s header %q", HeaderAttempt, v)
		}
		m.attempt = n
	}
	if v, ok := header(r, HeaderNotBefore); ok {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return meta{}, fmt.Errorf("kafka: bad %s header %q", HeaderNotBefore, v)
		}
		m.notBefore = t
	}
	return m, nil
}

// header returns the value of the last header of r with key.
func header(r *kgo.Record, key string) (string, bool) {
	for i := len(r.Headers) - 1; i >= 0; i-- {
		if r.Headers[i].Key == key {
			return string(r.Headers[i].Value), true
		}
	}
	return "", false
}

// origin returns the original_* headers for a record derived from r: those
// of r when it has them, so retries keep pointing at the first record, or
// else r's own position.
func origin(r *kgo.Record) []kgo.RecordHeader {
	if topic, ok := header(r, HeaderOriginalTopic); ok {
		partition, _ := header(r, HeaderOriginalPartition)
		offset, _ := header(r, HeaderOriginalOffset)
		return headers(HeaderOriginalTopic, topic, HeaderOriginalPartition, partition, HeaderOriginalOffset, offset)
	}
	return headers(
		HeaderOriginalTopic, r.Topic,
		HeaderOriginalPartition, strconv.Itoa(int(r.Partition)),
		HeaderOriginalOffset, strconv.FormatInt(r.Offset, 10))
}

// headers returns the record headers of alternating keys and values.
func headers(kv ...string) []kgo.RecordHeader {
	hs := make([]kgo.RecordHeader, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		hs = append(hs, kgo.RecordHeader{Key: kv[i], Value: []byte(kv[i+1])})
	}
	return hs
}

// retryRecord returns the retry-topic copy of r for attempt n, due at
// notBefore.
func retryRecord(r *kgo.Record, n int, notBefore time.Time) *kgo.Record {
	hs := append(origin(r), headers(
		HeaderAttempt, strconv.Itoa(n),
		HeaderNotBefore, notBefore.UTC().Format(time.RFC3339Nano))...)
	return &kgo.Record{Topic: TopicSubmittedRetry, Key: r.Key, Value: r.Value, Headers: hs}
}

// dlqRecord returns the DLQ copy of r after attempt retry attempts, failed
// with class at failedAt.
func dlqRecord(r *kgo.Record, attempt int, class ErrorClass, failedAt time.Time) *kgo.Record {
	hs := append(origin(r), headers(
		HeaderAttempt, strconv.Itoa(attempt),
		HeaderErrorClass, string(class),
		HeaderFailedAt, failedAt.UTC().Format(time.RFC3339Nano))...)
	return &kgo.Record{Topic: TopicSubmittedDLQ, Key: r.Key, Value: r.Value, Headers: hs}
}
