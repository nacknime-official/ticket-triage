package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// SignalContext returns a copy of parent that is canceled on SIGINT or
// SIGTERM. After the first signal, call stop so a second signal kills the
// process with the default behavior instead of waiting for shutdown.
func SignalContext(parent context.Context) (ctx context.Context, stop context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// Step is one component to stop, such as an HTTP server, a consumer, or a
// connection pool.
type Step struct {
	Name string
	// Stop must return once ctx is done; Shutdown abandons it at the deadline.
	Stop func(ctx context.Context) error
}

// Shutdown runs the steps in order, sharing one deadline timeout from now.
// It keeps the values of ctx but not its cancellation, because shutdown
// usually starts after the signal context is canceled.
//
// A failing step does not stop the ones after it. Once the deadline passes,
// Shutdown returns without running the remaining steps; a step still running
// is left to finish on its own. The error joins every step error, each
// prefixed with the step name, and wraps context.DeadlineExceeded on timeout.
func Shutdown(ctx context.Context, timeout time.Duration, steps ...Step) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	var errs []error
	for i, step := range steps {
		done := make(chan error, 1) // buffered: an abandoned step never blocks on send
		go func() { done <- step.Stop(ctx) }()

		select {
		case err := <-done:
			if err != nil {
				errs = append(errs, fmt.Errorf("stop %s: %w", step.Name, err))
			}
			if ctx.Err() == nil {
				continue
			}
		case <-ctx.Done():
			select {
			case err := <-done: // returned at the deadline
				if err != nil {
					errs = append(errs, fmt.Errorf("stop %s: %w", step.Name, err))
				}
			default:
				errs = append(errs, fmt.Errorf("stop %s: abandoned", step.Name))
			}
		}

		var skipped []string
		for _, s := range steps[i+1:] {
			skipped = append(skipped, s.Name)
		}
		errs = append(errs, fmt.Errorf("shutdown timed out after %s, skipped %q: %w",
			timeout, skipped, context.DeadlineExceeded))
		break
	}
	return errors.Join(errs...)
}
