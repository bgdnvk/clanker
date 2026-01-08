package sre

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DiagnosticsManager handles diagnostic operations
type DiagnosticsManager struct {
	client K8sClient
	debug  bool
}

// NewDiagnosticsManager creates a new diagnostics manager
func NewDiagnosticsManager(client K8sClient, debug bool) *DiagnosticsManager {
	return &DiagnosticsManager{
		client: client,
		debug:  debug,
	}
}

// DiagnoseCluster performs cluster-wide diagnostics
func (d *DiagnosticsManager) DiagnoseCluster(ctx context.Context) (*DiagnosticReport, error) {
	report := &DiagnosticReport{
		GeneratedAt: time.Now(),
		Scope:       "cluster",
		Issues:      []Issue{},
		Events:      []EventInfo{},
	}

	// Detect cluster-wide issues
	issues, err := d.DetectClusterIssues(ctx)
	if err != nil {
		return nil, err
	}
	report.Issues = issues

	// Get recent warning events
	events, err := d.GetEvents(ctx, "", "")
	if err == nil {
		// Filter to warning events only
		for _, event := range events {
			if event.Type == "Warning" {
				report.Events = append(report.Events, event)
			}
		}
	}

	// Generate summary
	criticalCount := 0
	warningCount := 0
	for _, issue := range issues {
		switch issue.Severity {
		case SeverityCritical:
			criticalCount++
		case SeverityWarning:
			warningCount++
		}
	}

	if criticalCount > 0 {
		report.Summary = fmt.Sprintf("Cluster has %d critical issues and %d warnings", criticalCount, warningCount)
	} else if warningCount > 0 {
		report.Summary = fmt.Sprintf("Cluster has %d warnings", warningCount)
	} else {
		report.Summary = "Cluster appears healthy"
	}

	return report, nil
}

// DiagnoseNamespace performs diagnostics on a specific namespace
func (d *DiagnosticsManager) DiagnoseNamespace(ctx context.Context, namespace string) (*DiagnosticReport, error) {
	report := &DiagnosticReport{
		GeneratedAt: time.Now(),
		Scope:       "namespace",
		Namespace:   namespace,
		Issues:      []Issue{},
		Events:      []EventInfo{},
	}

	// Detect issues in namespace
	issues, err := d.DetectIssuesInNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	report.Issues = issues

	// Get events for namespace
	events, err := d.GetEvents(ctx, namespace, "")
	if err == nil {
		report.Events = events
	}

	// Generate summary
	criticalCount := 0
	warningCount := 0
	for _, issue := range issues {
		switch issue.Severity {
		case SeverityCritical:
			criticalCount++
		case SeverityWarning:
			warningCount++
		}
	}

	if criticalCount > 0 {
		report.Summary = fmt.Sprintf("Namespace %s has %d critical issues and %d warnings", namespace, criticalCount, warningCount)
	} else if warningCount > 0 {
		report.Summary = fmt.Sprintf("Namespace %s has %d warnings", namespace, warningCount)
	} else {
		report.Summary = fmt.Sprintf("Namespace %s appears healthy", namespace)
	}

	return report, nil
}

// DiagnoseResource performs diagnostics on a specific resource
func (d *DiagnosticsManager) DiagnoseResource(ctx context.Context, resourceType, name, namespace string) (*DiagnosticReport, error) {
	report := &DiagnosticReport{
		GeneratedAt:  time.Now(),
		Scope:        "resource",
		ResourceType: resourceType,
		ResourceName: name,
		Namespace:    namespace,
		Issues:       []Issue{},
		Events:       []EventInfo{},
	}

	if namespace == "" {
		namespace = "default"
	}

	switch resourceType {
	case "pod":
		return d.diagnosePod(ctx, name, namespace, report)
	case "deployment":
		return d.diagnoseDeployment(ctx, name, namespace, report)
	case "node":
		return d.diagnoseNode(ctx, name, report)
	default:
		// Generic resource diagnosis
		return d.diagnoseGenericResource(ctx, resourceType, name, namespace, report)
	}
}

// diagnosePod performs diagnostics on a specific pod
func (d *DiagnosticsManager) diagnosePod(ctx context.Context, name, namespace string, report *DiagnosticReport) (*DiagnosticReport, error) {
	// Get pod details
	output, err := d.client.RunJSON(ctx, "get", "pod", name, "-n", namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}

	podStatus, err := d.parsePodJSON(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse pod status: %w", err)
	}

	// Detect issues based on pod status
	issues := d.detectPodIssues(podStatus)
	report.Issues = issues

	// Get pod events
	events, err := d.GetEvents(ctx, namespace, name)
	if err == nil {
		report.Events = events
	}

	// Get pod logs if there are crash issues
	for _, issue := range issues {
		if issue.Category == CategoryCrash {
			logs, err := d.GetLogsWithAnalysis(ctx, name, namespace, 50, "")
			if err == nil {
				report.Logs = logs
			}
			break
		}
	}

	// Generate summary
	if len(issues) == 0 {
		report.Summary = fmt.Sprintf("Pod %s/%s appears healthy (phase: %s)", namespace, name, podStatus.Phase)
	} else {
		report.Summary = fmt.Sprintf("Pod %s/%s has %d issues (phase: %s)", namespace, name, len(issues), podStatus.Phase)
	}

	return report, nil
}

// diagnoseDeployment performs diagnostics on a specific deployment
func (d *DiagnosticsManager) diagnoseDeployment(ctx context.Context, name, namespace string, report *DiagnosticReport) (*DiagnosticReport, error) {
	// Get deployment details
	output, err := d.client.RunJSON(ctx, "get", "deployment", name, "-n", namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment: %w", err)
	}

	deployStatus, err := d.parseDeploymentJSON(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse deployment status: %w", err)
	}

	// Detect issues based on deployment status
	issues := d.detectDeploymentIssues(deployStatus)
	report.Issues = issues

	// Get deployment events
	events, err := d.GetEvents(ctx, namespace, name)
	if err == nil {
		report.Events = events
	}

	// If replicas are unavailable, check pods
	if deployStatus.UnavailableReplicas > 0 {
		// Get pods for this deployment
		selector := fmt.Sprintf("app=%s", name)
		podOutput, err := d.client.RunJSON(ctx, "get", "pods", "-n", namespace, "-l", selector)
		if err == nil {
			podIssues := d.detectPodIssuesFromList(podOutput)
			report.Issues = append(report.Issues, podIssues...)
		}
	}

	// Generate summary
	if len(issues) == 0 {
		report.Summary = fmt.Sprintf("Deployment %s/%s is healthy (%d/%d replicas ready)",
			namespace, name, deployStatus.ReadyReplicas, deployStatus.Replicas)
	} else {
		report.Summary = fmt.Sprintf("Deployment %s/%s has %d issues (%d/%d replicas ready)",
			namespace, name, len(issues), deployStatus.ReadyReplicas, deployStatus.Replicas)
	}

	return report, nil
}

// diagnoseNode performs diagnostics on a specific node
func (d *DiagnosticsManager) diagnoseNode(ctx context.Context, name string, report *DiagnosticReport) (*DiagnosticReport, error) {
	// Get node details
	output, err := d.client.RunJSON(ctx, "get", "node", name)
	if err != nil {
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	nodeStatus, err := d.parseNodeJSON(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse node status: %w", err)
	}

	// Detect issues based on node status
	issues := d.detectNodeIssues(nodeStatus)
	report.Issues = issues

	// Get node events
	events, err := d.GetEvents(ctx, "", name)
	if err == nil {
		report.Events = events
	}

	// Generate summary
	if len(issues) == 0 {
		report.Summary = fmt.Sprintf("Node %s is healthy", name)
	} else {
		report.Summary = fmt.Sprintf("Node %s has %d issues", name, len(issues))
	}

	return report, nil
}

// diagnoseGenericResource performs generic resource diagnostics
func (d *DiagnosticsManager) diagnoseGenericResource(ctx context.Context, resourceType, name, namespace string, report *DiagnosticReport) (*DiagnosticReport, error) {
	// Get resource description
	args := []string{"describe", resourceType, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}

	output, err := d.client.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to describe resource: %w", err)
	}

	// Parse output for common issues
	issues := d.detectIssuesFromDescription(output, resourceType, name, namespace)
	report.Issues = issues

	// Get events for resource
	events, err := d.GetEvents(ctx, namespace, name)
	if err == nil {
		report.Events = events
	}

	if len(issues) == 0 {
		report.Summary = fmt.Sprintf("%s %s appears healthy", resourceType, name)
	} else {
		report.Summary = fmt.Sprintf("%s %s has %d issues", resourceType, name, len(issues))
	}

	return report, nil
}

// DetectClusterIssues detects issues across the entire cluster
func (d *DiagnosticsManager) DetectClusterIssues(ctx context.Context) ([]Issue, error) {
	var allIssues []Issue

	// Check node issues
	nodeIssues, err := d.detectAllNodeIssues(ctx)
	if err == nil {
		allIssues = append(allIssues, nodeIssues...)
	}

	// Check pod issues across all namespaces
	podIssues, err := d.detectAllPodIssues(ctx)
	if err == nil {
		allIssues = append(allIssues, podIssues...)
	}

	return allIssues, nil
}

// DetectIssuesInNamespace detects issues within a specific namespace
func (d *DiagnosticsManager) DetectIssuesInNamespace(ctx context.Context, namespace string) ([]Issue, error) {
	var allIssues []Issue

	// Get all pods in namespace
	output, err := d.client.RunJSON(ctx, "get", "pods", "-n", namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get pods: %w", err)
	}

	podIssues := d.detectPodIssuesFromList(output)
	allIssues = append(allIssues, podIssues...)

	// Check deployments
	deployOutput, err := d.client.RunJSON(ctx, "get", "deployments", "-n", namespace)
	if err == nil {
		deployIssues := d.detectDeploymentIssuesFromList(deployOutput, namespace)
		allIssues = append(allIssues, deployIssues...)
	}

	return allIssues, nil
}

// GetEvents retrieves events for a resource or namespace
func (d *DiagnosticsManager) GetEvents(ctx context.Context, namespace, resourceName string) ([]EventInfo, error) {
	args := []string{"get", "events", "--sort-by=.lastTimestamp", "-o", "json"}

	if namespace != "" {
		args = append(args, "-n", namespace)
	} else {
		args = append(args, "-A")
	}

	if resourceName != "" {
		args = append(args, "--field-selector", fmt.Sprintf("involvedObject.name=%s", resourceName))
	}

	output, err := d.client.Run(ctx, args...)
	if err != nil {
		return nil, err
	}

	return d.parseEventsJSON([]byte(output))
}

// GetLogsWithAnalysis retrieves logs and analyzes them for errors
func (d *DiagnosticsManager) GetLogsWithAnalysis(ctx context.Context, podName, namespace string, tailLines int, since string) ([]LogEntry, error) {
	args := []string{"logs", podName, "-n", namespace}

	if tailLines > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", tailLines))
	}
	if since != "" {
		args = append(args, "--since", since)
	}

	output, err := d.client.Run(ctx, args...)
	if err != nil {
		return nil, err
	}

	return d.parseAndAnalyzeLogs(output, podName), nil
}

// parsePodJSON parses pod JSON output
func (d *DiagnosticsManager) parsePodJSON(data []byte) (*PodStatus, error) {
	var pod struct {
		Metadata struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			NodeName string `json:"nodeName"`
		} `json:"spec"`
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
			ContainerStatuses []struct {
				Name         string `json:"name"`
				Ready        bool   `json:"ready"`
				RestartCount int    `json:"restartCount"`
				State        struct {
					Running *struct {
						StartedAt string `json:"startedAt"`
					} `json:"running"`
					Waiting *struct {
						Reason  string `json:"reason"`
						Message string `json:"message"`
					} `json:"waiting"`
					Terminated *struct {
						ExitCode   int    `json:"exitCode"`
						Reason     string `json:"reason"`
						Message    string `json:"message"`
						FinishedAt string `json:"finishedAt"`
					} `json:"terminated"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &pod); err != nil {
		return nil, err
	}

	status := &PodStatus{
		Name:      pod.Metadata.Name,
		Namespace: pod.Metadata.Namespace,
		Phase:     pod.Status.Phase,
		NodeName:  pod.Spec.NodeName,
		Labels:    pod.Metadata.Labels,
	}

	// Parse conditions
	for _, cond := range pod.Status.Conditions {
		status.Conditions = append(status.Conditions, PodCondition{
			Type:    cond.Type,
			Status:  cond.Status,
			Reason:  cond.Reason,
			Message: cond.Message,
		})
		if cond.Type == "Ready" && cond.Status == "True" {
			status.Ready = true
		}
	}

	// Parse container statuses
	for _, cs := range pod.Status.ContainerStatuses {
		containerState := ContainerState{
			Name:         cs.Name,
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
		}

		if cs.State.Running != nil {
			containerState.State = "running"
		} else if cs.State.Waiting != nil {
			containerState.State = "waiting"
			containerState.Reason = cs.State.Waiting.Reason
			containerState.Message = cs.State.Waiting.Message
		} else if cs.State.Terminated != nil {
			containerState.State = "terminated"
			containerState.Reason = cs.State.Terminated.Reason
			containerState.Message = cs.State.Terminated.Message
			containerState.ExitCode = cs.State.Terminated.ExitCode
		}

		status.ContainerStates = append(status.ContainerStates, containerState)
		status.RestartCount += cs.RestartCount
	}

	return status, nil
}

// parseDeploymentJSON parses deployment JSON output
func (d *DiagnosticsManager) parseDeploymentJSON(data []byte) (*DeploymentStatus, error) {
	var deploy struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Replicas            int `json:"replicas"`
			ReadyReplicas       int `json:"readyReplicas"`
			AvailableReplicas   int `json:"availableReplicas"`
			UnavailableReplicas int `json:"unavailableReplicas"`
			UpdatedReplicas     int `json:"updatedReplicas"`
			Conditions          []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &deploy); err != nil {
		return nil, err
	}

	status := &DeploymentStatus{
		Name:                deploy.Metadata.Name,
		Namespace:           deploy.Metadata.Namespace,
		Replicas:            deploy.Spec.Replicas,
		ReadyReplicas:       deploy.Status.ReadyReplicas,
		AvailableReplicas:   deploy.Status.AvailableReplicas,
		UnavailableReplicas: deploy.Status.UnavailableReplicas,
		UpdatedReplicas:     deploy.Status.UpdatedReplicas,
	}

	for _, cond := range deploy.Status.Conditions {
		status.Conditions = append(status.Conditions, DeploymentCondition{
			Type:    cond.Type,
			Status:  cond.Status,
			Reason:  cond.Reason,
			Message: cond.Message,
		})
	}

	return status, nil
}

// parseNodeJSON parses node JSON output
func (d *DiagnosticsManager) parseNodeJSON(data []byte) (*NodeStatus, error) {
	var node struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Conditions []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
			Allocatable map[string]string `json:"allocatable"`
			Capacity    map[string]string `json:"capacity"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &node); err != nil {
		return nil, err
	}

	status := &NodeStatus{
		Name: node.Metadata.Name,
	}

	if node.Status.Allocatable != nil {
		status.Allocatable = ResourceList{
			CPU:    node.Status.Allocatable["cpu"],
			Memory: node.Status.Allocatable["memory"],
			Pods:   node.Status.Allocatable["pods"],
		}
	}

	if node.Status.Capacity != nil {
		status.Capacity = ResourceList{
			CPU:    node.Status.Capacity["cpu"],
			Memory: node.Status.Capacity["memory"],
			Pods:   node.Status.Capacity["pods"],
		}
	}

	for _, cond := range node.Status.Conditions {
		status.Conditions = append(status.Conditions, NodeCondition{
			Type:    cond.Type,
			Status:  cond.Status,
			Reason:  cond.Reason,
			Message: cond.Message,
		})

		switch cond.Type {
		case "Ready":
			status.Ready = cond.Status == "True"
		case "MemoryPressure":
			status.MemoryPressure = cond.Status == "True"
		case "DiskPressure":
			status.DiskPressure = cond.Status == "True"
		case "PIDPressure":
			status.PIDPressure = cond.Status == "True"
		case "NetworkUnavailable":
			status.NetworkAvailable = cond.Status == "False"
		}
	}

	return status, nil
}

// parseEventsJSON parses events JSON output
func (d *DiagnosticsManager) parseEventsJSON(data []byte) ([]EventInfo, error) {
	var eventList struct {
		Items []struct {
			Type           string `json:"type"`
			Reason         string `json:"reason"`
			Message        string `json:"message"`
			Count          int    `json:"count"`
			FirstTimestamp string `json:"firstTimestamp"`
			LastTimestamp  string `json:"lastTimestamp"`
			InvolvedObject struct {
				Kind      string `json:"kind"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"involvedObject"`
		} `json:"items"`
	}

	if err := json.Unmarshal(data, &eventList); err != nil {
		return nil, err
	}

	var events []EventInfo
	for _, item := range eventList.Items {
		event := EventInfo{
			Type:    item.Type,
			Reason:  item.Reason,
			Message: item.Message,
			Count:   item.Count,
		}

		if t, err := time.Parse(time.RFC3339, item.FirstTimestamp); err == nil {
			event.FirstTimestamp = t
		}
		if t, err := time.Parse(time.RFC3339, item.LastTimestamp); err == nil {
			event.LastTimestamp = t
		}

		event.InvolvedObject.Kind = item.InvolvedObject.Kind
		event.InvolvedObject.Name = item.InvolvedObject.Name
		event.InvolvedObject.Namespace = item.InvolvedObject.Namespace

		events = append(events, event)
	}

	return events, nil
}

// detectPodIssues detects issues from pod status
func (d *DiagnosticsManager) detectPodIssues(status *PodStatus) []Issue {
	var issues []Issue
	now := time.Now()

	// Check phase
	switch status.Phase {
	case "Failed":
		issues = append(issues, Issue{
			ID:           fmt.Sprintf("pod-failed-%s", status.Name),
			Severity:     SeverityCritical,
			Category:     CategoryCrash,
			ResourceType: ResourcePod,
			ResourceName: status.Name,
			Namespace:    status.Namespace,
			Message:      fmt.Sprintf("Pod %s is in Failed state", status.Name),
			Timestamp:    now,
			Suggestions:  []string{"Check pod events for failure reason", "Review container logs"},
		})
	case "Pending":
		issues = append(issues, Issue{
			ID:           fmt.Sprintf("pod-pending-%s", status.Name),
			Severity:     SeverityWarning,
			Category:     CategoryPending,
			ResourceType: ResourcePod,
			ResourceName: status.Name,
			Namespace:    status.Namespace,
			Message:      fmt.Sprintf("Pod %s is stuck in Pending state", status.Name),
			Timestamp:    now,
			Suggestions:  []string{"Check node resources", "Verify scheduling constraints"},
		})
	}

	// Check container states
	for _, cs := range status.ContainerStates {
		if cs.State == "waiting" {
			category := CategoryPending
			severity := SeverityWarning

			// Classify based on reason
			switch cs.Reason {
			case "ImagePullBackOff", "ErrImagePull", "ImagePullError":
				category = CategoryImagePull
				severity = SeverityCritical
			case "CrashLoopBackOff":
				category = CategoryCrash
				severity = SeverityCritical
			case "CreateContainerError", "CreateContainerConfigError":
				category = CategoryConfiguration
				severity = SeverityCritical
			}

			issues = append(issues, Issue{
				ID:           fmt.Sprintf("container-waiting-%s-%s", status.Name, cs.Name),
				Severity:     severity,
				Category:     category,
				ResourceType: ResourcePod,
				ResourceName: status.Name,
				Namespace:    status.Namespace,
				Message:      fmt.Sprintf("Container %s is waiting: %s", cs.Name, cs.Reason),
				Details:      cs.Message,
				Timestamp:    now,
			})
		}

		// Check for high restart count
		if cs.RestartCount >= 5 {
			issues = append(issues, Issue{
				ID:           fmt.Sprintf("container-restarts-%s-%s", status.Name, cs.Name),
				Severity:     SeverityWarning,
				Category:     CategoryCrash,
				ResourceType: ResourcePod,
				ResourceName: status.Name,
				Namespace:    status.Namespace,
				Message:      fmt.Sprintf("Container %s has restarted %d times", cs.Name, cs.RestartCount),
				Timestamp:    now,
				Suggestions:  []string{"Check container logs for crash reason", "Review resource limits"},
			})
		}

		// Check for OOM killed
		if cs.State == "terminated" && cs.Reason == "OOMKilled" {
			issues = append(issues, Issue{
				ID:           fmt.Sprintf("container-oom-%s-%s", status.Name, cs.Name),
				Severity:     SeverityCritical,
				Category:     CategoryResourceLimit,
				ResourceType: ResourcePod,
				ResourceName: status.Name,
				Namespace:    status.Namespace,
				Message:      fmt.Sprintf("Container %s was OOM killed", cs.Name),
				Timestamp:    now,
				Suggestions:  []string{"Increase memory limits", "Investigate memory usage"},
			})
		}
	}

	// Check conditions for probe failures
	for _, cond := range status.Conditions {
		if cond.Type == "Ready" && cond.Status == "False" && strings.Contains(cond.Reason, "probe") {
			issues = append(issues, Issue{
				ID:           fmt.Sprintf("pod-probe-failed-%s", status.Name),
				Severity:     SeverityWarning,
				Category:     CategoryProbe,
				ResourceType: ResourcePod,
				ResourceName: status.Name,
				Namespace:    status.Namespace,
				Message:      fmt.Sprintf("Pod %s is failing probes: %s", status.Name, cond.Message),
				Timestamp:    now,
				Suggestions:  []string{"Check probe configuration", "Verify application is responding"},
			})
		}
	}

	return issues
}

// detectDeploymentIssues detects issues from deployment status
func (d *DiagnosticsManager) detectDeploymentIssues(status *DeploymentStatus) []Issue {
	var issues []Issue
	now := time.Now()

	// Check unavailable replicas
	if status.UnavailableReplicas > 0 {
		issues = append(issues, Issue{
			ID:           fmt.Sprintf("deployment-unavailable-%s", status.Name),
			Severity:     SeverityWarning,
			Category:     CategoryPending,
			ResourceType: ResourceDeployment,
			ResourceName: status.Name,
			Namespace:    status.Namespace,
			Message:      fmt.Sprintf("Deployment %s has %d unavailable replicas", status.Name, status.UnavailableReplicas),
			Timestamp:    now,
			Suggestions:  []string{"Check pod status", "Review recent events"},
		})
	}

	// Check if no replicas are ready
	if status.Replicas > 0 && status.ReadyReplicas == 0 {
		issues = append(issues, Issue{
			ID:           fmt.Sprintf("deployment-no-ready-%s", status.Name),
			Severity:     SeverityCritical,
			Category:     CategoryCrash,
			ResourceType: ResourceDeployment,
			ResourceName: status.Name,
			Namespace:    status.Namespace,
			Message:      fmt.Sprintf("Deployment %s has no ready replicas", status.Name),
			Timestamp:    now,
			Suggestions:  []string{"Check pod status for errors", "Review deployment events"},
		})
	}

	// Check conditions
	for _, cond := range status.Conditions {
		if cond.Type == "Available" && cond.Status == "False" {
			issues = append(issues, Issue{
				ID:           fmt.Sprintf("deployment-not-available-%s", status.Name),
				Severity:     SeverityCritical,
				Category:     CategoryPending,
				ResourceType: ResourceDeployment,
				ResourceName: status.Name,
				Namespace:    status.Namespace,
				Message:      fmt.Sprintf("Deployment %s is not available: %s", status.Name, cond.Message),
				Timestamp:    now,
			})
		}
		if cond.Type == "Progressing" && cond.Status == "False" {
			issues = append(issues, Issue{
				ID:           fmt.Sprintf("deployment-not-progressing-%s", status.Name),
				Severity:     SeverityWarning,
				Category:     CategoryPending,
				ResourceType: ResourceDeployment,
				ResourceName: status.Name,
				Namespace:    status.Namespace,
				Message:      fmt.Sprintf("Deployment %s is not progressing: %s", status.Name, cond.Message),
				Timestamp:    now,
			})
		}
	}

	return issues
}

// detectNodeIssues detects issues from node status
func (d *DiagnosticsManager) detectNodeIssues(status *NodeStatus) []Issue {
	var issues []Issue
	now := time.Now()

	if !status.Ready {
		issues = append(issues, Issue{
			ID:           fmt.Sprintf("node-not-ready-%s", status.Name),
			Severity:     SeverityCritical,
			Category:     CategoryNodeUnreachable,
			ResourceType: ResourceNode,
			ResourceName: status.Name,
			Message:      fmt.Sprintf("Node %s is not ready", status.Name),
			Timestamp:    now,
			Suggestions:  []string{"Check node connectivity", "Review kubelet logs"},
		})
	}

	if status.MemoryPressure {
		issues = append(issues, Issue{
			ID:           fmt.Sprintf("node-memory-pressure-%s", status.Name),
			Severity:     SeverityWarning,
			Category:     CategoryNodePressure,
			ResourceType: ResourceNode,
			ResourceName: status.Name,
			Message:      fmt.Sprintf("Node %s is under memory pressure", status.Name),
			Timestamp:    now,
			Suggestions:  []string{"Evict non-critical pods", "Add more nodes"},
		})
	}

	if status.DiskPressure {
		issues = append(issues, Issue{
			ID:           fmt.Sprintf("node-disk-pressure-%s", status.Name),
			Severity:     SeverityWarning,
			Category:     CategoryNodePressure,
			ResourceType: ResourceNode,
			ResourceName: status.Name,
			Message:      fmt.Sprintf("Node %s is under disk pressure", status.Name),
			Timestamp:    now,
			Suggestions:  []string{"Clean up unused images", "Increase disk space"},
		})
	}

	if status.PIDPressure {
		issues = append(issues, Issue{
			ID:           fmt.Sprintf("node-pid-pressure-%s", status.Name),
			Severity:     SeverityWarning,
			Category:     CategoryNodePressure,
			ResourceType: ResourceNode,
			ResourceName: status.Name,
			Message:      fmt.Sprintf("Node %s is under PID pressure", status.Name),
			Timestamp:    now,
			Suggestions:  []string{"Check for runaway processes", "Restart problematic pods"},
		})
	}

	if !status.NetworkAvailable {
		issues = append(issues, Issue{
			ID:           fmt.Sprintf("node-network-unavailable-%s", status.Name),
			Severity:     SeverityCritical,
			Category:     CategoryNetwork,
			ResourceType: ResourceNode,
			ResourceName: status.Name,
			Message:      fmt.Sprintf("Node %s has network unavailable", status.Name),
			Timestamp:    now,
			Suggestions:  []string{"Check CNI plugin status", "Review node network configuration"},
		})
	}

	return issues
}

// detectAllNodeIssues detects issues across all nodes
func (d *DiagnosticsManager) detectAllNodeIssues(ctx context.Context) ([]Issue, error) {
	output, err := d.client.RunJSON(ctx, "get", "nodes")
	if err != nil {
		return nil, err
	}

	var nodeList struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(output, &nodeList); err != nil {
		return nil, err
	}

	var allIssues []Issue
	for _, item := range nodeList.Items {
		status, err := d.parseNodeJSON(item)
		if err != nil {
			continue
		}
		issues := d.detectNodeIssues(status)
		allIssues = append(allIssues, issues...)
	}

	return allIssues, nil
}

// detectAllPodIssues detects issues across all pods
func (d *DiagnosticsManager) detectAllPodIssues(ctx context.Context) ([]Issue, error) {
	output, err := d.client.RunJSON(ctx, "get", "pods", "-A")
	if err != nil {
		return nil, err
	}

	return d.detectPodIssuesFromList(output), nil
}

// detectPodIssuesFromList detects issues from a pod list
func (d *DiagnosticsManager) detectPodIssuesFromList(data []byte) []Issue {
	var podList struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &podList); err != nil {
		return nil
	}

	var allIssues []Issue
	for _, item := range podList.Items {
		status, err := d.parsePodJSON(item)
		if err != nil {
			continue
		}
		issues := d.detectPodIssues(status)
		allIssues = append(allIssues, issues...)
	}

	return allIssues
}

// detectDeploymentIssuesFromList detects issues from deployment list
func (d *DiagnosticsManager) detectDeploymentIssuesFromList(data []byte, namespace string) []Issue {
	var deployList struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &deployList); err != nil {
		return nil
	}

	var allIssues []Issue
	for _, item := range deployList.Items {
		status, err := d.parseDeploymentJSON(item)
		if err != nil {
			continue
		}
		issues := d.detectDeploymentIssues(status)
		allIssues = append(allIssues, issues...)
	}

	return allIssues
}

// detectIssuesFromDescription detects issues from kubectl describe output
func (d *DiagnosticsManager) detectIssuesFromDescription(output, resourceType, name, namespace string) []Issue {
	var issues []Issue
	now := time.Now()

	// Common error patterns
	errorPatterns := []struct {
		pattern  string
		severity IssueSeverity
		category IssueCategory
		message  string
	}{
		{`ImagePullBackOff`, SeverityCritical, CategoryImagePull, "Image pull failing"},
		{`ErrImagePull`, SeverityCritical, CategoryImagePull, "Error pulling image"},
		{`CrashLoopBackOff`, SeverityCritical, CategoryCrash, "Container crash loop"},
		{`OOMKilled`, SeverityCritical, CategoryResourceLimit, "Container killed due to OOM"},
		{`Insufficient cpu`, SeverityWarning, CategoryScheduling, "Insufficient CPU for scheduling"},
		{`Insufficient memory`, SeverityWarning, CategoryScheduling, "Insufficient memory for scheduling"},
		{`FailedScheduling`, SeverityWarning, CategoryScheduling, "Pod scheduling failed"},
		{`FailedMount`, SeverityWarning, CategoryStorage, "Volume mount failed"},
		{`FailedAttach`, SeverityWarning, CategoryStorage, "Volume attach failed"},
	}

	for _, ep := range errorPatterns {
		re := regexp.MustCompile(ep.pattern)
		if re.MatchString(output) {
			issues = append(issues, Issue{
				ID:           fmt.Sprintf("%s-%s-%s", resourceType, ep.category, name),
				Severity:     ep.severity,
				Category:     ep.category,
				ResourceType: ResourceType(resourceType),
				ResourceName: name,
				Namespace:    namespace,
				Message:      ep.message,
				Timestamp:    now,
			})
		}
	}

	return issues
}

// parseAndAnalyzeLogs parses logs and identifies errors
func (d *DiagnosticsManager) parseAndAnalyzeLogs(output, podName string) []LogEntry {
	var entries []LogEntry

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		entry := LogEntry{
			Timestamp: time.Now(), // Will be overwritten if log has timestamp
			Container: podName,
			Message:   line,
		}

		// Detect log level
		lineLower := strings.ToLower(line)
		if strings.Contains(lineLower, "error") || strings.Contains(lineLower, "fatal") ||
			strings.Contains(lineLower, "panic") || strings.Contains(lineLower, "exception") {
			entry.Level = "error"
			entry.IsError = true
		} else if strings.Contains(lineLower, "warn") {
			entry.Level = "warn"
		} else if strings.Contains(lineLower, "info") {
			entry.Level = "info"
		}

		entries = append(entries, entry)
	}

	return entries
}
