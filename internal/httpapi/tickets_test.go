package httpapi

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

var _ TicketService = (*ticket.Service)(nil)

const (
	testSubject = "My card was charged twice"
	testBody    = "Order 4411, please refund the second payment"
	testToken   = "ACCESSTOKENB32VALUE7Q"
)

var (
	testID      = uuid.MustParse("0192f6c1-7a2b-7c3d-8e4f-0123456789ab")
	testCreated = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
)

// fakeService stores tickets in memory and checks tokens like ticket.Service.
type fakeService struct {
	tickets   map[string]ticket.Ticket
	submitErr error
	getErr    error
	// subject and body are the last submitted values.
	subject, body string
}

func newFakeService() *fakeService {
	return &fakeService{tickets: map[string]ticket.Ticket{}}
}

func (f *fakeService) Submit(_ context.Context, subject, body string) (ticket.Ticket, ticket.Token, error) {
	f.subject, f.body = subject, body
	if f.submitErr != nil {
		return ticket.Ticket{}, ticket.Token{}, f.submitErr
	}
	token := ticket.TokenFromString(testToken)
	t := ticket.Ticket{
		ID: testID, Subject: subject, Body: body, Status: ticket.StatusPending,
		AccessTokenHash: token.Hash(), CreatedAt: testCreated, UpdatedAt: testCreated,
	}
	f.tickets[t.ID.String()] = t
	return t, token, nil
}

func (f *fakeService) Get(_ context.Context, id string, token ticket.Token) (ticket.Ticket, error) {
	if f.getErr != nil {
		return ticket.Ticket{}, f.getErr
	}
	t, ok := f.tickets[id]
	if !ok || !t.AccessTokenHash.Matches(token) {
		return ticket.Ticket{}, ticket.ErrNotFound
	}
	return t, nil
}

func (f *fakeService) add(t ticket.Ticket) {
	t.AccessTokenHash = ticket.TokenFromString(testToken).Hash()
	f.tickets[t.ID.String()] = t
}

// newTestLogger logs everything as JSON to w, without the redaction rules,
// so tests see exactly what the package logs.
func newTestLogger(w *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func newTestHandler(t *testing.T, svc TicketService) (http.Handler, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	return NewHandler(svc, newTestLogger(&logs)), &logs
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func postTicket(body, contentType string) *http.Request {
	req := newRequest(http.MethodPost, "/tickets", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

func getTicket(id, authorization string) *http.Request {
	req := newRequest(http.MethodGet, "/tickets/"+id, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorDetail {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body, err)
	}
	return body.Error
}

func TestSubmitTicketAccepted(t *testing.T) {
	svc := newFakeService()
	h, _ := newTestHandler(t, svc)

	reqBody := `{"subject":"` + testSubject + `","body":"` + testBody + `"}`
	rec := serve(h, postTicket(reqBody, "application/json; charset=utf-8"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	for header, want := range map[string]string{
		"Location":      "/tickets/" + testID.String(),
		"Cache-Control": "no-store",
		"Content-Type":  "application/json",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := map[string]any{
		"id":           testID.String(),
		"status":       "pending",
		"access_token": testToken,
		"created_at":   "2026-10-28T12:00:00Z",
	}
	if len(got) != len(want) {
		t.Errorf("body = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, got[k], v)
		}
	}
	if svc.subject != testSubject || svc.body != testBody {
		t.Errorf("service got subject %q body %q", svc.subject, svc.body)
	}
}

func TestSubmitTicketErrors(t *testing.T) {
	validBody := `{"subject":"s","body":"b"}`
	tests := []struct {
		name        string
		body        string
		contentType string
		submitErr   error
		wantStatus  int
		wantCode    string
		wantMessage string
		wantHeaders map[string]string
	}{
		{
			name: "missing content type", body: validBody,
			wantStatus: http.StatusUnsupportedMediaType, wantCode: codeUnsupportedMediaType,
		},
		{
			name: "wrong content type", body: validBody, contentType: "text/plain",
			wantStatus: http.StatusUnsupportedMediaType, wantCode: codeUnsupportedMediaType,
		},
		{
			name: "form content type", body: "subject=s&body=b", contentType: "application/x-www-form-urlencoded",
			wantStatus: http.StatusUnsupportedMediaType, wantCode: codeUnsupportedMediaType,
		},
		{
			name: "body over the limit", contentType: "application/json",
			body:       `{"subject":"s","body":"` + strings.Repeat("a", MaxBodyBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge, wantCode: codePayloadTooLarge,
		},
		{
			name: "malformed json", body: `{"subject":`, contentType: "application/json",
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidJSON,
		},
		{
			name: "not an object", body: `["s","b"]`, contentType: "application/json",
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidJSON,
		},
		{
			name: "wrong field type", body: `{"subject":1,"body":"b"}`, contentType: "application/json",
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidJSON,
		},
		{
			name: "unknown field", body: `{"subject":"s","body":"b","priority":"high"}`, contentType: "application/json",
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidJSON,
		},
		{
			name: "trailing data", body: validBody + `{}`, contentType: "application/json",
			wantStatus: http.StatusBadRequest, wantCode: codeInvalidJSON,
		},
		{
			name: "validation failed", body: validBody, contentType: "application/json",
			submitErr: &ticket.ValidationError{Problems: []ticket.Problem{
				{Field: ticket.FieldSubject, Code: ticket.ProblemRequired},
				{Field: ticket.FieldBody, Code: ticket.ProblemTooLong, Max: 5000},
			}},
			wantStatus: http.StatusBadRequest, wantCode: codeValidationFailed,
			wantMessage: "subject is required; body must be at most 5000 characters",
		},
		{
			name: "backlog full", body: validBody, contentType: "application/json",
			submitErr:  ticket.ErrBacklogFull,
			wantStatus: http.StatusServiceUnavailable, wantCode: codeBacklogFull,
			wantHeaders: map[string]string{"Retry-After": backlogRetryAfter},
		},
		{
			name: "backlog full wrapped", body: validBody, contentType: "application/json",
			submitErr:  errors.Join(errors.New("x"), ticket.ErrBacklogFull),
			wantStatus: http.StatusServiceUnavailable, wantCode: codeBacklogFull,
		},
		{
			name: "service failure", body: validBody, contentType: "application/json",
			submitErr:  errors.New("db down"),
			wantStatus: http.StatusInternalServerError, wantCode: codeInternal,
			wantMessage: "internal error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newFakeService()
			svc.submitErr = tt.submitErr
			h, _ := newTestHandler(t, svc)

			rec := serve(h, postTicket(tt.body, tt.contentType))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			got := decodeError(t, rec)
			if got.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", got.Code, tt.wantCode)
			}
			if got.Message == "" {
				t.Error("message is empty")
			}
			if tt.wantMessage != "" && got.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", got.Message, tt.wantMessage)
			}
			for header, want := range tt.wantHeaders {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
		})
	}
}

func TestGetTicket(t *testing.T) {
	tests := []struct {
		name   string
		ticket ticket.Ticket
		want   string
	}{
		{
			name: "pending",
			ticket: ticket.Ticket{
				ID: testID, Subject: testSubject, Body: testBody, Status: ticket.StatusPending,
				CreatedAt: testCreated, UpdatedAt: testCreated,
			},
			want: `{"id":"0192f6c1-7a2b-7c3d-8e4f-0123456789ab","status":"pending","status_reason":null,` +
				`"message":"Your request was received and is waiting to be classified.",` +
				`"category":null,"priority":null,"is_complaint":null,` +
				`"created_at":"2026-10-28T12:00:00Z","updated_at":"2026-10-28T12:00:00Z","classified_at":null}`,
		},
		{
			name: "classified",
			ticket: ticket.Ticket{
				ID: testID, Subject: testSubject, Body: testBody, Status: ticket.StatusClassified,
				Classification: &ticket.Classification{
					Category: "billing", CategoryConfidence: 0.9, ComplaintP: 0.8, Urgency: 2.2, UrgencyConfidence: 0.7,
				},
				CreatedAt: testCreated, UpdatedAt: testCreated.Add(time.Second), ClassifiedAt: testCreated.Add(time.Second),
			},
			want: `{"id":"0192f6c1-7a2b-7c3d-8e4f-0123456789ab","status":"classified","status_reason":null,` +
				`"message":"Your request was handed to the billing team, priority high.",` +
				`"category":"billing","priority":"high","is_complaint":true,` +
				`"created_at":"2026-10-28T12:00:00Z","updated_at":"2026-10-28T12:00:01Z","classified_at":"2026-10-28T12:00:01Z"}`,
		},
		{
			name: "needs human",
			ticket: ticket.Ticket{
				ID: testID, Subject: testSubject, Body: testBody, Status: ticket.StatusNeedsHuman,
				StatusReason: ticket.ReasonClassifierUnavailable,
				CreatedAt:    testCreated, UpdatedAt: testCreated.Add(time.Second),
			},
			want: `{"id":"0192f6c1-7a2b-7c3d-8e4f-0123456789ab","status":"needs_human","status_reason":"classifier_unavailable",` +
				`"message":"A person will contact you.",` +
				`"category":null,"priority":null,"is_complaint":null,` +
				`"created_at":"2026-10-28T12:00:00Z","updated_at":"2026-10-28T12:00:01Z","classified_at":null}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newFakeService()
			svc.add(tt.ticket)
			h, _ := newTestHandler(t, svc)

			rec := serve(h, getTicket(testID.String(), "Bearer "+testToken))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.want {
				t.Errorf("body:\n got %s\nwant %s", got, tt.want)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

// TestGetTicketNotFoundIsUniform checks that every not-found and unauthorized
// case returns the very same response.
func TestGetTicketNotFoundIsUniform(t *testing.T) {
	svc := newFakeService()
	svc.add(ticket.Ticket{ID: testID, Status: ticket.StatusPending, CreatedAt: testCreated, UpdatedAt: testCreated})
	h, _ := newTestHandler(t, svc)

	tests := []struct {
		name string
		req  *http.Request
	}{
		{"unknown id", getTicket(uuid.NewV7().String(), "Bearer "+testToken)},
		{"malformed id", getTicket("not-a-uuid", "Bearer "+testToken)},
		{"missing token", getTicket(testID.String(), "")},
		{"wrong token", getTicket(testID.String(), "Bearer WRONGTOKEN")},
		{"empty bearer", getTicket(testID.String(), "Bearer ")},
		{"wrong scheme", getTicket(testID.String(), "Basic "+testToken)},
		{"no scheme", getTicket(testID.String(), testToken)},
		{"token with space", getTicket(testID.String(), "Bearer "+testToken+" x")},
		{"token in query", newRequest(http.MethodGet, "/tickets/"+testID.String()+"?token="+testToken, nil)},
		{"two authorization headers", func() *http.Request {
			req := getTicket(testID.String(), "Bearer "+testToken)
			req.Header.Add("Authorization", "Bearer "+testToken)
			return req
		}()},
		{"unknown route", getTicket(testID.String()+"/x", "Bearer "+testToken)},
	}
	var first []byte
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, tt.req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if got := decodeError(t, rec); got.Code != codeNotFound {
				t.Errorf("code = %q, want %q", got.Code, codeNotFound)
			}
			if first == nil {
				first = rec.Body.Bytes()
			} else if !bytes.Equal(rec.Body.Bytes(), first) {
				t.Errorf("body = %s, want the same as the other cases: %s", rec.Body, first)
			}
		})
	}

	// The bearer scheme is case-insensitive, so the same token works lowercased.
	if rec := serve(h, getTicket(testID.String(), "bearer "+testToken)); rec.Code != http.StatusOK {
		t.Errorf("lowercase scheme: status = %d, want 200", rec.Code)
	}
}

func TestGetTicketServiceFailure(t *testing.T) {
	svc := newFakeService()
	svc.getErr = errors.New("db down")
	h, _ := newTestHandler(t, svc)

	rec := serve(h, getTicket(testID.String(), "Bearer "+testToken))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := decodeError(t, rec); got.Code != codeInternal {
		t.Errorf("code = %q, want %q", got.Code, codeInternal)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h, _ := newTestHandler(t, newFakeService())
	tests := []struct {
		method, path, wantAllow string
	}{
		{http.MethodGet, "/tickets", "POST"},
		{http.MethodPut, "/tickets", "POST"},
		{http.MethodDelete, "/tickets/" + testID.String(), "GET, HEAD"},
		{http.MethodPost, "/tickets/" + testID.String(), "GET, HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := serve(h, newRequest(tt.method, tt.path, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tt.wantAllow)
			}
			if got := decodeError(t, rec); got.Code != codeMethodNotAllowed {
				t.Errorf("code = %q, want %q", got.Code, codeMethodNotAllowed)
			}
		})
	}
}

// TestRealServer runs the handler behind a real server, where MaxBytesReader
// also closes the connection after an oversized body.
func TestRealServer(t *testing.T) {
	h, _ := newTestHandler(t, newFakeService())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	big := `{"subject":"s","body":"` + strings.Repeat("a", 2*MaxBodyBytes) + `"}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/tickets", strings.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Errorf("read body: %v", err)
	}
}

func newRequest(method, target string, body io.Reader) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), method, target, body)
}
