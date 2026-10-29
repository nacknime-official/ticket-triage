// Command worker classifies submitted tickets: it consumes tickets.submitted
// in the KAFKA_CONSUMER_GROUP group and runs the triage processor on each
// record, committing a batch once all of it is processed.
//
// A ticket that fails transiently goes to tickets.submitted.retry, which the
// worker consumes too, in the KAFKA_RETRY_CONSUMER_GROUP group, once each
// record is due. After WORKER_RETRY_MAX_ATTEMPTS retries, or at once for a
// poison record, the ticket goes to needs_human and its record to
// tickets.submitted.dlq; cmd/redrive re-publishes DLQ records by hand.
//
// Usage: worker
//
// Configuration comes from the environment; see .env.example. On SIGINT or
// SIGTERM the worker stops polling, finishes the tickets in flight within
// WORKER_SHUTDOWN_TIMEOUT, commits them, leaves the groups and exits; a
// second signal kills it at once. When the classifier rejects its
// credentials it stops without committing the batch and exits non-zero.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/classifier/jev"
	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/lifecycle"
	"github.com/nacknime-official/ticket-triage/internal/logging"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/triage"
)

const (
	clientID      = "ticket-triage-worker"
	retryClientID = "ticket-triage-worker-retry"
)

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
	proc := triage.NewProcessor(postgres.NewTriageStore(pool, kafka.TopicEvents), cls, triage.Config{
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

	consumerCfg := kafka.ConsumerConfig{
		Concurrency: workerCfg.Concurrency,
		BatchSize:   workerCfg.BatchSize,
		Retry: kafka.RetryPolicy{
			MaxAttempts: workerCfg.RetryMaxAttempts,
			BackoffBase: workerCfg.RetryBackoffBase,
			BackoffMax:  workerCfg.RetryBackoffMax,
		},
	}
	primary, err := newConsumer(kafkaCfg.Brokers, clientID, kafka.ConsumerOpts(kafkaCfg.ConsumerGroup), proc, consumerCfg, log)
	if err != nil {
		pool.Close()
		return err
	}
	retry, err := newConsumer(kafkaCfg.Brokers, retryClientID,
		kafka.RetryConsumerOpts(kafkaCfg.RetryConsumerGroup), proc, consumerCfg, log)
	if err != nil {
		primary.cl.Close()
		pool.Close()
		return err
	}
	consumers := []consumer{primary, retry}

	runErr := make(chan error, len(consumers))
	for _, c := range consumers {
		go func() { runErr <- c.Run(ctx) }()
	}
	running := len(consumers)
	var errs []error
	select {
	case err := <-runErr:
		// Run stops on its own only when a record failed, ErrAuth included.
		running--
		errs = append(errs, err)
	case <-ctx.Done():
		log.InfoContext(ctx, "worker shutting down", slog.Duration("timeout", workerCfg.ShutdownTimeout))
	}

	errs = append(errs, lifecycle.Shutdown(ctx, workerCfg.ShutdownTimeout,
		lifecycle.Step{Name: "consumers", Stop: func(ctx context.Context) error {
			return each(consumers, func(c consumer) error { return c.Stop(ctx) })
		}},
		lifecycle.Step{Name: "kafka", Stop: func(ctx context.Context) error {
			return each(consumers, func(c consumer) error {
				defer c.cl.Close()
				return c.cl.LeaveGroupContext(ctx)
			})
		}},
		lifecycle.Step{Name: "postgres", Stop: func(context.Context) error {
			pool.Close()
			return nil
		}},
	))
	// Every Run has returned unless its drain was cut off.
	for range running {
		select {
		case err := <-runErr:
			errs = append(errs, err)
		default:
		}
	}
	return errors.Join(errs...)
}

// consumer is a kafka.Consumer with the client it polls with.
type consumer struct {
	*kafka.Consumer
	cl *kgo.Client
}

// newConsumer returns a consumer whose client is built with opts.
func newConsumer(brokers []string, clientID string, opts []kgo.Opt, proc kafka.Processor,
	cfg kafka.ConsumerConfig, log *slog.Logger,
) (consumer, error) {
	cl, err := kafka.NewClient(brokers, clientID, opts...)
	if err != nil {
		return consumer{}, err
	}
	c, err := kafka.NewConsumer(cl, proc, cfg, log)
	if err != nil {
		cl.Close()
		return consumer{}, err
	}
	return consumer{Consumer: c, cl: cl}, nil
}

// each runs fn for every consumer at once and joins the errors.
func each(cs []consumer, fn func(consumer) error) error {
	errs := make([]error, len(cs))
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Go(func() { errs[i] = fn(c) })
	}
	wg.Wait()
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
