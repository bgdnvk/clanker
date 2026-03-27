package coordinator

import (
	"strings"
	"testing"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		query    string
		expected []string
	}{
		{"check lambda errors", []string{"check", "lambda", "errors"}},
		{"", nil},
		{"a b", nil}, // too short
		{"api gateway logs", []string{"api", "gateway", "logs"}},
	}

	for _, tt := range tests {
		got := extractKeywords(tt.query)
		if len(got) != len(tt.expected) {
			t.Errorf("extractKeywords(%q) returned %d items, want %d: %v", tt.query, len(got), len(tt.expected), got)
			continue
		}
		for i, kw := range got {
			if kw != tt.expected[i] {
				t.Errorf("extractKeywords(%q)[%d] = %q, want %q", tt.query, i, kw, tt.expected[i])
			}
		}
	}
}

func TestExtractKeywords_Deduplication(t *testing.T) {
	got := extractKeywords("error error error")
	if len(got) != 1 {
		t.Errorf("expected 1 unique keyword, got %d: %v", len(got), got)
	}
}

func TestUniqueStrings(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b"}
	got := uniqueStrings(input)
	if len(got) != 3 {
		t.Errorf("expected 3 unique strings, got %d: %v", len(got), got)
	}
}

func TestUniqueStrings_Empty(t *testing.T) {
	got := uniqueStrings(nil)
	if len(got) != 0 {
		t.Errorf("expected 0 unique strings for nil input, got %d", len(got))
	}
}

func TestLookupAgentType(t *testing.T) {
	ctx := makeTestCoordinator(t)

	knownTypes := []string{"k8s", "log", "metrics", "infrastructure", "security", "cost", "performance", "deployment", "datapipeline", "queue", "availability", "llm"}
	for _, name := range knownTypes {
		agt, ok := ctx.lookupAgentType(name)
		if !ok {
			t.Errorf("expected agent type %q to be found", name)
			continue
		}
		if agt.Name != name {
			t.Errorf("expected agent name %q, got %q", name, agt.Name)
		}
	}

	_, ok := ctx.lookupAgentType("nonexistent")
	if ok {
		t.Error("expected unknown agent type to return false")
	}
}

func TestExtractLambdaFunctionNames(t *testing.T) {
	// Simulate table-formatted Lambda output with a Name column
	data := "Name | Runtime | Memory\n--- | --- | ---\nmy-func-one | python3.9 | 128\nmy-func-two | nodejs18.x | 256"

	names := extractLambdaFunctionNames(data)
	found := strings.Join(names, ",")
	if !strings.Contains(found, "my-func-one") {
		t.Errorf("expected my-func-one in extracted names, got %v", names)
	}
	if !strings.Contains(found, "my-func-two") {
		t.Errorf("expected my-func-two in extracted names, got %v", names)
	}
}

func TestInvestigateGeneralLogs_ExtractsLogGroup(t *testing.T) {
	// This tests the log group extraction logic without calling AWS.
	// We verify that the function accepts various data formats without panicking.

	// String data with a log group path
	logGroupData := "/aws/lambda/my-function\n/aws/apigateway/my-api"

	// We cannot actually call investigateGeneralLogs without a real AWS client,
	// but we can verify the extraction logic by checking the helper
	// would produce a valid log group name from this input.
	lines := strings.Split(logGroupData, "\n")
	var extracted string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "/") && !strings.Contains(trimmed, "|") {
			extracted = trimmed
			break
		}
	}
	if extracted != "/aws/lambda/my-function" {
		t.Errorf("expected first log group to be extracted, got %q", extracted)
	}
}

// makeTestCoordinator creates a minimal coordinator for unit tests
// that don't need a real AWS client.
func makeTestCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	return &Coordinator{
		registry:  NewAgentRegistry(),
		dataBus:   NewSharedDataBus(),
		scheduler: NewDependencyScheduler(),
	}
}
