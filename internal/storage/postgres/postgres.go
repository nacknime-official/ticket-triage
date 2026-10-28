package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier runs statements. *pgxpool.Pool, *pgx.Conn and pgx.Tx all satisfy
// it, so a repository built on a pgx.Tx from WithTx joins that transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Beginner starts transactions; *pgxpool.Pool and *pgx.Conn satisfy it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// NewPool connects a pool to the database at url and pings it. url carries
// the password, so it never appears in the returned errors.
func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// The parse error can quote the URL; drop it.
		return nil, errors.New("postgres: parse database url: invalid connection string")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: new pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// WithTx runs fn in a transaction on db. It commits when fn returns nil and
// rolls back when fn returns an error or panics; fn's error is returned as is.
func WithTx(ctx context.Context, db Beginner, fn func(tx pgx.Tx) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() {
		// After a commit this is a no-op; the rollback error of a failed fn is
		// less useful than fn's own, so it is dropped.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}
