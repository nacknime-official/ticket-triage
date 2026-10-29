// Package e2e holds the end-to-end test of README §9: a ticket submitted over
// HTTP goes through the outbox, the relay and Kafka to the classifier worker
// and comes back classified. The test runs behind the integration build tag
// against Postgres and Kafka containers, with the API and the worker composed
// in-process around the fake classifier.
//
// Import boundaries: test code only; it may import every internal package,
// pgx, franz-go and testcontainers. Production code never imports it.
package e2e
