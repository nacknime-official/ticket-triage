package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/logging"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

const testStreamToken = "STREAMTOKENB32VALUE3KQW"

// syncBuffer is a bytes.Buffer safe for the server's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lockedService serializes calls to a fakeService, which the server calls
// from one goroutine per connection.
type lockedService struct {
	mu  sync.Mutex
	svc *fakeService
}

func (s *lockedService) Submit(ctx context.Context, subject, body string) (ticket.Ticket, ticket.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.svc.Submit(ctx, subject, body)
}

func (s *lockedService) Get(ctx context.Context, id string, token ticket.Token) (ticket.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.svc.Get(ctx, id, token)
}

// carelessLog stands in for a middleware written without the README §4.1
// rules: it logs the raw URL, query and headers. The redacting handler alone
// must keep the tokens out of its output.
func carelessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.InfoContext(r.Context(), "careless",
				"url", r.URL.String(), "query", r.URL.Query(), "headers", r.Header,
				"client_ip", ClientIPFrom(r).String())
		})
	}
}

func TestRedactionThroughMiddlewareChain(t *testing.T) {
	var logs syncBuffer
	logger := logging.New(&logs, slog.LevelDebug)

	limiter := NewRateLimiter(RateLimit{PerMinute: 1, Burst: 1})
	clientIP := NewClientIP([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	h := NewHandler(&lockedService{svc: newFakeService()}, testStreams(), alwaysReady, logger,
		clientIP.Middleware,
		carelessLog(logger),
		SecurityHeaders,
		ForRoute("POST /tickets", limiter.Middleware),
		LimitBody(256),
		WriteDeadline(5*time.Second, logger),
	)
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = NewServer("", h, ServerTimeouts{ReadHeader: time.Second, Read: time.Second, Idle: time.Second}, logger)
	srv.Start()
	defer srv.Close()

	submitBody := `{"subject":"` + testSubject + `","body":"` + testBody + `"}`
	requests := []struct {
		method, path, body string
		wantStatus         int
	}{
		{http.MethodPost, "/tickets", submitBody, http.StatusAccepted},
		{http.MethodPost, "/tickets?token=" + testStreamToken, submitBody, http.StatusTooManyRequests},
		{http.MethodGet, "/tickets/" + testID.String() + "?token=" + testStreamToken, "", http.StatusOK},
		{http.MethodGet, "/tickets/" + testID.String() + "/events?token=" + testStreamToken, "", http.StatusNotFound},
		{http.MethodPut, "/tickets/" + testID.String() + "?token=" + testStreamToken, "", http.StatusMethodNotAllowed},
		{http.MethodGet, "/tickets/" + testID.String(), strings.Repeat("x", 300), http.StatusRequestEntityTooLarge},
	}
	for _, rq := range requests {
		req, err := http.NewRequestWithContext(t.Context(), rq.method, srv.URL+rq.path, strings.NewReader(rq.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("X-Forwarded-For", "198.51.100.1")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", rq.method, rq.path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != rq.wantStatus {
			t.Errorf("%s %s: status %d, want %d", rq.method, rq.path, resp.StatusCode, rq.wantStatus)
		}
	}

	out := logs.String()
	for msg, want := range map[string]int{`"msg":"http request"`: len(requests), `"msg":"careless"`: len(requests)} {
		if n := strings.Count(out, msg); n != want {
			t.Fatalf("logged %d %s lines, want %d:\n%s", n, msg, want, out)
		}
	}
	for _, want := range []string{`"Authorization":"[REDACTED]"`, `"url":"[REDACTED]"`, `"token":"[REDACTED]"`,
		`"client_ip":"198.51.100.1"`, `"pattern":"GET /tickets/{id}"`} {
		if !strings.Contains(out, want) {
			t.Errorf("logs lack %s:\n%s", want, out)
		}
	}
	for _, secret := range []string{testToken, testStreamToken, testSubject, testBody} {
		if strings.Contains(out, secret) {
			t.Errorf("logs contain %q:\n%s", secret, out)
		}
	}
}
