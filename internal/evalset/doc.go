// Package evalset defines the schema of the labeled eval set in testdata/eval
// (one JSON row per ticket with the expected category, complaint flag and
// urgency), and reads, validates and writes its JSONL files. cmd/eval-import
// writes rows with it and cmd/eval reads them.
//
// Import boundaries: the standard library only.
package evalset
