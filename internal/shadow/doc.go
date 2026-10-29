// Package shadow re-classifies history in shadow mode (README §6.1, roadmap
// T24): its Processor classifies a ticket with the model and thresholds of a
// replay run and stores the result only as a shadow row keyed by run and
// ticket. It never changes a ticket, never writes the classifications audit
// table and never publishes anything. The run respects the daily budget,
// which live and shadow calls share. Report compares the shadow results with
// the live classification of the same tickets.
//
// Import boundaries: ticket, classifier, and the standard library. It must
// not import net/http, Kafka, or Postgres; the Store it needs is declared
// here, and the replay transport lives in the kafka package.
package shadow
