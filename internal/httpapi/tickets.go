package httpapi

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/ticket"
)

// MaxBodyBytes caps the POST /tickets request body (PRD §4.1).
const MaxBodyBytes = 16 << 10

// backlogRetryAfter is the Retry-After, in seconds, sent with the 503 for a
// full pending backlog.
const backlogRetryAfter = "30"

type submitRequest struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type submitResponse struct {
	ID          string        `json:"id"`
	Status      ticket.Status `json:"status"`
	AccessToken string        `json:"access_token"`
	CreatedAt   time.Time     `json:"created_at"`
}

// ticketResponse is the public view of a ticket. Confidences and raw
// probabilities are internal and left out (PRD §3.1).
type ticketResponse struct {
	ID           string         `json:"id"`
	Status       ticket.Status  `json:"status"`
	StatusReason *ticket.Reason `json:"status_reason"`
	Message      string         `json:"message"`
	Category     *string        `json:"category"`
	Priority     *string        `json:"priority"`
	IsComplaint  *bool          `json:"is_complaint"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ClassifiedAt *time.Time     `json:"classified_at"`
}

func newTicketResponse(t ticket.Ticket) ticketResponse {
	resp := ticketResponse{
		ID:        t.ID.String(),
		Status:    t.Status,
		Message:   t.Message(),
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
	}
	if t.StatusReason != "" {
		resp.StatusReason = &t.StatusReason
	}
	if c := t.Classification; c != nil {
		priority := c.Priority().String()
		complaint := c.ComplaintP >= 0.5
		resp.Category, resp.Priority, resp.IsComplaint = &c.Category, &priority, &complaint
	}
	if !t.ClassifiedAt.IsZero() {
		resp.ClassifiedAt = &t.ClassifiedAt
	}
	return resp
}

// submitTicket handles POST /tickets.
func (a *api) submitTicket(w http.ResponseWriter, r *http.Request) {
	if !isJSON(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType,
			"Content-Type must be application/json")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if maxErr := (*http.MaxBytesError)(nil); errors.As(err, &maxErr) {
		writeError(w, http.StatusRequestEntityTooLarge, codePayloadTooLarge,
			fmt.Sprintf("request body must be at most %d bytes", maxErr.Limit))
		return
	}
	if err != nil {
		// The client stopped sending; there is no one to answer.
		a.log(r).Debug("read request body", slog.Any("error", err))
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "could not read the request body")
		return
	}
	var req submitRequest
	if err := json.Unmarshal(data, &req, json.RejectUnknownMembers(true)); err != nil {
		// The decode error is not echoed: it can quote the request body.
		writeError(w, http.StatusBadRequest, codeInvalidJSON,
			`request body must be a JSON object with string fields "subject" and "body"`)
		return
	}

	t, token, err := a.svc.Submit(r.Context(), req.Subject, req.Body)
	if vErr := (*ticket.ValidationError)(nil); errors.As(err, &vErr) {
		writeError(w, http.StatusBadRequest, codeValidationFailed, validationMessage(vErr))
		return
	}
	if errors.Is(err, ticket.ErrBacklogFull) {
		w.Header().Set("Retry-After", backlogRetryAfter)
		writeError(w, http.StatusServiceUnavailable, codeBacklogFull, "too many pending tickets, retry later")
		return
	}
	if err != nil {
		a.log(r).Error("submit ticket", slog.Any("error", err))
		writeInternalError(w)
		return
	}

	w.Header().Set("Location", "/tickets/"+t.ID.String())
	writeJSON(w, http.StatusAccepted, submitResponse{
		ID:          t.ID.String(),
		Status:      t.Status,
		AccessToken: token.Reveal(),
		CreatedAt:   t.CreatedAt,
	})
}

// getTicket handles GET /tickets/{id}. A missing or malformed token, an
// unknown or malformed id, and a wrong token all get the same 404.
func (a *api) getTicket(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r.Header)
	if !ok {
		// Answering without a lookup reveals nothing about the ticket: the
		// client knows it sent no usable token.
		writeNotFound(w)
		return
	}
	t, err := a.svc.Get(r.Context(), r.PathValue("id"), token)
	if errors.Is(err, ticket.ErrNotFound) {
		writeNotFound(w)
		return
	}
	if err != nil {
		a.log(r).Error("get ticket", slog.Any("error", err))
		writeInternalError(w)
		return
	}
	writeJSON(w, http.StatusOK, newTicketResponse(t))
}

// bearerToken returns the token of a single "Authorization: Bearer <token>"
// header. The scheme is case-insensitive (RFC 9110 §11.1).
func bearerToken(h http.Header) (ticket.Token, bool) {
	values := h.Values("Authorization")
	if len(values) != 1 {
		return ticket.Token{}, false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return ticket.Token{}, false
	}
	return ticket.TokenFromString(token), true
}

// isJSON reports whether a Content-Type header value is application/json,
// with or without parameters such as charset.
func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

// validationMessage lists the problems; they hold field names and limits,
// never the submitted text.
func validationMessage(err *ticket.ValidationError) string {
	msgs := make([]string, len(err.Problems))
	for i, p := range err.Problems {
		msgs[i] = p.String()
	}
	return strings.Join(msgs, "; ")
}
