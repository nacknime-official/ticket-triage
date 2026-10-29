package httpapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"github.com/nacknime-official/ticket-triage/internal/notify"
	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// Defaults of the zero fields of Streams (PRD §3.4).
const (
	DefaultHeartbeat          = 15 * time.Second
	DefaultMaxStreamDuration  = 10 * time.Minute
	DefaultStreamWriteTimeout = 10 * time.Second
)

// StreamService issues and redeems stream tokens. ticket.Streams implements it.
type StreamService interface {
	// Issue returns a new stream token for the ticket and its lifetime if
	// access is the ticket's access token, and an error wrapping
	// ticket.ErrNotFound for every not-found or unauthorized case.
	Issue(ctx context.Context, id string, access ticket.Token) (ticket.Token, time.Duration, error)
	// Open consumes the stream token of the ticket and returns the ticket
	// id, or an error wrapping ticket.ErrNotFound for an unknown, used,
	// expired or other ticket's token.
	Open(ctx context.Context, id string, token ticket.Token) (uuid.UUID, error)
	// Current returns the ticket with the id, which Open returned, without
	// checking a token.
	Current(ctx context.Context, id uuid.UUID) (ticket.Ticket, error)
}

// Streams configures the stream-token and SSE routes. Zero durations take
// the defaults.
type Streams struct {
	Service StreamService
	// Hub delivers the status events of tickets.events to the streams.
	// Closing it ends every open stream, which lets the server shut down.
	Hub *notify.Hub
	// Heartbeat is the interval of the ": heartbeat" comment that keeps
	// proxies from closing an idle stream.
	Heartbeat time.Duration
	// MaxDuration ends a stream that is still open after it.
	MaxDuration time.Duration
	// WriteTimeout bounds each write to the stream, so a client that stops
	// reading cannot hold the handler forever.
	WriteTimeout time.Duration
}

func (s Streams) withDefaults() Streams {
	if s.Heartbeat <= 0 {
		s.Heartbeat = DefaultHeartbeat
	}
	if s.MaxDuration <= 0 {
		s.MaxDuration = DefaultMaxStreamDuration
	}
	if s.WriteTimeout <= 0 {
		s.WriteTimeout = DefaultStreamWriteTimeout
	}
	return s
}

type streamTokenResponse struct {
	StreamToken string `json:"stream_token"`
	ExpiresIn   int    `json:"expires_in"` // seconds
}

// issueStreamToken handles POST /tickets/{id}/stream-token. It answers like
// GET /tickets/{id}: the same 404 for every not-found or unauthorized case.
func (a *api) issueStreamToken(w http.ResponseWriter, r *http.Request) {
	access, ok := bearerToken(r.Header)
	if !ok {
		writeNotFound(w)
		return
	}
	token, ttl, err := a.streams.Service.Issue(r.Context(), r.PathValue("id"), access)
	if errors.Is(err, ticket.ErrNotFound) {
		writeNotFound(w)
		return
	}
	if err != nil {
		a.log(r).Error("issue stream token", slog.Any("error", err))
		writeInternalError(w)
		return
	}
	writeJSON(w, http.StatusCreated, streamTokenResponse{
		StreamToken: token.Reveal(),
		ExpiresIn:   int(ttl / time.Second),
	})
}

// streamEvents handles GET /tickets/{id}/events?token=: the SSE stream of
// PRD §3.4. It consumes the stream token, subscribes to the hub and only
// then reads the current state, so a status change between submit and
// subscribe is not lost: it is either in the state read or in the hub. The
// first event is that state; then each status change follows, with a
// heartbeat comment in between, until a final status, MaxDuration, client
// disconnect, or the hub closing.
func (a *api) streamEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// HEAD would hold a stream open with nothing to send.
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed")
		return
	}
	values := r.URL.Query()["token"]
	if len(values) != 1 || values[0] == "" {
		writeNotFound(w)
		return
	}
	ctx := r.Context()
	id, err := a.streams.Service.Open(ctx, r.PathValue("id"), ticket.TokenFromString(values[0]))
	if errors.Is(err, ticket.ErrNotFound) {
		writeNotFound(w)
		return
	}
	if err != nil {
		a.log(r).Error("open stream", slog.Any("error", err))
		writeInternalError(w)
		return
	}

	sub := a.streams.Hub.Subscribe(id)
	defer sub.Close()
	t, err := a.streams.Service.Current(ctx, id)
	if errors.Is(err, ticket.ErrNotFound) {
		writeNotFound(w)
		return
	}
	if err != nil {
		a.log(r).Error("read ticket for stream", slog.Any("error", err))
		writeInternalError(w)
		return
	}

	s := &sseWriter{w: w, rc: http.NewResponseController(w), timeout: a.streams.WriteTimeout}
	if err := s.setDeadline(); err != nil {
		// See WriteDeadline: a wrapper hides the connection.
		a.log(r).Error("set stream write deadline", slog.Any("error", err))
		writeInternalError(w)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	state := newTicketResponse(t)
	if err := s.status(state); err != nil || t.Status.Final() {
		return
	}

	heartbeat := time.NewTicker(a.streams.Heartbeat)
	defer heartbeat.Stop()
	maxDuration := time.NewTimer(a.streams.MaxDuration)
	defer maxDuration.Stop()
	log := a.log(r).With(slog.String("ticket_id", id.String()))
	for {
		select {
		case <-ctx.Done():
			return
		case <-maxDuration.C:
			log.Debug("stream reached its max duration")
			return
		case <-heartbeat.C:
			if err := s.send([]byte(": heartbeat\n\n")); err != nil {
				log.Debug("stream write failed", slog.Any("error", err))
				return
			}
		case e, ok := <-sub.Events():
			if !ok {
				log.Debug("stream closed by the hub", slog.Bool("dropped", sub.Dropped()))
				return
			}
			if e.Status == state.Status {
				continue // a duplicate: the events topic is at least once
			}
			state = applyStatusChanged(state, e)
			if err := s.status(state); err != nil {
				log.Debug("stream write failed", slog.Any("error", err))
				return
			}
			if e.Status.Final() {
				return
			}
		}
	}
}

// applyStatusChanged returns the GET /tickets/{id} view after the event. The
// event carries every field that a status change sets; a final status sets
// classified_at too.
func applyStatusChanged(resp ticketResponse, e ticket.StatusChanged) ticketResponse {
	resp.Status, resp.StatusReason, resp.Message = e.Status, e.StatusReason, e.Message
	resp.Category, resp.Priority, resp.IsComplaint = e.Category, e.Priority, e.IsComplaint
	resp.UpdatedAt = e.At
	if e.Status.Final() {
		at := e.At
		resp.ClassifiedAt = &at
	}
	return resp
}

// sseWriter writes server-sent events, each under its own write deadline,
// and flushes them at once.
type sseWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

func (s *sseWriter) setDeadline() error {
	return s.rc.SetWriteDeadline(time.Now().Add(s.timeout))
}

// status sends the ticket view as a "status" event.
func (s *sseWriter) status(resp ticketResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal status event: %w", err)
	}
	// json.Marshal output has no newline, so it fits one data line.
	msg := make([]byte, 0, len(b)+len("event: status\ndata: \n\n"))
	msg = append(msg, "event: status\ndata: "...)
	msg = append(msg, b...)
	return s.send(append(msg, "\n\n"...))
}

func (s *sseWriter) send(msg []byte) error {
	if err := s.setDeadline(); err != nil {
		return err
	}
	if _, err := s.w.Write(msg); err != nil {
		return err
	}
	return s.rc.Flush()
}
