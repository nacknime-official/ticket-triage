// Package outbox implements the transactional-outbox Relay: it claims
// unpublished outbox rows, publishes them, and marks them published, so no
// code path ever writes to the database and the broker separately.
//
// Import boundaries: standard library only. The Store and Publisher it needs
// are declared here and implemented by storage/postgres and kafka.
package outbox
