package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nacknime-official/ticket-triage/internal/classifier"
	"github.com/nacknime-official/ticket-triage/internal/classifier/jev"
	"github.com/nacknime-official/ticket-triage/internal/config"
)

func TestRunRejects(t *testing.T) {
	const key = "sk-or-test-secret"
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{"extra argument", []string{"now"}, nil, "usage: worker"},
		{"invalid config", nil, map[string]string{"KAFKA_BROKERS": "no-port", "WORKER_CONCURRENCY": "0"},
			`KAFKA_BROKERS: must be a comma-separated list of host:port, got "no-port"; WORKER_CONCURRENCY`},
		{"invalid database url", nil, map[string]string{"DATABASE_URL": "postgres://%zz"}, "parse database url"},
		{"jev, invalid database url", nil,
			map[string]string{"CLASSIFIER": "jev", "OPENROUTER_API_KEY": key, "DATABASE_URL": "postgres://%zz"},
			"parse database url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(name string) string { return tt.env[name] }
			var logs bytes.Buffer
			err := run(t.Context(), tt.args, getenv, &logs)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("run error = %v, want one containing %q", err, tt.wantErr)
			}
			if strings.Contains(logs.String(), key) || strings.Contains(err.Error(), key) {
				t.Errorf("logs leak the API key: %s", logs.String())
			}
		})
	}
}

func TestNewClassifier(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Classifier
		want any
	}{
		{"fake", config.Classifier{Kind: config.ClassifierFake}, &classifier.Fake{}},
		{"jev", config.Classifier{Kind: config.ClassifierJev, Jev: config.Jev{
			Provider: config.ProviderOpenRouter, APIKey: config.NewSecret("k"), Model: "typesafe/jev-1.14",
			Timeout: time.Second, RateLimit: 1, MaxConcurrency: 1,
		}}, &jev.Client{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := newClassifier(tt.cfg)
			if err != nil {
				t.Fatalf("newClassifier: %v", err)
			}
			if got, want := reflect.TypeOf(c), reflect.TypeOf(tt.want); got != want {
				t.Errorf("newClassifier = %v, want %v", got, want)
			}
		})
	}

	if _, err := newClassifier(config.Classifier{Kind: "magic"}); err == nil {
		t.Error("newClassifier(magic) succeeded, want an error")
	}
	if _, err := newClassifier(config.Classifier{Kind: config.ClassifierJev}); err == nil {
		t.Error("newClassifier(jev without config) succeeded, want an error")
	}
}
