package cluster

import (
	"testing"
)

func TestCountReadyNodes(t *testing.T) {
	tests := []struct {
		name     string
		nodes    []NodeInfo
		expected int
	}{
		{
			name:     "empty list",
			nodes:    []NodeInfo{},
			expected: 0,
		},
		{
			name: "all ready",
			nodes: []NodeInfo{
				{Name: "node-1", Status: "Ready"},
				{Name: "node-2", Status: "Ready"},
				{Name: "node-3", Status: "Ready"},
			},
			expected: 3,
		},
		{
			name: "some ready",
			nodes: []NodeInfo{
				{Name: "node-1", Status: "Ready"},
				{Name: "node-2", Status: "NotReady"},
				{Name: "node-3", Status: "Ready"},
			},
			expected: 2,
		},
		{
			name: "none ready",
			nodes: []NodeInfo{
				{Name: "node-1", Status: "NotReady"},
				{Name: "node-2", Status: "NotReady"},
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CountReadyNodes(tt.nodes)
			if result != tt.expected {
				t.Errorf("CountReadyNodes() = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestAllNodesReady(t *testing.T) {
	tests := []struct {
		name     string
		nodes    []NodeInfo
		expected bool
	}{
		{
			name:     "empty list",
			nodes:    []NodeInfo{},
			expected: false,
		},
		{
			name: "all ready",
			nodes: []NodeInfo{
				{Name: "node-1", Status: "Ready"},
				{Name: "node-2", Status: "Ready"},
			},
			expected: true,
		},
		{
			name: "one not ready",
			nodes: []NodeInfo{
				{Name: "node-1", Status: "Ready"},
				{Name: "node-2", Status: "NotReady"},
			},
			expected: false,
		},
		{
			name: "single ready node",
			nodes: []NodeInfo{
				{Name: "node-1", Status: "Ready"},
			},
			expected: true,
		},
		{
			name: "single not ready node",
			nodes: []NodeInfo{
				{Name: "node-1", Status: "NotReady"},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AllNodesReady(tt.nodes)
			if result != tt.expected {
				t.Errorf("AllNodesReady() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestNodeInfoFields(t *testing.T) {
	node := NodeInfo{
		Name:       "worker-node-1",
		Role:       "worker",
		Status:     "Ready",
		InternalIP: "10.0.0.5",
		ExternalIP: "203.0.113.10",
		InstanceID: "i-1234567890abcdef0",
		Labels: map[string]string{
			"node.kubernetes.io/instance-type": "t3.medium",
			"topology.kubernetes.io/zone":      "us-east-1a",
		},
	}

	if node.Name != "worker-node-1" {
		t.Errorf("expected name 'worker-node-1', got %q", node.Name)
	}
	if node.Role != "worker" {
		t.Errorf("expected role 'worker', got %q", node.Role)
	}
	if node.Status != "Ready" {
		t.Errorf("expected status 'Ready', got %q", node.Status)
	}
	if node.InternalIP != "10.0.0.5" {
		t.Errorf("expected internal IP '10.0.0.5', got %q", node.InternalIP)
	}
	if node.Labels["node.kubernetes.io/instance-type"] != "t3.medium" {
		t.Errorf("expected instance type label 't3.medium', got %q", node.Labels["node.kubernetes.io/instance-type"])
	}
}
