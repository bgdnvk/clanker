package logs

import (
	"testing"
	"time"
)

func TestNewEntryStructuredJSON(t *testing.T) {
	raw := `{"level":"ERROR","message":"upstream timeout","trace_id":"abc123"}`
	e := NewEntry("aws", "/svc", "stream-1", raw, time.Unix(1750000000, 0))
	if e.Level != LevelError {
		t.Fatalf("level = %q, want error", e.Level)
	}
	if e.RawLevel != "ERROR" {
		t.Fatalf("rawLevel = %q, want ERROR", e.RawLevel)
	}
	if e.Message != "upstream timeout" {
		t.Fatalf("message = %q, want extracted text", e.Message)
	}
	if e.Labels["trace_id"] != "abc123" {
		t.Fatalf("trace_id label not lifted: %v", e.Labels)
	}
	if e.Ref == "" || e.EpochMs == 0 {
		t.Fatalf("ref/epoch not set: ref=%q epoch=%d", e.Ref, e.EpochMs)
	}
}

func TestNewEntryUnstructuredHeuristicAndUnknown(t *testing.T) {
	e := NewEntry("k8s", "pod", "pod", "plain readiness probe ok", time.Now())
	if e.Level != LevelUnknown {
		t.Fatalf("expected unknown for benign line, got %q", e.Level)
	}
	e2 := NewEntry("k8s", "pod", "pod", "FATAL panic: nil deref", time.Now())
	if e2.Level != LevelFatal {
		t.Fatalf("expected fatal heuristic, got %q", e2.Level)
	}
}

func TestMatcherLevelAndGrep(t *testing.T) {
	warn := NewEntry("aws", "g", "s", `{"level":"warn","message":"slow"}`, time.Now())
	info := NewEntry("aws", "g", "s", `{"level":"info","message":"ok hello"}`, time.Now())

	m := newMatcher(Options{Level: "warn"})
	if !m.Match(warn) || m.Match(info) {
		t.Fatalf("level filter wrong: warn=%v info=%v", m.Match(warn), m.Match(info))
	}

	mg := newMatcher(Options{Grep: "hello"})
	if mg.Match(warn) || !mg.Match(info) {
		t.Fatalf("grep filter wrong")
	}

	mr := newMatcher(Options{Grep: "/sl.w/"})
	if !mr.Match(warn) {
		t.Fatalf("regex grep should match 'slow'")
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	got, err := ParseSince("15m", now)
	if err != nil || !got.Equal(now.Add(-15*time.Minute)) {
		t.Fatalf("15m => %v err=%v", got, err)
	}
	got, err = ParseSince("", now)
	if err != nil || !got.Equal(now.Add(-15*time.Minute)) {
		t.Fatalf("default => %v err=%v", got, err)
	}
	got, err = ParseSince("2026-06-23T11:00:00Z", now)
	if err != nil || got.UTC().Hour() != 11 {
		t.Fatalf("rfc3339 => %v err=%v", got, err)
	}
	if _, err := ParseSince("bogus", now); err == nil {
		t.Fatalf("expected error for bogus since")
	}
}

func TestClusterAndContext(t *testing.T) {
	base := time.Unix(1750000000, 0)
	var entries []Entry
	for i := 0; i < 5; i++ {
		entries = append(entries, NewEntry("aws", "g", "s", `{"level":"info","message":"request 12 served"}`, base.Add(time.Duration(i)*time.Second)))
	}
	entries = append(entries, NewEntry("aws", "g", "s", `{"level":"error","message":"db connection refused"}`, base))

	clusters := ClusterEntries(entries)
	if len(clusters) != 2 {
		t.Fatalf("want 2 clusters (numbers templated together), got %d", len(clusters))
	}
	if clusters[0].Level != LevelError {
		t.Fatalf("error cluster should sort first, got %q", clusters[0].Level)
	}
	infoCluster := clusters[1]
	if infoCluster.Count != 5 {
		t.Fatalf("info cluster count = %d, want 5", infoCluster.Count)
	}

	block, truncated := BuildChatContext(entries, 60)
	if truncated {
		t.Fatalf("should not truncate small set")
	}
	if block == "" {
		t.Fatalf("empty context block")
	}
}
