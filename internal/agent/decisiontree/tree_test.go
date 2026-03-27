package decisiontree

import (
	"testing"
)

func TestNew_ReturnsValidTree(t *testing.T) {
	tree := New()
	if tree == nil {
		t.Fatal("expected non-nil tree")
	}
	if tree.Root == nil {
		t.Fatal("expected non-nil root node")
	}
	if tree.Root.Condition != "always" {
		t.Errorf("expected root condition 'always', got %q", tree.Root.Condition)
	}
	if len(tree.Root.Children) == 0 {
		t.Error("expected root to have children")
	}
}

func TestTraverse_AlwaysMatchesRoot(t *testing.T) {
	tree := New()
	nodes := tree.Traverse("hello", nil)

	// Root always matches, so we should get at least the root node.
	if len(nodes) == 0 {
		t.Error("expected at least one node from traversal")
	}
	found := false
	for _, n := range nodes {
		if n.ID == "root" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected root node in traversal results")
	}
}

func TestTraverse_LogKeywordMatch(t *testing.T) {
	tree := New()
	nodes := tree.Traverse("show me recent logs for the api", nil)

	foundLog := false
	for _, n := range nodes {
		if n.ID == "logs_priority" {
			foundLog = true
			break
		}
	}
	if !foundLog {
		t.Error("expected 'logs_priority' node to match for query containing 'logs'")
	}
}

func TestTraverse_K8sKeywordMatch(t *testing.T) {
	tree := New()
	nodes := tree.Traverse("check kubernetes pod status", nil)

	foundK8s := false
	for _, n := range nodes {
		if n.ID == "k8s_context" {
			foundK8s = true
			break
		}
	}
	if !foundK8s {
		t.Error("expected 'k8s_context' node to match for query containing 'kubernetes'")
	}
}

func TestTraverse_SecurityKeywordMatch(t *testing.T) {
	tree := New()
	nodes := tree.Traverse("investigate unauthorized access attempt", nil)

	foundSecurity := false
	for _, n := range nodes {
		if n.ID == "security_alerts" {
			foundSecurity = true
			break
		}
	}
	if !foundSecurity {
		t.Error("expected 'security_alerts' node to match for query containing 'unauthorized'")
	}
}

func TestTraverse_CostKeywordMatch(t *testing.T) {
	tree := New()
	nodes := tree.Traverse("why did our billing spike", nil)

	foundCost := false
	for _, n := range nodes {
		if n.ID == "cost_anomaly" {
			foundCost = true
			break
		}
	}
	if !foundCost {
		t.Error("expected 'cost_anomaly' node to match for query containing 'billing'")
	}
}

func TestTraverse_NoExtraMatchForUnrelatedQuery(t *testing.T) {
	tree := New()
	// This query should NOT match k8s, security, cost, etc.
	nodes := tree.Traverse("hello world", nil)

	for _, n := range nodes {
		if n.ID != "root" {
			t.Errorf("unexpected node matched for generic query: %s (%s)", n.ID, n.Name)
		}
	}
}

func TestEvaluateCondition_Always(t *testing.T) {
	tree := New()
	if !tree.evaluateCondition("always", "anything") {
		t.Error("expected 'always' condition to return true")
	}
}

func TestEvaluateCondition_ContainsKeywords(t *testing.T) {
	tree := New()

	if !tree.evaluateCondition("contains_keywords(['error', 'failed'])", "there was an error") {
		t.Error("expected keyword 'error' to match")
	}
	if tree.evaluateCondition("contains_keywords(['error', 'failed'])", "everything is fine") {
		t.Error("expected no match when keywords are absent")
	}
}

func TestEvaluateCondition_MalformedKeywords(t *testing.T) {
	tree := New()

	// Malformed condition should return false, not panic.
	if tree.evaluateCondition("contains_keywords(broken)", "error") {
		t.Error("expected malformed condition to return false")
	}
}

func TestEvaluateCondition_UnknownCondition(t *testing.T) {
	tree := New()
	if tree.evaluateCondition("unknown_type", "any query") {
		t.Error("expected unknown condition type to return false")
	}
}

func TestTraverse_RecordsPath(t *testing.T) {
	tree := New()
	_ = tree.Traverse("check logs", nil)

	if len(tree.CurrentPath) == 0 {
		t.Error("expected CurrentPath to be populated after traversal")
	}
	if tree.CurrentPath[0] != "root" {
		t.Errorf("expected first path element to be 'root', got %q", tree.CurrentPath[0])
	}

	if len(tree.Decisions) == 0 {
		t.Error("expected Decisions to be populated after traversal")
	}
}

func TestTraverse_ChildNodesMatch(t *testing.T) {
	tree := New()
	// "service" + "logs" should trigger the service_logs child node
	nodes := tree.Traverse("investigate service logs", nil)

	foundChild := false
	for _, n := range nodes {
		if n.ID == "service_logs" {
			foundChild = true
			break
		}
	}
	if !foundChild {
		t.Error("expected child node 'service_logs' to match")
	}
}
