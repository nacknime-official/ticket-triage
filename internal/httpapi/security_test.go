package httpapi

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

func TestSecurityHeaders(t *testing.T) {
	want := map[string]string{
		"Content-Security-Policy": contentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	}
	handlers := map[string]http.Handler{
		"success": okHandler,
		"error": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeNotFound(w)
		}),
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			SecurityHeaders(h).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			for k, v := range want {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
		})
	}
}

func TestLimitBody(t *testing.T) {
	const limit = 16
	var readErr error
	h := LimitBody(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		if _, ok := errors.AsType[*http.MaxBytesError](readErr); ok {
			writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "too large")
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))

	tests := []struct {
		name        string
		body        string
		unknownLen  bool // send without Content-Length, as a chunked body would
		wantStatus  int
		wantHandler bool // the handler ran and saw a read error
	}{
		{"at the limit", strings.Repeat("a", limit), false, http.StatusAccepted, false},
		{"declared length above the limit", strings.Repeat("a", limit+1), false, http.StatusRequestEntityTooLarge, false},
		{"unknown length within the limit", "short", true, http.StatusAccepted, false},
		{"unknown length above the limit", strings.Repeat("a", 10*limit), true, http.StatusRequestEntityTooLarge, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readErr = nil
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/tickets", strings.NewReader(tt.body))
			if tt.unknownLen {
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.wantStatus {
				t.Errorf("status %d, want %d", w.Code, tt.wantStatus)
			}
			if (readErr != nil) != tt.wantHandler {
				t.Errorf("handler read error = %v, want error: %v", readErr, tt.wantHandler)
			}
			if tt.wantStatus == http.StatusRequestEntityTooLarge && !strings.Contains(w.Body.String(), `"code":"payload_too_large"`) {
				t.Errorf("body = %s, want code payload_too_large", w.Body)
			}
		})
	}
}

func TestWriteDeadlineUnsupportedWriter(t *testing.T) {
	var logs bytes.Buffer
	ran := false
	h := WriteDeadline(time.Second, slog.New(slog.NewTextHandler(&logs, nil)))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true }))

	// httptest.ResponseRecorder, like a wrapper without Unwrap, cannot set
	// deadlines; the request must fail instead of running unbounded.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if ran || w.Code != http.StatusInternalServerError {
		t.Errorf("handler ran = %v, status %d; want not run, 500", ran, w.Code)
	}
	if !strings.Contains(logs.String(), "set write deadline") {
		t.Errorf("logs = %q, want the deadline error", logs.String())
	}
}

func TestWriteDeadline(t *testing.T) {
	const deadline = 50 * time.Millisecond
	logger := slog.New(slog.DiscardHandler)
	mux := http.NewServeMux()
	mux.Handle("GET /fast", WriteDeadline(deadline, logger)(okHandler))
	mux.Handle("GET /slow", WriteDeadline(deadline, logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(4 * deadline)
		w.WriteHeader(http.StatusOK)
	})))
	// Like an SSE handler: not wrapped, writes long after the deadline.
	mux.Handle("GET /stream", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(4 * deadline)
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func(path string) (status int, reused bool, err error) {
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			return 0, false, err
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			return 0, reused, err
		}
		defer func() { _ = resp.Body.Close() }()
		_, err = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, reused, err
	}

	if _, _, err := get("/slow"); err == nil {
		t.Error("slow handler: response arrived after the write deadline")
	}

	if status, _, err := get("/fast"); err != nil || status != http.StatusAccepted {
		t.Fatalf("fast handler: status %d, err %v", status, err)
	}
	// The next request on the same keep-alive connection must not inherit
	// the deadline of the previous one.
	time.Sleep(2 * deadline)
	status, reused, err := get("/stream")
	if err != nil || status != http.StatusOK {
		t.Fatalf("stream after fast on one connection: status %d, err %v", status, err)
	}
	if !reused {
		t.Error("stream request did not reuse the connection; the test proves nothing")
	}
}

func TestForRoute(t *testing.T) {
	reject := Middleware(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	})
	tests := []struct {
		pattern, method, target string
		wantApplied             bool
	}{
		{"POST /tickets", http.MethodPost, "/tickets", true},
		{"POST /tickets", http.MethodPost, "/tickets?token=x", true},
		{"POST /tickets", http.MethodGet, "/tickets", false},
		{"POST /tickets", http.MethodPost, "/tickets/", false},
		{"POST /tickets", http.MethodPost, "/tickets/" + testID.String(), false},
		{"POST /tickets/{id}/stream-token", http.MethodPost, "/tickets/" + testID.String() + "/stream-token", true},
		{"POST /tickets/{id}/stream-token", http.MethodPost, "/tickets/" + testID.String(), false},
		{"GET /tickets/{id}/events", http.MethodGet, "/tickets/" + testID.String() + "/events?token=x", true},
		{"GET /tickets/{id}/events", http.MethodHead, "/tickets/" + testID.String() + "/events", true},
		{"GET /tickets/{id}/events", http.MethodGet, "/tickets/" + testID.String(), false},
		{"GET /tickets/{id}/events", http.MethodPost, "/tickets/" + testID.String() + "/events", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+" "+tt.method+" "+tt.target, func(t *testing.T) {
			for name, scope := range map[string]func(string, Middleware) Middleware{
				"ForRoute": ForRoute, "ExceptRoute": ExceptRoute,
			} {
				w := httptest.NewRecorder()
				scope(tt.pattern, reject)(okHandler).ServeHTTP(w,
					httptest.NewRequestWithContext(t.Context(), tt.method, tt.target, nil))
				// ExceptRoute applies the middleware exactly where ForRoute does not.
				want := tt.wantApplied == (name == "ForRoute")
				if applied := w.Code == http.StatusTeapot; applied != want {
					t.Errorf("%s: middleware applied = %v, want %v", name, applied, want)
				}
			}
		})
	}
}
