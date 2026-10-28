package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// TicketService submits and reads tickets. ticket.Service implements it.
type TicketService interface {
	// Submit validates and stores a new ticket and returns it with its
	// plaintext access token. It returns a *ticket.ValidationError for bad
	// input and ticket.ErrBacklogFull while the pending cap is reached.
	Submit(ctx context.Context, subject, body string) (ticket.Ticket, ticket.Token, error)
	// Get returns the ticket if token is its access token, and an error
	// wrapping ticket.ErrNotFound for every not-found or unauthorized case.
	Get(ctx context.Context, id string, token ticket.Token) (ticket.Ticket, error)
}

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// NewHandler returns the API handler: the ticket routes and the health checks
// behind the request-id and access-log middleware. GET /readyz pings ready.
//
// The extra middleware (limits, security headers, client IP) runs inside the
// access log, the first one outermost, so every response it writes is logged
// with its request id.
func NewHandler(svc TicketService, ready Pinger, logger *slog.Logger, extra ...Middleware) http.Handler {
	api := &api{svc: svc, ready: ready, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tickets", api.submitTicket)
	mux.HandleFunc("GET /tickets/{id}", api.getTicket)
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", api.readyz)
	// Other methods on known paths get the JSON 405, other paths the JSON 404,
	// instead of the mux's plain-text defaults.
	mux.Handle("/tickets", methodNotAllowed(http.MethodPost))
	mux.Handle("/tickets/{id}", methodNotAllowed(http.MethodGet, http.MethodHead))
	mux.Handle("/healthz", methodNotAllowed(http.MethodGet, http.MethodHead))
	mux.Handle("/readyz", methodNotAllowed(http.MethodGet, http.MethodHead))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { writeNotFound(w) })

	h := recordPattern(mux)
	for i := len(extra) - 1; i >= 0; i-- {
		h = extra[i](h)
	}
	return withRequestID(accessLog(logger, h))
}

type api struct {
	svc    TicketService
	ready  Pinger
	logger *slog.Logger
}

// log returns the logger with the request id of r.
func (a *api) log(r *http.Request) *slog.Logger {
	return a.logger.With(slog.String("request_id", RequestID(r.Context())))
}
