// Package notify is the in-process fan-out of ticket status events: a Hub
// that lets SSE handlers subscribe by ticket id and receive the events read
// from tickets.events.
//
// Import boundaries: ticket and the standard library. It must not import
// net/http, Kafka, or Postgres; the events reader in kafka feeds it and
// httpapi consumes it.
package notify
