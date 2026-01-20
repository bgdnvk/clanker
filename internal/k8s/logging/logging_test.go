package logging

import (
	"context"
	"strings"
	"testing"
)

// mockK8sClient is a mock implementation of K8sClient for testing
type mockK8sClient struct {
	runFunc              func(ctx context.Context, args ...string) (string, error)
	runWithNamespaceFunc func(ctx context.Context, namespace string, args ...string) (string, error)
	runJSONFunc          func(ctx context.Context, args ...string) ([]byte, error)
}

func (m *mockK8sClient) Run(ctx context.Context, args ...string) (string, error) {
	if m.runFunc != nil {
		return m.runFunc(ctx, args...)
	}
	return "", nil
}

func (m *mockK8sClient) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	if m.runWithNamespaceFunc != nil {
		return m.runWithNamespaceFunc(ctx, namespace, args...)
	}
	return "", nil
}

func (m *mockK8sClient) RunJSON(ctx context.Context, args ...string) ([]byte, error) {
	if m.runJSONFunc != nil {
		return m.runJSONFunc(ctx, args...)
	}
	return nil, nil
}

func TestAnalyzeQuery(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		expectedScope  LogScope
		wantsAnalysis  bool
		wantsFix       bool
		expectedNs     string
		expectedDeploy string
	}{
		{
			name:          "cluster scope query",
			query:         "show me all errors in the cluster",
			expectedScope: ScopeCluster,
			wantsAnalysis: false,
			wantsFix:      false,
		},
		{
			name:          "deployment scope query",
			query:         "get logs from deployment nginx",
			expectedScope: ScopeDeployment,
			wantsAnalysis: false,
			wantsFix:      false,
		},
		{
			name:          "node scope query",
			query:         "show logs from node worker-1",
			expectedScope: ScopeNode,
			wantsAnalysis: false,
			wantsFix:      false,
		},
		{
			name:          "pod scope query",
			query:         "get logs from pod my-app-abc123",
			expectedScope: ScopePod,
			wantsAnalysis: false,
			wantsFix:      false,
		},
		{
			name:          "analysis query",
			query:         "what is happening with my pods",
			expectedScope: ScopePod,
			wantsAnalysis: true,
			wantsFix:      false,
		},
		{
			name:          "fix query",
			query:         "why is my pod not starting",
			expectedScope: ScopePod,
			wantsAnalysis: false,
			wantsFix:      true,
		},
		{
			name:          "namespace extraction",
			query:         "show logs from namespace production",
			expectedScope: ScopeNamespace,
			wantsAnalysis: false,
			wantsFix:      false,
			expectedNs:    "production",
		},
		{
			name:           "deployment with name",
			query:          "logs from deployment web-server",
			expectedScope:  ScopeDeployment,
			wantsAnalysis:  false,
			wantsFix:       false,
			expectedDeploy: "web-server",
		},
	}

	subAgent := NewSubAgent(&mockK8sClient{}, false)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis := subAgent.analyzeQuery(tt.query)

			if analysis.Scope != tt.expectedScope {
				t.Errorf("expected scope %s, got %s", tt.expectedScope, analysis.Scope)
			}

			if analysis.WantsAnalysis != tt.wantsAnalysis {
				t.Errorf("expected wantsAnalysis %v, got %v", tt.wantsAnalysis, analysis.WantsAnalysis)
			}

			if analysis.WantsFix != tt.wantsFix {
				t.Errorf("expected wantsFix %v, got %v", tt.wantsFix, analysis.WantsFix)
			}

			if tt.expectedNs != "" && analysis.Namespace != tt.expectedNs {
				t.Errorf("expected namespace %s, got %s", tt.expectedNs, analysis.Namespace)
			}

			if tt.expectedDeploy != "" && analysis.ResourceName != tt.expectedDeploy {
				t.Errorf("expected deployment %s, got %s", tt.expectedDeploy, analysis.ResourceName)
			}
		})
	}
}

func TestExtractNamespace(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{"logs from namespace kube-system", "kube-system"},
		{"show logs -n production", "production"},
		{"get errors in ns staging", "staging"},
		{"logs from monitoring namespace", "monitoring"},
		{"random query without namespace", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := extractNamespace(tt.query)
			if result != tt.expected {
				t.Errorf("extractNamespace(%q) = %q, want %q", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractTimeConstraint(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{"show logs from last hour", "1h"},
		{"get errors from past 30 minutes", "30m"},
		{"logs from last 15 minutes", "15m"},
		{"show recent logs", "30m"},
		{"logs from today", "24h"},
		{"show last 5 minutes of logs", "5m"},
		{"just show me the logs", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := extractTimeConstraint(tt.query)
			if result != tt.expected {
				t.Errorf("extractTimeConstraint(%q) = %q, want %q", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractErrorPatterns(t *testing.T) {
	tests := []struct {
		query    string
		expected []string
	}{
		{"show me 503 errors", []string{"503"}},
		{"find timeout issues", []string{"timeout"}},
		{"get connection refused errors", []string{"connection refused"}},
		{"show oom killed pods", []string{"oom", "killed"}},
		{"normal query", nil},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := extractErrorPatterns(tt.query)

			if len(tt.expected) == 0 && len(result) > 0 {
				t.Errorf("expected no patterns, got %v", result)
				return
			}

			for _, exp := range tt.expected {
				found := false
				for _, r := range result {
					if r == exp {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected pattern %q not found in result %v", exp, result)
				}
			}
		})
	}
}

func TestDetectLogLevel(t *testing.T) {
	tests := []struct {
		line     string
		expected LogLevel
	}{
		{"ERROR: something went wrong", LevelError},
		{"FATAL: application crashed", LevelError},
		{"panic: nil pointer dereference", LevelError},
		{"WARNING: deprecated function used", LevelWarn},
		{"WARN: low memory", LevelWarn},
		{"DEBUG: entering function", LevelDebug},
		{"INFO: server started", LevelInfo},
		{"regular log message", LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			result := detectLogLevel(tt.line)
			if result != tt.expected {
				t.Errorf("detectLogLevel(%q) = %v, want %v", tt.line, result, tt.expected)
			}
		})
	}
}

func TestFilterByPatterns(t *testing.T) {
	logs := &AggregatedLogs{
		Entries: []LogEntry{
			{Message: "ERROR: connection refused to database", IsError: true, Level: LevelError},
			{Message: "INFO: request processed successfully", Level: LevelInfo},
			{Message: "ERROR: timeout waiting for response", IsError: true, Level: LevelError},
			{Message: "DEBUG: entering main loop", Level: LevelDebug},
		},
		TotalLines: 4,
		ErrorCount: 2,
	}

	patterns := []string{"connection refused", "timeout"}
	filtered := filterByPatterns(logs, patterns)

	if filtered.TotalLines != 2 {
		t.Errorf("expected 2 filtered entries, got %d", filtered.TotalLines)
	}

	for _, entry := range filtered.Entries {
		if !strings.Contains(strings.ToLower(entry.Message), "connection refused") &&
			!strings.Contains(strings.ToLower(entry.Message), "timeout") {
			t.Errorf("entry %q does not match any pattern", entry.Message)
		}
	}
}

func TestFilterByLevel(t *testing.T) {
	logs := &AggregatedLogs{
		Entries: []LogEntry{
			{Message: "ERROR: something failed", IsError: true, Level: LevelError},
			{Message: "WARN: low memory", Level: LevelWarn},
			{Message: "INFO: started", Level: LevelInfo},
			{Message: "ERROR: another error", IsError: true, Level: LevelError},
		},
		TotalLines: 4,
		ErrorCount: 2,
		WarnCount:  1,
	}

	// Filter for errors only
	filtered := filterByLevel(logs, []LogLevel{LevelError})

	if filtered.TotalLines != 2 {
		t.Errorf("expected 2 error entries, got %d", filtered.TotalLines)
	}

	for _, entry := range filtered.Entries {
		if entry.Level != LevelError {
			t.Errorf("expected only error level, got %v", entry.Level)
		}
	}
}

func TestQuickAnalyze(t *testing.T) {
	analyzer := NewLogAnalyzer(false)

	t.Run("no errors", func(t *testing.T) {
		logs := &AggregatedLogs{
			Entries: []LogEntry{
				{Message: "INFO: all good", Level: LevelInfo},
			},
			TotalLines: 1,
			ErrorCount: 0,
			PodCount:   1,
		}

		analysis := analyzer.QuickAnalyze(logs)

		if !strings.Contains(analysis.Summary, "No errors") {
			t.Errorf("expected 'No errors' in summary, got %q", analysis.Summary)
		}
	})

	t.Run("with OOM errors", func(t *testing.T) {
		logs := &AggregatedLogs{
			Entries: []LogEntry{
				{Message: "ERROR: OOM killed", IsError: true, Level: LevelError},
				{Message: "ERROR: out of memory", IsError: true, Level: LevelError},
			},
			TotalLines: 2,
			ErrorCount: 2,
			PodCount:   1,
		}

		analysis := analyzer.QuickAnalyze(logs)

		if analysis.RootCause == "" {
			t.Error("expected root cause to be set for OOM errors")
		}

		if !strings.Contains(strings.ToLower(analysis.RootCause), "memory") {
			t.Errorf("expected memory-related root cause, got %q", analysis.RootCause)
		}
	})

	t.Run("with connection errors", func(t *testing.T) {
		logs := &AggregatedLogs{
			Entries: []LogEntry{
				{Message: "ERROR: connection refused", IsError: true, Level: LevelError},
			},
			TotalLines: 1,
			ErrorCount: 1,
			PodCount:   1,
		}

		analysis := analyzer.QuickAnalyze(logs)

		if !strings.Contains(strings.ToLower(analysis.RootCause), "network") &&
			!strings.Contains(strings.ToLower(analysis.RootCause), "connectivity") {
			t.Errorf("expected network-related root cause, got %q", analysis.RootCause)
		}
	})
}

func TestParseLogOutput(t *testing.T) {
	collector := NewLogCollector(&mockK8sClient{}, false)

	output := `2024-01-15T10:30:00Z INFO: Application started
2024-01-15T10:30:01Z ERROR: Failed to connect to database
2024-01-15T10:30:02Z WARN: Retrying connection`

	entries := collector.parseLogOutput(output, "test-pod", "default", "node-1")

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	// Check levels are detected correctly
	expectedLevels := []LogLevel{LevelInfo, LevelError, LevelWarn}
	for i, entry := range entries {
		if entry.Level != expectedLevels[i] {
			t.Errorf("entry %d: expected level %v, got %v", i, expectedLevels[i], entry.Level)
		}
		if entry.Pod != "test-pod" {
			t.Errorf("entry %d: expected pod test-pod, got %s", i, entry.Pod)
		}
		if entry.Namespace != "default" {
			t.Errorf("entry %d: expected namespace default, got %s", i, entry.Namespace)
		}
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple JSON",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON with surrounding text",
			input:    `Here is the response: {"summary": "test", "confidence": 0.9} end`,
			expected: `{"summary": "test", "confidence": 0.9}`,
		},
		{
			name:     "nested JSON",
			input:    `{"outer": {"inner": "value"}}`,
			expected: `{"outer": {"inner": "value"}}`,
		},
		{
			name:     "no JSON",
			input:    "just plain text",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractJSON(tt.input)
			if result != tt.expected {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestBuildLogSummary(t *testing.T) {
	logs := &AggregatedLogs{
		Entries: []LogEntry{
			{Message: "ERROR: connection refused", IsError: true, Level: LevelError},
			{Message: "ERROR: connection refused", IsError: true, Level: LevelError},
			{Message: "ERROR: timeout", IsError: true, Level: LevelError},
			{Message: "WARN: slow query", Level: LevelWarn},
		},
		TotalLines: 4,
		ErrorCount: 3,
		WarnCount:  1,
		PodCount:   2,
	}

	summary := buildLogSummary(logs)

	if summary.TotalLines != 4 {
		t.Errorf("expected TotalLines 4, got %d", summary.TotalLines)
	}

	if summary.ErrorCount != 3 {
		t.Errorf("expected ErrorCount 3, got %d", summary.ErrorCount)
	}

	if len(summary.TopErrors) == 0 {
		t.Error("expected TopErrors to be populated")
	}

	// Check that connection refused has higher count
	foundConnectionRefused := false
	for _, err := range summary.TopErrors {
		if err.Pattern == "connection refused" && err.Count == 2 {
			foundConnectionRefused = true
			break
		}
	}
	if !foundConnectionRefused {
		t.Error("expected to find 'connection refused' pattern with count 2")
	}
}

func TestContainsAny(t *testing.T) {
	tests := []struct {
		s        string
		substrs  []string
		expected bool
	}{
		{"hello world", []string{"world"}, true},
		{"hello world", []string{"foo", "world"}, true},
		{"hello world", []string{"foo", "bar"}, false},
		{"", []string{"foo"}, false},
		{"hello", []string{}, false},
	}

	for _, tt := range tests {
		result := containsAny(tt.s, tt.substrs)
		if result != tt.expected {
			t.Errorf("containsAny(%q, %v) = %v, want %v", tt.s, tt.substrs, result, tt.expected)
		}
	}
}

func TestIsCommonWord(t *testing.T) {
	tests := []struct {
		word     string
		expected bool
	}{
		{"the", true},
		{"cluster", true},
		{"my-app", false},
		{"nginx", false},
		{"show", true},
	}

	for _, tt := range tests {
		result := isCommonWord(tt.word)
		if result != tt.expected {
			t.Errorf("isCommonWord(%q) = %v, want %v", tt.word, result, tt.expected)
		}
	}
}
