// Command redrive re-publishes records of tickets.submitted.dlq to
// tickets.submitted, so the worker processes their tickets again.
//
// Usage: redrive [-dry-run] [-class class,...] [-ticket id,...]
//
// It reads the DLQ from the start up to its end when the run starts, prints
// one line per record the filters match (partition, offset, ticket id, error
// class, attempts, failed_at and original record; never the payload), and,
// unless -dry-run, re-publishes each match with its key and value as a first
// attempt. The DLQ is left as it is.
//
// The worker skips a ticket that is no longer pending, and a ticket in the
// DLQ usually went to needs_human first (README §4.2). Re-drive is for a
// ticket that is still pending or was set back to pending by hand.
//
// The brokers come from the KAFKA_BROKERS environment variable.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/lifecycle"
)

const (
	clientID = "ticket-triage-redrive"
	// timeout bounds the whole run, so unreachable brokers fail it instead of
	// hanging.
	timeout = 5 * time.Minute
)

func main() {
	ctx, stop := lifecycle.SignalContext(context.Background())
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "redrive:", err)
		os.Exit(1)
	}
}

type options struct {
	dryRun bool
	filter kafka.RedriveFilter
}

func parse(args []string, stderr io.Writer) (options, error) {
	var (
		o              options
		classes, ticks string
	)
	fs := flag.NewFlagSet("redrive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.dryRun, "dry-run", false, "only print the matching records")
	fs.StringVar(&classes, "class", "", "comma-separated error classes to re-drive (default all)")
	fs.StringVar(&ticks, "ticket", "", "comma-separated ticket ids to re-drive (default all)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: redrive [-dry-run] [-class class,...] [-ticket id,...]\nclasses: %s\n",
			strings.Join(classNames(), ", "))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	for _, c := range split(classes) {
		if !slices.Contains(kafka.ErrorClasses(), kafka.ErrorClass(c)) {
			return o, fmt.Errorf("unknown -class %q, want one of %s", c, strings.Join(classNames(), ", "))
		}
		o.filter.Classes = append(o.filter.Classes, kafka.ErrorClass(c))
	}
	o.filter.TicketIDs = split(ticks)
	return o, nil
}

func classNames() []string {
	var names []string
	for _, c := range kafka.ErrorClasses() {
		names = append(names, string(c))
	}
	return names
}

// split returns the non-empty, trimmed items of a comma-separated list.
func split(list string) []string {
	var items []string
	for item := range strings.SplitSeq(list, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func run(ctx context.Context, args []string, getenv func(string) string, out, stderr io.Writer) error {
	o, err := parse(args, stderr)
	if err != nil {
		return err
	}
	l := config.NewLoader(getenv)
	cfg := l.Kafka()
	if err := l.Err(); err != nil {
		return err
	}

	cl, err := kafka.NewClient(cfg.Brokers, clientID, kafka.RedriveOpts()...)
	if err != nil {
		return err
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	verb, summary := "redriven", "re-driven"
	if o.dryRun {
		verb, summary = "would-redrive", "matched (dry run)"
	}
	n := 0
	err = kafka.Redrive(ctx, cl, o.filter, o.dryRun, func(d kafka.DeadLetter) error {
		n++
		failedAt := "-"
		if !d.FailedAt.IsZero() {
			failedAt = d.FailedAt.UTC().Format(time.RFC3339)
		}
		_, err := fmt.Fprintf(out, "%s partition=%d offset=%d ticket=%s class=%s attempt=%d failed_at=%s original=%s\n",
			verb, d.Partition, d.Offset, d.TicketID, d.Class, d.Attempt, failedAt, d.Origin)
		return err
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%d records %s\n", n, summary)
	return err
}
