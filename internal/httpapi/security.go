package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// contentSecurityPolicy allows only same-origin resources and forbids
// plugins, framing, <base> rewriting and cross-origin form posts.
const contentSecurityPolicy = "default-src 'self'; object-src 'none'; base-uri 'none'; " +
	"frame-ancestors 'none'; form-action 'self'"

// SecurityHeaders sets the response headers of PRD §4.1 on every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// LimitBody caps the request body at maxBytes. A declared Content-Length
// above the cap is refused with 413 before the handler runs; a body that
// turns out longer while being read fails with *http.MaxBytesError, which
// the handler maps to 413.
func LimitBody(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge,
					fmt.Sprintf("request body must be at most %d bytes", maxBytes))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// WriteDeadline gives a non-streaming handler d to write its response, since
// the server has no WriteTimeout (PRD §4.1). Every ResponseWriter wrapper
// between the server and this middleware must implement
// Unwrap() http.ResponseWriter. If the deadline cannot be set the request
// fails with 500 rather than run unbounded, so a wrapper that hides the
// connection shows up on the first request.
//
// The deadline also bounds the flush of the buffered response after the
// handler returns; the server clears it then, so it does not carry over to
// the next request on a keep-alive connection. Streaming routes must not be
// wrapped: pass it to NewHandler through ExceptRoute for the SSE route,
// which sets a deadline per event instead.
func WriteDeadline(d time.Duration, logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d)); err != nil {
				logger.ErrorContext(r.Context(), "set write deadline",
					slog.String("request_id", RequestID(r.Context())), slog.Any("error", err))
				writeInternalError(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ForRoute applies mw only to requests that match pattern, a ServeMux
// pattern such as "POST /tickets", so route-specific middleware can be passed
// to NewHandler, whose extra middleware otherwise wraps every route. The
// match uses ServeMux itself, so it agrees with the routing.
func ForRoute(pattern string, mw Middleware) Middleware {
	return routeSwitch(pattern, mw, true)
}

// ExceptRoute applies mw to every request but those that match pattern, such
// as WriteDeadline to all but the streaming route. It matches like ForRoute.
func ExceptRoute(pattern string, mw Middleware) Middleware {
	return routeSwitch(pattern, mw, false)
}

// routeSwitch applies mw to the requests whose match against pattern equals
// match.
func routeSwitch(pattern string, mw Middleware, match bool) Middleware {
	matcher := http.NewServeMux()
	matcher.Handle(pattern, http.NotFoundHandler())
	return func(next http.Handler) http.Handler {
		wrapped := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, p := matcher.Handler(r); (p == pattern) == match {
				wrapped.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
