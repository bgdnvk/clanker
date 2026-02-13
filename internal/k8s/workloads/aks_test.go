package workloads

import (
	"strings"
	"testing"
)

func TestAKSNodeSelectorForPool(t *testing.T) {
	selector := AKSNodeSelectorForPool("mypool")

	if selector[AKSLabelNodePool] != "mypool" {
		t.Errorf("expected pool name 'mypool', got %s", selector[AKSLabelNodePool])
	}
}

func TestAKSNodeSelectorForSpot(t *testing.T) {
	selector := AKSNodeSelectorForSpot()

	if selector[AKSLabelSpot] != AKSLabelSpotValue {
		t.Errorf("expected spot value %s, got %s", AKSLabelSpotValue, selector[AKSLabelSpot])
	}
}

func TestAKSNodeSelectorForGPU(t *testing.T) {
	selector := AKSNodeSelectorForGPU("Standard_NC6s_v3")

	if selector[AKSLabelVMSize] != "Standard_NC6s_v3" {
		t.Errorf("expected VM size 'Standard_NC6s_v3', got %s", selector[AKSLabelVMSize])
	}
}

func TestAKSNodeSelectorForVirtualNode(t *testing.T) {
	selector := AKSNodeSelectorForVirtualNode()

	if selector[AKSLabelVirtualNode] != AKSLabelVirtualNodeValue {
		t.Errorf("expected virtual node value %s, got %s", AKSLabelVirtualNodeValue, selector[AKSLabelVirtualNode])
	}
}

func TestAKSTolerationForSpot(t *testing.T) {
	tol := AKSTolerationForSpot()

	if tol.Key != AKSTaintSpot {
		t.Errorf("expected taint key %s, got %s", AKSTaintSpot, tol.Key)
	}

	if tol.Operator != "Equal" {
		t.Errorf("expected operator 'Equal', got %s", tol.Operator)
	}

	if tol.Value != AKSLabelSpotValue {
		t.Errorf("expected value %s, got %s", AKSLabelSpotValue, tol.Value)
	}

	if tol.Effect != "NoSchedule" {
		t.Errorf("expected effect 'NoSchedule', got %s", tol.Effect)
	}
}

func TestAKSTolerationForGPU(t *testing.T) {
	tol := AKSTolerationForGPU()

	if tol.Key != AKSTaintGPU {
		t.Errorf("expected taint key %s, got %s", AKSTaintGPU, tol.Key)
	}

	if tol.Effect != "NoSchedule" {
		t.Errorf("expected effect 'NoSchedule', got %s", tol.Effect)
	}
}

func TestAKSTolerationForVirtualNode(t *testing.T) {
	tol := AKSTolerationForVirtualNode()

	if tol.Key != AKSTaintVirtualNode {
		t.Errorf("expected taint key %s, got %s", AKSTaintVirtualNode, tol.Key)
	}

	if tol.Operator != "Exists" {
		t.Errorf("expected operator 'Exists', got %s", tol.Operator)
	}
}

func TestGetAKSSchedulingRecommendation(t *testing.T) {
	tests := []struct {
		name            string
		useCase         string
		wantSpot        bool
		wantVirtualNode bool
		wantGPU         bool
		wantHA          bool
	}{
		{
			name:     "Batch processing",
			useCase:  "batch data processing job",
			wantSpot: true,
		},
		{
			name:    "GPU workload",
			useCase: "machine learning training",
			wantGPU: true,
		},
		{
			name:    "Production HA",
			useCase: "production critical service",
			wantHA:  true,
		},
		{
			name:            "Burst workload",
			useCase:         "serverless burst scaling",
			wantVirtualNode: true,
		},
		{
			name:     "Development",
			useCase:  "dev test environment",
			wantSpot: true,
		},
		{
			name:    "Default",
			useCase: "general application",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GetAKSSchedulingRecommendation(tt.useCase)

			if tt.wantSpot {
				if rec.NodeSelector == nil || rec.NodeSelector[AKSLabelSpot] != AKSLabelSpotValue {
					t.Error("expected Spot node selector")
				}
				if len(rec.Tolerations) == 0 {
					t.Error("expected Spot tolerations")
				}
			}

			if tt.wantVirtualNode {
				if rec.NodeSelector == nil || rec.NodeSelector[AKSLabelVirtualNode] != AKSLabelVirtualNodeValue {
					t.Error("expected Virtual Node selector")
				}
			}

			if tt.wantGPU {
				if rec.NodeSelector == nil {
					t.Error("expected GPU node selector")
				}
				if len(rec.Tolerations) == 0 {
					t.Error("expected GPU tolerations")
				}
			}

			if tt.wantHA {
				if rec.Affinity == "" {
					t.Error("expected affinity for HA")
				}
			}

			if rec.Reason == "" {
				t.Error("recommendation should have a reason")
			}

			if len(rec.Notes) == 0 {
				t.Error("recommendation should have notes")
			}
		})
	}
}

func TestGetAKSNodePoolRecommendation(t *testing.T) {
	tests := []struct {
		name             string
		workloadType     string
		wantVMSizePrefix string
		wantReason       bool
	}{
		{
			name:             "Memory workload",
			workloadType:     "redis cache",
			wantVMSizePrefix: "Standard_E",
		},
		{
			name:             "CPU workload",
			workloadType:     "compute processing",
			wantVMSizePrefix: "Standard_F",
		},
		{
			name:             "Storage workload",
			workloadType:     "database storage",
			wantVMSizePrefix: "Standard_L",
		},
		{
			name:             "General workload",
			workloadType:     "web server",
			wantVMSizePrefix: "Standard_D",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := GetAKSNodePoolRecommendation(tt.workloadType)

			if !strings.HasPrefix(rec.VMSize, tt.wantVMSizePrefix) {
				t.Errorf("VMSize = %s, want prefix %s", rec.VMSize, tt.wantVMSizePrefix)
			}

			if rec.OSDiskType == "" {
				t.Error("expected OSDiskType")
			}

			if rec.OSDiskSize <= 0 {
				t.Error("expected positive OSDiskSize")
			}

			if rec.Reason == "" {
				t.Error("recommendation should have a reason")
			}

			if len(rec.Notes) == 0 {
				t.Error("recommendation should have notes")
			}
		})
	}
}

func TestAKSVirtualNodesConsiderations(t *testing.T) {
	notes := AKSVirtualNodesConsiderations()

	if len(notes) == 0 {
		t.Error("expected at least one consideration")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"Container Instances",
		"burst",
		"billing",
		"DaemonSets",
		"CNI",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("virtual nodes considerations should mention %s", topic)
		}
	}
}

func TestAKSWorkloadNotes(t *testing.T) {
	notes := AKSWorkloadNotes()

	if len(notes) == 0 {
		t.Error("expected at least one workload note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"node pool",
		"Spot",
		"Virtual Nodes",
		"autoscaler",
		"GPU",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("workload notes should mention %s", topic)
		}
	}
}

func TestIsAKSNodeLabel(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{AKSLabelNodePool, true},
		{AKSLabelNodePoolName, true},
		{AKSLabelSpot, true},
		{AKSLabelMode, true},
		{AKSLabelVirtualNodeProvider, true},
		{"kubernetes.azure.com/custom", true},
		{"node.kubernetes.io/instance-type", true},
		{GKELabelNodePool, false},
		{GKELabelSpot, false},
		{EKSLabelNodeGroup, false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsAKSNodeLabel(tt.key)
			if got != tt.want {
				t.Errorf("IsAKSNodeLabel(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestGKEWorkloadComparison(t *testing.T) {
	comparison := GKEWorkloadComparison()

	if len(comparison) == 0 {
		t.Error("expected workload comparison entries")
	}

	// Verify AKS entries
	aksKeys := []string{"aks_node_pool_label", "aks_spot_label", "aks_gpu_taint", "aks_serverless", "aks_general_purpose_vm"}
	for _, key := range aksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify GKE entries
	gkeKeys := []string{"gke_node_pool_label", "gke_spot_label", "gke_gpu_taint", "gke_serverless", "gke_general_purpose_vm"}
	for _, key := range gkeKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify EKS entries
	eksKeys := []string{"eks_node_pool_label", "eks_spot_label", "eks_serverless", "eks_general_purpose_vm"}
	for _, key := range eksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}
}

func TestAKSPodSpecForPool(t *testing.T) {
	tests := []struct {
		name         string
		poolName     string
		spot         bool
		wantContains []string
	}{
		{
			name:     "Regular pool",
			poolName: "mypool",
			spot:     false,
			wantContains: []string{
				"nodeSelector:",
				"agentpool: mypool",
			},
		},
		{
			name:     "Spot pool",
			poolName: "spotpool",
			spot:     true,
			wantContains: []string{
				"nodeSelector:",
				"agentpool: spotpool",
				"kubernetes.azure.com/scalesetpriority: spot",
				"tolerations:",
				"effect: NoSchedule",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := AKSPodSpecForPool(tt.poolName, tt.spot)

			for _, want := range tt.wantContains {
				if !strings.Contains(spec, want) {
					t.Errorf("pod spec should contain %s", want)
				}
			}
		})
	}
}

func TestAKSPodSpecForVirtualNode(t *testing.T) {
	spec := AKSPodSpecForVirtualNode()

	expectedContents := []string{
		"nodeSelector:",
		"type: virtual-kubelet",
		"tolerations:",
		"virtual-kubelet.io/provider",
		"Exists",
		"NoSchedule",
	}

	for _, expected := range expectedContents {
		if !strings.Contains(spec, expected) {
			t.Errorf("virtual node spec should contain %s", expected)
		}
	}
}

func TestAKSPodAntiAffinityForHA(t *testing.T) {
	affinity := AKSPodAntiAffinityForHA("myapp")

	expectedContents := []string{
		"podAntiAffinity:",
		"preferredDuringSchedulingIgnoredDuringExecution",
		"labelSelector:",
		"app",
		"myapp",
		"topologyKey: topology.kubernetes.io/zone",
	}

	for _, expected := range expectedContents {
		if !strings.Contains(affinity, expected) {
			t.Errorf("anti-affinity should contain %s", expected)
		}
	}
}

func TestAKSNodeLabelConstants(t *testing.T) {
	if AKSLabelNodePool != "agentpool" {
		t.Errorf("AKSLabelNodePool = %s, want agentpool", AKSLabelNodePool)
	}

	if AKSLabelSpot != "kubernetes.azure.com/scalesetpriority" {
		t.Errorf("AKSLabelSpot = %s, want kubernetes.azure.com/scalesetpriority", AKSLabelSpot)
	}

	if AKSLabelSpotValue != "spot" {
		t.Errorf("AKSLabelSpotValue = %s, want spot", AKSLabelSpotValue)
	}

	if AKSLabelVirtualNode != "type" {
		t.Errorf("AKSLabelVirtualNode = %s, want type", AKSLabelVirtualNode)
	}

	if AKSLabelVirtualNodeValue != "virtual-kubelet" {
		t.Errorf("AKSLabelVirtualNodeValue = %s, want virtual-kubelet", AKSLabelVirtualNodeValue)
	}
}

func TestAKSTaintConstants(t *testing.T) {
	if AKSTaintSpot != "kubernetes.azure.com/scalesetpriority" {
		t.Errorf("AKSTaintSpot = %s, want kubernetes.azure.com/scalesetpriority", AKSTaintSpot)
	}

	if AKSTaintGPU != "sku" {
		t.Errorf("AKSTaintGPU = %s, want sku", AKSTaintGPU)
	}

	if AKSTaintVirtualNode != "virtual-kubelet.io/provider" {
		t.Errorf("AKSTaintVirtualNode = %s, want virtual-kubelet.io/provider", AKSTaintVirtualNode)
	}
}
