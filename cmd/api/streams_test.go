package main

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
	"github.com/nacknime-official/ticket-triage/internal/notify"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// memStore is an in-memory ticket.Repository and ticket.StreamTokenStore.
type memStore struct {
	mu      sync.Mutex
	tickets map[uuid.UUID]ticket.Ticket
	tokens  map[ticket.TokenHash]*memToken
}

type memToken struct {
	ticketID uuid.UUID
	expires  time.Time
	used     bool
}

func newMemStore() *memStore {
	return &memStore{tickets: map[uuid.UUID]ticket.Ticket{}, tokens: map[ticket.TokenHash]*memToken{}}
}

func (s *memStore) Submit(_ context.Context, t ticket.Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[t.ID] = t
	return nil
}

func (s *memStore) Get(_ context.Context, id uuid.UUID) (ticket.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[id]
	if !ok {
		return ticket.Ticket{}, ticket.ErrNotFound
	}
	return t, nil
}

func (s *memStore) CountPending(context.Context) (int, error) { return 0, nil }

func (s *memStore) Create(_ context.Context, ticketID uuid.UUID, hash ticket.TokenHash, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[hash] = &memToken{ticketID: ticketID, expires: time.Now().Add(ttl)}
	return nil
}

func (s *memStore) Consume(_ context.Context, ticketID uuid.UUID, hash ticket.TokenHash) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.tokens[hash]
	if !ok || tok.used || tok.ticketID != ticketID || !time.Now().Before(tok.expires) {
		return ticket.ErrNotFound
	}
	tok.used = true
	return nil
}

// submitTicket submits a ticket from ip and returns its id and access token.
func submitTicket(t *testing.T, srv *httptest.Server, ip string) (id, access string) {
	t.Helper()
	got := do(t, srv, http.MethodPost, "/tickets", `{"subject":"Refund","body":"Charged twice"}`,
		http.Header{"Content-Type": {"application/json"}, "X-Forwarded-For": {ip}})
	if got.status != http.StatusAccepted {
		t.Fatalf("submit: %d %s", got.status, got.body)
	}
	var body struct {
		ID          string `json:"id"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatalf("decode %s: %v", got.body, err)
	}
	return body.ID, body.AccessToken
}

// mintStreamToken calls POST /tickets/{id}/stream-token from ip.
func mintStreamToken(t *testing.T, srv *httptest.Server, id, access, ip string) (response, string) {
	t.Helper()
	got := do(t, srv, http.MethodPost, "/tickets/"+id+"/stream-token", "",
		http.Header{"Authorization": {"Bearer " + access}, "X-Forwarded-For": {ip}})
	var body struct {
		StreamToken string `json:"stream_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if got.status == http.StatusCreated {
		if err := json.Unmarshal([]byte(got.body), &body); err != nil || body.ExpiresIn != 60 {
			t.Fatalf("stream-token body %s: %v", got.body, err)
		}
	}
	return got, body.StreamToken
}

// stream is an open SSE response.
type stream struct {
	status int
	br     *bufio.Reader
	close  func()
}

// openStream calls GET base/tickets/{id}/events?token=, from ip unless it
// is empty. A refused stream is read and closed at once.
func openStream(t *testing.T, base, id, token, ip string) stream {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/tickets/"+id+"/events?token="+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by s.close or t.Cleanup.
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	s := stream{status: resp.StatusCode, br: bufio.NewReader(resp.Body), close: func() {
		cancel()
		_ = resp.Body.Close()
	}}
	t.Cleanup(s.close)
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		s.close()
	}
	return s
}

// next reads the next SSE event or comment, without its final blank line.
func (s stream) next() (string, error) {
	var b strings.Builder
	for {
		line, err := s.br.ReadString('\n')
		if err != nil {
			return b.String(), err
		}
		if line == "\n" {
			return b.String(), nil
		}
		b.WriteString(line)
	}
}

// nextStatus skips heartbeats and returns the status of the next event.
func (s stream) nextStatus(t *testing.T) (status, message string) {
	t.Helper()
	for {
		e, err := s.next()
		if err != nil {
			t.Fatalf("read stream: %q, %v", e, err)
		}
		if e == ": heartbeat\n" {
			continue
		}
		data, ok := strings.CutPrefix(e, "event: status\ndata: ")
		if !ok {
			t.Fatalf("unexpected event %q", e)
		}
		var body struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &body); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
		return body.Status, body.Message
	}
}

func TestHandlerStreamsStatusEvents(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, kafka.TopicEvents))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	cl, err := kafka.NewClient(cluster.ListenAddrs(), clientID,
		append(kafka.EventsOpts(), kgo.FetchMaxWait(100*time.Millisecond))...)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	hub := notify.NewHub(0)
	events, err := kafka.NewEventsReader(cl, hub, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	events.Start(t.Context())
	defer func() {
		if err := events.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	}()
	produceStatus := func(id uuid.UUID, status ticket.Status) {
		t.Helper()
		e := ticket.StatusChanged{TicketID: id, Status: status, At: time.Now().UTC()}
		if status == ticket.StatusNeedsHuman {
			reason := ticket.ReasonLowConfidence
			e.StatusReason, e.Message = &reason, "A person will contact you."
		}
		value, err := e.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		rec := &kgo.Record{Topic: kafka.TopicEvents, Key: []byte(id.String()), Value: value,
			Headers: []kgo.RecordHeader{{Key: "type", Value: []byte(ticket.StatusChangedType)}}}
		if err := cl.ProduceSync(t.Context(), rec).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	// The reader starts at the end, at a time the test cannot observe:
	// produce probes until one reaches the hub.
	probe := uuid.NewV7()
	sub := hub.Subscribe(probe)
	for deadline := time.Now().Add(10 * time.Second); ; {
		produceStatus(probe, ticket.StatusClassified)
		select {
		case <-sub.Events():
		case <-time.After(100 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("the events reader never delivered a probe")
			}
			continue
		}
		break
	}
	sub.Close()

	httpCfg, limits := defaultConfig(t)
	httpCfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	// Far shorter than the stream, which neither the route nor the per-event
	// write deadline may cut.
	httpCfg.WriteTimeout = 100 * time.Millisecond
	limits.SSEHeartbeat = 50 * time.Millisecond
	store := newMemStore()
	svc := ticket.NewService(store, ticket.Config{})
	srv := serveHandler(t, svc, ticket.NewStreams(svc, store, 0), hub, &switchPinger{}, httpCfg, limits)

	const ip = "198.51.100.1"
	id, access := submitTicket(t, srv, ip)
	got, token := mintStreamToken(t, srv, id, access, ip)
	if got.status != http.StatusCreated {
		t.Fatalf("stream-token: %d %s", got.status, got.body)
	}
	s := openStream(t, srv.URL, id, token, ip)
	if s.status != http.StatusOK {
		t.Fatalf("events: status %d", s.status)
	}
	if status, _ := s.nextStatus(t); status != "pending" {
		t.Fatalf("first event status %s, want pending", status)
	}
	// Heartbeats keep coming past several write timeouts.
	for start := time.Now(); time.Since(start) < 4*httpCfg.WriteTimeout; {
		if e, err := s.next(); err != nil || e != ": heartbeat\n" {
			t.Fatalf("got %q, %v, want a heartbeat", e, err)
		}
	}

	produceStatus(uuid.MustParse(id), ticket.StatusNeedsHuman)
	if status, msg := s.nextStatus(t); status != "needs_human" || msg != "A person will contact you." {
		t.Fatalf("second event %s %q, want needs_human", status, msg)
	}
	if e, err := s.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the final status: %q, %v, want EOF", e, err)
	}
	if again := openStream(t, srv.URL, id, token, ip); again.status != http.StatusNotFound {
		t.Errorf("token reused: status %d, want 404", again.status)
	}
}

func TestHandlerStreamLimits(t *testing.T) {
	httpCfg, limits := defaultConfig(t)
	httpCfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	limits.SSEMaxPerIP, limits.SSEMaxGlobal = 1, 2
	limits.StreamTokenBurst = 3
	store := newMemStore()
	svc := ticket.NewService(store, ticket.Config{})
	hub := notify.NewHub(0)
	srv := serveHandler(t, svc, ticket.NewStreams(svc, store, 0), hub, &switchPinger{}, httpCfg, limits)

	const a, b, c = "198.51.100.1", "198.51.100.2", "198.51.100.3"
	id, access := submitTicket(t, srv, a)
	var tokens []string
	for i := range limits.StreamTokenBurst {
		got, token := mintStreamToken(t, srv, id, access, a)
		if got.status != http.StatusCreated {
			t.Fatalf("mint %d: %d %s", i, got.status, got.body)
		}
		tokens = append(tokens, token)
	}
	if got, _ := mintStreamToken(t, srv, id, access, a); got.status != http.StatusTooManyRequests ||
		got.header.Get("Retry-After") == "" {
		t.Errorf("mint past the burst: %d Retry-After %q, want 429 with Retry-After",
			got.status, got.header.Get("Retry-After"))
	}

	first := openStream(t, srv.URL, id, tokens[0], a)
	if first.status != http.StatusOK {
		t.Fatalf("first stream: %d", first.status)
	}
	if status, _ := first.nextStatus(t); status != "pending" {
		t.Fatalf("first stream status %s", status)
	}
	if s := openStream(t, srv.URL, id, tokens[1], a); s.status != http.StatusTooManyRequests {
		t.Errorf("second stream of %s: %d, want 429", a, s.status)
	}
	// The 429 came before the token was consumed: it still opens a stream.
	second := openStream(t, srv.URL, id, tokens[1], b)
	if second.status != http.StatusOK {
		t.Fatalf("stream of %s with the refused token: %d, want 200", b, second.status)
	}
	if status, _ := second.nextStatus(t); status != "pending" {
		t.Fatalf("second stream status %s", status)
	}
	if s := openStream(t, srv.URL, id, tokens[2], c); s.status != http.StatusServiceUnavailable {
		t.Errorf("stream past the global cap: %d, want 503", s.status)
	}

	// A disconnect frees the slot once the handler notices. A 503 leaves the
	// token unspent, so retry until then.
	first.close()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		s := openStream(t, srv.URL, id, tokens[2], c)
		if s.status == http.StatusOK {
			break
		}
		if s.status != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("stream after a disconnect: %d, want 200", s.status)
		}
	}
}
