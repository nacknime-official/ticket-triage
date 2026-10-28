// Package triage holds the transport-agnostic Processor that classifies one
// pending ticket: skip if not pending, check the daily budget, call the
// classifier with a per-call timeout and retries, apply the decision policy,
// and store the result with one audit row per call in one transaction. It
// returns a typed Outcome (Done, Skipped, Retryable, Poison) for the
// transport, and an error wrapping classifier.ErrAuth to stop consumption.
//
// Import boundaries: ticket, classifier and the standard library. It must not
// import net/http, Kafka, or Postgres, so Kafka, River or RabbitMQ drivers can
// all reuse it. The Store it needs is declared here.
package triage
