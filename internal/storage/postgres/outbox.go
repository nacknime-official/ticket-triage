package postgres

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/nacknime-official/ticket-triage/internal/outbox"
)

var _ outbox.Store = (*Outbox)(nil)

// maxLastError caps the stored publish error; broker errors are short, this
// only guards the column against a pathological one.
const maxLastError = 1000

// Outbox stores outbox messages in the outbox table.
type Outbox struct {
	q Querier
}

// NewOutbox returns an outbox store that runs its statements on q. Built on
// the pgx.Tx of a state change, Add joins that transaction.
func NewOutbox(q Querier) *Outbox {
	return &Outbox{q: q}
}

// Add inserts m as an unpublished message. Its ID and CreatedAt are ignored;
// the database assigns them.
func (o *Outbox) Add(ctx context.Context, m outbox.Message) error {
	headers := m.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	h, err := json.Marshal(headers)
	if err != nil {
		return fmt.Errorf("postgres: marshal outbox headers: %w", err)
	}
	_, err = o.q.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload, headers) VALUES ($1, $2, $3, $4::jsonb)`,
		m.Topic, m.Key, m.Payload, string(h))
	if err != nil {
		return fmt.Errorf("postgres: insert outbox message for %s: %w", m.Topic, err)
	}
	return nil
}

// claimOutbox locks the oldest unpublished rows; SKIP LOCKED leaves rows
// another relay holds to that relay instead of waiting for them.
const claimOutbox = `
SELECT id, topic, key, payload, headers, created_at
FROM outbox
WHERE published_at IS NULL
ORDER BY id
LIMIT $1
FOR UPDATE SKIP LOCKED`

// PublishPending implements outbox.Store. The claimed rows stay locked until
// the transaction that records the publish outcome commits.
func (o *Outbox) PublishPending(ctx context.Context, limit int, publish outbox.PublishFunc) (int, error) {
	var claimed int
	err := WithTx(ctx, o.q, func(tx pgx.Tx) error {
		msgs, err := claim(ctx, tx, limit)
		if err != nil {
			return err
		}
		claimed = len(msgs)
		if claimed == 0 {
			return nil
		}
		errs := publish(ctx, msgs)
		if len(errs) != len(msgs) {
			return fmt.Errorf("postgres: publish returned %d results for %d outbox messages", len(errs), len(msgs))
		}
		var published, failed []int64
		var reasons []string
		for i, m := range msgs {
			if errs[i] == nil {
				published = append(published, m.ID)
				continue
			}
			failed = append(failed, m.ID)
			reasons = append(reasons, truncate(errs[i].Error(), maxLastError))
		}
		if len(published) > 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, published); err != nil {
				return fmt.Errorf("postgres: mark outbox messages published: %w", err)
			}
		}
		if len(failed) > 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE outbox AS o SET attempts = o.attempts + 1, last_error = f.err
				 FROM unnest($1::bigint[], $2::text[]) AS f(id, err)
				 WHERE o.id = f.id`, failed, reasons); err != nil {
				return fmt.Errorf("postgres: record outbox publish errors: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return claimed, nil
}

func claim(ctx context.Context, tx pgx.Tx, limit int) ([]outbox.Message, error) {
	rows, err := tx.Query(ctx, claimOutbox, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: claim outbox messages: %w", err)
	}
	defer rows.Close()
	var msgs []outbox.Message
	for rows.Next() {
		var (
			m       outbox.Message
			headers []byte
		)
		if err := rows.Scan(&m.ID, &m.Topic, &m.Key, &m.Payload, &headers, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan outbox message: %w", err)
		}
		if err := json.Unmarshal(headers, &m.Headers); err != nil {
			return nil, fmt.Errorf("postgres: outbox message %d headers: %w", m.ID, err)
		}
		if len(m.Headers) == 0 {
			m.Headers = nil
		}
		m.CreatedAt = m.CreatedAt.UTC()
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: claim outbox messages: %w", err)
	}
	return msgs, nil
}

// DeletePublished implements outbox.Store.
func (o *Outbox) DeletePublished(ctx context.Context, before time.Time) (int64, error) {
	tag, err := o.q.Exec(ctx, `DELETE FROM outbox WHERE published_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("postgres: delete published outbox messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
