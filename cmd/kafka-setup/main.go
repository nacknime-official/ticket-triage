// Command kafka-setup creates the Kafka topics the service uses.
//
// Usage: kafka-setup
//
// It creates every missing topic with its partitions and retention and leaves
// existing topics unchanged, so running it again is a no-op. The brokers come
// from the KAFKA_BROKERS environment variable.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"

	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/lifecycle"
)

const (
	clientID = "ticket-triage-kafka-setup"
	// timeout bounds the whole run, so unreachable brokers fail it instead of
	// hanging.
	timeout = 30 * time.Second
)

func main() {
	ctx, stop := lifecycle.SignalContext(context.Background())
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kafka-setup:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: kafka-setup")
	}
	l := config.NewLoader(getenv)
	cfg := l.Kafka()
	if err := l.Err(); err != nil {
		return err
	}

	cl, err := kafka.NewClient(cfg.Brokers, clientID)
	if err != nil {
		return err
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	specs := kafka.Topics()
	created, err := kafka.EnsureTopics(ctx, kadm.NewClient(cl), specs...)
	if err != nil {
		return err
	}
	for _, s := range specs {
		state := "exists"
		if slices.Contains(created, s.Name) {
			state = "created"
		}
		if _, err := fmt.Fprintf(out, "%-8s %s\n", state, s.Name); err != nil {
			return err
		}
	}
	return nil
}
