// Package triage holds the transport-agnostic Processor that classifies one
// pending ticket: skip if not pending, check the daily budget, call the
// classifier with retries, apply the decision policy, and store the result,
// the audit row and the status event in one transaction.
//
// Import boundaries: ticket, classifier and the standard library. It must not
// import net/http, Kafka, or Postgres, so Kafka, River or RabbitMQ drivers can
// all reuse it. The Store it needs is declared here.
package triage
