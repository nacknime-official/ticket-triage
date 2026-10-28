package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kfake"
)

func TestRun(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1))
	if err != nil {
		t.Fatalf("start kfake cluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	env := map[string]string{"KAFKA_BROKERS": strings.Join(cluster.ListenAddrs(), ",")}
	getenv := func(name string) string { return env[name] }

	tests := []struct {
		name string
		want string
	}{
		{"first run creates", "created  tickets.submitted\ncreated  tickets.submitted.retry\n" +
			"created  tickets.submitted.dlq\ncreated  tickets.events\n"},
		{"second run is a no-op", "exists   tickets.submitted\nexists   tickets.submitted.retry\n" +
			"exists   tickets.submitted.dlq\nexists   tickets.events\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(t.Context(), nil, getenv, &out); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := out.String(); got != tt.want {
				t.Errorf("output:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestRunRejects(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{"extra argument", []string{"up"}, nil, "usage: kafka-setup"},
		{"invalid brokers", nil, map[string]string{"KAFKA_BROKERS": "no-port"}, "KAFKA_BROKERS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(name string) string { return tt.env[name] }
			err := run(t.Context(), tt.args, getenv, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("run error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}
