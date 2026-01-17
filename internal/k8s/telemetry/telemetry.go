package telemetry

import (
	"context"
	"fmt"
	"strings"
)

// SubAgent handles telemetry and metrics queries
type SubAgent struct {
	client  K8sClient
	metrics *MetricsManager
	debug   bool
}

// NewSubAgent creates a new telemetry sub-agent
func NewSubAgent(client K8sClient, debug bool) *SubAgent {
	return &SubAgent{
		client:  client,
		metrics: NewMetricsManager(client, debug),
		debug:   debug,
	}
}

// HandleQuery processes a telemetry query and returns the result
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[telemetry] handling query: %s\n", query)
	}

	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[telemetry] analysis: scope=%s, target=%s\n", analysis.Scope, analysis.Target)
	}

	// Apply options from analysis if not already set
	if opts.Scope == "" {
		opts.Scope = analysis.Scope
	}
	if opts.NodeName == "" {
		opts.NodeName = analysis.Target
	}
	if opts.PodName == "" && analysis.Scope == ScopePod {
		opts.PodName = analysis.Target
	}
	if opts.Namespace == "" {
		opts.Namespace = analysis.Namespace
	}

	// Route to appropriate handler based on scope
	switch opts.Scope {
	case ScopeCluster:
		return s.handleClusterMetrics(ctx, opts)
	case ScopeNode:
		return s.handleNodeMetrics(ctx, opts)
	case ScopeNamespace:
		return s.handleNamespaceMetrics(ctx, opts)
	case ScopePod:
		return s.handlePodMetrics(ctx, opts)
	case ScopeContainer:
		return s.handleContainerMetrics(ctx, opts)
	default:
		// Default to cluster-wide metrics
		return s.handleClusterMetrics(ctx, opts)
	}
}

// queryAnalysis contains parsed query information
type queryAnalysis struct {
	Scope     MetricsScope
	Target    string // node name, pod name, etc.
	Namespace string
	SortBy    string
}

// analyzeQuery parses the query to determine scope and targets
func (s *SubAgent) analyzeQuery(query string) queryAnalysis {
	q := strings.ToLower(query)
	analysis := queryAnalysis{
		Scope: ScopeCluster, // default
	}

	// Detect scope from keywords
	if containsAny(q, []string{"node", "nodes"}) {
		analysis.Scope = ScopeNode
	} else if containsAny(q, []string{"container", "containers"}) {
		analysis.Scope = ScopeContainer
	} else if containsAny(q, []string{"pod", "pods"}) {
		analysis.Scope = ScopePod
	} else if containsAny(q, []string{"namespace"}) {
		analysis.Scope = ScopeNamespace
	}

	// Extract namespace if specified
	analysis.Namespace = extractNamespace(q)

	// Extract specific resource name
	analysis.Target = extractResourceName(q, analysis.Scope)

	// Detect sort preference
	if strings.Contains(q, "cpu") && containsAny(q, []string{"most", "top", "highest"}) {
		analysis.SortBy = "cpu"
	} else if strings.Contains(q, "memory") && containsAny(q, []string{"most", "top", "highest"}) {
		analysis.SortBy = "memory"
	}

	return analysis
}

// handleClusterMetrics returns cluster-wide metrics
func (s *SubAgent) handleClusterMetrics(ctx context.Context, opts QueryOptions) (*Response, error) {
	result, err := s.metrics.GetClusterMetrics(ctx)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to get cluster metrics: %v", err),
			Error:   err,
		}, nil
	}

	return &Response{
		Type: ResponseTypeResult,
		Data: result,
		Message: fmt.Sprintf("Cluster metrics: %d nodes, CPU %s/%s (%.1f%%), Memory %s/%s (%.1f%%)",
			result.NodeCount, result.UsedCPU, result.TotalCPU, result.CPUPercent,
			result.UsedMemory, result.TotalMemory, result.MemoryPercent),
	}, nil
}

// handleNodeMetrics returns node metrics
func (s *SubAgent) handleNodeMetrics(ctx context.Context, opts QueryOptions) (*Response, error) {
	nodes, err := s.metrics.GetNodeMetrics(ctx)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to get node metrics: %v", err),
			Error:   err,
		}, nil
	}

	// Filter by specific node if requested
	if opts.NodeName != "" {
		for _, node := range nodes {
			if node.Name == opts.NodeName || strings.Contains(node.Name, opts.NodeName) {
				return &Response{
					Type: ResponseTypeResult,
					Data: node,
					Message: fmt.Sprintf("Node %s: CPU %s (%.1f%%), Memory %s (%.1f%%)",
						node.Name, node.CPUUsage, node.CPUPercent, node.MemUsage, node.MemPercent),
				}, nil
			}
		}
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Node '%s' not found", opts.NodeName),
		}, nil
	}

	// Sort if requested
	if opts.SortBy != "" {
		sortNodeMetrics(nodes, opts.SortBy)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Data:    nodes,
		Message: fmt.Sprintf("Found %d nodes", len(nodes)),
	}, nil
}

// handleNamespaceMetrics returns namespace metrics
func (s *SubAgent) handleNamespaceMetrics(ctx context.Context, opts QueryOptions) (*Response, error) {
	namespace := opts.Namespace
	if namespace == "" {
		namespace = "default"
	}

	result, err := s.metrics.GetNamespaceMetrics(ctx, namespace)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to get namespace metrics: %v", err),
			Error:   err,
		}, nil
	}

	return &Response{
		Type: ResponseTypeResult,
		Data: result,
		Message: fmt.Sprintf("Namespace %s: %d pods, CPU %s, Memory %s",
			result.Namespace, result.PodCount, result.TotalCPU, result.TotalMemory),
	}, nil
}

// handlePodMetrics returns pod metrics
func (s *SubAgent) handlePodMetrics(ctx context.Context, opts QueryOptions) (*Response, error) {
	if opts.PodName != "" {
		// Get specific pod metrics
		pod, err := s.metrics.GetPodMetrics(ctx, opts.PodName, opts.Namespace)
		if err != nil {
			return &Response{
				Type:    ResponseTypeError,
				Message: fmt.Sprintf("Failed to get pod metrics: %v", err),
				Error:   err,
			}, nil
		}

		return &Response{
			Type: ResponseTypeResult,
			Data: pod,
			Message: fmt.Sprintf("Pod %s/%s: CPU %s, Memory %s",
				pod.Namespace, pod.Name, pod.CPUUsage, pod.MemUsage),
		}, nil
	}

	// Get all pods in namespace
	namespace := opts.Namespace
	allNamespaces := opts.AllNamespaces
	if namespace == "" && !allNamespaces {
		namespace = "default"
	}

	pods, err := s.metrics.GetAllPodMetrics(ctx, namespace, allNamespaces)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to get pod metrics: %v", err),
			Error:   err,
		}, nil
	}

	// Sort if requested
	if opts.SortBy != "" {
		sortPodMetrics(pods, opts.SortBy)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Data:    pods,
		Message: fmt.Sprintf("Found %d pods", len(pods)),
	}, nil
}

// handleContainerMetrics returns container-level metrics
func (s *SubAgent) handleContainerMetrics(ctx context.Context, opts QueryOptions) (*Response, error) {
	if opts.PodName == "" {
		return &Response{
			Type:    ResponseTypeError,
			Message: "Pod name is required for container metrics",
		}, nil
	}

	containers, err := s.metrics.GetContainerMetrics(ctx, opts.PodName, opts.Namespace)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to get container metrics: %v", err),
			Error:   err,
		}, nil
	}

	// Filter by specific container if requested
	if opts.ContainerName != "" {
		for _, c := range containers {
			if c.Name == opts.ContainerName {
				return &Response{
					Type:    ResponseTypeResult,
					Data:    c,
					Message: fmt.Sprintf("Container %s: CPU %s, Memory %s", c.Name, c.CPUUsage, c.MemUsage),
				}, nil
			}
		}
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Container '%s' not found in pod '%s'", opts.ContainerName, opts.PodName),
		}, nil
	}

	return &Response{
		Type:    ResponseTypeResult,
		Data:    containers,
		Message: fmt.Sprintf("Found %d containers in pod %s", len(containers), opts.PodName),
	}, nil
}

// CheckMetricsServerAvailable checks if metrics-server is available
func (s *SubAgent) CheckMetricsServerAvailable(ctx context.Context) bool {
	return s.metrics.CheckMetricsServerAvailable(ctx)
}

// Helper functions

func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func extractNamespace(query string) string {
	patterns := []string{
		"namespace ", "ns ", "-n ", "in namespace ", "in ns ",
	}
	for _, pattern := range patterns {
		if idx := strings.Index(query, pattern); idx != -1 {
			rest := query[idx+len(pattern):]
			words := strings.Fields(rest)
			if len(words) > 0 {
				return words[0]
			}
		}
	}
	return ""
}

func extractResourceName(query string, scope MetricsScope) string {
	var patterns []string
	switch scope {
	case ScopeNode:
		patterns = []string{"node ", "for node "}
	case ScopePod:
		patterns = []string{"pod ", "for pod "}
	case ScopeContainer:
		patterns = []string{"container ", "for container "}
	default:
		return ""
	}

	for _, pattern := range patterns {
		if idx := strings.Index(query, pattern); idx != -1 {
			rest := query[idx+len(pattern):]
			words := strings.Fields(rest)
			if len(words) > 0 {
				// Clean up common suffixes
				name := words[0]
				name = strings.TrimSuffix(name, ",")
				name = strings.TrimSuffix(name, "?")
				return name
			}
		}
	}
	return ""
}

func sortNodeMetrics(nodes []NodeMetrics, sortBy string) {
	// Simple bubble sort for small lists
	for i := 0; i < len(nodes)-1; i++ {
		for j := 0; j < len(nodes)-i-1; j++ {
			swap := false
			if sortBy == "cpu" {
				swap = nodes[j].CPUPercent < nodes[j+1].CPUPercent
			} else if sortBy == "memory" {
				swap = nodes[j].MemPercent < nodes[j+1].MemPercent
			}
			if swap {
				nodes[j], nodes[j+1] = nodes[j+1], nodes[j]
			}
		}
	}
}

func sortPodMetrics(pods []PodMetrics, sortBy string) {
	// Simple bubble sort - descending order
	for i := 0; i < len(pods)-1; i++ {
		for j := 0; j < len(pods)-i-1; j++ {
			swap := false
			if sortBy == "cpu" {
				// Parse CPU values for comparison
				cpu1 := parseCPUToMillicores(pods[j].CPUUsage)
				cpu2 := parseCPUToMillicores(pods[j+1].CPUUsage)
				swap = cpu1 < cpu2
			} else if sortBy == "memory" {
				mem1 := parseMemoryToBytes(pods[j].MemUsage)
				mem2 := parseMemoryToBytes(pods[j+1].MemUsage)
				swap = mem1 < mem2
			}
			if swap {
				pods[j], pods[j+1] = pods[j+1], pods[j]
			}
		}
	}
}
