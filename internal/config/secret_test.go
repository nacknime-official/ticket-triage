package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const (
	testAPIKey = "sk-or-v1-0123456789abcdef"
	testDBPass = "hunter2"
)

func secretConfig(t *testing.T) all {
	t.Helper()
	c, err := loadAll(envOf(map[string]string{
		"CLASSIFIER":         "jev",
		"OPENROUTER_API_KEY": testAPIKey,
		"DATABASE_URL":       "postgres://triage:" + testDBPass + "@db:5432/triage",
	}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSecretFormatting(t *testing.T) {
	s := NewSecret(testAPIKey)
	if s.Reveal() != testAPIKey {
		t.Fatalf("Reveal = %q", s.Reveal())
	}
	text, err := s.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{
		"String":      s.String(),
		"GoString":    s.GoString(),
		"MarshalText": string(text),
		"LogValue":    s.LogValue().String(),
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%10s"} {
		outputs[verb] = fmt.Sprintf(verb, s)
		outputs[verb+" pointer"] = fmt.Sprintf(verb, &s)
	}
	for name, out := range outputs {
		if strings.Contains(out, testAPIKey) || !strings.Contains(out, redacted) {
			t.Errorf("%s = %q, want it redacted", name, out)
		}
	}

	var empty Secret
	if !empty.IsZero() || empty.String() != "" || fmt.Sprint(empty) != "" {
		t.Errorf("unset secret prints %q, want empty", empty.String())
	}
}

func TestGroupsNeverPrintSecrets(t *testing.T) {
	c := secretConfig(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("config loaded", "postgres", c.Postgres, "classifier", c.Classifier, "all", c)
	logger.Info("as any", slog.Any("jev", any(c.Classifier.Jev)))

	outputs := map[string]string{
		"slog JSON": buf.String(),
		"%v":        fmt.Sprintf("%v", c),
		"%+v":       fmt.Sprintf("%+v", c),
		"%#v":       fmt.Sprintf("%#v", c),
		"%s":        fmt.Sprintf("%s", c.Postgres),
	}
	for name, out := range outputs {
		for _, secret := range []string{testAPIKey, testDBPass} {
			if strings.Contains(out, secret) {
				t.Errorf("%s output leaks %q: %s", name, secret, out)
			}
		}
	}
	for _, want := range []string{`"api_key":"[REDACTED]"`, `"url":"[REDACTED]"`, `"timeout":3000000000`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("slog output lacks %s: %s", want, buf.String())
		}
	}
}

func TestGroupLogValues(t *testing.T) {
	c := secretConfig(t)
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("cfg",
		"log", c.Log, "http", c.HTTP, "kafka", c.Kafka, "thresholds", c.Thresholds, "limits", c.Limits)
	for _, want := range []string{
		"log.level=INFO", "http.debug_addr=127.0.0.1:6060", "kafka.brokers=[localhost:29092]",
		"thresholds.complaint_uncertain_high=0.65", "limits.sse_max_duration=10m0s",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output lacks %s: %s", want, buf.String())
		}
	}
}
