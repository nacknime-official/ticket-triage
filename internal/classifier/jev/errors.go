package jev

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
)

// statusOverloaded is the TypeSafe "overloaded" status.
const statusOverloaded = 529

// Error is a call that got a non-success HTTP response. It wraps one of
// classifier.ErrTransient, ErrPermanent or ErrAuth. Its text never holds the
// response body, which may echo the request and so the ticket text.
type Error struct {
	kind       error
	status     int
	code       string
	retryAfter time.Duration
}

// Error implements error.
func (e *Error) Error() string {
	msg := fmt.Sprintf("%v: jev: HTTP %d", e.kind, e.status)
	if e.code != "" {
		msg += " (code " + e.code + ")"
	}
	if e.retryAfter > 0 {
		msg += fmt.Sprintf(", retry after %v", e.retryAfter)
	}
	return msg
}

// Unwrap returns the classifier error the status maps to.
func (e *Error) Unwrap() error { return e.kind }

// HTTPStatus returns the response status code.
func (e *Error) HTTPStatus() int { return e.status }

// RetryAfter returns the delay the Retry-After header asked for, or 0 when
// there was none. The processor reads it through an interface it declares.
func (e *Error) RetryAfter() time.Duration { return e.retryAfter }

// statusKind maps a non-2xx status to a classifier error (PRD §7).
func statusKind(status int) error {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return classifier.ErrAuth
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests,
		status == statusOverloaded, status >= 500:
		return classifier.ErrTransient
	default:
		// 400, 422 and every other status a retry will not fix, including a
		// redirect, which the client never follows.
		return classifier.ErrPermanent
	}
}

// statusError builds the Error for a non-2xx response. now is the time the
// response arrived, for a Retry-After date.
func statusError(status int, h http.Header, body []byte, now time.Time) *Error {
	e := &Error{kind: statusKind(status), status: status, code: errorCode(body)}
	if errors.Is(e.kind, classifier.ErrTransient) {
		e.retryAfter = parseRetryAfter(h.Get("Retry-After"), now)
	}
	return e
}

// parseRetryAfter reads a Retry-After value: delay seconds or an HTTP date.
// [unverified] Whether Jev sends it on 429 or 529 is unknown: cmd/jevprobe
// (T12) never got either, and the 400 and 401 answers it saw carry none. A
// missing, malformed or past value gives 0 and the caller's own backoff
// applies.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// safeCode is what an error code may look like to be logged.
var safeCode = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// errorCode returns the provider's error code from an error body, or "".
// Seen live (cmd/jevprobe, T12): OpenRouter answers
// {"error": {"code": <HTTP status>, "message": ...}} on 400 and 401, and the
// direct API {"detail": {"error_type": ..., "message": ...}} on 401. Only a
// short code made of safe characters is kept; the message is dropped because
// it may quote the request (a validation message names the offending field).
func errorCode(body []byte) string {
	var w struct {
		Error  jsontext.Value `json:"error"`
		Detail jsontext.Value `json:"detail"`
	}
	if json.Unmarshal(body, &w) != nil {
		return ""
	}
	var (
		e struct {
			Code jsontext.Value `json:"code"`
		}
		d struct {
			ErrorType jsontext.Value `json:"error_type"`
		}
		raw jsontext.Value
	)
	switch {
	case w.Error.Kind() == '{' && json.Unmarshal(w.Error, &e) == nil && len(e.Code) > 0:
		raw = e.Code
	case w.Detail.Kind() == '{' && json.Unmarshal(w.Detail, &d) == nil && len(d.ErrorType) > 0:
		raw = d.ErrorType
	default:
		return ""
	}
	var code string
	switch raw.Kind() {
	case '"':
		if json.Unmarshal(raw, &code) != nil {
			return ""
		}
	case '0':
		code = string(raw)
	}
	if !safeCode.MatchString(code) {
		return ""
	}
	return code
}
