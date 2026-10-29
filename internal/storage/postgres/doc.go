// Package postgres is the pgx-based storage: the connection pool, the WithTx
// helper, and the repositories for tickets, classifications, outbox rows,
// stream tokens and shadow classifications that implement the interfaces
// declared by the domain packages.
//
// Import boundaries: pgx, the domain packages whose interfaces it implements,
// and the standard library. It must not import net/http, Kafka, or httpapi.
package postgres
