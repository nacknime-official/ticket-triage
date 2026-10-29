package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/nacknime-official/ticket-triage/internal/kafka"
)

func TestRun(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1),
		kfake.SeedTopics(1, kafka.TopicSubmitted, kafka.TopicSubmittedDLQ))
	if err != nil {
		t.Fatalf("start kfake cluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	addrs := cluster.ListenAddrs()
	env := map[string]string{"KAFKA_BROKERS": strings.Join(addrs, ",")}
	getenv := func(name string) string { return env[name] }

	cl, err := kafka.NewClient(addrs, "redrive-test")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(cl.Close)
	dead := func(id, class string) *kgo.Record {
		return &kgo.Record{Topic: kafka.TopicSubmittedDLQ, Key: []byte(id), Value: []byte("payload"),
			Headers: []kgo.RecordHeader{
				{Key: kafka.HeaderOriginalTopic, Value: []byte(kafka.TopicSubmitted)},
				{Key: kafka.HeaderOriginalPartition, Value: []byte("0")},
				{Key: kafka.HeaderOriginalOffset, Value: []byte("7")},
				{Key: kafka.HeaderAttempt, Value: []byte("3")},
				{Key: kafka.HeaderErrorClass, Value: []byte(class)},
				{Key: kafka.HeaderFailedAt, Value: []byte("2026-10-29T12:00:00Z")},
			}}
	}
	if err := cl.ProduceSync(t.Context(), dead("t1", "transient"), dead("t2", "permanent")).FirstErr(); err != nil {
		t.Fatalf("ProduceSync: %v", err)
	}
	submitted := func() int64 {
		ends, err := kadm.NewClient(cl).ListEndOffsets(t.Context(), kafka.TopicSubmitted)
		if err != nil {
			t.Fatalf("ListEndOffsets: %v", err)
		}
		o, _ := ends.Lookup(kafka.TopicSubmitted, 0)
		return o.Offset
	}

	tests := []struct {
		name          string
		args          []string
		want          string
		wantSubmitted int64
	}{
		{"dry run", []string{"-dry-run"},
			"would-redrive partition=0 offset=0 ticket=t1 class=transient attempt=3 failed_at=2026-10-29T12:00:00Z original=tickets.submitted/0/7\n" +
				"would-redrive partition=0 offset=1 ticket=t2 class=permanent attempt=3 failed_at=2026-10-29T12:00:00Z original=tickets.submitted/0/7\n" +
				"2 records matched (dry run)\n", 0},
		{"by class", []string{"-class", "transient"},
			"redriven partition=0 offset=0 ticket=t1 class=transient attempt=3 failed_at=2026-10-29T12:00:00Z original=tickets.submitted/0/7\n" +
				"1 records re-driven\n", 1},
		{"no match", []string{"-ticket", "t3"}, "0 records re-driven\n", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(t.Context(), tt.args, getenv, &out, &bytes.Buffer{}); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := out.String(); got != tt.want {
				t.Errorf("output:\n%s\nwant:\n%s", got, tt.want)
			}
			if got := submitted(); got != tt.wantSubmitted {
				t.Errorf("tickets.submitted has %d records, want %d", got, tt.wantSubmitted)
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
		{"extra argument", []string{"now"}, nil, `unexpected argument "now"`},
		{"unknown flag", []string{"-all"}, nil, "flag provided but not defined"},
		{"unknown class", []string{"-class", "transient,oops"}, nil, `unknown -class "oops"`},
		{"invalid brokers", nil, map[string]string{"KAFKA_BROKERS": "no-port"}, "KAFKA_BROKERS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(name string) string { return tt.env[name] }
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := run(ctx, tt.args, getenv, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("run error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSplit(t *testing.T) {
	if got := split(" a, ,b,"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("split = %q, want [a b]", got)
	}
	if got := split(""); got != nil {
		t.Errorf("split(\"\") = %q, want nil", got)
	}
}
