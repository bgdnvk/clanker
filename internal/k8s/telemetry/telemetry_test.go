package telemetry

import (
	"context"
	"testing"
)

// mockK8sClient implements K8sClient interface for testing
type mockK8sClient struct {
	runOutput           string
	runErr              error
	runWithNamespace    string
	runWithNamespaceErr error
	runJSONOutput       []byte
	runJSONErr          error
	getJSONOutput       []byte
	getJSONErr          error
}

func (m *mockK8sClient) Run(ctx context.Context, args ...string) (string, error) {
	return m.runOutput, m.runErr
}

func (m *mockK8sClient) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return m.runWithNamespace, m.runWithNamespaceErr
}

func (m *mockK8sClient) RunJSON(ctx context.Context, args ...string) ([]byte, error) {
	return m.runJSONOutput, m.runJSONErr
}

func (m *mockK8sClient) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return m.getJSONOutput, m.getJSONErr
}

func TestParseTopNodesOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected int
	}{
		{
			name:     "single node no header",
			output:   `node-1                  250m         6%     2147Mi          27%`,
			expected: 1,
		},
		{
			name: "multiple nodes no header",
			output: `node-1                  250m         6%     2147Mi          27%
node-2                  180m         4%     1800Mi          23%`,
			expected: 2,
		},
		{
			name:     "empty output",
			output:   "",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseTopNodesOutput(tt.output)
			if len(result) != tt.expected {
				t.Errorf("parseTopNodesOutput() returned %d nodes, expected %d", len(result), tt.expected)
			}
		})
	}
}

func TestParseTopNodesOutputValues(t *testing.T) {
	output := `node-1                  250m         6%     2147Mi          27%`
	result := parseTopNodesOutput(output)

	if len(result) != 1 {
		t.Fatalf("expected 1 node, got %d", len(result))
	}

	node := result[0]
	if node.Name != "node-1" {
		t.Errorf("expected name 'node-1', got '%s'", node.Name)
	}
	if node.CPUUsage != "250m" {
		t.Errorf("expected CPU '250m', got '%s'", node.CPUUsage)
	}
	if node.CPUPercent != 6 {
		t.Errorf("expected CPUPercent 6, got %f", node.CPUPercent)
	}
	if node.MemUsage != "2147Mi" {
		t.Errorf("expected Memory '2147Mi', got '%s'", node.MemUsage)
	}
	if node.MemPercent != 27 {
		t.Errorf("expected MemPercent 27, got %f", node.MemPercent)
	}
}

func TestParseTopPodsOutput(t *testing.T) {
	tests := []struct {
		name          string
		output        string
		namespace     string
		expectedCount int
	}{
		{
			name:          "single pod no header",
			output:        `my-pod                  100m         256Mi`,
			namespace:     "default",
			expectedCount: 1,
		},
		{
			name: "multiple pods no header",
			output: `pod-1                   100m         256Mi
pod-2                   200m         512Mi`,
			namespace:     "default",
			expectedCount: 2,
		},
		{
			name:          "empty output",
			output:        "",
			namespace:     "default",
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseTopPodsOutput(tt.output, tt.namespace)
			if len(result) != tt.expectedCount {
				t.Errorf("parseTopPodsOutput() returned %d pods, expected %d", len(result), tt.expectedCount)
			}
		})
	}
}

func TestParseTopContainersOutput(t *testing.T) {
	output := `my-pod                  container-1       50m          128Mi
my-pod                  container-2       75m          256Mi`

	result := parseTopContainersOutput(output)

	if len(result) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(result))
	}

	if result[0].Name != "container-1" {
		t.Errorf("expected name 'container-1', got '%s'", result[0].Name)
	}
	if result[0].CPUUsage != "50m" {
		t.Errorf("expected CPU '50m', got '%s'", result[0].CPUUsage)
	}
}

func TestParseCPUToMillicores(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"250m", 250},
		{"1", 1000},
		{"1.5", 1500},
		{"0.5", 500},
		{"", 0},
		{"<unknown>", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseCPUToMillicores(tt.input)
			if result != tt.expected {
				t.Errorf("parseCPUToMillicores(%q) = %d, expected %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseMemoryToBytes(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"1024Ki", 1024 * 1024},
		{"100Mi", 100 * 1024 * 1024},
		{"1Gi", 1024 * 1024 * 1024},
		{"1000M", 1000 * 1000 * 1000},
		{"1000K", 1000 * 1000},
		{"", 0},
		{"<unknown>", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseMemoryToBytes(tt.input)
			if result != tt.expected {
				t.Errorf("parseMemoryToBytes(%q) = %d, expected %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFormatMillicores(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{250, "250m"},
		{1000, "1.0"},
		{1500, "1.5"},
		{0, "0m"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatMillicores(tt.input)
			if result != tt.expected {
				t.Errorf("formatMillicores(%d) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1024, "1.0Ki"},
		{1024 * 1024, "1.0Mi"},
		{1024 * 1024 * 1024, "1.0Gi"},
		{100, "100"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatBytes(tt.input)
			if result != tt.expected {
				t.Errorf("formatBytes(%d) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSubAgentAnalyzeQuery(t *testing.T) {
	agent := NewSubAgent(&mockK8sClient{}, false)

	tests := []struct {
		query         string
		expectedScope MetricsScope
	}{
		{"show node metrics", ScopeNode},
		{"get nodes cpu usage", ScopeNode},
		{"pod metrics", ScopePod},
		{"show pods memory usage", ScopePod},
		{"container metrics", ScopeContainer},
		{"namespace metrics", ScopeNamespace},
		{"cluster metrics", ScopeCluster},
		{"show metrics", ScopeCluster}, // default
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.Scope != tt.expectedScope {
				t.Errorf("analyzeQuery(%q).Scope = %v, expected %v", tt.query, analysis.Scope, tt.expectedScope)
			}
		})
	}
}

func TestContainsAny(t *testing.T) {
	tests := []struct {
		s        string
		substrs  []string
		expected bool
	}{
		{"hello world", []string{"hello", "test"}, true},
		{"hello world", []string{"test", "world"}, true},
		{"hello world", []string{"foo", "bar"}, false},
		{"", []string{"foo"}, false},
		{"hello", []string{}, false},
	}

	for _, tt := range tests {
		result := containsAny(tt.s, tt.substrs)
		if result != tt.expected {
			t.Errorf("containsAny(%q, %v) = %v, expected %v", tt.s, tt.substrs, result, tt.expected)
		}
	}
}

func TestExtractNamespace(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{"show pods in namespace kube-system", "kube-system"},
		{"get pods -n default", "default"},
		{"namespace production metrics", "production"},
		{"show all pods", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := extractNamespace(tt.query)
			if result != tt.expected {
				t.Errorf("extractNamespace(%q) = %q, expected %q", tt.query, result, tt.expected)
			}
		})
	}
}

func TestExtractResourceName(t *testing.T) {
	tests := []struct {
		query    string
		scope    MetricsScope
		expected string
	}{
		{"show metrics for node worker-1", ScopeNode, "worker-1"},
		{"get pod my-pod metrics", ScopePod, "my-pod"},
		{"container my-container metrics", ScopeContainer, "my-container"},
		{"node worker-2 metrics", ScopeNode, "worker-2"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := extractResourceName(tt.query, tt.scope)
			if result != tt.expected {
				t.Errorf("extractResourceName(%q, %v) = %q, expected %q", tt.query, tt.scope, result, tt.expected)
			}
		})
	}
}
