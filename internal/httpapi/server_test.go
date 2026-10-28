package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewServer(t *testing.T) {
	var logs bytes.Buffer
	h := http.NewServeMux()
	timeouts := ServerTimeouts{ReadHeader: 5 * time.Second, Read: 10 * time.Second, Idle: 60 * time.Second}
	s := NewServer(":8080", h, timeouts, slog.New(slog.NewJSONHandler(&logs, nil)))

	if s.Addr != ":8080" || s.Handler != h {
		t.Errorf("Addr = %q, Handler = %v", s.Addr, s.Handler)
	}
	if s.ReadHeaderTimeout != 5*time.Second || s.ReadTimeout != 10*time.Second || s.IdleTimeout != 60*time.Second {
		t.Errorf("timeouts = %v, %v, %v; want 5s, 10s, 60s", s.ReadHeaderTimeout, s.ReadTimeout, s.IdleTimeout)
	}
	if s.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want none (it would cut SSE)", s.WriteTimeout)
	}

	s.ErrorLog.Print("http: TLS handshake error")
	if got := logs.String(); !strings.Contains(got, `"level":"WARN"`) || !strings.Contains(got, "TLS handshake error") {
		t.Errorf("ErrorLog output = %q, want a WARN record through the logger", got)
	}
}

// TestNewServerLongRequest guards the premise of README §4.1: without a
// server-wide WriteTimeout, ReadTimeout alone does not end a long-running
// response such as an SSE stream.
func TestNewServerLongRequest(t *testing.T) {
	const readTimeout = 50 * time.Millisecond
	canceled := make(chan bool, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			canceled <- true
		case <-time.After(6 * readTimeout):
			canceled <- false
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = NewServer("", h, ServerTimeouts{ReadHeader: readTimeout, Read: readTimeout, Idle: time.Second},
		slog.New(slog.DiscardHandler))
	srv.Start()
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if <-canceled || resp.StatusCode != http.StatusOK {
		t.Errorf("long request canceled or failed (status %d) after ReadTimeout", resp.StatusCode)
	}
}
