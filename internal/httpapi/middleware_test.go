package httpapi

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

func logLines(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.Lines(logs.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestAccessLog(t *testing.T) {
	svc := newFakeService()
	svc.add(ticket.Ticket{ID: testID, Status: ticket.StatusPending, CreatedAt: testCreated, UpdatedAt: testCreated})
	h, logs := newTestHandler(t, svc)

	tests := []struct {
		name        string
		req         *http.Request
		wantPattern string
		wantStatus  int
	}{
		{"submit", postTicket(`{"subject":"s","body":"b"}`, "application/json"), "POST /tickets", http.StatusAccepted},
		{"get", getTicket(testID.String(), "Bearer "+testToken), "GET /tickets/{id}", http.StatusOK},
		{"not found", getTicket(testID.String(), ""), "GET /tickets/{id}", http.StatusNotFound},
		{"method not allowed", newRequest(http.MethodPut, "/tickets", nil), "/tickets", http.StatusMethodNotAllowed},
		{"unknown route", newRequest(http.MethodGet, "/nope", nil), "/", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs.Reset()
			rec := serve(h, tt.req)

			lines := logLines(t, logs)
			if len(lines) != 1 {
				t.Fatalf("got %d log lines, want 1: %s", len(lines), logs)
			}
			line := lines[0]
			if line["msg"] != "http request" {
				t.Errorf("msg = %v", line["msg"])
			}
			if line["pattern"] != tt.wantPattern {
				t.Errorf("pattern = %v, want %q", line["pattern"], tt.wantPattern)
			}
			if line["status"] != float64(tt.wantStatus) {
				t.Errorf("status = %v, want %d", line["status"], tt.wantStatus)
			}
			if line["method"] != tt.req.Method {
				t.Errorf("method = %v, want %s", line["method"], tt.req.Method)
			}
			if _, ok := line["duration_ms"].(float64); !ok {
				t.Errorf("duration_ms = %v, want a number", line["duration_ms"])
			}
			id := rec.Header().Get(RequestIDHeader)
			if id == "" || line["request_id"] != id {
				t.Errorf("request_id = %v, want the %s header %q", line["request_id"], RequestIDHeader, id)
			}
		})
	}
}

// TestLogsCarryNoSecrets sends requests full of secrets and ticket text,
// including failures that log errors, through a logger without redaction:
// the handlers and middleware themselves must keep them out of the log.
func TestLogsCarryNoSecrets(t *testing.T) {
	svc := newFakeService()
	svc.add(ticket.Ticket{ID: testID, Subject: testSubject, Body: testBody, Status: ticket.StatusPending})
	h, logs := newTestHandler(t, svc)

	submit := func(contentType string) *http.Request {
		return postTicket(`{"subject":"`+testSubject+`","body":"`+testBody+`"}`, contentType)
	}
	requests := []*http.Request{
		submit("application/json"),
		submit("text/plain"),
		postTicket(`{"subject":"`+testSubject+`","body":`, "application/json"),
		postTicket(`{"subject":"`+testSubject+`","body":"`+testBody+`","x":1}`, "application/json"),
		getTicket(testID.String(), "Bearer "+testToken),
		getTicket(testID.String(), "Bearer "+testToken+"x"),
		newRequest(http.MethodGet, "/tickets/"+testID.String()+"?token="+testToken, nil),
		newRequest(http.MethodGet, "/nope?token="+testToken, nil),
	}
	for _, req := range requests {
		serve(h, req)
	}
	svc.submitErr = context.DeadlineExceeded
	serve(h, submit("application/json"))
	svc.getErr = context.DeadlineExceeded
	serve(h, getTicket(testID.String(), "Bearer "+testToken))

	if n := len(logLines(t, logs)); n < len(requests)+2 {
		t.Fatalf("got %d log lines, want at least %d", n, len(requests)+2)
	}
	for _, secret := range []string{testToken, testSubject, testBody, "token=", "Bearer", "Authorization"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log contains %q:\n%s", secret, logs)
		}
	}
}

// TestExtraMiddleware checks that extra middleware runs inside the access log
// and that the log still gets the route pattern when the middleware replaces
// the request.
func TestExtraMiddleware(t *testing.T) {
	type ctxKey struct{}
	var order []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				if RequestID(r.Context()) == "" {
					t.Errorf("%s: no request id in the context", name)
				}
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, name)))
			})
		}
	}
	reject := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				writeError(w, http.StatusTooManyRequests, "rate_limited", "slow down")
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	var logs bytes.Buffer
	h := NewHandler(newFakeService(), alwaysReady, newTestLogger(&logs), mark("first"), mark("second"), reject)

	serve(h, postTicket(`{"subject":"s","body":"b"}`, "application/json"))
	if got := strings.Join(order, ","); got != "first,second" {
		t.Errorf("middleware order = %s, want first,second", got)
	}
	serve(h, newRequest(http.MethodPut, "/tickets", nil))

	lines := logLines(t, &logs)
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2: %s", len(lines), &logs)
	}
	if lines[0]["pattern"] != "POST /tickets" || lines[0]["status"] != float64(http.StatusAccepted) {
		t.Errorf("first line = %v, want pattern POST /tickets, status 202", lines[0])
	}
	// The middleware answered before routing, so there is no pattern.
	if lines[1]["pattern"] != "" || lines[1]["status"] != float64(http.StatusTooManyRequests) {
		t.Errorf("second line = %v, want empty pattern, status 429", lines[1])
	}
}

func TestRequestIDsDiffer(t *testing.T) {
	h, _ := newTestHandler(t, newFakeService())
	a := serve(h, getTicket(testID.String(), "")).Header().Get(RequestIDHeader)
	req := getTicket(testID.String(), "")
	req.Header.Set(RequestIDHeader, "client-chosen")
	b := serve(h, req).Header().Get(RequestIDHeader)
	if a == "" || b == "" || a == b || b == "client-chosen" {
		t.Errorf("request ids %q and %q: want two distinct server-generated ids", a, b)
	}
}

func TestStatusRecorderUnwraps(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec}
	if err := http.NewResponseController(sr).Flush(); err != nil {
		t.Fatalf("Flush through the recorder: %v", err)
	}
	if !rec.Flushed {
		t.Error("underlying writer was not flushed")
	}
	if got := sr.Status(); got != http.StatusOK {
		t.Errorf("Status() before any write = %d, want 200", got)
	}
}
