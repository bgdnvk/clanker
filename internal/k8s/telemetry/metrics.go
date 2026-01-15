package telemetry

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MetricsManager handles metrics collection and parsing
type MetricsManager struct {
	client K8sClient
	debug  bool
}

// NewMetricsManager creates a new metrics manager
func NewMetricsManager(client K8sClient, debug bool) *MetricsManager {
	return &MetricsManager{
		client: client,
		debug:  debug,
	}
}

// CheckMetricsServerAvailable checks if metrics-server is available
func (m *MetricsManager) CheckMetricsServerAvailable(ctx context.Context) bool {
	_, err := m.client.Run(ctx, "top", "nodes", "--no-headers")
	return err == nil
}

// GetClusterMetrics returns cluster-wide metrics
func (m *MetricsManager) GetClusterMetrics(ctx context.Context) (*ClusterMetrics, error) {
	nodes, err := m.GetNodeMetrics(ctx)
	if err != nil {
		return nil, err
	}

	result := &ClusterMetrics{
		Timestamp:  time.Now(),
		Source:     SourceMetricsServer,
		Nodes:      nodes,
		NodeCount:  len(nodes),
		ReadyNodes: len(nodes), // Assume all nodes with metrics are ready
	}

	// Aggregate metrics
	var totalCPU, usedCPU int64     // millicores
	var totalMem, usedMem int64     // bytes
	var totalCPUPct, totalMemPct float64

	for _, node := range nodes {
		usedCPU += parseCPUToMillicores(node.CPUUsage)
		usedMem += parseMemoryToBytes(node.MemUsage)
		totalCPU += parseCPUToMillicores(node.Allocatable.CPU)
		totalMem += parseMemoryToBytes(node.Allocatable.Memory)
		totalCPUPct += node.CPUPercent
		totalMemPct += node.MemPercent
	}

	result.TotalCPU = formatMillicores(totalCPU)
	result.UsedCPU = formatMillicores(usedCPU)
	result.TotalMemory = formatBytes(totalMem)
	result.UsedMemory = formatBytes(usedMem)

	if totalCPU > 0 {
		result.CPUPercent = float64(usedCPU) / float64(totalCPU) * 100
	}
	if totalMem > 0 {
		result.MemoryPercent = float64(usedMem) / float64(totalMem) * 100
	}

	return result, nil
}

// GetNodeMetrics returns metrics for all nodes
func (m *MetricsManager) GetNodeMetrics(ctx context.Context) ([]NodeMetrics, error) {
	output, err := m.client.Run(ctx, "top", "nodes", "--no-headers")
	if err != nil {
		return nil, fmt.Errorf("failed to get node metrics: %w", err)
	}

	nodes := parseTopNodesOutput(output)

	// Get allocatable resources for each node
	nodeInfo, err := m.getNodeAllocatable(ctx)
	if err != nil {
		if m.debug {
			fmt.Printf("[telemetry] warning: could not get node allocatable: %v\n", err)
		}
	} else {
		for i := range nodes {
			if info, ok := nodeInfo[nodes[i].Name]; ok {
				nodes[i].Allocatable = info.Allocatable
				nodes[i].Capacity = info.Capacity
			}
		}
	}

	return nodes, nil
}

// GetNamespaceMetrics returns metrics for a namespace
func (m *MetricsManager) GetNamespaceMetrics(ctx context.Context, namespace string) (*NamespaceMetrics, error) {
	pods, err := m.GetAllPodMetrics(ctx, namespace, false)
	if err != nil {
		return nil, err
	}

	result := &NamespaceMetrics{
		Namespace: namespace,
		Source:    SourceMetricsServer,
		Pods:      pods,
		PodCount:  len(pods),
	}

	// Aggregate metrics
	var totalCPU, totalMem int64
	for _, pod := range pods {
		totalCPU += parseCPUToMillicores(pod.CPUUsage)
		totalMem += parseMemoryToBytes(pod.MemUsage)
	}

	result.TotalCPU = formatMillicores(totalCPU)
	result.TotalMemory = formatBytes(totalMem)

	return result, nil
}

// GetPodMetrics returns metrics for a specific pod
func (m *MetricsManager) GetPodMetrics(ctx context.Context, podName, namespace string) (*PodMetrics, error) {
	if namespace == "" {
		namespace = "default"
	}

	output, err := m.client.RunWithNamespace(ctx, namespace, "top", "pod", podName, "--no-headers")
	if err != nil {
		return nil, fmt.Errorf("failed to get pod metrics: %w", err)
	}

	pods := parseTopPodsOutput(output, namespace)
	if len(pods) == 0 {
		return nil, fmt.Errorf("pod %s not found in namespace %s", podName, namespace)
	}

	pod := &pods[0]

	// Get container-level metrics
	containers, err := m.GetContainerMetrics(ctx, podName, namespace)
	if err == nil {
		pod.Containers = containers
	}

	// Get resource requests/limits
	m.enrichPodWithResourceSpecs(ctx, pod, namespace)

	return pod, nil
}

// GetAllPodMetrics returns metrics for all pods in a namespace
func (m *MetricsManager) GetAllPodMetrics(ctx context.Context, namespace string, allNamespaces bool) ([]PodMetrics, error) {
	var output string
	var err error

	if allNamespaces {
		output, err = m.client.Run(ctx, "top", "pods", "--all-namespaces", "--no-headers")
	} else {
		if namespace == "" {
			namespace = "default"
		}
		output, err = m.client.RunWithNamespace(ctx, namespace, "top", "pods", "--no-headers")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get pod metrics: %w", err)
	}

	return parseTopPodsOutput(output, namespace), nil
}

// GetContainerMetrics returns metrics for containers in a pod
func (m *MetricsManager) GetContainerMetrics(ctx context.Context, podName, namespace string) ([]ContainerMetrics, error) {
	if namespace == "" {
		namespace = "default"
	}

	output, err := m.client.RunWithNamespace(ctx, namespace, "top", "pod", podName, "--containers", "--no-headers")
	if err != nil {
		return nil, fmt.Errorf("failed to get container metrics: %w", err)
	}

	return parseTopContainersOutput(output), nil
}

// getNodeAllocatable gets allocatable resources for all nodes
func (m *MetricsManager) getNodeAllocatable(ctx context.Context) (map[string]NodeMetrics, error) {
	output, err := m.client.RunJSON(ctx, "get", "nodes")
	if err != nil {
		return nil, err
	}

	// Parse the JSON output to get allocatable resources
	nodes := make(map[string]NodeMetrics)

	// Simple JSON parsing for node allocatable
	lines := strings.Split(string(output), "\n")
	var currentNode string
	var inAllocatable, inCapacity bool
	var allocCPU, allocMem, capCPU, capMem string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, `"name":`) && strings.Contains(line, `"metadata"`) == false {
			// Extract node name
			parts := strings.Split(line, `"`)
			if len(parts) >= 4 {
				currentNode = parts[3]
			}
		}
		if strings.Contains(line, `"allocatable"`) {
			inAllocatable = true
			inCapacity = false
		}
		if strings.Contains(line, `"capacity"`) {
			inCapacity = true
			inAllocatable = false
		}
		if inAllocatable {
			if strings.Contains(line, `"cpu"`) {
				parts := strings.Split(line, `"`)
				if len(parts) >= 4 {
					allocCPU = parts[3]
				}
			}
			if strings.Contains(line, `"memory"`) {
				parts := strings.Split(line, `"`)
				if len(parts) >= 4 {
					allocMem = parts[3]
				}
			}
		}
		if inCapacity {
			if strings.Contains(line, `"cpu"`) {
				parts := strings.Split(line, `"`)
				if len(parts) >= 4 {
					capCPU = parts[3]
				}
			}
			if strings.Contains(line, `"memory"`) {
				parts := strings.Split(line, `"`)
				if len(parts) >= 4 {
					capMem = parts[3]
				}
			}
		}
		if currentNode != "" && allocCPU != "" && allocMem != "" {
			nodes[currentNode] = NodeMetrics{
				Name: currentNode,
				Allocatable: ResourceUsage{
					CPU:    allocCPU,
					Memory: allocMem,
				},
				Capacity: ResourceUsage{
					CPU:    capCPU,
					Memory: capMem,
				},
			}
			if capCPU != "" && capMem != "" {
				currentNode = ""
				allocCPU, allocMem, capCPU, capMem = "", "", "", ""
				inAllocatable, inCapacity = false, false
			}
		}
	}

	return nodes, nil
}

// enrichPodWithResourceSpecs adds resource requests/limits to pod metrics
func (m *MetricsManager) enrichPodWithResourceSpecs(ctx context.Context, pod *PodMetrics, namespace string) {
	output, err := m.client.GetJSON(ctx, "pod", pod.Name, namespace)
	if err != nil {
		return
	}

	// Simple JSON parsing for resource specs
	content := string(output)

	// Look for requests and limits in the JSON
	if idx := strings.Index(content, `"requests"`); idx != -1 {
		section := content[idx:min(idx+200, len(content))]
		if cpuIdx := strings.Index(section, `"cpu"`); cpuIdx != -1 {
			parts := strings.Split(section[cpuIdx:], `"`)
			if len(parts) >= 4 {
				pod.CPURequest = parts[3]
			}
		}
		if memIdx := strings.Index(section, `"memory"`); memIdx != -1 {
			parts := strings.Split(section[memIdx:], `"`)
			if len(parts) >= 4 {
				pod.MemRequest = parts[3]
			}
		}
	}

	if idx := strings.Index(content, `"limits"`); idx != -1 {
		section := content[idx:min(idx+200, len(content))]
		if cpuIdx := strings.Index(section, `"cpu"`); cpuIdx != -1 {
			parts := strings.Split(section[cpuIdx:], `"`)
			if len(parts) >= 4 {
				pod.CPULimit = parts[3]
			}
		}
		if memIdx := strings.Index(section, `"memory"`); memIdx != -1 {
			parts := strings.Split(section[memIdx:], `"`)
			if len(parts) >= 4 {
				pod.MemLimit = parts[3]
			}
		}
	}
}

// parseTopNodesOutput parses kubectl top nodes output
func parseTopNodesOutput(output string) []NodeMetrics {
	var nodes []NodeMetrics
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		// Format: NAME CPU(cores) CPU% MEMORY(bytes) MEMORY%
		node := NodeMetrics{
			Name:     fields[0],
			CPUUsage: fields[1],
			MemUsage: fields[3],
		}

		// Parse CPU percentage
		cpuPct := strings.TrimSuffix(fields[2], "%")
		if pct, err := strconv.ParseFloat(cpuPct, 64); err == nil {
			node.CPUPercent = pct
		}

		// Parse memory percentage
		memPct := strings.TrimSuffix(fields[4], "%")
		if pct, err := strconv.ParseFloat(memPct, 64); err == nil {
			node.MemPercent = pct
		}

		nodes = append(nodes, node)
	}

	return nodes
}

// parseTopPodsOutput parses kubectl top pods output
func parseTopPodsOutput(output string, defaultNamespace string) []PodMetrics {
	var pods []PodMetrics
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)

		var pod PodMetrics

		// Check if output includes namespace (--all-namespaces)
		if len(fields) >= 4 && !strings.HasSuffix(fields[1], "m") && !strings.HasSuffix(fields[1], "Mi") && !strings.HasSuffix(fields[1], "Gi") {
			// Format: NAMESPACE NAME CPU MEMORY
			pod = PodMetrics{
				Namespace: fields[0],
				Name:      fields[1],
				CPUUsage:  fields[2],
				MemUsage:  fields[3],
			}
		} else if len(fields) >= 3 {
			// Format: NAME CPU MEMORY
			pod = PodMetrics{
				Namespace: defaultNamespace,
				Name:      fields[0],
				CPUUsage:  fields[1],
				MemUsage:  fields[2],
			}
		} else {
			continue
		}

		pods = append(pods, pod)
	}

	return pods
}

// parseTopContainersOutput parses kubectl top pods --containers output
func parseTopContainersOutput(output string) []ContainerMetrics {
	var containers []ContainerMetrics
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)

		// Format with namespace: NAMESPACE POD CONTAINER CPU MEMORY
		// Format without namespace: POD CONTAINER CPU MEMORY
		var container ContainerMetrics

		if len(fields) >= 5 {
			// With namespace
			container = ContainerMetrics{
				Name:     fields[2],
				CPUUsage: fields[3],
				MemUsage: fields[4],
			}
		} else if len(fields) >= 4 {
			// Without namespace
			container = ContainerMetrics{
				Name:     fields[1],
				CPUUsage: fields[2],
				MemUsage: fields[3],
			}
		} else {
			continue
		}

		containers = append(containers, container)
	}

	return containers
}

// parseCPUToMillicores converts CPU string to millicores
func parseCPUToMillicores(cpu string) int64 {
	cpu = strings.TrimSpace(cpu)
	if cpu == "" || cpu == "<unknown>" {
		return 0
	}

	if strings.HasSuffix(cpu, "m") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(cpu, "m"), 10, 64)
		return val
	}

	// Assume cores
	val, _ := strconv.ParseFloat(cpu, 64)
	return int64(val * 1000)
}

// parseMemoryToBytes converts memory string to bytes
func parseMemoryToBytes(mem string) int64 {
	mem = strings.TrimSpace(mem)
	if mem == "" || mem == "<unknown>" {
		return 0
	}

	multipliers := map[string]int64{
		"Ki": 1024,
		"Mi": 1024 * 1024,
		"Gi": 1024 * 1024 * 1024,
		"Ti": 1024 * 1024 * 1024 * 1024,
		"K":  1000,
		"M":  1000 * 1000,
		"G":  1000 * 1000 * 1000,
		"T":  1000 * 1000 * 1000 * 1000,
	}

	for suffix, mult := range multipliers {
		if strings.HasSuffix(mem, suffix) {
			val, _ := strconv.ParseInt(strings.TrimSuffix(mem, suffix), 10, 64)
			return val * mult
		}
	}

	// Try parsing as bytes
	val, _ := strconv.ParseInt(mem, 10, 64)
	return val
}

// formatMillicores formats millicores to a readable string
func formatMillicores(m int64) string {
	if m >= 1000 {
		return fmt.Sprintf("%.1f", float64(m)/1000)
	}
	return fmt.Sprintf("%dm", m)
}

// formatBytes formats bytes to a readable string
func formatBytes(b int64) string {
	const (
		Ki = 1024
		Mi = Ki * 1024
		Gi = Mi * 1024
		Ti = Gi * 1024
	)

	switch {
	case b >= Ti:
		return fmt.Sprintf("%.1fTi", float64(b)/float64(Ti))
	case b >= Gi:
		return fmt.Sprintf("%.1fGi", float64(b)/float64(Gi))
	case b >= Mi:
		return fmt.Sprintf("%.1fMi", float64(b)/float64(Mi))
	case b >= Ki:
		return fmt.Sprintf("%.1fKi", float64(b)/float64(Ki))
	default:
		return fmt.Sprintf("%d", b)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
