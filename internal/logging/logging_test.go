package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const (
	accessToken = "ACCESSTOKENB32VALUE7Q"
	streamToken = "STREAMTOKENB32VALUE3K"
	subject     = "My card was charged twice"
	body        = "Order 4411, please refund the second payment"
)

// secrets are the values that must never appear in any test's output.
var secrets = []string{accessToken, streamToken, subject, body}

// ticket mimics a domain type that does not hide its own fields.
type ticket struct{ Subject, Body string }

func (t ticket) LogValue() slog.Value {
	return slog.GroupValue(slog.String("subject", t.Subject), slog.String("body", t.Body), slog.Int("len", 3))
}

func TestRedaction(t *testing.T) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+accessToken)
	header.Set("Content-Type", "application/json")

	tests := []struct {
		name string
		log  func(l *slog.Logger)
		want []string // substrings proving the attribute was kept or redacted
	}{
		{
			name: "sensitive keys",
			log: func(l *slog.Logger) {
				l.Info("submit", "token", accessToken, "access_token", accessToken, "stream_token", streamToken,
					"subject", subject, "body", body, "ticket_id", "0190-id")
			},
			want: []string{`"token":"[REDACTED]"`, `"stream_token":"[REDACTED]"`, `"subject":"[REDACTED]"`,
				`"body":"[REDACTED]"`, `"ticket_id":"0190-id"`},
		},
		{
			name: "keys match without regard to case or dashes",
			log: func(l *slog.Logger) {
				l.Info("req", "Authorization", "Bearer "+accessToken, "Stream-Token", streamToken,
					"Ticket_Subject", subject, "REQUEST_BODY", body)
			},
			want: []string{`"Authorization":"[REDACTED]"`, `"Stream-Token":"[REDACTED]"`,
				`"Ticket_Subject":"[REDACTED]"`, `"REQUEST_BODY":"[REDACTED]"`},
		},
		{
			name: "attributes added with With and WithGroup",
			log: func(l *slog.Logger) {
				l.With("token", accessToken).WithGroup("ticket").With("body", body).Info("x", "subject", subject)
			},
			want: []string{`"token":"[REDACTED]"`, `"ticket":{"body":"[REDACTED]","subject":"[REDACTED]"}`},
		},
		{
			name: "everything inside a sensitive group",
			log: func(l *slog.Logger) {
				l.Info("x", slog.Group("body", "text", body, "nested", slog.GroupValue(slog.String("t", accessToken))))
				l.WithGroup("subject").Info("y", "text", subject)
			},
			want: []string{`"body":{"text":"[REDACTED]","nested":{"t":"[REDACTED]"}}`, `"subject":{"text":"[REDACTED]"}`},
		},
		{
			name: "LogValuer resolved before redaction",
			log: func(l *slog.Logger) {
				l.Info("x", "ticket", ticket{Subject: subject, Body: body})
			},
			want: []string{`"ticket":{"subject":"[REDACTED]","body":"[REDACTED]","len":3}`},
		},
		{
			name: "http.Header keeps safe headers",
			log: func(l *slog.Logger) {
				l.Info("req", "headers", header)
			},
			want: []string{`"Authorization":"[REDACTED]"`, `"Content-Type":["application/json"]`},
		},
		{
			name: "url.Values and nested maps",
			log: func(l *slog.Logger) {
				l.Info("req", "query", url.Values{"token": {streamToken}, "page": {"2"}},
					"meta", map[string]any{"inner": map[string]string{"Subject": subject}, "n": 1})
			},
			want: []string{`"query":{"page":["2"],"token":"[REDACTED]"}`,
				`"meta":{"inner":{"Subject":"[REDACTED]"},"n":1}`},
		},
		{
			name: "bearer and token= values under any key",
			log: func(l *slog.Logger) {
				l.Info("req", "auth", "bearer "+accessToken, "url", "/tickets/1/events?token="+streamToken,
					"hdr", map[string][]string{"X-Fwd": {"Bearer " + accessToken}}, "route", "GET /tickets/{id}/events")
			},
			want: []string{`"auth":"[REDACTED]"`, `"url":"[REDACTED]"`, `"hdr":{"X-Fwd":"[REDACTED]"}`,
				`"route":"GET /tickets/{id}/events"`},
		},
		{
			name: "message carrying a raw URL",
			log: func(l *slog.Logger) {
				l.Info("GET /tickets/1/events?token=" + streamToken)
			},
			want: []string{`"msg":"[REDACTED]"`},
		},
		{
			name: "safe Jev call fields untouched",
			log: func(l *slog.Logger) {
				l.Info("jev call", "input_tokens", 450, "output_tokens", 3, "model", "typesafe/jev-1.14")
			},
			want: []string{`"input_tokens":450`, `"output_tokens":3`, `"model":"typesafe/jev-1.14"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tt.log(New(&buf, slog.LevelDebug))
			out := buf.String()
			for _, s := range secrets {
				if strings.Contains(out, s) {
					t.Errorf("output leaks %q:\n%s", s, out)
				}
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %s:\n%s", w, out)
				}
			}
		})
	}
}

func TestLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelWarn)
	l.Info("hidden")
	l.Warn("shown")
	if out := buf.String(); strings.Contains(out, "hidden") || !strings.Contains(out, `"level":"WARN","msg":"shown"`) {
		t.Errorf("output = %s", out)
	}
}

func TestRedactWithTextHandler(t *testing.T) {
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: Redact})).Info("x", "token", accessToken)
	if out := buf.String(); strings.Contains(out, accessToken) || !strings.Contains(out, "token="+Redacted) {
		t.Errorf("output = %s", out)
	}
}
