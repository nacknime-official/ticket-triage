package main

// "worker shadow" re-classifies history in shadow mode (README §6.1, T24).
//
// Usage: worker shadow [-run-id ID] [-from earliest|RFC3339] [-max-tickets N] [-dry-run]
//
// It reads tickets.submitted in its own consumer group, triage-shadow-<run
// id>: from the earliest offset or the first record at or after -from for a
// new run, or from where an earlier invocation with the same -run-id stopped.
// Each ticket is classified with the classifier, JEV_MODEL and thresholds of
// the environment, and the result goes only to shadow_classifications, keyed
// by run and ticket. A replay never changes tickets, never writes the
// classifications audit table, never produces to Kafka, and never commits the
// workers' group. It stops at the end of the history as it was at start,
// after -max-tickets records, once the daily budget (DAILY_BUDGET_USD, shared
// with the live flow) is spent, or on SIGINT/SIGTERM, and then prints a
// report comparing the shadow results with the live classification to
// stdout. -dry-run uses the fake classifier and writes and commits nothing.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/logging"
	"github.com/nacknime-official/ticket-triage/internal/shadow"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
)

const shadowClientID = "ticket-triage-shadow"

// defaultMaxTickets bounds a run when -max-tickets is not given.
const defaultMaxTickets = 100

// shadowFlags are the flags of "worker shadow".
type shadowFlags struct {
	runID      string
	from       time.Time // zero means the earliest offset
	maxTickets int
	dryRun     bool
}

// parseShadowFlags parses args; now names the default run.
func parseShadowFlags(args []string, now time.Time, out io.Writer) (shadowFlags, error) {
	fs := flag.NewFlagSet("worker shadow", flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		f    shadowFlags
		from string
	)
	fs.StringVar(&f.runID, "run-id", strings.ToLower(now.UTC().Format("20060102t150405z")),
		"replay run id; reuse it to resume a run (default: the start time)")
	fs.StringVar(&from, "from", "earliest", `start of a new run: "earliest" or an RFC 3339 time`)
	fs.IntVar(&f.maxTickets, "max-tickets", defaultMaxTickets, "max records replayed by this invocation")
	fs.BoolVar(&f.dryRun, "dry-run", false, "use the fake classifier; write and commit nothing")
	if err := fs.Parse(args); err != nil {
		return shadowFlags{}, fmt.Errorf("worker shadow: %w", err)
	}
	if fs.NArg() != 0 {
		return shadowFlags{}, fmt.Errorf("worker shadow: unexpected argument %q", fs.Arg(0))
	}
	if !shadow.ValidRunID(f.runID) {
		return shadowFlags{}, fmt.Errorf("worker shadow: -run-id %q: want 1 to 63 of a-z, 0-9, '.', '_', '-', "+
			"starting with a letter or digit", f.runID)
	}
	if from != "earliest" {
		t, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return shadowFlags{}, fmt.Errorf(`worker shadow: -from %q: want "earliest" or an RFC 3339 time`, from)
		}
		f.from = t
	}
	if f.maxTickets < 1 {
		return shadowFlags{}, fmt.Errorf("worker shadow: -max-tickets %d: must be at least 1", f.maxTickets)
	}
	return f, nil
}

// runShadow runs "worker shadow" with args; the report goes to out and the
// logs to logOut.
func runShadow(ctx context.Context, args []string, getenv func(string) string, out, logOut io.Writer) error {
	f, err := parseShadowFlags(args, time.Now(), logOut)
	if err != nil {
		return err
	}
	l := config.NewLoader(getenv)
	logCfg, pgCfg, kafkaCfg := l.Log(), l.Postgres(), l.Kafka()
	clsCfg, thrCfg, workerCfg := l.Classifier(), l.Thresholds(), l.Worker()
	if err := l.Err(); err != nil {
		return err
	}
	group := kafka.ReplayGroup(f.runID)
	switch group {
	case kafkaCfg.ConsumerGroup:
		return fmt.Errorf("worker shadow: replay group %s is KAFKA_CONSUMER_GROUP", group)
	case kafkaCfg.RetryConsumerGroup:
		return fmt.Errorf("worker shadow: replay group %s is KAFKA_RETRY_CONSUMER_GROUP", group)
	}
	from := "earliest"
	if !f.from.IsZero() {
		from = f.from.Format(time.RFC3339)
	}
	log := logging.New(logOut, logCfg.Level)
	log.InfoContext(ctx, "shadow replay starting",
		slog.String("run_id", f.runID), slog.String("group", group), slog.String("from", from),
		slog.Int("max_tickets", f.maxTickets), slog.Bool("dry_run", f.dryRun),
		slog.Any("postgres", pgCfg), slog.Any("kafka", kafkaCfg), slog.Any("classifier", clsCfg),
		slog.Any("thresholds", thrCfg), slog.Any("worker", workerCfg))

	var cls classifier.Classifier = &classifier.Fake{}
	if !f.dryRun {
		if cls, err = newClassifier(clsCfg); err != nil {
			return err
		}
	}
	pool, err := postgres.NewPool(ctx, pgCfg.URL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()
	proc, err := shadow.NewProcessor(postgres.NewShadowStore(pool), cls, shadow.Config{
		RunID: f.runID,
		Thresholds: classifier.Thresholds{
			CategoryMinConfidence:  thrCfg.CategoryMinConfidence,
			UrgencyMinConfidence:   thrCfg.UrgencyMinConfidence,
			ComplaintUncertainLow:  thrCfg.ComplaintUncertainLow,
			ComplaintUncertainHigh: thrCfg.ComplaintUncertainHigh,
			RouteOtherToHuman:      thrCfg.RouteOtherToHuman,
		},
		DailyBudgetUSD: clsCfg.DailyBudgetUSD,
		DryRun:         f.dryRun,
		CallTimeout:    clsCfg.Jev.Timeout,
	}, log)
	if err != nil {
		return err
	}

	cl, err := kafka.NewClient(kafkaCfg.Brokers, shadowClientID, kafka.ReplayOpts(group, f.from)...)
	if err != nil {
		return err
	}
	defer cl.Close()
	rp, err := kafka.NewReplayer(cl, proc, kafka.ReplayConfig{
		Concurrency: workerCfg.Concurrency,
		BatchSize:   workerCfg.BatchSize,
		From:        f.from,
		MaxRecords:  f.maxTickets,
		NoCommit:    f.dryRun,
	}, log)
	if err != nil {
		return err
	}

	res, runErr := rp.Run(ctx)
	stop := res.Stop.String()
	if errors.Is(runErr, shadow.ErrBudgetExhausted) {
		stop, runErr = "daily budget exhausted", nil
	} else if runErr != nil {
		stop = "failed"
	}
	leaveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerCfg.ShutdownTimeout)
	defer cancel()
	leaveErr := cl.LeaveGroupContext(leaveCtx)
	if leaveErr != nil {
		leaveErr = fmt.Errorf("worker shadow: leave group %s: %w", group, leaveErr)
	}
	log.InfoContext(ctx, "shadow replay stopped",
		slog.String("run_id", f.runID), slog.String("stop", stop), slog.Int("records", res.Records))

	rep := proc.Report()
	_, err = fmt.Fprintf(out, "Stopped: %s after %d records (group %s)\n", stop, res.Records, group)
	if err == nil {
		err = rep.Write(out)
	}
	if err != nil {
		err = fmt.Errorf("worker shadow: write report: %w", err)
	}
	return errors.Join(runErr, leaveErr, err)
}
