package coordinator

import (
	"testing"

	"github.com/bgdnvk/clanker/internal/agent/model"
)

func TestNew_NilContext(t *testing.T) {
	_, err := New(nil, nil)
	if err == nil {
		t.Fatal("expected error when mainContext is nil")
	}
	if err.Error() != "coordinator: mainContext must not be nil" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNew_NilClient(t *testing.T) {
	ctx := &model.AgentContext{
		GatheredData:  make(model.AWSData),
		ServiceData:   make(model.ServiceData),
		Metrics:       make(model.MetricsData),
		ServiceStatus: make(map[string]string),
	}
	_, err := New(ctx, nil)
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
	if err.Error() != "coordinator: AWS client must not be nil" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCopyContextForAgent_NilInput(t *testing.T) {
	result := CopyContextForAgent(nil)
	if result == nil {
		t.Fatal("expected non-nil context from nil input")
	}
	if result.GatheredData == nil {
		t.Error("expected GatheredData to be initialized")
	}
	if result.ServiceData == nil {
		t.Error("expected ServiceData to be initialized")
	}
	if result.MaxSteps != 3 {
		t.Errorf("expected MaxSteps=3, got %d", result.MaxSteps)
	}
}

func TestCopyContextForAgent_CopiesFields(t *testing.T) {
	main := &model.AgentContext{
		OriginalQuery: "test query",
		CurrentStep:   5,
		MaxSteps:      10,
		GatheredData:  make(model.AWSData),
		Decisions: []model.AgentDecision{
			{Action: "test"},
		},
		ChainOfThought: []model.ChainOfThought{},
		ServiceData:    make(model.ServiceData),
		Metrics:        make(model.MetricsData),
		ServiceStatus:  make(map[string]string),
	}

	result := CopyContextForAgent(main)
	if result.OriginalQuery != "test query" {
		t.Errorf("expected OriginalQuery to be copied, got %q", result.OriginalQuery)
	}
	if result.CurrentStep != 0 {
		t.Errorf("expected CurrentStep to be reset to 0, got %d", result.CurrentStep)
	}
	if len(result.Decisions) != 1 {
		t.Errorf("expected 1 decision to be copied, got %d", len(result.Decisions))
	}
	// Verify it is a copy, not a shared reference.
	result.Decisions = append(result.Decisions, model.AgentDecision{Action: "extra"})
	if len(main.Decisions) != 1 {
		t.Error("modifying copied decisions should not affect original")
	}
}
