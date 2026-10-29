// Package ticket is the ticket domain: the Ticket type, its statuses and the
// allowed transitions between them, input validation, UUIDv7 ids, access-token
// issue and verify, the Submitted and StatusChanged event payloads, the
// Service that submits and reads tickets, and the Streams that issue and
// redeem single-use SSE stream tokens.
//
// Import boundaries: standard library only. It must not import net/http,
// Kafka, Postgres, or any other internal package. The Repository and
// StreamTokenStore it needs are declared here and implemented by
// storage/postgres.
package ticket
