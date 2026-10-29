package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// pingFunc adapts a function to Pinger.
type pingFunc func(ctx context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

var alwaysReady = pingFunc(func(context.Context) error { return nil })

func TestHealth(t *testing.T) {
	down := pingFunc(func(context.Context) error { return errors.New("dial tcp 127.0.0.1:5432: connection refused") })
	tests := []struct {
		name       string
		ready      Pinger
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"healthz", alwaysReady, http.MethodGet, "/healthz", http.StatusOK, `{"status":"ok"}`},
		{"healthz ignores the database", down, http.MethodGet, "/healthz", http.StatusOK, `{"status":"ok"}`},
		{"readyz up", alwaysReady, http.MethodGet, "/readyz", http.StatusOK, `{"status":"ok"}`},
		{
			"readyz down", down, http.MethodGet, "/readyz", http.StatusServiceUnavailable,
			`{"error":{"code":"not_ready","message":"database unreachable"}}`,
		},
		{
			"healthz other method", alwaysReady, http.MethodPost, "/healthz", http.StatusMethodNotAllowed,
			`{"error":{"code":"method_not_allowed","message":"method not allowed"}}`,
		},
		{
			"readyz other method", alwaysReady, http.MethodDelete, "/readyz", http.StatusMethodNotAllowed,
			`{"error":{"code":"method_not_allowed","message":"method not allowed"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := NewHandler(newFakeService(), testStreams(), tt.ready, newTestLogger(&logs))
			rec := serve(h, newRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
				t.Errorf("body = %s, want %s", got, tt.wantBody)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cc)
			}
		})
	}
}

func TestReadyzLogsTheFailure(t *testing.T) {
	var logs bytes.Buffer
	down := pingFunc(func(context.Context) error { return errors.New("connection refused") })
	serve(NewHandler(newFakeService(), testStreams(), down, newTestLogger(&logs)), newRequest(http.MethodGet, "/readyz", nil))

	lines := logLines(t, &logs)
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want the failure and the access log: %s", len(lines), &logs)
	}
	if lines[0]["msg"] != "readiness check failed" || lines[0]["error"] != "connection refused" ||
		lines[0]["request_id"] == "" {
		t.Errorf("failure line = %v", lines[0])
	}
	if lines[1]["pattern"] != "GET /readyz" || lines[1]["status"] != float64(http.StatusServiceUnavailable) {
		t.Errorf("access line = %v, want pattern GET /readyz, status 503", lines[1])
	}
}

func TestReadyzPingHasDeadline(t *testing.T) {
	var deadlineSet bool
	ready := pingFunc(func(ctx context.Context) error {
		_, deadlineSet = ctx.Deadline()
		return nil
	})
	var logs bytes.Buffer
	serve(NewHandler(newFakeService(), testStreams(), ready, newTestLogger(&logs)), newRequest(http.MethodGet, "/readyz", nil))
	if !deadlineSet {
		t.Error("ping context has no deadline; a hung database would hang the probe")
	}
}
