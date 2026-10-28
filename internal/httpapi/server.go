package httpapi

import (
	"log/slog"
	"net/http"
	"time"
)

// ServerTimeouts are the http.Server timeouts of README §4.1.
type ServerTimeouts struct {
	ReadHeader time.Duration // reading the request header
	Read       time.Duration // reading the whole request, body included
	Idle       time.Duration // waiting for the next request on a keep-alive connection
}

// NewServer returns an http.Server for h on addr with the given timeouts and
// errors logged through logger.
//
// It deliberately has no WriteTimeout, which would cut SSE streams; wrap
// non-streaming handlers in WriteDeadline instead. ReadTimeout does not end
// a long response either: the server drops the read deadline once the request
// is read.
func NewServer(addr string, h http.Handler, t ServerTimeouts, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: t.ReadHeader,
		ReadTimeout:       t.Read,
		IdleTimeout:       t.Idle,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}
