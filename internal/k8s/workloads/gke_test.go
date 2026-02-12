package workloads

import (
	"strings"
	"testing"
)

func TestGKENodeSelectorForPool(t *testing.T) {
	selector := GKENodeSelectorForPool("my-pool")

	if len(selector) != 1 {
		t.Errorf("expected 1 selector, got %d", len(selector))
	}

	if selector[GKELabelNodePool] != "my-pool" {
		t.Errorf("expected node pool 'my-pool', got %s", selector[GKELabelNodePool])
	}
}

func TestGKENodeSelectorForPreemptible(t *testing.T) {
	selector := GKENodeSelectorForPreemptible()

	if selector[GKELabelPreemptible] != "true" {
		t.Errorf("expected preemptible 'true', got %s", selector[GKELabelPreemptible])
	}
}

func TestGKENodeSelectorForSpot(t *testing.T) {
	selector := GKENodeSelectorForSpot()

	if selector[GKELabelSpot] != "true" {
		t.Errorf("expected spot 'true', got %s", selector[GKELabelSpot])
	}
}

func TestGKENodeSelectorForGPU(t *testing.T) {
	selector := GKENodeSelectorForGPU("nvidia-tesla-t4")

	if selector[GKELabelAcceleratorType] != "nvidia-tesla-t4" {
		t.Errorf("expected accelerator type 'nvidia-tesla-t4', got %s", selector[GKELabelAcceleratorType])
	}
}

func TestGKETolerationForPreemptible(t *testing.T) {
	toleration := GKETolerationForPreemptible()

	if toleration.Key != GKETaintPreemptible {
		t.Errorf("expected key %s, got %s", GKETaintPreemptible, toleration.Key)
	}

	if toleration.Operator != "Equal" {
		t.Errorf("expected operator 'Equal', got %s", toleration.Operator)
	}

	if toleration.Value != "true" {
		t.Errorf("expected value 'true', got %s", toleration.Value)
	}

	if toleration.Effect != "NoSchedule" {
		t.Errorf("expected effect 'NoSchedule', got %s", toleration.Effect)
	}
}

func TestGKETolerationForSpot(t *testing.T) {
	toleration := GKETolerationForSpot()

	if toleration.Key != GKETaintSpot {
		t.Errorf("expected key %s, got %s", GKETaintSpot, toleration.Key)
	}

	if toleration.Effect != "NoSchedule" {
		t.Errorf("expected effect 'NoSchedule', got %s", toleration.Effect)
	}
}

func TestGKETolerationForGPU(t *testing.T) {
	toleration := GKETolerationForGPU()

	if toleration.Key != GKETaintGPU {
		t.Errorf("expected key %s, got %s", GKETaintGPU, toleration.Key)
	}

	if toleration.Operator != "Exists" {
		t.Errorf("expected operator 'Exists', got %s", toleration.Operator)
	}
}

func TestGKEAutopilotConsiderations(t *testing.T) {
	considerations := GKEAutopilotConsiderations()

	if len(considerations) == 0 {
		t.Error("expected at least one Autopilot consideration")
	}

	// Verify key topics are mentioned
	considerationsText := strings.Join(considerations, " ")

	expectedTopics := []string{
		"Autopilot",
		"Resource",
		"security",
		"DaemonSets",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(considerationsText, topic) {
			t.Errorf("Autopilot considerations should mention %s", topic)
		}
	}
}

func TestGKEAutopilotResourceClassAnnotation(t *testing.T) {
	annotation := GKEAutopilotResourceClassAnnotation(GKEAutopilotResourceClassGeneral)

	if annotation["cloud.google.com/compute-class"] != GKEAutopilotResourceClassGeneral {
		t.Errorf("expected compute-class %s, got %s",
			GKEAutopilotResourceClassGeneral, annotation["cloud.google.com/compute-class"])
	}
}

func TestGetGKESchedulingRecommendation(t *testing.T) {
	tests := []struct {
		name         string
		useCase      string
		wantSpot     bool
		wantPreempt  bool
		wantGPU      bool
		wantAffinity bool
	}{
		{
			name:     "Batch processing",
			useCase:  "batch data processing job",
			wantSpot: true,
		},
		{
			name:     "Cost optimization",
			useCase:  "cost optimized workload",
			wantSpot: true,
		},
		{
			name:    "GPU workload",
			useCase: "machine learning training",
			wantGPU: true,
		},
		{
			name:    "AI inference",
			useCase: "ai inference service",
			wantGPU: true,
		},
		{
			name:         "Production workload",
			useCase:      "critical production service",
			wantAffinity: true,
		},
		{
			name:         "High availability",
			useCase:      "regional multi-zone ha deployment",
			wantAffinity: true,
		},
		{
			name:        "Development",
			useCase:     "dev environment",
			wantPreempt: true,
		},
		{
			name:        "Testing",
			useCase:     "test workload",
			wantPreempt: true,
		},
		{
			name:    "Default",
			useCase: "general application",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GetGKESchedulingRecommendation(tt.useCase)

			if tt.wantSpot {
				if rec.NodeSelector[GKELabelSpot] != "true" {
					t.Error("expected Spot node selector for batch workload")
				}
				if len(rec.Tolerations) == 0 {
					t.Error("expected tolerations for Spot workload")
				}
			}

			if tt.wantPreempt {
				if rec.NodeSelector[GKELabelPreemptible] != "true" {
					t.Error("expected preemptible node selector for dev workload")
				}
			}

			if tt.wantGPU {
				if _, ok := rec.NodeSelector[GKELabelAcceleratorType]; !ok {
					t.Error("expected GPU accelerator type in node selector")
				}
			}

			if tt.wantAffinity {
				if rec.Affinity == "" {
					t.Error("expected affinity configuration for HA workload")
				}
			}

			if rec.Reason == "" {
				t.Error("expected a reason for recommendation")
			}

			if len(rec.Notes) == 0 {
				t.Error("expected notes for recommendation")
			}
		})
	}
}

func TestGetGKENodePoolRecommendation(t *testing.T) {
	tests := []struct {
		name                string
		workloadType        string
		wantMachineContains string
	}{
		{
			name:                "Memory intensive",
			workloadType:        "redis cache",
			wantMachineContains: "highmem",
		},
		{
			name:                "In-memory database",
			workloadType:        "in-memory database",
			wantMachineContains: "highmem",
		},
		{
			name:                "CPU intensive",
			workloadType:        "compute processing",
			wantMachineContains: "c2",
		},
		{
			name:                "General workload",
			workloadType:        "web application",
			wantMachineContains: "e2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GetGKENodePoolRecommendation(tt.workloadType)

			if !strings.Contains(rec.MachineType, tt.wantMachineContains) {
				t.Errorf("expected machine type containing %s, got %s",
					tt.wantMachineContains, rec.MachineType)
			}

			if rec.DiskType == "" {
				t.Error("expected disk type recommendation")
			}

			if rec.DiskSizeGB == 0 {
				t.Error("expected disk size recommendation")
			}

			if rec.Reason == "" {
				t.Error("expected reason for recommendation")
			}

			if len(rec.Notes) == 0 {
				t.Error("expected notes for recommendation")
			}
		})
	}
}

func TestGKEPodSpecForPool(t *testing.T) {
	tests := []struct {
		name        string
		poolName    string
		preemptible bool
		wantStrings []string
	}{
		{
			name:        "Regular pool",
			poolName:    "my-pool",
			preemptible: false,
			wantStrings: []string{
				"nodeSelector:",
				"cloud.google.com/gke-nodepool: my-pool",
			},
		},
		{
			name:        "Preemptible pool",
			poolName:    "preempt-pool",
			preemptible: true,
			wantStrings: []string{
				"nodeSelector:",
				"cloud.google.com/gke-nodepool: preempt-pool",
				"cloud.google.com/gke-preemptible: \"true\"",
				"tolerations:",
				"effect: NoSchedule",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := GKEPodSpecForPool(tt.poolName, tt.preemptible)

			for _, want := range tt.wantStrings {
				if !strings.Contains(spec, want) {
					t.Errorf("pod spec missing expected string: %s\nGot:\n%s", want, spec)
				}
			}
		})
	}
}

func TestGKEPodAntiAffinityForHA(t *testing.T) {
	affinity := GKEPodAntiAffinityForHA("my-app")

	expectedStrings := []string{
		"podAntiAffinity",
		"preferredDuringSchedulingIgnoredDuringExecution",
		"labelSelector",
		"app",
		"my-app",
		"topology.kubernetes.io/zone",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(affinity, expected) {
			t.Errorf("anti-affinity config missing: %s\nGot:\n%s", expected, affinity)
		}
	}
}

func TestIsGKENodeLabel(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"cloud.google.com/gke-nodepool", true},
		{"cloud.google.com/gke-preemptible", true},
		{"cloud.google.com/gke-spot", true},
		{"cloud.google.com/gke-accelerator", true},
		{"node.kubernetes.io/instance-type", true},
		{"topology.gke.io/zone", true},
		{"eks.amazonaws.com/nodegroup", false},
		{"kubernetes.io/hostname", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsGKENodeLabel(tt.key)
			if got != tt.want {
				t.Errorf("IsGKENodeLabel(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestIsEKSNodeLabel(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"eks.amazonaws.com/nodegroup", true},
		{"eks.amazonaws.com/capacityType", true},
		{"node.kubernetes.io/instance-type", true},
		{"cloud.google.com/gke-nodepool", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsEKSNodeLabel(tt.key)
			if got != tt.want {
				t.Errorf("IsEKSNodeLabel(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestGKEWorkloadNotes(t *testing.T) {
	notes := GKEWorkloadNotes()

	if len(notes) == 0 {
		t.Error("expected at least one workload note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"Standard",
		"Autopilot",
		"node pool",
		"Preemptible",
		"Spot",
		"GPU",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("workload notes should mention %s", topic)
		}
	}
}

func TestGKEWorkloadConstants(t *testing.T) {
	// Verify GKE constants are defined correctly
	if GKELabelNodePool != "cloud.google.com/gke-nodepool" {
		t.Errorf("GKELabelNodePool = %s, want cloud.google.com/gke-nodepool", GKELabelNodePool)
	}

	if GKELabelPreemptible != "cloud.google.com/gke-preemptible" {
		t.Errorf("GKELabelPreemptible = %s, want cloud.google.com/gke-preemptible", GKELabelPreemptible)
	}

	if GKELabelSpot != "cloud.google.com/gke-spot" {
		t.Errorf("GKELabelSpot = %s, want cloud.google.com/gke-spot", GKELabelSpot)
	}

	if GKETaintPreemptible != "cloud.google.com/gke-preemptible" {
		t.Errorf("GKETaintPreemptible = %s, want cloud.google.com/gke-preemptible", GKETaintPreemptible)
	}

	if GKETaintGPU != "nvidia.com/gpu" {
		t.Errorf("GKETaintGPU = %s, want nvidia.com/gpu", GKETaintGPU)
	}

	if GKEAutopilotResourceClassGeneral != "general-purpose" {
		t.Errorf("GKEAutopilotResourceClassGeneral = %s, want general-purpose", GKEAutopilotResourceClassGeneral)
	}
}

func TestEKSWorkloadConstants(t *testing.T) {
	// Verify EKS constants for comparison
	if EKSLabelNodeGroup != "eks.amazonaws.com/nodegroup" {
		t.Errorf("EKSLabelNodeGroup = %s, want eks.amazonaws.com/nodegroup", EKSLabelNodeGroup)
	}

	if EKSLabelCapacityType != "eks.amazonaws.com/capacityType" {
		t.Errorf("EKSLabelCapacityType = %s, want eks.amazonaws.com/capacityType", EKSLabelCapacityType)
	}
}
