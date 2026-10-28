// Package classifier defines the Classifier interface and its Result, the
// category taxonomy, the Thresholds config, the pure decision policy that maps
// a Result to a ticket status and reason, the errors adapters map failures to
// (ErrTransient, ErrPermanent, ErrAuth, ErrInvalidAnswer), and a deterministic
// fake classifier for tests and local runs without a key.
//
// Import boundaries: ticket (for statuses and reasons) and the standard
// library. It must not import net/http, Kafka, Postgres, or its own adapters
// such as classifier/jev. Its tests also read the eval set through evalset.
package classifier
