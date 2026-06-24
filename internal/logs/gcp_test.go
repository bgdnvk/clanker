package logs

import (
	"strings"
	"testing"
	"time"

	gcplogging "google.golang.org/api/logging/v2"
)

func TestGCPEntryFilterPushesTimeAndSeverity(t *testing.T) {
	since := time.Date(2026, 6, 24, 15, 0, 0, 0, time.UTC)
	filter := gcpEntryFilter(Options{
		Resource: `logName="projects/demo/logs/run.googleapis.com%2Fstderr"`,
		Since:    since,
		Level:    "error",
	}, true)

	for _, want := range []string{
		`(logName="projects/demo/logs/run.googleapis.com%2Fstderr")`,
		`timestamp >= "2026-06-24T15:00:00Z"`,
		`severity >= ERROR`,
	} {
		if !strings.Contains(filter, want) {
			t.Fatalf("filter %q missing %q", filter, want)
		}
	}
}

func TestGCPToEntryNormalizesAPILogEntry(t *testing.T) {
	raw := []byte(`{"message":"database connection failed","code":500}`)
	entry := (&gcpCollector{}).toEntry(Options{}, &gcplogging.LogEntry{
		Timestamp:   "2026-06-24T15:04:31.766177773Z",
		Severity:    "ERROR",
		LogName:     "projects/demo/logs/run.googleapis.com%2Fstderr",
		InsertId:    "insert-1",
		JsonPayload: raw,
		Resource: &gcplogging.MonitoredResource{
			Type: "cloud_run_revision",
			Labels: map[string]string{
				"service_name": "api",
				"location":     "us-east4",
			},
		},
		Trace: "projects/demo/traces/trace-1",
	})

	if entry.Message != "database connection failed" {
		t.Fatalf("Message = %q, want database connection failed", entry.Message)
	}
	if entry.Level != LevelError || entry.RawLevel != "ERROR" {
		t.Fatalf("level = %q raw = %q, want error/ERROR", entry.Level, entry.RawLevel)
	}
	if entry.Service != "api" {
		t.Fatalf("Service = %q, want api", entry.Service)
	}
	if entry.Labels["resource.location"] != "us-east4" || entry.Labels["trace"] == "" {
		t.Fatalf("labels = %#v, want location and trace", entry.Labels)
	}
}
