// Package ticket is the ticket domain: the Ticket type, its statuses and the
// allowed transitions between them, input validation, UUIDv7 ids, access-token
// issue and verify, and the Service that submits and reads tickets.
//
// Import boundaries: standard library only. It must not import net/http,
// Kafka, Postgres, or any other internal package. The Repository it needs is
// declared here and implemented by storage/postgres.
package ticket
