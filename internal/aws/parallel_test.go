package aws

import (
	"context"
	"testing"
)

func TestExecuteOperationsConcurrently_EmptyOperations(t *testing.T) {
	c := &Client{}
	result, err := c.ExecuteOperationsConcurrently(context.Background(), nil, "")
	if err != nil {
		t.Errorf("expected nil error for empty operations, got %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for empty operations, got %q", result)
	}
}

func TestExecuteOperationsWithAWSProfile_EmptyOperations(t *testing.T) {
	c := &Client{}
	result, err := c.ExecuteOperationsWithAWSProfile(context.Background(), nil, "test", "us-east-1")
	if err != nil {
		t.Errorf("expected nil error for empty operations, got %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for empty operations, got %q", result)
	}
}

func TestExecuteOperationsWithProfile_CancelledContext(t *testing.T) {
	c := &Client{}

	// Create an already-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	profile := &AIProfile{
		Provider:   "openai",
		AWSProfile: "test",
		Region:     "us-east-1",
	}

	ops := []LLMOperation{
		{Operation: "list_lambda_functions", Parameters: map[string]interface{}{}},
	}

	result, err := c.executeOperationsWithProfile(ctx, ops, profile)
	if err != nil {
		// The function itself returns nil error (errors are per-operation).
		t.Errorf("expected nil top-level error, got %v", err)
	}

	// The result should contain the cancellation error for the operation.
	if result == "" {
		t.Error("expected non-empty result string with error info")
	}
}
