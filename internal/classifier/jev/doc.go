// Package jev is the classifier adapter for Jev, called through OpenRouter or
// the direct TypeSafe API. It builds the questions, decodes typed answers,
// maps HTTP failures to the classifier errors, applies the per-call timeout
// and client-side rate limit, and reports latency, tokens and cost.
//
// Import boundaries: classifier, net/http, golang.org/x/time/rate and the
// standard library. It must not import Kafka, Postgres, ticket, or triage.
package jev
