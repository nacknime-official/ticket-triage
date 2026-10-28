//go:build integration

package migrations_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/nacknime-official/ticket-triage/migrations"
)

// postgresImage matches the Postgres service in compose.yaml.
const postgresImage = "pgvector/pgvector:0.8.7-pg16"

var tables = []string{"classifications", "stream_tokens", "tickets"}

// startPostgres runs a fresh Postgres container for one test and returns its
// connection string.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	ctr, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("triage"),
		tcpostgres.WithUsername("triage"),
		tcpostgres.WithPassword("triage"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	return dsn
}

func newProvider(t *testing.T, dsn string) *goose.Provider {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	p, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("close connection: %v", err)
		}
	})
	return conn
}

// publicTables lists the application tables, leaving out goose's own.
func publicTables(ctx context.Context, t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name <> 'goose_db_version'
		 ORDER BY table_name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	return names
}

func TestUpDownUp(t *testing.T) {
	dsn := startPostgres(t)
	p := newProvider(t, dsn)
	conn := connect(t, dsn)
	ctx := t.Context()
	all := len(p.ListSources())

	checkApplied := func(step string) {
		t.Helper()
		if got := publicTables(ctx, t, conn); !slices.Equal(got, tables) {
			t.Fatalf("%s: tables = %v, want %v", step, got, tables)
		}
		statuses, err := p.Status(ctx)
		if err != nil {
			t.Fatalf("%s: status: %v", step, err)
		}
		for _, s := range statuses {
			if s.State != goose.StateApplied {
				t.Errorf("%s: %s is %s, want applied", step, s.Source.Path, s.State)
			}
		}
	}

	results, err := p.Up(ctx)
	if err != nil {
		t.Fatalf("first up: %v", err)
	}
	if len(results) != all {
		t.Fatalf("first up applied %d migrations, want %d", len(results), all)
	}
	checkApplied("first up")

	results, err = p.Up(ctx)
	if err != nil {
		t.Fatalf("repeated up: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("repeated up applied %d migrations, want 0", len(results))
	}

	results, err = p.DownTo(ctx, 0)
	if err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	if len(results) != all {
		t.Fatalf("down to 0 rolled back %d migrations, want %d", len(results), all)
	}
	if got := publicTables(ctx, t, conn); len(got) != 0 {
		t.Fatalf("after down to 0: tables = %v, want none", got)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != 0 {
		t.Fatalf("after down to 0: version = %d, %v; want 0, nil", v, err)
	}

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("second up: %v", err)
	}
	checkApplied("second up")
}

// migrated starts Postgres, applies every migration and returns a connection.
func migrated(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := startPostgres(t)
	if _, err := newProvider(t, dsn).Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	return connect(t, dsn)
}

func insertTicket(t *testing.T, conn *pgx.Conn) uuid.UUID {
	t.Helper()
	id := uuid.NewV7()
	hash := sha256.Sum256([]byte(id.String()))
	_, err := conn.Exec(t.Context(),
		`INSERT INTO tickets (id, subject, body, access_token_hash) VALUES ($1, 'subject', 'body', $2)`,
		id, hash[:])
	if err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	return id
}

// TestConditionalUpdate checks the idempotent-consumer write from README §4.2:
// only the first result for a pending ticket changes it, and the audit row and
// the update share one transaction.
func TestConditionalUpdate(t *testing.T) {
	conn := migrated(t)
	ctx := t.Context()
	id := insertTicket(t, conn)

	var pending int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE status = 'pending'`).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}

	classify := func() int64 {
		t.Helper()
		var updated int64
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx,
				`UPDATE tickets
				 SET status = 'classified', category = 'billing', category_confidence = 0.91,
				     complaint_p = 0.82, is_complaint = true, urgency = 2.2, urgency_confidence = 0.7,
				     priority = 'high', attempts = attempts + 1,
				     updated_at = now(), classified_at = now()
				 WHERE id = $1 AND status = 'pending'`, id)
			if err != nil {
				return err
			}
			updated = tag.RowsAffected()
			if updated == 0 {
				return nil
			}
			_, err = tx.Exec(ctx,
				`INSERT INTO classifications
				   (ticket_id, provider, model, attempt, outcome, http_status, latency_ms,
				    input_tokens, output_tokens, cost_usd, cost_source,
				    complaint_p, category, category_confidence, urgency, urgency_confidence, raw_response)
				 VALUES ($1, 'openrouter', 'typesafe/jev-1.14', 1, 'ok', 200, 120,
				         450, 0, 0.0000189, 'reported',
				         0.82, 'billing', 0.91, 2.2, 0.7, '{"answers": {}}')`, id)
			return err
		})
		if err != nil {
			t.Fatalf("classify: %v", err)
		}
		return updated
	}

	if got := classify(); got != 1 {
		t.Fatalf("first conditional update changed %d rows, want 1", got)
	}
	if got := classify(); got != 0 {
		t.Fatalf("duplicate conditional update changed %d rows, want 0", got)
	}

	var status string
	var audits int
	err := conn.QueryRow(ctx,
		`SELECT t.status, (SELECT count(*) FROM classifications c WHERE c.ticket_id = t.id)
		 FROM tickets t WHERE t.id = $1`, id).Scan(&status, &audits)
	if err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if status != "classified" || audits != 1 {
		t.Fatalf("status = %q, audit rows = %d; want classified, 1", status, audits)
	}
}

// TestStreamTokenConsume checks the atomic single-use consume from README §4.1.
func TestStreamTokenConsume(t *testing.T) {
	conn := migrated(t)
	ctx := t.Context()
	ticket := insertTicket(t, conn)
	other := insertTicket(t, conn)

	insert := func(token, ttl string) []byte {
		t.Helper()
		hash := sha256.Sum256([]byte(token))
		_, err := conn.Exec(ctx,
			`INSERT INTO stream_tokens (token_hash, ticket_id, expires_at)
			 VALUES ($1, $2, now() + $3::interval)`, hash[:], ticket, ttl)
		if err != nil {
			t.Fatalf("insert stream token: %v", err)
		}
		return hash[:]
	}
	valid := insert("valid", "60 seconds")
	expired := insert("expired", "1 microsecond")
	if _, err := conn.Exec(ctx, `SELECT pg_sleep(0.01)`); err != nil {
		t.Fatalf("sleep: %v", err)
	}

	tests := []struct {
		name     string
		hash     []byte
		ticketID uuid.UUID
		wantOK   bool
	}{
		{"bound to another ticket", valid, other, false},
		{"first use", valid, ticket, true},
		{"second use", valid, ticket, false},
		{"expired", expired, ticket, false},
		{"unknown token", make([]byte, sha256.Size), ticket, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got uuid.UUID
			err := conn.QueryRow(ctx,
				`UPDATE stream_tokens SET used_at = now()
				 WHERE token_hash = $1 AND ticket_id = $2 AND used_at IS NULL AND expires_at > now()
				 RETURNING ticket_id`, tt.hash, tt.ticketID).Scan(&got)
			switch {
			case tt.wantOK && err != nil:
				t.Fatalf("consume: %v", err)
			case tt.wantOK && got != tt.ticketID:
				t.Fatalf("consume returned ticket %v, want %v", got, tt.ticketID)
			case !tt.wantOK && !errors.Is(err, pgx.ErrNoRows):
				t.Fatalf("consume error = %v, want pgx.ErrNoRows", err)
			}
		})
	}
}

// TestConstraints checks that the schema rejects states the README rules out.
func TestConstraints(t *testing.T) {
	conn := migrated(t)
	ctx := t.Context()
	id := insertTicket(t, conn)

	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{"unknown status", `UPDATE tickets SET status = 'resolved' WHERE id = $1`, []any{id}},
		{"needs_human without reason",
			`UPDATE tickets SET status = 'needs_human', classified_at = now() WHERE id = $1`, []any{id}},
		{"reason on classified ticket",
			`UPDATE tickets SET status = 'classified', status_reason = 'low_confidence' WHERE id = $1`, []any{id}},
		{"unknown reason",
			`UPDATE tickets SET status = 'needs_human', status_reason = 'tired' WHERE id = $1`, []any{id}},
		{"short token hash",
			`INSERT INTO tickets (id, subject, body, access_token_hash) VALUES ($1, 's', 'b', '\x00')`,
			[]any{uuid.NewV7()}},
		{"stream token for missing ticket",
			`INSERT INTO stream_tokens (token_hash, ticket_id, expires_at) VALUES ($1, $2, now() + interval '1 minute')`,
			[]any{make([]byte, sha256.Size), uuid.NewV7()}},
		{"cost without source",
			`INSERT INTO classifications (ticket_id, provider, attempt, outcome, latency_ms, cost_usd)
			 VALUES ($1, 'openrouter', 1, 'ok', 10, 0.00002)`, []any{id}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := conn.Exec(ctx, tt.sql, tt.args...)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code[:2] != "23" {
				t.Fatalf("Exec error = %v, want an integrity constraint violation (class 23)", err)
			}
		})
	}
}
