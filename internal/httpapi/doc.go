// Package httpapi is the public HTTP surface: the ticket and stream-token
// handlers, the SSE stream, the error model, health checks, middleware for
// limits, client IP, security headers and access logging, and the embedded
// HTML page.
//
// Import boundaries: ticket, notify, net/http, golang.org/x/time/rate and the
// standard library. It must not import Kafka or Postgres; the services it
// calls are declared here as interfaces.
package httpapi
