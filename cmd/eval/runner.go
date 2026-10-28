package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/evalset"
)

// outcome is one eval row and what the classifier answered for it.
type outcome struct {
	Row evalset.Row
	// Result holds the answer when Err is nil; on error only its Call may be
	// filled (a paid invalid answer still has a cost).
	Result classifier.Result
	Err    error
	// Cached means Result came from the cache and no call was made.
	Cached bool
	// Attempts is the number of calls made, 0 when cached.
	Attempts int
}

// runner classifies eval rows with bounded concurrency, retrying transient
// errors and reading and writing the cache. The classifier applies its own
// rate limit and concurrency cap on top.
type runner struct {
	clf classifier.Classifier
	// tax validates every answer, cached ones included.
	tax classifier.Taxonomy
	// provider and model are the cache key of this classifier.
	provider, model string
	// cache is nil when caching is off.
	cache *cache
	// refresh skips cache reads; answers are still written.
	refresh     bool
	concurrency int
	// retries is the number of extra attempts after a transient error.
	retries int
	// backoff is the wait before the first retry; it doubles each retry. A
	// longer Retry-After on the error wins.
	backoff time.Duration
}

// run classifies every row and returns the outcomes in row order. An
// ErrAuth, a cache write failure or a done ctx stops the run with an error;
// any other classifier error is recorded on the row's outcome.
func (r *runner) run(ctx context.Context, rows []evalset.Row) ([]outcome, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	out := make([]outcome, len(rows))
	next := make(chan int)
	var wg sync.WaitGroup
	for range max(1, min(r.concurrency, len(rows))) {
		wg.Go(func() {
			for i := range next {
				// The feed may still hand out a row after the run stopped.
				if ctx.Err() != nil {
					continue
				}
				o, err := r.classify(ctx, rows[i])
				if err != nil {
					cancel(err)
				}
				out[i] = o
			}
		})
	}
feed:
	for i := range rows {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// classify answers one row. The error is fatal to the whole run.
func (r *runner) classify(ctx context.Context, row evalset.Row) (outcome, error) {
	o := outcome{Row: row}
	key := newCacheKey(r.provider, r.model, row)
	if r.cache != nil && !r.refresh {
		// An entry that no longer validates, say after a taxonomy change, is a miss.
		if res, ok := r.cache.get(key); ok && res.Validate(r.tax) == nil {
			o.Result, o.Cached = res, true
			return o, nil
		}
	}

	in := classifier.Input{Subject: row.Subject, Body: row.Body}
	wait := r.backoff
	for {
		o.Attempts++
		o.Result, o.Err = r.clf.Classify(ctx, in)
		if !errors.Is(o.Err, classifier.ErrTransient) || o.Attempts > r.retries || ctx.Err() != nil {
			break
		}
		d := wait
		if ra, ok := errors.AsType[retryAfterer](o.Err); ok && ra.RetryAfter() > d {
			d = ra.RetryAfter()
		}
		if !sleep(ctx, d) {
			break
		}
		wait *= 2
	}
	if o.Err == nil {
		o.Err = o.Result.Validate(r.tax)
	}

	switch {
	case errors.Is(o.Err, classifier.ErrAuth):
		return o, fmt.Errorf("row %s: %w", row.ID, o.Err)
	case o.Err != nil && ctx.Err() != nil:
		return o, context.Cause(ctx)
	case o.Err == nil && r.cache != nil:
		if err := r.cache.put(key, o.Result); err != nil {
			return o, err
		}
	}
	return o, nil
}

// retryAfterer is implemented by classifier errors that carry a Retry-After.
type retryAfterer interface {
	error
	RetryAfter() time.Duration
}

// sleep waits for d and reports whether ctx is still live.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
