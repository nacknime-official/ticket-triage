// Package jev is the classifier adapter for Jev, called through OpenRouter or
// the direct TypeSafe API. It builds the questions, decodes typed answers,
// maps HTTP failures to the classifier errors, applies the per-call timeout,
// the client-side rate limit and a concurrency cap, and reports latency,
// tokens and cost. It can log each call for direct callers; under
// triage.Processor the processor writes the per-call line. It makes exactly
// one HTTP call per Classify; retries belong to the caller.
//
// The wire format was confirmed against OpenRouter by cmd/jevprobe (T12,
// 2026-10-28); testdata/live holds those recordings. What it could not
// confirm (the direct API's success response and model naming, Retry-After
// on 429 or 529) is marked [unverified] and kept in a small decode helper
// (wire.go, errors.go).
//
// Import boundaries: classifier, net/http, golang.org/x/time/rate and the
// standard library. It must not import Kafka, Postgres, ticket, or triage.
package jev
