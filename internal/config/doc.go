// Package config loads process configuration from environment variables into
// typed structs grouped by consumer (Log, HTTP, Postgres, Kafka, Outbox,
// Classifier, Thresholds, Limits), so each cmd/* reads only the groups it needs. A Loader
// applies the defaults from .env.example, validates every value, and reports
// every invalid or missing variable by name in one *Error.
//
// Secrets (API keys, the database URL) are held as Secret, which never prints
// its value through fmt, slog or JSON; only Reveal returns it.
//
// Import boundaries: standard library only. It must not import any other
// internal package; cmd/* passes the values it needs to each component.
package config
