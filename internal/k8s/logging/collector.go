package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// LogCollector handles log collection from various sources
type LogCollector struct {
	client K8sClient
	debug  bool
}

// NewLogCollector creates a new log collector
func NewLogCollector(client K8sClient, debug bool) *LogCollector {
	return &LogCollector{
		client: client,
		debug:  debug,
	}
}

// CollectPodLogs collects logs from a specific pod
func (c *LogCollector) CollectPodLogs(ctx context.Context, podName string, opts QueryOptions) (*AggregatedLogs, error) {
	namespace := opts.Namespace
	if namespace == "" {
		namespace = "default"
	}

	if podName == "" {
		return nil, fmt.Errorf("pod name is required")
	}

	args := []string{"logs", podName}

	if opts.Container != "" {
		args = append(args, "-c", opts.Container)
	}
	if opts.TailLines > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", opts.TailLines))
	}
	if opts.Since != "" {
		args = append(args, "--since", opts.Since)
	}
	if opts.Previous {
		args = append(args, "-p")
	}
	if opts.Timestamps {
		args = append(args, "--timestamps")
	}

	output, err := c.client.RunWithNamespace(ctx, namespace, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get logs for pod %s: %w", podName, err)
	}

	entries := c.parseLogOutput(output, podName, namespace, "")
	return c.buildAggregatedLogs(entries, ScopePod, 1, output)
}

// CollectDeploymentLogs collects logs from all pods of a deployment
func (c *LogCollector) CollectDeploymentLogs(ctx context.Context, deploymentName string, opts QueryOptions) (*AggregatedLogs, error) {
	namespace := opts.Namespace
	if namespace == "" {
		namespace = "default"
	}

	if deploymentName == "" {
		return nil, fmt.Errorf("deployment name is required")
	}

	// Get pods for this deployment using multiple label patterns
	pods, err := c.getPodsForDeployment(ctx, deploymentName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get pods for deployment %s: %w", deploymentName, err)
	}

	if len(pods) == 0 {
		return nil, fmt.Errorf("no pods found for deployment %s in namespace %s", deploymentName, namespace)
	}

	if c.debug {
		fmt.Printf("[collector] found %d pods for deployment %s\n", len(pods), deploymentName)
	}

	return c.collectLogsFromPods(ctx, pods, namespace, opts, fmt.Sprintf("deployment/%s", deploymentName))
}

// CollectNodeLogs collects logs from all pods on a specific node
func (c *LogCollector) CollectNodeLogs(ctx context.Context, nodeName string, opts QueryOptions) (*AggregatedLogs, error) {
	if nodeName == "" {
		return nil, fmt.Errorf("node name is required")
	}

	// Get pods on this node
	pods, err := c.getPodsOnNode(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get pods on node %s: %w", nodeName, err)
	}

	if len(pods) == 0 {
		return nil, fmt.Errorf("no pods found on node %s", nodeName)
	}

	if c.debug {
		fmt.Printf("[collector] found %d pods on node %s\n", len(pods), nodeName)
	}

	return c.collectLogsFromPods(ctx, pods, "", opts, fmt.Sprintf("node/%s", nodeName))
}

// CollectClusterLogs collects logs cluster-wide with optional filtering
func (c *LogCollector) CollectClusterLogs(ctx context.Context, opts QueryOptions) (*AggregatedLogs, error) {
	// Get all pods (optionally filtered)
	pods, err := c.getAllPods(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to get pods: %w", err)
	}

	if len(pods) == 0 {
		return nil, fmt.Errorf("no pods found in cluster")
	}

	if c.debug {
		fmt.Printf("[collector] found %d pods in cluster\n", len(pods))
	}

	// Limit pods to avoid overwhelming the system
	maxPods := 50
	if len(pods) > maxPods {
		if c.debug {
			fmt.Printf("[collector] limiting to %d pods\n", maxPods)
		}
		pods = pods[:maxPods]
	}

	return c.collectLogsFromPods(ctx, pods, "", opts, "cluster")
}

// CollectNamespaceLogs collects logs from all pods in a namespace
func (c *LogCollector) CollectNamespaceLogs(ctx context.Context, namespace string, opts QueryOptions) (*AggregatedLogs, error) {
	if namespace == "" {
		namespace = "default"
	}

	pods, err := c.getPodsInNamespace(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get pods in namespace %s: %w", namespace, err)
	}

	if len(pods) == 0 {
		return nil, fmt.Errorf("no pods found in namespace %s", namespace)
	}

	if c.debug {
		fmt.Printf("[collector] found %d pods in namespace %s\n", len(pods), namespace)
	}

	return c.collectLogsFromPods(ctx, pods, namespace, opts, fmt.Sprintf("namespace/%s", namespace))
}

// collectLogsFromPods collects logs from multiple pods in parallel
func (c *LogCollector) collectLogsFromPods(ctx context.Context, pods []PodInfo, namespace string, opts QueryOptions, source string) (*AggregatedLogs, error) {
	const maxConcurrency = 10
	sem := make(chan struct{}, maxConcurrency)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var allEntries []LogEntry
	var rawOutputs []string

	// Calculate lines per pod
	linesPerPod := opts.TailLines
	if linesPerPod == 0 {
		linesPerPod = 100
	}
	// Distribute lines across pods but ensure minimum per pod
	if len(pods) > 1 {
		linesPerPod = max(linesPerPod/len(pods), 20)
	}

	for _, pod := range pods {
		wg.Add(1)
		go func(p PodInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ns := p.Namespace
			if namespace != "" {
				ns = namespace
			}

			args := []string{"logs", p.Name}
			if linesPerPod > 0 {
				args = append(args, "--tail", fmt.Sprintf("%d", linesPerPod))
			}
			if opts.Since != "" {
				args = append(args, "--since", opts.Since)
			}
			if opts.Timestamps {
				args = append(args, "--timestamps")
			}

			output, err := c.client.RunWithNamespace(ctx, ns, args...)
			if err != nil {
				if c.debug {
					fmt.Printf("[collector] failed to get logs for pod %s: %v\n", p.Name, err)
				}
				return
			}

			entries := c.parseLogOutput(output, p.Name, ns, p.Node)

			mu.Lock()
			allEntries = append(allEntries, entries...)
			rawOutputs = append(rawOutputs, fmt.Sprintf("=== Pod: %s/%s ===\n%s", ns, p.Name, output))
			mu.Unlock()
		}(pod)
	}

	wg.Wait()

	// Sort by timestamp
	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].Timestamp.Before(allEntries[j].Timestamp)
	})

	rawOutput := strings.Join(rawOutputs, "\n\n")
	return c.buildAggregatedLogs(allEntries, ScopeCluster, len(pods), rawOutput)
}

// parseLogOutput parses raw log output into structured entries
func (c *LogCollector) parseLogOutput(output, podName, namespace, nodeName string) []LogEntry {
	var entries []LogEntry
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		entry := LogEntry{
			Timestamp: parseTimestamp(line),
			Pod:       podName,
			Namespace: namespace,
			Node:      nodeName,
			Message:   line,
			Level:     detectLogLevel(line),
		}

		if entry.Level == LevelError {
			entry.IsError = true
		}

		entries = append(entries, entry)
	}

	return entries
}

// buildAggregatedLogs builds an AggregatedLogs from entries
func (c *LogCollector) buildAggregatedLogs(entries []LogEntry, scope LogScope, podCount int, rawOutput string) (*AggregatedLogs, error) {
	errorCount := 0
	warnCount := 0
	var minTime, maxTime time.Time

	for i, entry := range entries {
		if entry.IsError {
			errorCount++
		}
		if entry.Level == LevelWarn {
			warnCount++
		}

		if i == 0 || entry.Timestamp.Before(minTime) {
			minTime = entry.Timestamp
		}
		if i == 0 || entry.Timestamp.After(maxTime) {
			maxTime = entry.Timestamp
		}
	}

	return &AggregatedLogs{
		Source:     string(scope),
		Scope:      scope,
		TotalLines: len(entries),
		PodCount:   podCount,
		TimeRange: TimeRange{
			Start: minTime,
			End:   maxTime,
		},
		Entries:    entries,
		ErrorCount: errorCount,
		WarnCount:  warnCount,
		RawOutput:  rawOutput,
	}, nil
}

// getPodsForDeployment returns pods belonging to a deployment
func (c *LogCollector) getPodsForDeployment(ctx context.Context, deploymentName, namespace string) ([]PodInfo, error) {
	// Try multiple label patterns commonly used for deployments
	labelPatterns := []string{
		fmt.Sprintf("app=%s", deploymentName),
		fmt.Sprintf("app.kubernetes.io/name=%s", deploymentName),
		fmt.Sprintf("app.kubernetes.io/instance=%s", deploymentName),
	}

	for _, label := range labelPatterns {
		output, err := c.client.RunWithNamespace(ctx, namespace, "get", "pods", "-l", label, "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\n\"}{end}")
		if err != nil {
			continue
		}

		pods := c.parsePodList(output, namespace)
		if len(pods) > 0 {
			return pods, nil
		}
	}

	// Fallback: try to get pods that match the deployment name prefix
	output, err := c.client.RunWithNamespace(ctx, namespace, "get", "pods", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\n\"}{end}")
	if err != nil {
		return nil, err
	}

	allPods := c.parsePodList(output, namespace)
	var matchingPods []PodInfo
	for _, pod := range allPods {
		if strings.HasPrefix(pod.Name, deploymentName+"-") {
			matchingPods = append(matchingPods, pod)
		}
	}

	return matchingPods, nil
}

// getPodsOnNode returns pods running on a specific node
func (c *LogCollector) getPodsOnNode(ctx context.Context, nodeName string) ([]PodInfo, error) {
	output, err := c.client.Run(ctx, "get", "pods", "-A", "--field-selector", fmt.Sprintf("spec.nodeName=%s", nodeName), "-o", "jsonpath={range .items[*]}{.metadata.namespace}{\"\\t\"}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\n\"}{end}")
	if err != nil {
		return nil, err
	}
	return c.parsePodListWithNamespace(output), nil
}

// getAllPods returns all pods in the cluster
func (c *LogCollector) getAllPods(ctx context.Context, opts QueryOptions) ([]PodInfo, error) {
	args := []string{"get", "pods", "-A", "-o", "jsonpath={range .items[*]}{.metadata.namespace}{\"\\t\"}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\n\"}{end}"}

	output, err := c.client.Run(ctx, args...)
	if err != nil {
		return nil, err
	}

	pods := c.parsePodListWithNamespace(output)

	// Filter out system namespaces unless explicitly requested
	if !opts.AllNamespaces {
		var filtered []PodInfo
		systemNamespaces := map[string]bool{
			"kube-system":        true,
			"kube-public":        true,
			"kube-node-lease":    true,
			"local-path-storage": true,
		}
		for _, pod := range pods {
			if !systemNamespaces[pod.Namespace] {
				filtered = append(filtered, pod)
			}
		}
		return filtered, nil
	}

	return pods, nil
}

// getPodsInNamespace returns all pods in a namespace
func (c *LogCollector) getPodsInNamespace(ctx context.Context, namespace string) ([]PodInfo, error) {
	output, err := c.client.RunWithNamespace(ctx, namespace, "get", "pods", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\n\"}{end}")
	if err != nil {
		return nil, err
	}
	return c.parsePodList(output, namespace), nil
}

// parsePodList parses pod output in format "name\tnodeName\n"
func (c *LogCollector) parsePodList(output, namespace string) []PodInfo {
	var pods []PodInfo
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 1 && parts[0] != "" {
			pod := PodInfo{
				Name:      parts[0],
				Namespace: namespace,
			}
			if len(parts) >= 2 {
				pod.Node = parts[1]
			}
			pods = append(pods, pod)
		}
	}

	return pods
}

// parsePodListWithNamespace parses pod output in format "namespace\tname\tnodeName\n"
func (c *LogCollector) parsePodListWithNamespace(output string) []PodInfo {
	var pods []PodInfo
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			pod := PodInfo{
				Namespace: parts[0],
				Name:      parts[1],
			}
			if len(parts) >= 3 {
				pod.Node = parts[2]
			}
			pods = append(pods, pod)
		}
	}

	return pods
}

// GetPodContainers returns the containers for a specific pod
func (c *LogCollector) GetPodContainers(ctx context.Context, podName, namespace string) ([]string, error) {
	output, err := c.client.RunJSON(ctx, "get", "pod", podName, "-n", namespace)
	if err != nil {
		return nil, err
	}

	var pod struct {
		Spec struct {
			Containers []struct {
				Name string `json:"name"`
			} `json:"containers"`
			InitContainers []struct {
				Name string `json:"name"`
			} `json:"initContainers"`
		} `json:"spec"`
	}

	if err := json.Unmarshal(output, &pod); err != nil {
		return nil, err
	}

	var containers []string
	for _, c := range pod.Spec.InitContainers {
		containers = append(containers, c.Name)
	}
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}

	return containers, nil
}
