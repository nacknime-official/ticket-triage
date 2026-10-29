package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/config"
	"github.com/nacknime-official/ticket-triage/internal/httpapi"
	"github.com/nacknime-official/ticket-triage/internal/notify"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// fakeService accepts every ticket and finds none.
type fakeService struct{}

func (fakeService) Submit(context.Context, string, string) (ticket.Ticket, ticket.Token, error) {
	token, hash := ticket.NewToken()
	now := time.Now().UTC()
	return ticket.Ticket{
		ID: uuid.NewV7(), Status: ticket.StatusPending, AccessTokenHash: hash, CreatedAt: now, UpdatedAt: now,
	}, token, nil
}

func (fakeService) Get(context.Context, string, ticket.Token) (ticket.Ticket, error) {
	return ticket.Ticket{}, ticket.ErrNotFound
}

// switchPinger fails while down is set.
type switchPinger struct{ down atomic.Bool }

func (p *switchPinger) Ping(context.Context) error {
	if p.down.Load() {
		return errors.New("connection refused")
	}
	return nil
}

// newTestServer serves newHandler the way run does: through httpapi.NewServer,
// so the write deadline middleware has a real connection to set it on.
func newTestServer(t *testing.T, ready httpapi.Pinger, httpCfg config.HTTP, limits config.Limits) *httptest.Server {
	t.Helper()
	store := newMemStore()
	streams := ticket.NewStreams(ticket.NewService(store, ticket.Config{}), store, 0)
	return serveHandler(t, fakeService{}, streams, notify.NewHub(0), ready, httpCfg, limits)
}

func serveHandler(t *testing.T, svc httpapi.TicketService, streams httpapi.StreamService, hub *notify.Hub,
	ready httpapi.Pinger, httpCfg config.HTTP, limits config.Limits,
) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = httpapi.NewServer("", newHandler(svc, streams, hub, ready, httpCfg, limits, logger),
		httpapi.ServerTimeouts{ReadHeader: time.Second, Read: time.Second, Idle: time.Second}, logger)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func defaultConfig(t *testing.T) (config.HTTP, config.Limits) {
	t.Helper()
	l := config.NewLoader(func(string) string { return "" })
	httpCfg, limits := l.HTTP(), l.Limits()
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	return httpCfg, limits
}

type response struct {
	status int
	header http.Header
	body   string
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, header http.Header) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: strings.TrimSpace(string(b))}
}

func TestHandlerReadiness(t *testing.T) {
	httpCfg, limits := defaultConfig(t)
	ready := &switchPinger{}
	srv := newTestServer(t, ready, httpCfg, limits)

	steps := []struct {
		name       string
		down       bool
		path       string
		wantStatus int
		wantBody   string
	}{
		{"healthz while up", false, "/healthz", http.StatusOK, `{"status":"ok"}`},
		{"readyz while up", false, "/readyz", http.StatusOK, `{"status":"ok"}`},
		{"healthz while down", true, "/healthz", http.StatusOK, `{"status":"ok"}`},
		{
			"readyz while down", true, "/readyz", http.StatusServiceUnavailable,
			`{"error":{"code":"not_ready","message":"database unreachable"}}`,
		},
		{"readyz after recovery", false, "/readyz", http.StatusOK, `{"status":"ok"}`},
	}
	for _, s := range steps {
		ready.down.Store(s.down)
		got := do(t, srv, http.MethodGet, s.path, "", nil)
		if got.status != s.wantStatus || got.body != s.wantBody {
			t.Errorf("%s: %d %s, want %d %s", s.name, got.status, got.body, s.wantStatus, s.wantBody)
		}
		if got.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: security headers missing: %v", s.name, got.header)
		}
	}
}

func TestHandlerMiddleware(t *testing.T) {
	httpCfg, limits := defaultConfig(t)
	httpCfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	limits.SubmitBurst = 2
	limits.MaxBodyBytes = 64
	srv := newTestServer(t, &switchPinger{}, httpCfg, limits)

	submit := `{"subject":"s","body":"b"}`
	from := func(ip string) http.Header {
		return http.Header{"Content-Type": {"application/json"}, "X-Forwarded-For": {ip}}
	}
	steps := []struct {
		name           string
		method, path   string
		body           string
		header         http.Header
		wantStatus     int
		wantRetryAfter bool
	}{
		{"submit", http.MethodPost, "/tickets", submit, from("198.51.100.1"), http.StatusAccepted, false},
		{"oversized body", http.MethodPost, "/tickets", strings.Repeat("x", 65), from("198.51.100.1"),
			http.StatusRequestEntityTooLarge, false},
		{"burst spent", http.MethodPost, "/tickets", submit, from("198.51.100.1"), http.StatusTooManyRequests, true},
		{"other client via trusted proxy", http.MethodPost, "/tickets", submit, from("198.51.100.2"),
			http.StatusAccepted, false},
		{"other routes not rate limited", http.MethodGet, "/healthz", "", from("198.51.100.1"), http.StatusOK, false},
		{"built-in page", http.MethodGet, "/", "", from("198.51.100.1"), http.StatusOK, false},
		{"get stays 404", http.MethodGet, "/tickets/" + uuid.NewV7().String(), "", from("198.51.100.1"),
			http.StatusNotFound, false},
	}
	for _, s := range steps {
		got := do(t, srv, s.method, s.path, s.body, s.header)
		if got.status != s.wantStatus {
			t.Errorf("%s: status %d, want %d (%s)", s.name, got.status, s.wantStatus, got.body)
		}
		if ra := got.header.Get("Retry-After"); (ra != "") != s.wantRetryAfter {
			t.Errorf("%s: Retry-After = %q, want set = %v", s.name, ra, s.wantRetryAfter)
		}
		for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy",
			httpapi.RequestIDHeader} {
			if got.header.Get(h) == "" {
				t.Errorf("%s: header %s missing", s.name, h)
			}
		}
	}
}

func TestDebugHandler(t *testing.T) {
	srv := httptest.NewServer(newDebugHandler())
	t.Cleanup(srv.Close)

	tests := []struct {
		path string
		want string
	}{
		{"/debug/pprof/", "goroutineleak"},
		{"/debug/pprof/goroutineleak?debug=1", "goroutineleak profile: total"},
		{"/debug/pprof/goroutine?debug=1", "goroutine profile: total"},
		{"/debug/pprof/cmdline", ""},
	}
	for _, tt := range tests {
		got := do(t, srv, http.MethodGet, tt.path, "", nil)
		if got.status != http.StatusOK || !strings.Contains(got.body, tt.want) {
			t.Errorf("GET %s: %d, body does not contain %q:\n%.300s", tt.path, got.status, tt.want, got.body)
		}
	}
}

func TestRunStartupErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			"invalid config",
			map[string]string{"DEBUG_ADDR": "0.0.0.0:6060"},
			"DEBUG_ADDR",
		},
		{
			"database unreachable",
			map[string]string{
				"HTTP_ADDR": "127.0.0.1:0", "DEBUG_ADDR": "127.0.0.1:0",
				"DATABASE_URL": "postgres://triage:hunter2@127.0.0.1:1/triage?sslmode=disable&connect_timeout=2",
			},
			"postgres: ping",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			stopped := false
			err := run(t.Context(), func() { stopped = true }, func(k string) string { return tt.env[k] }, &logs)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if strings.Contains(err.Error()+logs.String(), "hunter2") {
				t.Errorf("the database password leaked: %v\n%s", err, &logs)
			}
			if stopped {
				t.Error("stopSignals called before serving")
			}
		})
	}
}
