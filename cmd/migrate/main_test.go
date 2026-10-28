package main

import (
	"io"
	"strings"
	"testing"
)

func TestRunRejectsBadInput(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		databaseURL string
		wantErr     string
	}{
		{"no command", nil, "postgres://x", "usage"},
		{"two commands", []string{"up", "down"}, "postgres://x", "usage"},
		{"unknown command", []string{"redo"}, "postgres://x", `unknown command "redo"`},
		{"missing DATABASE_URL", []string{"up"}, "", "DATABASE_URL is not set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(t.Context(), tt.args, tt.databaseURL, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("run() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
