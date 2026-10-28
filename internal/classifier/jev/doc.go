// Package jev is the classifier adapter for Jev, called through OpenRouter or
// the direct TypeSafe API. It builds the questions, decodes typed answers,
// maps HTTP failures to the classifier errors, applies the per-call timeout,
// the client-side rate limit and a concurrency cap, and reports latency,
// tokens and cost. It can log each call for direct callers; under
// triage.Processor the processor writes the per-call line. It makes exactly
// one HTTP call per Classify; retries belong to the caller.
//
// Every assumption about Jev not yet confirmed by a live call (T12) is marked
// [unverified] and kept in a small decode helper (wire.go, errors.go).
//
// Import boundaries: classifier, net/http, golang.org/x/time/rate and the
// standard library. It must not import Kafka, Postgres, ticket, or triage.
package jev
