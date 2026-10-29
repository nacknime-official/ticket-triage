package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/notify"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

var _ StreamService = (*ticket.Streams)(nil)

// fakeStreams issues and redeems stream tokens like ticket.Streams, in
// memory. Every ticket's access token is testToken.
type fakeStreams struct {
	mu      sync.Mutex
	tickets map[uuid.UUID]ticket.Ticket
	tokens  map[string]*fakeStreamToken
	issued  int
	// Errors injected per method.
	issueErr, openErr, currentErr error
	// beforeCurrent runs at the start of Current.
	beforeCurrent func(id uuid.UUID)
}

type fakeStreamToken struct {
	ticketID uuid.UUID
	used     bool
}

func newFakeStreams() *fakeStreams {
	return &fakeStreams{tickets: map[uuid.UUID]ticket.Ticket{}, tokens: map[string]*fakeStreamToken{}}
}

func testStreams() Streams {
	return Streams{Service: newFakeStreams(), Hub: notify.NewHub(0)}
}

func (f *fakeStreams) add(t ticket.Ticket) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t.AccessTokenHash = ticket.TokenFromString(testToken).Hash()
	f.tickets[t.ID] = t
}

func (f *fakeStreams) Issue(_ context.Context, id string, access ticket.Token) (ticket.Token, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issueErr != nil {
		return ticket.Token{}, 0, f.issueErr
	}
	tid, err := uuid.Parse(id)
	if err != nil {
		return ticket.Token{}, 0, ticket.ErrNotFound
	}
	t, ok := f.tickets[tid]
	if !ok || !t.AccessTokenHash.Matches(access) {
		return ticket.Token{}, 0, ticket.ErrNotFound
	}
	f.issued++
	token := fmt.Sprintf("STREAMTOKEN%dB32VALUE", f.issued)
	f.tokens[token] = &fakeStreamToken{ticketID: tid}
	return ticket.TokenFromString(token), time.Minute, nil
}

func (f *fakeStreams) Open(_ context.Context, id string, token ticket.Token) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return uuid.UUID{}, f.openErr
	}
	tid, err := uuid.Parse(id)
	if err != nil {
		return uuid.UUID{}, ticket.ErrNotFound
	}
	tok, ok := f.tokens[token.Reveal()]
	if !ok || tok.used || tok.ticketID != tid {
		return uuid.UUID{}, fmt.Errorf("stream token: %w", ticket.ErrNotFound)
	}
	tok.used = true
	return tid, nil
}

func (f *fakeStreams) Current(_ context.Context, id uuid.UUID) (ticket.Ticket, error) {
	if f.beforeCurrent != nil {
		f.beforeCurrent(id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.currentErr != nil {
		return ticket.Ticket{}, f.currentErr
	}
	t, ok := f.tickets[id]
	if !ok {
		return ticket.Ticket{}, ticket.ErrNotFound
	}
	return t, nil
}

// issue mints a stream token for id directly, as POST .../stream-token would.
func (f *fakeStreams) issue(t *testing.T, id uuid.UUID) string {
	t.Helper()
	token, _, err := f.Issue(t.Context(), id.String(), ticket.TokenFromString(testToken))
	if err != nil {
		t.Fatalf("issue stream token: %v", err)
	}
	return token.Reveal()
}

func pendingTicket(id uuid.UUID) ticket.Ticket {
	return ticket.Ticket{ID: id, Status: ticket.StatusPending, CreatedAt: testCreated, UpdatedAt: testCreated}
}

func classifiedEvent(id uuid.UUID, at time.Time) ticket.StatusChanged {
	category, priority, complaint := "billing", "high", true
	return ticket.StatusChanged{
		TicketID: id, Status: ticket.StatusClassified,
		Category: &category, Priority: &priority, IsComplaint: &complaint,
		Message: "Your request was handed to the billing team, priority high.", At: at,
	}
}

func postStreamToken(id, authorization string) *http.Request {
	req := newRequest(http.MethodPost, "/tickets/"+id+"/stream-token", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req
}

func TestIssueStreamToken(t *testing.T) {
	streams := newFakeStreams()
	streams.add(pendingTicket(testID))
	var logs bytes.Buffer
	h := NewHandler(newFakeService(), Streams{Service: streams, Hub: notify.NewHub(0)}, alwaysReady,
		newTestLogger(&logs))

	rec := serve(h, postStreamToken(testID.String(), "Bearer "+testToken))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var body struct {
		StreamToken string `json:"stream_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if body.StreamToken != "STREAMTOKEN1B32VALUE" || body.ExpiresIn != 60 {
		t.Errorf("body = %+v, want the issued token and expires_in 60", body)
	}
	if strings.Contains(logs.String(), body.StreamToken) || strings.Contains(logs.String(), testToken) {
		t.Errorf("logs contain a token:\n%s", &logs)
	}
}

func TestIssueStreamTokenNotFound(t *testing.T) {
	streams := newFakeStreams()
	streams.add(pendingTicket(testID))
	var logs bytes.Buffer
	h := NewHandler(newFakeService(), Streams{Service: streams, Hub: notify.NewHub(0)}, alwaysReady,
		newTestLogger(&logs))

	tests := []struct {
		name, id, authorization string
	}{
		{"no token", testID.String(), ""},
		{"wrong token", testID.String(), "Bearer WRONG"},
		{"not bearer", testID.String(), "Basic " + testToken},
		{"unknown id", uuid.NewV7().String(), "Bearer " + testToken},
		{"malformed id", "nope", "Bearer " + testToken},
	}
	var first string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, postStreamToken(tt.id, tt.authorization))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if first == "" {
				first = rec.Body.String()
			} else if rec.Body.String() != first {
				t.Errorf("body %s differs from %s", rec.Body, first)
			}
		})
	}
	if streams.issued != 0 {
		t.Errorf("%d tokens issued for refused requests", streams.issued)
	}

	streams.issueErr = errors.New("db down")
	if rec := serve(h, postStreamToken(testID.String(), "Bearer "+testToken)); rec.Code != http.StatusInternalServerError {
		t.Errorf("service error: status = %d, want 500", rec.Code)
	}
	rec := serve(h, newRequest(http.MethodGet, "/tickets/"+testID.String()+"/stream-token", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("GET stream-token: %d Allow %q, want 405 Allow POST", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestStreamEventsRefused(t *testing.T) {
	streams := newFakeStreams()
	mine, other := uuid.NewV7(), uuid.NewV7()
	streams.add(pendingTicket(mine))
	streams.add(pendingTicket(other))
	used := streams.issue(t, mine)
	if _, err := streams.Open(t.Context(), mine.String(), ticket.TokenFromString(used)); err != nil {
		t.Fatal(err)
	}
	valid := streams.issue(t, mine)
	var logs bytes.Buffer
	h := NewHandler(newFakeService(), Streams{Service: streams, Hub: notify.NewHub(0)}, alwaysReady,
		newTestLogger(&logs))

	events := func(id, query string) string { return "/tickets/" + id + "/events" + query }
	tests := []struct {
		name, target string
	}{
		{"no token", events(mine.String(), "")},
		{"empty token", events(mine.String(), "?token=")},
		{"two tokens", events(mine.String(), "?token="+valid+"&token="+valid)},
		{"unknown token", events(mine.String(), "?token=UNKNOWN")},
		{"used token", events(mine.String(), "?token="+used)},
		{"other ticket's token", events(other.String(), "?token="+valid)},
		{"access token", events(mine.String(), "?token="+testToken)},
		{"malformed id", events("nope", "?token="+valid)},
	}
	notFound := `{"error":{"code":"not_found","message":"not found"}}`
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, newRequest(http.MethodGet, tt.target, nil))
			if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != notFound {
				t.Errorf("got %d %s, want 404 %s", rec.Code, rec.Body, notFound)
			}
		})
	}
	// The refused attempts did not spend the valid token: a HEAD does not
	// either.
	if rec := serve(h, newRequest(http.MethodHead, events(mine.String(), "?token="+valid), nil)); rec.Code !=
		http.StatusMethodNotAllowed {
		t.Errorf("HEAD: status = %d, want 405", rec.Code)
	}
	if rec := serve(h, newRequest(http.MethodPost, events(mine.String(), "?token="+valid), nil)); rec.Code !=
		http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Errorf("POST: %d Allow %q, want 405 Allow GET", rec.Code, rec.Header().Get("Allow"))
	}
	if streams.tokens[valid].used {
		t.Error("a refused request spent the valid token")
	}

	streams.openErr = errors.New("db down")
	if rec := serve(h, newRequest(http.MethodGet, events(mine.String(), "?token="+valid), nil)); rec.Code !=
		http.StatusInternalServerError {
		t.Errorf("open error: status = %d, want 500", rec.Code)
	}
	streams.openErr = nil
	streams.currentErr = errors.New("db down")
	if rec := serve(h, newRequest(http.MethodGet, events(mine.String(), "?token="+valid), nil)); rec.Code !=
		http.StatusInternalServerError {
		t.Errorf("current error: status = %d, want 500", rec.Code)
	}
	if strings.Contains(logs.String(), valid) {
		t.Errorf("logs contain the stream token:\n%s", &logs)
	}
}

// sseEvent is one parsed server-sent event; a comment has only comment set.
type sseEvent struct {
	event, data, comment string
}

// readEvent reads the next event or comment from an SSE stream.
func readEvent(br *bufio.Reader) (sseEvent, error) {
	var e sseEvent
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return e, err
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			return e, nil
		case strings.HasPrefix(line, ":"):
			e.comment = strings.TrimSpace(line[1:])
		case strings.HasPrefix(line, "event: "):
			e.event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			e.data = line[len("data: "):]
		default:
			return e, fmt.Errorf("unexpected SSE line %q", line)
		}
	}
}

// streamServer serves h over a real connection through NewServer, as
// cmd/api does.
func streamServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = NewServer("", h, ServerTimeouts{ReadHeader: time.Second, Read: time.Second, Idle: time.Second},
		newTestLogger(&bytes.Buffer{}))
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// openStream opens the SSE stream of id and returns the response status,
// header and body. The body is closed at the end of the test.
func openStream(t *testing.T, srv *httptest.Server, id uuid.UUID, token string) (int, http.Header, *bufio.Reader) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+"/tickets/"+id.String()+"/events?token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req) //nolint:bodyclose // closed by t.Cleanup, after the test reads the stream.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp.StatusCode, resp.Header, bufio.NewReader(resp.Body)
}

func decodeStatus(t *testing.T, e sseEvent) ticketResponse {
	t.Helper()
	if e.event != "status" {
		t.Fatalf("event %+v, want a status event", e)
	}
	var resp ticketResponse
	if err := json.Unmarshal([]byte(e.data), &resp, json.RejectUnknownMembers(true)); err != nil {
		t.Fatalf("decode %s: %v", e.data, err)
	}
	return resp
}

func TestStreamEventsPendingThenClassified(t *testing.T) {
	streams, hub := newFakeStreams(), notify.NewHub(0)
	streams.add(pendingTicket(testID))
	var logs syncBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := NewHandler(newFakeService(), Streams{Service: streams, Hub: hub}, alwaysReady, logger)
	srv := streamServer(t, h)
	token := streams.issue(t, testID)

	status, header, br := openStream(t, srv, testID, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	for name, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-store"} {
		if got := header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	first, err := readEvent(br)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeStatus(t, first); got.Status != ticket.StatusPending || got.ID != testID.String() {
		t.Fatalf("first event %+v, want the pending state", got)
	}

	// The first event is sent after subscribing, so the hub has the stream.
	at := testCreated.Add(time.Minute)
	hub.Publish(ticket.StatusChanged{TicketID: testID, Status: ticket.StatusPending, At: at}) // a duplicate
	hub.Publish(classifiedEvent(testID, at))
	second, err := readEvent(br)
	if err != nil {
		t.Fatal(err)
	}
	// The event has the GET /tickets/{id} JSON.
	got, err := json.Marshal(decodeStatus(t, second))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"` + testID.String() + `","status":"classified","status_reason":null,` +
		`"message":"Your request was handed to the billing team, priority high.",` +
		`"category":"billing","priority":"high","is_complaint":true,` +
		`"created_at":"2026-10-28T12:00:00Z","updated_at":"2026-10-28T12:01:00Z","classified_at":"2026-10-28T12:01:00Z"}`
	if string(got) != want {
		t.Errorf("classified event\n got %s\nwant %s", got, want)
	}
	// A final status ends the stream.
	if e, err := readEvent(br); !errors.Is(err, io.EOF) {
		t.Errorf("after the final status: %+v, %v, want EOF", e, err)
	}

	// The token was single use.
	if status, _, _ := openStream(t, srv, testID, token); status != http.StatusNotFound {
		t.Errorf("second use of the token: status = %d, want 404", status)
	}
	waitForHub(t, hub, 0)

	// The access log has the route pattern, never the URL with the token.
	waitForLog(t, &logs, `"pattern":"GET /tickets/{id}/events","status":200`)
	if strings.Contains(logs.String(), token) {
		t.Errorf("logs contain the stream token:\n%s", logs.String())
	}
}

func TestStreamEventsFinalStateCloses(t *testing.T) {
	streams, hub := newFakeStreams(), notify.NewHub(0)
	tk := pendingTicket(testID)
	tk.Status, tk.StatusReason = ticket.StatusNeedsHuman, ticket.ReasonLowConfidence
	streams.add(tk)
	srv := streamServer(t, NewHandler(newFakeService(), Streams{Service: streams, Hub: hub}, alwaysReady,
		newTestLogger(&bytes.Buffer{})))

	_, _, br := openStream(t, srv, testID, streams.issue(t, testID))
	e, err := readEvent(br)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeStatus(t, e); got.Status != ticket.StatusNeedsHuman || got.Message != "A person will contact you." {
		t.Errorf("event %+v, want the needs_human state", got)
	}
	if _, err := readEvent(br); !errors.Is(err, io.EOF) {
		t.Errorf("stream not closed after a final state: %v", err)
	}
	waitForHub(t, hub, 0)
}

// TestStreamEventsSubscribesBeforeReading classifies the ticket between the
// subscribe and the state read: the event must still reach the stream.
func TestStreamEventsSubscribesBeforeReading(t *testing.T) {
	streams, hub := newFakeStreams(), notify.NewHub(0)
	streams.add(pendingTicket(testID))
	streams.beforeCurrent = func(id uuid.UUID) { hub.Publish(classifiedEvent(id, testCreated.Add(time.Second))) }
	// A lost event would leave the stream pending until MaxDuration.
	cfg := Streams{Service: streams, Hub: hub, MaxDuration: 2 * time.Second}
	srv := streamServer(t, NewHandler(newFakeService(), cfg, alwaysReady, newTestLogger(&bytes.Buffer{})))

	_, _, br := openStream(t, srv, testID, streams.issue(t, testID))
	var statuses []ticket.Status
	for {
		e, err := readEvent(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, decodeStatus(t, e).Status)
	}
	if fmt.Sprint(statuses) != "[pending classified]" {
		t.Errorf("statuses = %v, want [pending classified]", statuses)
	}
}

func waitForHub(t *testing.T, hub *notify.Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for hub.Len() != n {
		if time.Now().After(deadline) {
			t.Fatalf("hub has %d subscriptions, want %d", hub.Len(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForLog(t *testing.T, logs *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("logs lack %s:\n%s", want, logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// streamRecorder is a ResponseWriter for streaming handlers inside a synctest
// bubble, where no network is allowed. It flushes and takes write deadlines
// like a server connection.
type streamRecorder struct {
	mu        sync.Mutex
	header    http.Header
	status    int
	body      bytes.Buffer
	deadline  time.Time
	lateWrite bool // a write without a fresh deadline
}

func newStreamRecorder() *streamRecorder { return &streamRecorder{header: http.Header{}} }

func (r *streamRecorder) Header() http.Header { return r.header }

func (r *streamRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = code
	}
}

func (r *streamRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if !time.Now().Before(r.deadline) {
		r.lateWrite = true
	}
	return r.body.Write(p)
}

func (r *streamRecorder) Flush() {}

func (r *streamRecorder) SetWriteDeadline(t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadline = t
	return nil
}

func (r *streamRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

// startStream serves a pending ticket's stream in a new goroutine. Cancel
// the returned context to disconnect; done is closed when the handler returns.
func startStream(t *testing.T, cfg Streams) (rec *streamRecorder, hub *notify.Hub, cancel context.CancelFunc,
	done chan struct{},
) {
	t.Helper()
	streams := newFakeStreams()
	streams.add(pendingTicket(testID))
	hub = notify.NewHub(0)
	cfg.Service, cfg.Hub = streams, hub
	h := NewHandler(newFakeService(), cfg, alwaysReady, newTestLogger(&bytes.Buffer{}))

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/tickets/"+testID.String()+"/events?token="+streams.issue(t, testID), nil)
	rec, done = newStreamRecorder(), make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	return rec, hub, cancel, done
}

func isDone(done chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// The synctest bubbles below also check for leaks: synctest.Test fails if a
// goroutine of the bubble, such as a stuck handler, is still blocked at the
// end.

func TestStreamHeartbeatAndDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, hub, cancel, done := startStream(t, Streams{Heartbeat: 15 * time.Second, WriteTimeout: 5 * time.Second})
		synctest.Wait()
		if !strings.HasPrefix(rec.String(), "event: status\ndata: {") || strings.Contains(rec.String(), "heartbeat") {
			t.Fatalf("stream start = %q, want only the state event", rec.String())
		}

		time.Sleep(15*time.Second - time.Nanosecond)
		synctest.Wait()
		if n := strings.Count(rec.String(), ": heartbeat\n\n"); n != 0 {
			t.Fatalf("%d heartbeats before 15s", n)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if n := strings.Count(rec.String(), ": heartbeat\n\n"); n != 1 {
			t.Fatalf("%d heartbeats at 15s, want 1", n)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if n := strings.Count(rec.String(), ": heartbeat\n\n"); n != 3 {
			t.Fatalf("%d heartbeats at 45s, want 3", n)
		}

		cancel() // the client disconnects
		synctest.Wait()
		if !isDone(done) {
			t.Fatal("handler still running after the client disconnected")
		}
		if hub.Len() != 0 {
			t.Errorf("hub has %d subscriptions after disconnect", hub.Len())
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if rec.lateWrite {
			t.Error("a write ran without a fresh write deadline")
		}
	})
}

func TestStreamMaxDuration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, hub, cancel, done := startStream(t, Streams{Heartbeat: time.Minute, MaxDuration: 10 * time.Minute})
		defer cancel()
		time.Sleep(10*time.Minute - time.Nanosecond)
		synctest.Wait()
		if isDone(done) {
			t.Fatal("stream ended before its max duration")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if !isDone(done) {
			t.Fatal("stream still open after its max duration")
		}
		if n := strings.Count(rec.String(), ": heartbeat"); n < 9 || n > 10 {
			t.Errorf("%d heartbeats in 10 minutes, want 9 or 10", n)
		}
		if hub.Len() != 0 {
			t.Errorf("hub has %d subscriptions after the stream ended", hub.Len())
		}
	})
}

func TestStreamEndsWhenHubCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, hub, cancel, done := startStream(t, Streams{})
		defer cancel()
		synctest.Wait()
		hub.Close() // shutdown
		synctest.Wait()
		if !isDone(done) {
			t.Fatal("stream still open after the hub closed")
		}
	})
}
