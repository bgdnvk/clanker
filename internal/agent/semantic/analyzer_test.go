package semantic

import (
	"testing"
)

func TestNewAnalyzer(t *testing.T) {
	a := NewAnalyzer()
	if a == nil {
		t.Fatal("expected non-nil analyzer")
	}
	if len(a.IntentSignals) == 0 {
		t.Error("expected IntentSignals to be populated")
	}
	if len(a.ServiceMapping) == 0 {
		t.Error("expected ServiceMapping to be populated")
	}
}

func TestAnalyzeQuery_ErrorQuery(t *testing.T) {
	a := NewAnalyzer()
	// Use keywords that exist in the intent signals: "error", "failed", "issue"
	intent := a.AnalyzeQuery("lambda error failed to process")

	if intent.Primary == "" {
		t.Error("expected a primary intent to be identified")
	}
	if intent.Primary != "troubleshoot" {
		t.Errorf("expected primary intent 'troubleshoot', got %q", intent.Primary)
	}
	if intent.Confidence <= 0 {
		t.Error("expected positive confidence for a meaningful query")
	}
	if intent.Urgency == "" {
		t.Error("expected urgency to be set")
	}
}

func TestAnalyzeQuery_EmptyQuery(t *testing.T) {
	a := NewAnalyzer()
	intent := a.AnalyzeQuery("")

	// Empty query should produce safe defaults, not panic.
	if intent.Urgency == "" {
		t.Error("expected default urgency for empty query")
	}
	if intent.TimeFrame == "" {
		t.Error("expected default time frame for empty query")
	}
	if intent.TargetServices == nil {
		t.Error("expected non-nil TargetServices")
	}
	if intent.DataTypes == nil {
		t.Error("expected non-nil DataTypes")
	}
}

func TestAnalyzeQuery_ServiceDetection(t *testing.T) {
	a := NewAnalyzer()

	tests := []struct {
		query           string
		expectedService string
	}{
		{"check lambda function status", "lambda"},
		{"show me ec2 instances", "ec2"},
		{"list s3 buckets", "s3"},
		{"ecs service health", "ecs"},
	}

	for _, tt := range tests {
		intent := a.AnalyzeQuery(tt.query)
		found := false
		for _, svc := range intent.TargetServices {
			if svc == tt.expectedService {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AnalyzeQuery(%q): expected service %q in %v", tt.query, tt.expectedService, intent.TargetServices)
		}
	}
}

func TestAnalyzeQuery_UrgencyLevels(t *testing.T) {
	a := NewAnalyzer()

	// A query with urgent keywords should rank higher than one without.
	critical := a.AnalyzeQuery("production is down outage critical")
	normal := a.AnalyzeQuery("list all ec2 instances")

	urgencyRank := map[string]int{
		"low": 0, "medium": 1, "high": 2, "critical": 3,
	}

	if urgencyRank[critical.Urgency] <= urgencyRank[normal.Urgency] {
		t.Errorf("expected critical urgency (%s) > normal urgency (%s)", critical.Urgency, normal.Urgency)
	}
}

func TestAnalyzeQuery_DataTypes(t *testing.T) {
	a := NewAnalyzer()

	intent := a.AnalyzeQuery("show me the logs and metrics")

	hasLogs := false
	hasMetrics := false
	for _, dt := range intent.DataTypes {
		if dt == "logs" {
			hasLogs = true
		}
		if dt == "metrics" {
			hasMetrics = true
		}
	}
	if !hasLogs {
		t.Error("expected 'logs' in DataTypes")
	}
	if !hasMetrics {
		t.Error("expected 'metrics' in DataTypes")
	}
}

func TestAnalyzeQuery_DefaultDataTypes(t *testing.T) {
	a := NewAnalyzer()

	// A query with no explicit data type keywords should still get defaults.
	intent := a.AnalyzeQuery("what is happening")
	if len(intent.DataTypes) == 0 {
		t.Error("expected default DataTypes to be populated")
	}
}

func TestAnalyzeQuery_ConfidenceRange(t *testing.T) {
	a := NewAnalyzer()

	queries := []string{
		"check lambda errors in production",
		"show me ec2 instances",
		"",
		"hello world",
	}

	for _, q := range queries {
		intent := a.AnalyzeQuery(q)
		if intent.Confidence < 0 || intent.Confidence > 1 {
			t.Errorf("AnalyzeQuery(%q): confidence %f is out of [0,1] range", q, intent.Confidence)
		}
	}
}
