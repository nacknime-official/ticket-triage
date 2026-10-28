// Command migrate applies the embedded Postgres migrations.
//
// Usage: migrate up|down|status
//
// up applies all pending migrations, down rolls back the latest one, and
// status lists every migration with its state. The database comes from the
// DATABASE_URL environment variable.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"

	"github.com/nacknime-official/ticket-triage/migrations"
)

const usage = "usage: migrate up|down|status"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv("DATABASE_URL"), os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, databaseURL string, out io.Writer) (err error) {
	if len(args) != 1 {
		return errors.New(usage)
	}
	cmd := args[0]
	if cmd != "up" && cmd != "down" && cmd != "status" {
		return fmt.Errorf("unknown command %q; %s", cmd, usage)
	}
	if databaseURL == "" {
		return errors.New("DATABASE_URL is not set")
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close database: %w", cerr)
		}
	}()

	p, err := migrations.NewProvider(db)
	if err != nil {
		return err
	}

	switch cmd {
	case "up":
		results, err := p.Up(ctx)
		if err != nil {
			return fmt.Errorf("up: %w", err)
		}
		if len(results) == 0 {
			_, err = fmt.Fprintln(out, "no pending migrations")
			return err
		}
		for _, r := range results {
			if _, err := fmt.Fprintln(out, r); err != nil {
				return err
			}
		}
	case "down":
		r, err := p.Down(ctx)
		if errors.Is(err, goose.ErrNoNextVersion) {
			_, err = fmt.Fprintln(out, "no applied migrations")
			return err
		}
		if err != nil {
			return fmt.Errorf("down: %w", err)
		}
		_, err = fmt.Fprintln(out, r)
		return err
	case "status":
		statuses, err := p.Status(ctx)
		if err != nil {
			return fmt.Errorf("status: %w", err)
		}
		for _, s := range statuses {
			appliedAt := "-"
			if !s.AppliedAt.IsZero() {
				appliedAt = s.AppliedAt.UTC().Format(time.RFC3339)
			}
			if _, err := fmt.Fprintf(out, "%-8s %-20s %s\n", s.State, appliedAt, s.Source.Path); err != nil {
				return err
			}
		}
	}
	return nil
}
