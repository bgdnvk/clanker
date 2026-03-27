package agent

import (
	"strings"
	"testing"
)

func TestBuildFinalContext_NilContext(t *testing.T) {
	a := &Agent{}
	result := a.BuildFinalContext(nil)
	if result != "" {
		t.Errorf("expected empty string for nil context, got %q", result)
	}
}

func TestBuildFinalContext_NoDebugOutput(t *testing.T) {
	a := &Agent{}
	ctx := &AgentContext{
		OriginalQuery: "test query",
		GatheredData: AWSData{
			"some_key": "some_value",
		},
		ServiceData:   make(ServiceData),
		Metrics:       make(MetricsData),
		ServiceStatus: make(map[string]string),
	}

	result := a.BuildFinalContext(ctx)

	if strings.Contains(result, "DEBUG") {
		t.Error("BuildFinalContext output should not contain DEBUG text")
	}
}

func TestBuildFinalContext_NoDuplicateKeys(t *testing.T) {
	a := &Agent{}
	ctx := &AgentContext{
		OriginalQuery: "check errors",
		GatheredData: AWSData{
			"ec2_data":    "instance info here",
			"lambda_data": "function info here",
		},
		ServiceData:   make(ServiceData),
		Metrics:       make(MetricsData),
		ServiceStatus: make(map[string]string),
	}

	result := a.BuildFinalContext(ctx)

	// Each key should appear exactly once in the output (as header).
	ec2Count := strings.Count(result, "EC2_DATA:")
	if ec2Count != 1 {
		t.Errorf("expected EC2_DATA to appear once, appeared %d times", ec2Count)
	}

	lambdaCount := strings.Count(result, "LAMBDA_DATA:")
	if lambdaCount != 1 {
		t.Errorf("expected LAMBDA_DATA to appear once, appeared %d times", lambdaCount)
	}
}

func TestBuildFinalContext_LambdaErrorsHighlighted(t *testing.T) {
	a := &Agent{}
	ctx := &AgentContext{
		OriginalQuery: "check lambda errors",
		GatheredData: AWSData{
			"analyze_lambda_errors": "timeout in handler",
		},
		ServiceData:   make(ServiceData),
		Metrics:       make(MetricsData),
		ServiceStatus: make(map[string]string),
	}

	result := a.BuildFinalContext(ctx)

	if !strings.Contains(result, "CRITICAL LAMBDA ERROR ANALYSIS") {
		t.Error("expected lambda errors to be highlighted with critical header")
	}
	if !strings.Contains(result, "timeout in handler") {
		t.Error("expected lambda error content to be included")
	}
}

func TestBuildFinalContext_NoCorruptedUnicode(t *testing.T) {
	a := &Agent{}
	ctx := &AgentContext{
		OriginalQuery: "test",
		GatheredData: AWSData{
			"test_key": "test_value",
		},
		ServiceData:   make(ServiceData),
		Metrics:       make(MetricsData),
		ServiceStatus: make(map[string]string),
	}

	result := a.BuildFinalContext(ctx)

	// The replacement character (U+FFFD) should never appear in output.
	if strings.Contains(result, "\ufffd") {
		t.Error("output contains corrupted unicode replacement character")
	}
}

func TestBuildFinalContext_ServiceDataIncluded(t *testing.T) {
	a := &Agent{}
	ctx := &AgentContext{
		OriginalQuery: "check services",
		GatheredData:  make(AWSData),
		ServiceData: ServiceData{
			"my-service": "running",
		},
		Metrics:       make(MetricsData),
		ServiceStatus: make(map[string]string),
	}

	result := a.BuildFinalContext(ctx)

	if !strings.Contains(result, "SERVICE DATA") {
		t.Error("expected service data section in output")
	}
	if !strings.Contains(result, "my-service") {
		t.Error("expected service name in output")
	}
}

func TestBuildFinalContext_ChainOfThought(t *testing.T) {
	a := &Agent{}
	ctx := &AgentContext{
		OriginalQuery: "debug issue",
		GatheredData:  make(AWSData),
		ServiceData:   make(ServiceData),
		Metrics:       make(MetricsData),
		ServiceStatus: make(map[string]string),
		ChainOfThought: []ChainOfThought{
			{Step: 1, Thought: "analyzing query", Action: "analyze", Outcome: "identified target"},
		},
	}

	result := a.BuildFinalContext(ctx)

	if !strings.Contains(result, "AGENT REASONING CHAIN") {
		t.Error("expected reasoning chain section")
	}
	if !strings.Contains(result, "analyzing query") {
		t.Error("expected thought content in output")
	}
}
