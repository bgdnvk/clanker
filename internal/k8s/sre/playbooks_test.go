package sre

import (
	"context"
	"strings"
	"testing"
)

// stubK8sClient satisfies sre.K8sClient for unit tests. None of the
// built-in playbooks actually CALL the client in their Plan(...) yet
// — they're pure functions of PlaybookInput today, with cluster-state
// inspection deferred to future playbooks (stuck-rollout, pending-
// pod-scheduling). Keeping the stub here so adding those is a no-op.
type stubK8sClient struct {
	runOutput     string
	runErr        error
	runJSONOutput []byte
	runJSONErr    error
}

func (s *stubK8sClient) Run(ctx context.Context, args ...string) (string, error) {
	return s.runOutput, s.runErr
}
func (s *stubK8sClient) RunWithNamespace(ctx context.Context, ns string, args ...string) (string, error) {
	return s.runOutput, s.runErr
}
func (s *stubK8sClient) RunJSON(ctx context.Context, args ...string) ([]byte, error) {
	return s.runJSONOutput, s.runJSONErr
}

// ============================================================
// Registry
// ============================================================

func TestNewPlaybookRegistry_ContainsCrashLoopRecovery(t *testing.T) {
	r := NewPlaybookRegistry()
	if _, err := r.Get("crashloop-recovery"); err != nil {
		t.Fatalf("expected crashloop-recovery to be registered: %v", err)
	}
}

func TestRegistry_GetUnknownIDReturnsHelpfulError(t *testing.T) {
	r := NewPlaybookRegistry()
	_, err := r.Get("not-a-playbook")
	if err == nil {
		t.Fatal("expected error for unknown id")
	}
	if !strings.Contains(err.Error(), "unknown playbook") {
		t.Errorf("error should mention 'unknown playbook', got %q", err.Error())
	}
	// Error should list the known IDs so the CLI surface gets a hint.
	if !strings.Contains(err.Error(), "crashloop-recovery") {
		t.Errorf("error should list known IDs, got %q", err.Error())
	}
}

func TestRegistry_IDsAreSorted(t *testing.T) {
	r := NewPlaybookRegistry()
	// Register a second mock so we have 2 items to verify ordering.
	r.Register(&mockPlaybook{id: "aaa-first"})
	r.Register(&mockPlaybook{id: "zzz-last"})
	ids := r.IDs()
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Errorf("IDs not sorted: %v", ids)
		}
	}
}

func TestRegistry_RegisterOverwritesExisting(t *testing.T) {
	r := NewPlaybookRegistry()
	r.Register(&mockPlaybook{id: "crashloop-recovery", title: "MOCKED"})
	p, err := r.Get("crashloop-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title() != "MOCKED" {
		t.Errorf("Register did not overwrite; got title %q", p.Title())
	}
}

// ============================================================
// crashLoopRecoveryPlaybook
// ============================================================

func TestCrashLoopRecovery_RequiresPodName(t *testing.T) {
	p := &crashLoopRecoveryPlaybook{}
	_, err := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{Namespace: "prod"})
	if err == nil {
		t.Fatal("expected error when name is empty")
	}
	if !strings.Contains(err.Error(), "pod name is required") {
		t.Errorf("error should mention pod name, got %q", err.Error())
	}
}

func TestCrashLoopRecovery_EmitsThreeStepPlan(t *testing.T) {
	p := &crashLoopRecoveryPlaybook{}
	plan, err := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name:      "my-pod",
		Namespace: "prod",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(plan.Steps))
	}
	wantIDs := []string{"logs-previous", "describe", "delete-pod"}
	for i, want := range wantIDs {
		if plan.Steps[i].ID != want {
			t.Errorf("step %d id = %q, want %q", i, plan.Steps[i].ID, want)
		}
	}
}

func TestCrashLoopRecovery_DiagnosticsStepsAreReadOnly(t *testing.T) {
	p := &crashLoopRecoveryPlaybook{}
	plan, _ := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name: "my-pod", Namespace: "prod",
	})
	// Steps 0+1 are logs/describe — must be non-mutating.
	if plan.Steps[0].Mutating || plan.Steps[1].Mutating {
		t.Errorf("logs/describe steps should be non-mutating: %v", plan.Steps[:2])
	}
	// Step 2 is delete-pod — mutating + requires approval.
	if !plan.Steps[2].Mutating {
		t.Error("delete-pod step should be mutating")
	}
	if !plan.Steps[2].RequiresApproval {
		t.Error("delete-pod step should require explicit approval even in auto mode")
	}
}

func TestCrashLoopRecovery_AppliesNamespaceAndContextToEveryStep(t *testing.T) {
	p := &crashLoopRecoveryPlaybook{}
	plan, _ := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name:      "my-pod",
		Namespace: "prod",
		Context:   "my-eks",
	})
	for i, step := range plan.Steps {
		if !contains(step.Args, "-n") || !contains(step.Args, "prod") {
			t.Errorf("step %d missing -n prod: %v", i, step.Args)
		}
		if !contains(step.Args, "--context") || !contains(step.Args, "my-eks") {
			t.Errorf("step %d missing --context my-eks: %v", i, step.Args)
		}
	}
}

func TestCrashLoopRecovery_DefaultsNamespaceToDefault(t *testing.T) {
	p := &crashLoopRecoveryPlaybook{}
	plan, _ := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name: "my-pod",
	})
	// Target string should use 'default' as the ns.
	if !strings.Contains(plan.Target, "@default") {
		t.Errorf("target should default to @default ns, got %q", plan.Target)
	}
}

func TestCrashLoopRecovery_TargetEncodesPodAndNS(t *testing.T) {
	p := &crashLoopRecoveryPlaybook{}
	plan, _ := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name: "my-pod", Namespace: "prod",
	})
	if plan.Target != "pod/my-pod@prod" {
		t.Errorf("target = %q, want pod/my-pod@prod", plan.Target)
	}
}

func TestKubectlArgs_OmitsFlagsWhenEmpty(t *testing.T) {
	got := kubectlArgs(PlaybookInput{}, "get", "pods")
	if contains(got, "-n") {
		t.Errorf("empty namespace should not produce -n: %v", got)
	}
	if contains(got, "--context") {
		t.Errorf("empty context should not produce --context: %v", got)
	}
	if !contains(got, "get") || !contains(got, "pods") {
		t.Errorf("caller args missing from output: %v", got)
	}
}

func TestKubectlArgsClusterScoped_NeverEmitsNamespace(t *testing.T) {
	got := kubectlArgsClusterScoped(PlaybookInput{Namespace: "prod", Context: "my-eks"}, "get", "nodes")
	if contains(got, "-n") || contains(got, "prod") {
		t.Errorf("cluster-scoped helper must not emit namespace: %v", got)
	}
	if !contains(got, "--context") || !contains(got, "my-eks") {
		t.Errorf("cluster-scoped helper should still pass --context: %v", got)
	}
}

// ============================================================
// stuckRolloutPlaybook
// ============================================================

func TestStuckRollout_RequiresDeploymentName(t *testing.T) {
	p := &stuckRolloutPlaybook{}
	_, err := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{Namespace: "prod"})
	if err == nil {
		t.Fatal("expected error when name is empty")
	}
	if !strings.Contains(err.Error(), "deployment name is required") {
		t.Errorf("error should mention deployment name, got %q", err.Error())
	}
}

func TestStuckRollout_EmitsCanonicalPlan(t *testing.T) {
	p := &stuckRolloutPlaybook{}
	plan, err := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name: "my-app", Namespace: "prod",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantIDs := []string{"rollout-status", "rollout-history", "describe", "rollout-undo"}
	if len(plan.Steps) != len(wantIDs) {
		t.Fatalf("expected %d steps, got %d", len(wantIDs), len(plan.Steps))
	}
	for i, want := range wantIDs {
		if plan.Steps[i].ID != want {
			t.Errorf("step %d id = %q, want %q", i, plan.Steps[i].ID, want)
		}
	}
}

func TestStuckRollout_OnlyUndoIsMutating(t *testing.T) {
	p := &stuckRolloutPlaybook{}
	plan, _ := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name: "my-app", Namespace: "prod",
	})
	for i, step := range plan.Steps {
		isUndo := step.ID == "rollout-undo"
		if step.Mutating != isUndo {
			t.Errorf("step %d (%s) mutating=%v; want %v", i, step.ID, step.Mutating, isUndo)
		}
		if isUndo && !step.RequiresApproval {
			t.Errorf("rollout-undo must require explicit approval")
		}
	}
}

func TestStuckRollout_TargetEncodesDeploymentAndNS(t *testing.T) {
	p := &stuckRolloutPlaybook{}
	plan, _ := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name: "my-app", Namespace: "prod",
	})
	if plan.Target != "deployment/my-app@prod" {
		t.Errorf("target = %q, want deployment/my-app@prod", plan.Target)
	}
}

// ============================================================
// pendingPodSchedulingPlaybook
// ============================================================

func TestPendingPodScheduling_RequiresPodName(t *testing.T) {
	p := &pendingPodSchedulingPlaybook{}
	_, err := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{Namespace: "prod"})
	if err == nil {
		t.Fatal("expected error when name is empty")
	}
}

func TestPendingPodScheduling_AllStepsAreReadOnly(t *testing.T) {
	p := &pendingPodSchedulingPlaybook{}
	plan, err := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name: "my-pod", Namespace: "prod",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, step := range plan.Steps {
		if step.Mutating {
			t.Errorf("step %d (%s) is marked mutating; pending-pod-scheduling is diagnostic-only", i, step.ID)
		}
		if step.RequiresApproval {
			t.Errorf("step %d (%s) requires approval; diagnostic-only playbook shouldn't", i, step.ID)
		}
	}
}

func TestPendingPodScheduling_GetNodesIsClusterScoped(t *testing.T) {
	p := &pendingPodSchedulingPlaybook{}
	plan, _ := p.Plan(context.Background(), &stubK8sClient{}, PlaybookInput{
		Name: "my-pod", Namespace: "prod", Context: "my-eks",
	})
	var nodeStep *PlaybookStep
	for i := range plan.Steps {
		if plan.Steps[i].ID == "get-nodes" {
			nodeStep = &plan.Steps[i]
			break
		}
	}
	if nodeStep == nil {
		t.Fatal("get-nodes step missing from plan")
	}
	// Nodes are cluster-scoped → no -n flag.
	if contains(nodeStep.Args, "-n") || contains(nodeStep.Args, "prod") {
		t.Errorf("get-nodes must not emit -n: %v", nodeStep.Args)
	}
	// Context still propagates.
	if !contains(nodeStep.Args, "--context") || !contains(nodeStep.Args, "my-eks") {
		t.Errorf("get-nodes should pass --context: %v", nodeStep.Args)
	}
}

func TestRegistry_ContainsAllThreePlaybooks(t *testing.T) {
	r := NewPlaybookRegistry()
	for _, id := range []string{"crashloop-recovery", "stuck-rollout", "pending-pod-scheduling"} {
		if _, err := r.Get(id); err != nil {
			t.Errorf("playbook %q not registered: %v", id, err)
		}
	}
}

// ============================================================
// helpers
// ============================================================

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

type mockPlaybook struct {
	id, title string
}

func (m *mockPlaybook) ID() string          { return m.id }
func (m *mockPlaybook) Title() string       { return m.title }
func (m *mockPlaybook) Description() string { return "mock" }
func (m *mockPlaybook) Plan(_ context.Context, _ K8sClient, _ PlaybookInput) (*PlaybookPlan, error) {
	return &PlaybookPlan{PlaybookID: m.id, Steps: []PlaybookStep{}}, nil
}
