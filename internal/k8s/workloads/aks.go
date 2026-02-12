package workloads

import (
	"fmt"
	"strings"
)

// AKSNodeSelectorForPool returns a node selector for targeting specific AKS node pools
func AKSNodeSelectorForPool(poolName string) map[string]string {
	return map[string]string{
		AKSLabelNodePool: poolName,
	}
}

// AKSNodeSelectorForSpot returns a node selector for Spot VM nodes
func AKSNodeSelectorForSpot() map[string]string {
	return map[string]string{
		AKSLabelSpot: AKSLabelSpotValue,
	}
}

// AKSNodeSelectorForGPU returns a node selector for GPU nodes
func AKSNodeSelectorForGPU(vmSize string) map[string]string {
	return map[string]string{
		AKSLabelVMSize: vmSize,
	}
}

// AKSNodeSelectorForVirtualNode returns a node selector for Virtual Nodes (ACI)
func AKSNodeSelectorForVirtualNode() map[string]string {
	return map[string]string{
		AKSLabelVirtualNode: AKSLabelVirtualNodeValue,
	}
}

// AKSToleration represents a Kubernetes toleration
type AKSToleration struct {
	Key      string `json:"key"`
	Operator string `json:"operator"`
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect"`
}

// AKSTolerationForSpot returns tolerations for Spot VM nodes
func AKSTolerationForSpot() AKSToleration {
	return AKSToleration{
		Key:      AKSTaintSpot,
		Operator: "Equal",
		Value:    AKSLabelSpotValue,
		Effect:   "NoSchedule",
	}
}

// AKSTolerationForGPU returns tolerations for GPU nodes
func AKSTolerationForGPU() AKSToleration {
	return AKSToleration{
		Key:      AKSTaintGPU,
		Operator: "Equal",
		Value:    "gpu",
		Effect:   "NoSchedule",
	}
}

// AKSTolerationForVirtualNode returns tolerations for Virtual Nodes
func AKSTolerationForVirtualNode() AKSToleration {
	return AKSToleration{
		Key:      AKSTaintVirtualNode,
		Operator: "Exists",
		Effect:   "NoSchedule",
	}
}

// AKSWorkloadSchedulingRecommendation provides scheduling recommendation
type AKSWorkloadSchedulingRecommendation struct {
	NodeSelector map[string]string
	Tolerations  []AKSToleration
	Affinity     string
	Reason       string
	Notes        []string
}

// GetAKSSchedulingRecommendation returns AKS-specific scheduling recommendations
func GetAKSSchedulingRecommendation(useCase string) AKSWorkloadSchedulingRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// Cost-optimized batch processing
	if containsAny(useCaseLower, []string{"batch", "job", "etl", "data processing", "cost"}) {
		return AKSWorkloadSchedulingRecommendation{
			NodeSelector: AKSNodeSelectorForSpot(),
			Tolerations:  []AKSToleration{AKSTolerationForSpot()},
			Reason:       "Spot VMs provide up to 90% discount for fault-tolerant batch workloads",
			Notes: []string{
				"Spot VMs can be evicted when Azure needs the capacity",
				"Use checkpointing for long-running jobs",
				"Consider using Job with restartPolicy: OnFailure",
				"Combine with PodDisruptionBudget for availability",
				"AKS provides eviction policies: Delete or Deallocate",
			},
		}
	}

	// GPU workloads
	if containsAny(useCaseLower, []string{"gpu", "ml", "machine learning", "ai", "training", "inference", "cuda"}) {
		return AKSWorkloadSchedulingRecommendation{
			NodeSelector: map[string]string{
				AKSLabelVMSize: "Standard_NC6s_v3", // Default, user should customize
			},
			Tolerations: []AKSToleration{AKSTolerationForGPU()},
			Reason:      "GPU workloads require node selector and toleration for GPU-enabled nodes",
			Notes: []string{
				"Available GPU VM sizes: NC-series (NVIDIA Tesla K80), NCv3 (V100), NCasT4_v3 (T4), NVv3 (M60)",
				"Request GPU resources: nvidia.com/gpu: 1",
				"Use cluster autoscaler for automatic GPU node scaling",
				"Consider time-slicing GPUs for inference workloads",
				"GPU node pools require specific VM sizes in supported regions",
			},
		}
	}

	// High availability production workloads
	if containsAny(useCaseLower, []string{"production", "critical", "high availability", "ha"}) {
		return AKSWorkloadSchedulingRecommendation{
			Affinity: "podAntiAffinity across zones",
			Reason:   "Production workloads should spread across availability zones",
			Notes: []string{
				"Use pod anti-affinity to spread replicas across nodes",
				"Use zone-redundant node pools for zone-level redundancy",
				"Use PodDisruptionBudget to maintain availability during upgrades",
				"Avoid Spot VMs for critical workloads",
				"Consider using Azure availability zones in supported regions",
			},
		}
	}

	// Burst workloads using Virtual Nodes
	if containsAny(useCaseLower, []string{"burst", "serverless", "aci", "virtual node", "scale to zero"}) {
		return AKSWorkloadSchedulingRecommendation{
			NodeSelector: AKSNodeSelectorForVirtualNode(),
			Tolerations:  []AKSToleration{AKSTolerationForVirtualNode()},
			Reason:       "Virtual Nodes (ACI) enable rapid scaling without pre-provisioned nodes",
			Notes: []string{
				"Virtual Nodes use Azure Container Instances for pod execution",
				"Ideal for burst workloads and rapid scaling",
				"Not all features are supported (e.g., DaemonSets, hostPath)",
				"Pods on Virtual Nodes have network isolation by default",
				"Billing is per-second based on actual resource usage",
			},
		}
	}

	// Development and testing
	if containsAny(useCaseLower, []string{"dev", "test", "staging", "preview"}) {
		return AKSWorkloadSchedulingRecommendation{
			NodeSelector: AKSNodeSelectorForSpot(),
			Tolerations:  []AKSToleration{AKSTolerationForSpot()},
			Reason:       "Non-production workloads can use Spot VMs for cost savings",
			Notes: []string{
				"Spot VMs are significantly cheaper than regular VMs",
				"VMs may be evicted when Azure needs capacity",
				"Suitable for stateless workloads that can tolerate interruptions",
				"Consider using B-series VMs for dev/test with low utilization",
			},
		}
	}

	// Default recommendation
	return AKSWorkloadSchedulingRecommendation{
		Reason: "Default scheduling uses regular nodes without specific constraints",
		Notes: []string{
			"Consider node pools for workload isolation",
			"Use resource requests and limits for proper scheduling",
			"AKS cluster autoscaler handles node provisioning automatically",
			"Use System node pools for critical system pods",
		},
	}
}

// AKSNodePoolRecommendation provides recommendations for node pool configuration
type AKSNodePoolRecommendation struct {
	VMSize     string
	OSDiskType string
	OSDiskSize int
	Spot       bool
	Reason     string
	Notes      []string
}

// GetAKSNodePoolRecommendation returns node pool recommendations for workload type
func GetAKSNodePoolRecommendation(workloadType string) AKSNodePoolRecommendation {
	workloadLower := strings.ToLower(workloadType)

	// Memory-intensive workloads
	if containsAny(workloadLower, []string{"memory", "cache", "redis", "memcached", "in-memory"}) {
		return AKSNodePoolRecommendation{
			VMSize:     "Standard_E4s_v5",
			OSDiskType: "Premium_LRS",
			OSDiskSize: 128,
			Reason:     "E-series VMs are memory-optimized for memory-intensive workloads",
			Notes: []string{
				"E-series provides high memory-to-vCPU ratio",
				"Consider Ev5 for latest generation memory-optimized VMs",
				"Use Premium SSD for consistent I/O performance",
				"Consider ephemeral OS disks for better performance",
			},
		}
	}

	// CPU-intensive workloads
	if containsAny(workloadLower, []string{"cpu", "compute", "processing", "calculation"}) {
		return AKSNodePoolRecommendation{
			VMSize:     "Standard_F8s_v2",
			OSDiskType: "Premium_LRS",
			OSDiskSize: 128,
			Reason:     "F-series VMs are compute-optimized for CPU-intensive workloads",
			Notes: []string{
				"F-series provides high CPU-to-memory ratio",
				"Fsv2 offers best per-vCPU performance",
				"Use ephemeral OS disks for latency-sensitive workloads",
				"Consider Fx-series for extreme compute needs",
			},
		}
	}

	// Storage-intensive workloads
	if containsAny(workloadLower, []string{"storage", "database", "disk", "io"}) {
		return AKSNodePoolRecommendation{
			VMSize:     "Standard_L8s_v3",
			OSDiskType: "Premium_LRS",
			OSDiskSize: 256,
			Reason:     "L-series VMs are storage-optimized with local NVMe SSDs",
			Notes: []string{
				"L-series provides high local storage throughput",
				"Local NVMe is ephemeral and not suitable for persistent data",
				"Use Azure Disk CSI for persistent storage",
				"Consider Premium SSD v2 for high IOPS requirements",
			},
		}
	}

	// General purpose
	return AKSNodePoolRecommendation{
		VMSize:     "Standard_D4s_v5",
		OSDiskType: "Premium_LRS",
		OSDiskSize: 128,
		Reason:     "D-series VMs provide good balance of compute and memory",
		Notes: []string{
			"D-series is suitable for most general workloads",
			"Dv5 offers latest generation general-purpose VMs",
			"Consider B-series for burstable workloads with low utilization",
			"Use ephemeral OS disks when possible for better performance",
		},
	}
}

// AKSVirtualNodesConsiderations returns notes about AKS Virtual Nodes (ACI)
func AKSVirtualNodesConsiderations() []string {
	return []string{
		"Virtual Nodes use Azure Container Instances for serverless container execution",
		"Enable rapid burst scaling without pre-provisioning nodes",
		"Per-second billing based on actual vCPU and memory usage",
		"Not all Kubernetes features are supported (DaemonSets, hostPath, etc.)",
		"Requires Azure CNI networking for VNet integration",
		"Linux containers only; Windows containers not supported on Virtual Nodes",
		"GPU containers are supported with specific ACI configurations",
		"Network policies are not supported on Virtual Nodes",
	}
}

// AKSWorkloadNotes returns important notes about AKS workload management
func AKSWorkloadNotes() []string {
	return []string{
		"AKS uses node pools (agentpools) for workload isolation",
		"System node pools host critical system pods; User node pools for applications",
		"Spot VMs provide significant cost savings for fault-tolerant workloads",
		"Virtual Nodes (ACI) enable serverless container execution for burst workloads",
		"Cluster autoscaler automatically adjusts node count based on workload demands",
		"Use pod disruption budgets to maintain availability during upgrades",
		"GPU node pools require specific VM sizes in supported regions",
		"Ephemeral OS disks improve performance and reduce cost for stateless workloads",
	}
}

// IsAKSNodeLabel checks if a label key is an AKS-specific node label
func IsAKSNodeLabel(key string) bool {
	aksNodeLabelPrefixes := []string{
		"kubernetes.azure.com/",
		"node.kubernetes.io/",
		"topology.kubernetes.io/",
		"virtual-kubelet.io/",
	}

	for _, prefix := range aksNodeLabelPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	// Also check for specific AKS labels
	if key == AKSLabelNodePool {
		return true
	}

	return false
}

// GKEWorkloadComparison returns comparison notes between AKS and GKE
func GKEWorkloadComparison() map[string]string {
	return map[string]string{
		"aks_node_pool_label":     AKSLabelNodePool,
		"gke_node_pool_label":     GKELabelNodePool,
		"eks_node_pool_label":     EKSLabelNodeGroup,
		"aks_spot_label":          AKSLabelSpot,
		"gke_spot_label":          GKELabelSpot,
		"eks_spot_label":          EKSLabelCapacityType,
		"aks_gpu_taint":           AKSTaintGPU,
		"gke_gpu_taint":           GKETaintGPU,
		"aks_serverless":          "Virtual Nodes (ACI)",
		"gke_serverless":          "GKE Autopilot",
		"eks_serverless":          "Fargate",
		"aks_general_purpose_vm":  "Standard_D4s_v5",
		"gke_general_purpose_vm":  "e2-standard-4",
		"eks_general_purpose_vm":  "m5.xlarge",
	}
}

// AKSPodSpecForPool generates pod spec YAML snippet for targeting a node pool
func AKSPodSpecForPool(poolName string, spot bool) string {
	var sb strings.Builder

	sb.WriteString("spec:\n")
	sb.WriteString("  nodeSelector:\n")
	sb.WriteString(fmt.Sprintf("    %s: %s\n", AKSLabelNodePool, poolName))

	if spot {
		sb.WriteString(fmt.Sprintf("    %s: %s\n", AKSLabelSpot, AKSLabelSpotValue))
		sb.WriteString("  tolerations:\n")
		sb.WriteString(fmt.Sprintf("  - key: %s\n", AKSTaintSpot))
		sb.WriteString("    operator: Equal\n")
		sb.WriteString(fmt.Sprintf("    value: %s\n", AKSLabelSpotValue))
		sb.WriteString("    effect: NoSchedule\n")
	}

	return sb.String()
}

// AKSPodSpecForVirtualNode generates pod spec YAML snippet for Virtual Nodes
func AKSPodSpecForVirtualNode() string {
	var sb strings.Builder

	sb.WriteString("spec:\n")
	sb.WriteString("  nodeSelector:\n")
	sb.WriteString(fmt.Sprintf("    %s: %s\n", AKSLabelVirtualNode, AKSLabelVirtualNodeValue))
	sb.WriteString("  tolerations:\n")
	sb.WriteString(fmt.Sprintf("  - key: %s\n", AKSTaintVirtualNode))
	sb.WriteString("    operator: Exists\n")
	sb.WriteString("    effect: NoSchedule\n")

	return sb.String()
}

// AKSPodAntiAffinityForHA generates pod anti-affinity YAML for high availability
func AKSPodAntiAffinityForHA(appLabel string) string {
	return fmt.Sprintf(`affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      podAffinityTerm:
        labelSelector:
          matchExpressions:
          - key: app
            operator: In
            values:
            - %s
        topologyKey: topology.kubernetes.io/zone`, appLabel)
}
