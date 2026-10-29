package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseShadowFlags(t *testing.T) {
	now := time.Date(2026, 10, 29, 15, 4, 5, 0, time.UTC)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		args    []string
		want    shadowFlags
		wantErr string
	}{
		{"defaults", nil, shadowFlags{runID: "20261029t150405z", maxTickets: defaultMaxTickets}, ""},
		{"all flags", []string{"-run-id", "jev-1.13", "-from", "2026-10-01T00:00:00Z", "-max-tickets", "5", "-dry-run"},
			shadowFlags{runID: "jev-1.13", from: from, maxTickets: 5, dryRun: true}, ""},
		{"earliest", []string{"-from", "earliest"}, shadowFlags{runID: "20261029t150405z", maxTickets: defaultMaxTickets}, ""},
		{"bad run id", []string{"-run-id", "Main"}, shadowFlags{}, `-run-id "Main"`},
		{"bad from", []string{"-from", "yesterday"}, shadowFlags{}, `-from "yesterday"`},
		{"zero max", []string{"-max-tickets", "0"}, shadowFlags{}, "-max-tickets 0"},
		{"unknown flag", []string{"-commit-main"}, shadowFlags{}, "flag provided but not defined"},
		{"extra argument", []string{"now"}, shadowFlags{}, `unexpected argument "now"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseShadowFlags(tt.args, now, io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("parseShadowFlags error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseShadowFlags: %v", err)
			}
			if !got.from.Equal(tt.want.from) {
				t.Errorf("from = %v, want %v", got.from, tt.want.from)
			}
			got.from, tt.want.from = time.Time{}, time.Time{}
			if got != tt.want {
				t.Errorf("parseShadowFlags = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRunShadowRejects(t *testing.T) {
	const key = "sk-or-test-secret"
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{"bad flag", []string{"-max-tickets", "-1"}, nil, "-max-tickets -1"},
		{"invalid config", nil, map[string]string{"KAFKA_BROKERS": "no-port"}, "KAFKA_BROKERS"},
		{"workers' group", []string{"-run-id", "x"}, map[string]string{"KAFKA_CONSUMER_GROUP": "triage-shadow-x"},
			"is KAFKA_CONSUMER_GROUP"},
		{"invalid database url", nil, map[string]string{"DATABASE_URL": "postgres://%zz"}, "parse database url"},
		{"jev, invalid database url", nil,
			map[string]string{"CLASSIFIER": "jev", "OPENROUTER_API_KEY": key, "DATABASE_URL": "postgres://%zz"},
			"parse database url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(name string) string { return tt.env[name] }
			var out, logs bytes.Buffer
			err := runShadow(t.Context(), tt.args, getenv, &out, &logs)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("runShadow error = %v, want one containing %q", err, tt.wantErr)
			}
			if strings.Contains(logs.String(), key) || strings.Contains(err.Error(), key) {
				t.Errorf("logs leak the API key: %s", logs.String())
			}
		})
	}
}
