// Command api serves the public HTTP API: POST /tickets, GET /tickets/{id},
// POST /tickets/{id}/stream-token, the SSE stream GET /tickets/{id}/events,
// GET /healthz, GET /readyz and the built-in page on GET /, behind the
// limits of PRD §4.1. It runs the outbox relay that publishes submitted
// tickets to Kafka, and reads tickets.events without a consumer group into
// the hub that feeds the SSE streams. A second listener, on a loopback address only, serves pprof,
// including the goroutineleak profile.
//
// Usage: api
//
// Configuration comes from the environment; .env.example lists every
// variable. SIGINT or SIGTERM starts a graceful shutdown; a second signal
// kills the process.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/httpapi"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/lifecycle"
	"github.com/nacknime-official/ticket-triage/internal/logging"
	"github.com/nacknime-official/ticket-triage/internal/notify"
	"github.com/nacknime-official/ticket-triage/internal/outbox"
	"github.com/nacknime-official/ticket-triage/internal/storage/postgres"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

const (
	clientID = "ticket-triage-api"
	// startupTimeout bounds connecting to Postgres, so an unreachable
	// database fails the start instead of hanging it.
	startupTimeout = 10 * time.Second
	// shutdownTimeout bounds the whole graceful shutdown.
	shutdownTimeout = 20 * time.Second
)

func main() {
	ctx, stop := lifecycle.SignalContext(context.Background())
	err := run(ctx, stop, os.Getenv, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

// run serves until ctx is done or a listener fails, then shuts down. It
// calls stopSignals once shutdown starts, so a second signal kills the
// process instead of waiting.
func run(ctx context.Context, stopSignals func(), getenv func(string) string, logOut io.Writer) error {
	l := config.NewLoader(getenv)
	logCfg, httpCfg, pgCfg, kafkaCfg, outboxCfg, limits := l.Log(), l.HTTP(), l.Postgres(), l.Kafka(), l.Outbox(),
		l.Limits()
	if err := l.Err(); err != nil {
		return err
	}
	logger := logging.New(logOut, logCfg.Level)

	// Listen before connecting, so a taken port fails the start at once.
	var lc net.ListenConfig
	apiLn, err := lc.Listen(ctx, "tcp", httpCfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on HTTP_ADDR: %w", err)
	}
	// Serve and Shutdown close the listeners too; a second close is harmless.
	defer func() { _ = apiLn.Close() }()
	debugLn, err := lc.Listen(ctx, "tcp", httpCfg.DebugAddr)
	if err != nil {
		return fmt.Errorf("listen on DEBUG_ADDR: %w", err)
	}
	defer func() { _ = debugLn.Close() }()

	// The client connects lazily, so a broker that is down delays publishing
	// and status events but not the API; the relay retries every poll. The
	// delivery timeout bounds a record's produce independently of the batch
	// context. The same client reads tickets.events, without a group.
	kafkaClient, err := kafka.NewClient(kafkaCfg.Brokers, clientID,
		append(kafka.EventsOpts(), kgo.RecordDeliveryTimeout(outboxCfg.PublishTimeout))...)
	if err != nil {
		return err
	}
	startCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	pool, err := postgres.NewPool(startCtx, pgCfg.URL.Reveal())
	cancel()
	if err != nil {
		kafkaClient.Close()
		return err
	}

	tickets := postgres.NewTickets(pool, kafka.TopicSubmitted)
	svc := ticket.NewService(tickets, ticket.Config{MaxPending: limits.PendingCap})
	streams := ticket.NewStreams(svc, postgres.NewStreamTokens(pool), limits.StreamTokenTTL)
	hub := notify.NewHub(0)
	events, err := kafka.NewEventsReader(kafkaClient, hub, logger)
	if err != nil {
		kafkaClient.Close()
		pool.Close()
		return err
	}
	timeouts := httpapi.ServerTimeouts{
		ReadHeader: httpCfg.ReadHeaderTimeout,
		Read:       httpCfg.ReadTimeout,
		Idle:       httpCfg.IdleTimeout,
	}
	apiSrv := httpapi.NewServer(httpCfg.Addr, newHandler(svc, streams, hub, pool, httpCfg, limits, logger),
		timeouts, logger)
	debugSrv := httpapi.NewServer(httpCfg.DebugAddr, newDebugHandler(), timeouts, logger)

	serveErr := make(chan error, 2) // buffered: a failing server never blocks on send
	var wg sync.WaitGroup
	for _, s := range []struct {
		name string
		srv  *http.Server
		ln   net.Listener
	}{{"http server", apiSrv, apiLn}, {"debug server", debugSrv, debugLn}} {
		wg.Go(func() {
			if err := s.srv.Serve(s.ln); !errors.Is(err, http.ErrServerClosed) {
				serveErr <- fmt.Errorf("%s: %w", s.name, err)
			}
		})
	}

	relay := outbox.NewRelay(postgres.NewOutbox(pool), kafka.NewPublisher(kafkaClient), outbox.Config{
		PollInterval:    outboxCfg.PollInterval,
		BatchSize:       outboxCfg.BatchSize,
		PublishTimeout:  outboxCfg.PublishTimeout,
		Retention:       outboxCfg.Retention,
		CleanupInterval: outboxCfg.CleanupInterval,
	}, logger)
	// A signal does not cancel the relay: the shutdown steps stop it after
	// the HTTP server, so tickets submitted during the drain are published.
	relay.Start(context.WithoutCancel(ctx))
	events.Start(context.WithoutCancel(ctx))

	logger.Info("api started",
		slog.String("addr", apiLn.Addr().String()),
		slog.String("debug_addr", debugLn.Addr().String()),
		slog.Any("http", httpCfg),
		slog.Any("postgres", pgCfg),
		slog.Any("kafka", kafkaCfg),
		slog.Any("outbox", outboxCfg),
	)

	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("shutting down", slog.Any("cause", context.Cause(ctx)))
	case runErr = <-serveErr:
		logger.Error("shutting down after a listener failed", slog.Any("error", runErr))
	}
	stopSignals()

	// Closing the hub ends the SSE streams, which Shutdown would otherwise
	// wait on for up to SSE_MAX_DURATION; their clients reconnect to another
	// instance. The HTTP server goes next, so no new ticket reaches the
	// outbox; then the events reader stops, the relay finishes its batch and
	// the Kafka client closes. The debug server stays up until the end to
	// profile a slow drain.
	shutdownErr := lifecycle.Shutdown(ctx, shutdownTimeout,
		lifecycle.Step{Name: "sse streams", Stop: func(context.Context) error {
			hub.Close()
			return nil
		}},
		lifecycle.Step{Name: "http server", Stop: apiSrv.Shutdown},
		lifecycle.Step{Name: "events reader", Stop: events.Stop},
		lifecycle.Step{Name: "outbox relay", Stop: relay.Stop},
		lifecycle.Step{Name: "kafka client", Stop: func(context.Context) error {
			kafkaClient.Close()
			return nil
		}},
		lifecycle.Step{Name: "debug server", Stop: debugSrv.Shutdown},
		lifecycle.Step{Name: "postgres pool", Stop: func(context.Context) error {
			pool.Close()
			return nil
		}},
	)
	// After a timed-out shutdown a server may still be serving; Close stops
	// it so its Serve goroutine returns. On a stopped server it is a no-op
	// whose error says nothing new.
	_ = apiSrv.Close()
	_ = debugSrv.Close()
	wg.Wait()
	if shutdownErr != nil {
		logger.Error("shutdown", slog.Any("error", shutdownErr))
	} else {
		logger.Info("shutdown complete")
	}
	return errors.Join(runErr, shutdownErr)
}

// eventsRoute is the SSE route, the only streaming one.
const eventsRoute = "GET /tickets/{id}/events"

// newHandler composes the public API with the T08 middleware. The client IP
// is resolved first, so the limiters and the logs see the real client.
func newHandler(svc httpapi.TicketService, streams httpapi.StreamService, hub *notify.Hub, ready httpapi.Pinger,
	httpCfg config.HTTP, limits config.Limits, logger *slog.Logger,
) http.Handler {
	submitLimiter := httpapi.NewRateLimiter(httpapi.RateLimit{
		PerMinute: limits.SubmitRatePerMin,
		Burst:     limits.SubmitBurst,
	})
	streamTokenLimiter := httpapi.NewRateLimiter(httpapi.RateLimit{
		PerMinute: limits.StreamTokenRatePerMin,
		Burst:     limits.StreamTokenBurst,
	})
	streamLimiter := httpapi.NewStreamLimiter(httpapi.StreamLimit{
		PerClient: limits.SSEMaxPerIP,
		Global:    limits.SSEMaxGlobal,
	})
	sse := httpapi.Streams{
		Service:      streams,
		Hub:          hub,
		Heartbeat:    limits.SSEHeartbeat,
		MaxDuration:  limits.SSEMaxDuration,
		WriteTimeout: httpCfg.WriteTimeout,
	}
	return httpapi.NewHandler(svc, sse, ready, logger,
		httpapi.NewClientIP(httpCfg.TrustedProxies).Middleware,
		httpapi.SecurityHeaders,
		httpapi.ForRoute("POST /tickets", submitLimiter.Middleware),
		httpapi.ForRoute("POST /tickets/{id}/stream-token", streamTokenLimiter.Middleware),
		httpapi.ForRoute(eventsRoute, streamLimiter.Middleware),
		httpapi.LimitBody(limits.MaxBodyBytes),
		// The SSE handler sets a write deadline per event instead.
		httpapi.ExceptRoute(eventsRoute, httpapi.WriteDeadline(httpCfg.WriteTimeout, logger)),
	)
}

// newDebugHandler serves pprof under /debug/pprof/. Index serves every named
// profile, goroutineleak included. The net/http/pprof import also registers
// these on http.DefaultServeMux, which this process never serves.
func newDebugHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}
