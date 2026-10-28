package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
	"uuid"
)

// RequestIDHeader carries the request id in every response.
const RequestIDHeader = "X-Request-Id"

type requestInfoKey struct{}

// requestInfo is shared by the middleware of one request. Only the request's
// own goroutine touches it, one middleware after another.
type requestInfo struct {
	id string
	// pattern is the matched route pattern, set once the mux has routed.
	pattern string
}

// RequestID returns the id withRequestID gave the request, or "" outside it.
func RequestID(ctx context.Context) string {
	if info, ok := ctx.Value(requestInfoKey{}).(*requestInfo); ok {
		return info.id
	}
	return ""
}

// withRequestID gives each request a new id, in its context and in the
// X-Request-Id response header. A client-sent id is ignored: it would let
// clients forge log correlation.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := &requestInfo{id: uuid.NewV7().String()}
		w.Header().Set(RequestIDHeader, info.id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestInfoKey{}, info)))
	})
}

// recordPattern copies the pattern the mux matched into the request info.
// The mux sets r.Pattern only on the request it receives, which middleware
// that calls r.WithContext has copied, so the access log cannot read it from
// its own request.
func recordPattern(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if info, ok := r.Context().Value(requestInfoKey{}).(*requestInfo); ok {
				info.pattern = r.Pattern
			}
		}()
		mux.ServeHTTP(w, r)
	})
}

// accessLog writes one line per request with the route pattern, status,
// duration and request id. It never logs the raw URL, which can carry
// ?token=, nor any header or body.
func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		var pattern string
		if info, ok := r.Context().Value(requestInfoKey{}).(*requestInfo); ok {
			pattern = info.pattern
		}
		logger.LogAttrs(r.Context(), slog.LevelInfo, "http request",
			slog.String("request_id", RequestID(r.Context())),
			slog.String("method", r.Method),
			slog.String("pattern", pattern),
			slog.Int("status", rec.Status()),
			slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
		)
	})
}

// statusRecorder remembers the response status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Status is the status written, or 200 if the handler wrote nothing.
func (r *statusRecorder) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}
