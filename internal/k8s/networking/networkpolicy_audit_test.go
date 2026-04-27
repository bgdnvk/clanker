package networking

import (
	"context"
	"testing"
)

const allNamespacesPolicies = `{
  "items": [
    {
      "metadata": {"name": "default-deny", "namespace": "prod"},
      "spec": {
        "podSelector": {},
        "policyTypes": ["Ingress", "Egress"]
      }
    },
    {
      "metadata": {"name": "allow-app", "namespace": "prod"},
      "spec": {
        "podSelector": {"matchLabels": {"app": "web"}},
        "policyTypes": ["Ingress"],
        "ingress": [{"from": [{"podSelector": {"matchLabels": {"role": "fe"}}}]}]
      }
    },
    {
      "metadata": {"name": "ingress-only", "namespace": "staging"},
      "spec": {
        "podSelector": {},
        "policyTypes": ["Ingress"]
      }
    },
    {
      "metadata": {"name": "scoped", "namespace": "dev"},
      "spec": {
        "podSelector": {"matchLabels": {"app": "x"}},
        "policyTypes": ["Ingress"]
      }
    }
  ]
}`

func TestAuditPolicies_AllNamespaces(t *testing.T) {
	client := &mockClient{runResponse: allNamespacesPolicies}
	mgr := NewNetworkPolicyManager(client, false)

	report, err := mgr.AuditPolicies(context.Background(), nil)
	if err != nil {
		t.Fatalf("AuditPolicies failed: %v", err)
	}
	if report == nil {
		t.Fatal("nil report")
	}

	got := map[string]NamespacePolicyAudit{}
	for _, ns := range report.Namespaces {
		got[ns.Namespace] = ns
	}

	prod := got["prod"]
	if !prod.DefaultDenyIn || !prod.DefaultDenyOut {
		t.Errorf("prod should be default-deny in both directions, got %+v", prod)
	}
	if prod.PolicyCount != 2 {
		t.Errorf("prod policy count = %d, want 2", prod.PolicyCount)
	}

	staging := got["staging"]
	if !staging.DefaultDenyIn {
		t.Errorf("staging should be default-deny ingress, got %+v", staging)
	}
	if staging.DefaultDenyOut {
		t.Errorf("staging should NOT be default-deny egress, got %+v", staging)
	}

	dev := got["dev"]
	if dev.DefaultDenyIn || dev.DefaultDenyOut {
		t.Errorf("dev (only scoped policy) should not be default-deny in any direction, got %+v", dev)
	}
}

func TestAuditPolicies_FilteredNamespaces(t *testing.T) {
	client := &mockClient{
		runWithNSResponse: `{"items": [{"metadata": {"name": "default-deny", "namespace": "only"}, "spec": {"podSelector": {}, "policyTypes": ["Ingress"]}}]}`,
	}
	mgr := NewNetworkPolicyManager(client, false)

	report, err := mgr.AuditPolicies(context.Background(), []string{"only", "missing"})
	if err != nil {
		t.Fatalf("AuditPolicies failed: %v", err)
	}
	if len(report.Namespaces) != 2 {
		t.Fatalf("expected 2 namespaces in report, got %d", len(report.Namespaces))
	}
}

func TestIsDefaultDenyForType(t *testing.T) {
	cases := []struct {
		name     string
		policy   NetworkPolicyInfo
		typ      string
		expected bool
	}{
		{
			name:     "empty selector with type",
			policy:   NetworkPolicyInfo{PodSelector: nil, PolicyTypes: []string{"Ingress"}},
			typ:      "Ingress",
			expected: true,
		},
		{
			name:     "scoped selector",
			policy:   NetworkPolicyInfo{PodSelector: map[string]string{"app": "x"}, PolicyTypes: []string{"Ingress"}},
			typ:      "Ingress",
			expected: false,
		},
		{
			name:     "missing direction",
			policy:   NetworkPolicyInfo{PolicyTypes: []string{"Egress"}},
			typ:      "Ingress",
			expected: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDefaultDenyForType(tt.policy, tt.typ); got != tt.expected {
				t.Errorf("isDefaultDenyForType = %v, want %v", got, tt.expected)
			}
		})
	}
}
