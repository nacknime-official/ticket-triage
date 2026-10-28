// Command worker classifies submitted tickets: it consumes tickets.submitted
// in the KAFKA_CONSUMER_GROUP group and runs the triage processor on each
// record, committing a batch once all of it is processed.
//
// Usage: worker
//
// Configuration comes from the environment; see .env.example. On SIGINT or
// SIGTERM the worker stops polling, finishes the tickets in flight within
// WORKER_SHUTDOWN_TIMEOUT, commits them, leaves the group and exits; a second
// signal kills it at once. When the classifier rejects its credentials it
// stops without committing the batch and exits non-zero.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/classifier/jev"
	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/lifecycle"
	"github.com/nacknime-official/ticket-triage/internal/logging"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

const clientID = "ticket-triage-worker"

func main() {
	ctx, stop := lifecycle.SignalContext(context.Background())
	// After the first signal, a second one kills the process.
	context.AfterFunc(ctx, stop)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, logOut io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: worker")
	}
	l := config.NewLoader(getenv)
	logCfg, pgCfg, kafkaCfg := l.Log(), l.Postgres(), l.Kafka()
	clsCfg, thrCfg, workerCfg := l.Classifier(), l.Thresholds(), l.Worker()
	if err := l.Err(); err != nil {
		return err
	}
	log := logging.New(logOut, logCfg.Level)
	log.InfoContext(ctx, "worker starting",
		slog.Any("postgres", pgCfg), slog.Any("kafka", kafkaCfg), slog.Any("classifier", clsCfg),
		slog.Any("thresholds", thrCfg), slog.Any("worker", workerCfg))

	cls, err := newClassifier(clsCfg)
	if err != nil {
		return err
	}
	pool, err := postgres.NewPool(ctx, pgCfg.URL.Reveal())
	if err != nil {
		return err
	}
	proc := triage.NewProcessor(postgres.NewTriageStore(pool), cls, triage.Config{
		Thresholds: classifier.Thresholds{
			CategoryMinConfidence:  thrCfg.CategoryMinConfidence,
			UrgencyMinConfidence:   thrCfg.UrgencyMinConfidence,
			ComplaintUncertainLow:  thrCfg.ComplaintUncertainLow,
			ComplaintUncertainHigh: thrCfg.ComplaintUncertainHigh,
			RouteOtherToHuman:      thrCfg.RouteOtherToHuman,
		},
		DailyBudgetUSD: clsCfg.DailyBudgetUSD,
		CallTimeout:    clsCfg.Jev.Timeout,
	}, log)

	cl, err := kafka.NewClient(kafkaCfg.Brokers, clientID, kafka.ConsumerOpts(kafkaCfg.ConsumerGroup)...)
	if err != nil {
		pool.Close()
		return err
	}
	consumer, err := kafka.NewConsumer(cl, proc, kafka.ConsumerConfig{
		Concurrency: workerCfg.Concurrency,
		BatchSize:   workerCfg.BatchSize,
	}, log)
	if err != nil {
		cl.Close()
		pool.Close()
		return err
	}

	runErr := make(chan error, 1)
	go func() { runErr <- consumer.Run(ctx) }()
	var errs []error
	draining := false
	select {
	case err := <-runErr:
		// Run stops on its own only when a record failed, ErrAuth included.
		errs = append(errs, err)
	case <-ctx.Done():
		draining = true
		log.InfoContext(ctx, "worker shutting down", slog.Duration("timeout", workerCfg.ShutdownTimeout))
	}

	errs = append(errs, lifecycle.Shutdown(ctx, workerCfg.ShutdownTimeout,
		lifecycle.Step{Name: "consumer", Stop: consumer.Stop},
		lifecycle.Step{Name: "kafka", Stop: func(ctx context.Context) error {
			defer cl.Close()
			return cl.LeaveGroupContext(ctx)
		}},
		lifecycle.Step{Name: "postgres", Stop: func(context.Context) error {
			pool.Close()
			return nil
		}},
	))
	if draining {
		// Run has returned unless the drain was cut off.
		select {
		case err := <-runErr:
			errs = append(errs, err)
		default:
		}
	}
	return errors.Join(errs...)
}

// newClassifier returns the classifier that CLASSIFIER selects.
func newClassifier(cfg config.Classifier) (classifier.Classifier, error) {
	switch cfg.Kind {
	case config.ClassifierFake:
		return &classifier.Fake{}, nil
	case config.ClassifierJev:
		// No Logger: triage.Processor writes the per-call log line itself.
		c, err := jev.New(jev.Config{
			Provider:             cfg.Jev.Provider,
			APIKey:               cfg.Jev.APIKey.Reveal(),
			Model:                cfg.Jev.Model,
			Timeout:              cfg.Jev.Timeout,
			RateLimit:            cfg.Jev.RateLimit,
			MaxConcurrency:       cfg.Jev.MaxConcurrency,
			InputPricePerMTokUSD: cfg.Jev.InputPricePerMTokUSD,
		})
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	return nil, fmt.Errorf("unknown CLASSIFIER %q", cfg.Kind)
}
