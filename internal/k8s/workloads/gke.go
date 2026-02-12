package workloads

import (
	"fmt"
	"strings"
)

// GKENodeSelector returns a node selector for targeting specific GKE node pools
func GKENodeSelectorForPool(poolName string) map[string]string {
	return map[string]string{
		GKELabelNodePool: poolName,
	}
}

// GKENodeSelectorForPreemptible returns a node selector for preemptible nodes
func GKENodeSelectorForPreemptible() map[string]string {
	return map[string]string{
		GKELabelPreemptible: "true",
	}
}

// GKENodeSelectorForSpot returns a node selector for Spot VM nodes
func GKENodeSelectorForSpot() map[string]string {
	return map[string]string{
		GKELabelSpot: "true",
	}
}

// GKENodeSelectorForGPU returns a node selector for GPU nodes
func GKENodeSelectorForGPU(acceleratorType string) map[string]string {
	return map[string]string{
		GKELabelAcceleratorType: acceleratorType,
	}
}

// GKEToleration represents a Kubernetes toleration
type GKEToleration struct {
	Key      string `json:"key"`
	Operator string `json:"operator"`
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect"`
}

// GKETolerationForPreemptible returns tolerations for preemptible nodes
func GKETolerationForPreemptible() GKEToleration {
	return GKEToleration{
		Key:      GKETaintPreemptible,
		Operator: "Equal",
		Value:    "true",
		Effect:   "NoSchedule",
	}
}

// GKETolerationForSpot returns tolerations for Spot VM nodes
func GKETolerationForSpot() GKEToleration {
	return GKEToleration{
		Key:      GKETaintSpot,
		Operator: "Equal",
		Value:    "true",
		Effect:   "NoSchedule",
	}
}

// GKETolerationForGPU returns tolerations for GPU nodes
func GKETolerationForGPU() GKEToleration {
	return GKEToleration{
		Key:      GKETaintGPU,
		Operator: "Exists",
		Effect:   "NoSchedule",
	}
}

// GKEAutopilotConsiderations returns important notes about GKE Autopilot
func GKEAutopilotConsiderations() []string {
	return []string{
		"Autopilot automatically provisions and manages nodes based on workload requirements",
		"Resource requests are required for all containers in Autopilot mode",
		"Node selectors and tolerations are limited in Autopilot mode",
		"Use compute class annotation to select resource class: general-purpose, balanced, scale-out",
		"Autopilot enforces security best practices including restricted pod security standards",
		"Host network, privileged containers, and host path volumes are not allowed",
		"DaemonSets require specific configuration to run in Autopilot",
	}
}

// GKEAutopilotResourceClassAnnotation returns the annotation for Autopilot resource class
func GKEAutopilotResourceClassAnnotation(resourceClass string) map[string]string {
	return map[string]string{
		"cloud.google.com/compute-class": resourceClass,
	}
}

// GKEWorkloadSchedulingRecommendation provides scheduling recommendation for a workload type
type GKEWorkloadSchedulingRecommendation struct {
	NodeSelector map[string]string
	Tolerations  []GKEToleration
	Affinity     string
	Reason       string
	Notes        []string
}

// GetGKESchedulingRecommendation returns GKE-specific scheduling recommendations
func GetGKESchedulingRecommendation(useCase string) GKEWorkloadSchedulingRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// Cost-optimized batch processing
	if containsAny(useCaseLower, []string{"batch", "job", "etl", "data processing", "cost"}) {
		return GKEWorkloadSchedulingRecommendation{
			NodeSelector: GKENodeSelectorForSpot(),
			Tolerations:  []GKEToleration{GKETolerationForSpot()},
			Reason:       "Spot VMs provide up to 91% discount for fault-tolerant batch workloads",
			Notes: []string{
				"Spot VMs can be preempted with 30 second notice",
				"Use checkpointing for long-running jobs",
				"Consider using Job with restartPolicy: OnFailure",
				"Combine with PodDisruptionBudget for availability",
			},
		}
	}

	// GPU workloads
	if containsAny(useCaseLower, []string{"gpu", "ml", "machine learning", "ai", "training", "inference", "cuda"}) {
		return GKEWorkloadSchedulingRecommendation{
			NodeSelector: map[string]string{
				GKELabelAcceleratorType: "nvidia-tesla-t4", // Default, user should customize
			},
			Tolerations: []GKEToleration{GKETolerationForGPU()},
			Reason:      "GPU workloads require node selector and toleration for GPU-enabled nodes",
			Notes: []string{
				"Available GPU types: nvidia-tesla-t4, nvidia-tesla-v100, nvidia-tesla-a100, nvidia-l4",
				"Request GPU resources: nvidia.com/gpu: 1",
				"Use node auto-provisioning for automatic GPU node scaling",
				"Consider time-sharing GPUs for inference workloads",
			},
		}
	}

	// High availability production workloads
	if containsAny(useCaseLower, []string{"production", "critical", "high availability", "ha"}) {
		return GKEWorkloadSchedulingRecommendation{
			Affinity: "podAntiAffinity across zones",
			Reason:   "Production workloads should spread across availability zones",
			Notes: []string{
				"Use pod anti-affinity to spread replicas across nodes",
				"Consider regional clusters for zone-level redundancy",
				"Use PodDisruptionBudget to maintain availability during upgrades",
				"Avoid Spot/preemptible nodes for critical workloads",
			},
		}
	}

	// Development and testing
	if containsAny(useCaseLower, []string{"dev", "test", "staging", "preview"}) {
		return GKEWorkloadSchedulingRecommendation{
			NodeSelector: GKENodeSelectorForPreemptible(),
			Tolerations:  []GKEToleration{GKETolerationForPreemptible()},
			Reason:       "Non-production workloads can use preemptible VMs for cost savings",
			Notes: []string{
				"Preemptible VMs are up to 80% cheaper than regular VMs",
				"VMs are terminated after 24 hours max",
				"Suitable for stateless workloads that can tolerate interruptions",
			},
		}
	}

	// Default recommendation
	return GKEWorkloadSchedulingRecommendation{
		Reason: "Default scheduling uses regular nodes without specific constraints",
		Notes: []string{
			"Consider node pools for workload isolation",
			"Use resource requests and limits for proper scheduling",
			"GKE Autopilot handles node provisioning automatically",
		},
	}
}

// GKENodePoolRecommendation provides recommendations for node pool configuration
type GKENodePoolRecommendation struct {
	MachineType string
	DiskType    string
	DiskSizeGB  int
	Preemptible bool
	Spot        bool
	Reason      string
	Notes       []string
}

// GetGKENodePoolRecommendation returns node pool recommendations for a workload type
func GetGKENodePoolRecommendation(workloadType string) GKENodePoolRecommendation {
	workloadLower := strings.ToLower(workloadType)

	// Memory-intensive workloads
	if containsAny(workloadLower, []string{"memory", "cache", "redis", "memcached", "in-memory"}) {
		return GKENodePoolRecommendation{
			MachineType: "n2-highmem-4",
			DiskType:    "pd-ssd",
			DiskSizeGB:  100,
			Reason:      "High memory machine types for memory-intensive workloads",
			Notes: []string{
				"n2-highmem series provides 8GB RAM per vCPU",
				"Consider n2d-highmem for AMD-based alternatives",
				"Use local SSD for temporary high-speed storage",
			},
		}
	}

	// CPU-intensive workloads
	if containsAny(workloadLower, []string{"cpu", "compute", "processing", "calculation"}) {
		return GKENodePoolRecommendation{
			MachineType: "c2-standard-8",
			DiskType:    "pd-ssd",
			DiskSizeGB:  100,
			Reason:      "Compute-optimized machine types for CPU-intensive workloads",
			Notes: []string{
				"c2 series provides highest per-core performance",
				"Consider c2d for AMD EPYC alternatives",
				"Use compact placement policy for low-latency communication",
			},
		}
	}

	// General purpose
	return GKENodePoolRecommendation{
		MachineType: "e2-standard-4",
		DiskType:    "pd-balanced",
		DiskSizeGB:  100,
		Reason:      "E2 machine types provide good balance of cost and performance",
		Notes: []string{
			"e2-standard provides 4GB RAM per vCPU",
			"Cost-effective for general workloads",
			"Consider n2-standard for consistent performance",
		},
	}
}

// GKEPodSpecForPool generates pod spec YAML snippet for targeting a node pool
func GKEPodSpecForPool(poolName string, preemptible bool) string {
	var sb strings.Builder

	sb.WriteString("spec:\n")
	sb.WriteString("  nodeSelector:\n")
	sb.WriteString(fmt.Sprintf("    %s: %s\n", GKELabelNodePool, poolName))

	if preemptible {
		sb.WriteString(fmt.Sprintf("    %s: \"true\"\n", GKELabelPreemptible))
		sb.WriteString("  tolerations:\n")
		sb.WriteString(fmt.Sprintf("  - key: %s\n", GKETaintPreemptible))
		sb.WriteString("    operator: Equal\n")
		sb.WriteString("    value: \"true\"\n")
		sb.WriteString("    effect: NoSchedule\n")
	}

	return sb.String()
}

// GKEPodAntiAffinityForHA generates pod anti-affinity YAML for high availability
func GKEPodAntiAffinityForHA(appLabel string) string {
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

// IsGKENodeLabel checks if a label key is a GKE-specific node label
func IsGKENodeLabel(key string) bool {
	gkeNodeLabelPrefixes := []string{
		"cloud.google.com/",
		"node.kubernetes.io/",
		"topology.gke.io/",
	}

	for _, prefix := range gkeNodeLabelPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}

// IsEKSNodeLabel checks if a label key is an EKS-specific node label
func IsEKSNodeLabel(key string) bool {
	eksNodeLabelPrefixes := []string{
		"eks.amazonaws.com/",
		"node.kubernetes.io/",
		"topology.kubernetes.io/",
	}

	for _, prefix := range eksNodeLabelPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	// Also check for specific EKS labels
	if key == EKSLabelNodeGroup || key == EKSLabelCapacityType {
		return true
	}

	return false
}

// GKEWorkloadNotes returns important notes about GKE workload management
func GKEWorkloadNotes() []string {
	return []string{
		"GKE Standard mode allows full control over node pools and configurations",
		"GKE Autopilot mode automatically provisions nodes based on pod requirements",
		"Use node pools to isolate workloads with different requirements",
		"Preemptible VMs provide up to 80% cost savings for fault-tolerant workloads",
		"Spot VMs provide up to 91% discount but may be preempted at any time",
		"GPU nodes require specific node selectors and tolerations",
		"Use pod disruption budgets to maintain availability during node upgrades",
	}
}

// containsAny checks if the string contains any of the patterns
func containsAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
