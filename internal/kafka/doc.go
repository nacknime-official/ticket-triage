// Package kafka is the franz-go transport: client setup, topic definitions,
// the outbox Publisher, the classifier consumer loop (batch, bounded pool,
// commit after processing, graceful drain), retry-topic and DLQ routing, the
// DLQ re-drive, the shadow-mode Replayer that reads the history in its own
// group, and the group-less tickets.events reader.
//
// Import boundaries: franz-go, the domain packages it drives, and the
// standard library. It must not import storage/postgres or httpapi.
package kafka
