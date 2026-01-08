package workloads

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SubAgent handles workload-related operations
type SubAgent struct {
	client K8sClient
	debug  bool
}

// NewSubAgent creates a new workloads sub-agent
func NewSubAgent(client K8sClient, debug bool) *SubAgent {
	return &SubAgent{
		client: client,
		debug:  debug,
	}
}

// HandleQuery processes workload-related queries
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[workloads] handling query: %s\n", query)
	}

	// Analyze the query
	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[workloads] analysis: readonly=%v, workloadType=%s, operation=%s\n",
			analysis.IsReadOnly, analysis.WorkloadType, analysis.Operation)
	}

	// For read-only operations, execute immediately
	if analysis.IsReadOnly {
		return s.executeReadOnly(ctx, query, analysis, opts)
	}

	// For modifications, generate a plan
	plan, err := s.generatePlan(ctx, query, analysis, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate plan: %w", err)
	}

	return &Response{
		Type:    ResponseTypePlan,
		Plan:    plan,
		Message: plan.Summary,
	}, nil
}

// queryAnalysis contains the result of analyzing a query
type queryAnalysis struct {
	IsReadOnly   bool
	WorkloadType WorkloadType
	Operation    string
	ResourceName string
	Namespace    string
}

// analyzeQuery determines the nature of a workload query
func (s *SubAgent) analyzeQuery(query string) queryAnalysis {
	queryLower := strings.ToLower(query)
	analysis := queryAnalysis{}

	// Detect workload type
	analysis.WorkloadType = s.detectWorkloadType(queryLower)

	// Detect operation
	analysis.Operation = s.detectOperation(queryLower)

	// Determine if read-only
	readOnlyOps := []string{"list", "get", "describe", "show", "status", "logs", "events"}
	for _, op := range readOnlyOps {
		if analysis.Operation == op {
			analysis.IsReadOnly = true
			break
		}
	}

	// Extract resource name if mentioned
	analysis.ResourceName = s.extractResourceName(queryLower, analysis.WorkloadType)

	// Extract namespace if mentioned
	analysis.Namespace = s.extractNamespace(queryLower)

	return analysis
}

// detectWorkloadType identifies the workload type from the query
func (s *SubAgent) detectWorkloadType(query string) WorkloadType {
	workloadPatterns := map[WorkloadType][]string{
		WorkloadDeployment:  {"deployment", "deployments", "deploy"},
		WorkloadPod:         {"pod", "pods"},
		WorkloadStatefulSet: {"statefulset", "statefulsets", "sts"},
		WorkloadDaemonSet:   {"daemonset", "daemonsets", "ds"},
		WorkloadReplicaSet:  {"replicaset", "replicasets", "rs"},
		WorkloadJob:         {"job", "jobs"},
		WorkloadCronJob:     {"cronjob", "cronjobs", "cj"},
	}

	for workloadType, patterns := range workloadPatterns {
		for _, pattern := range patterns {
			if strings.Contains(query, pattern) {
				return workloadType
			}
		}
	}

	// Default to deployment for general workload queries
	return WorkloadDeployment
}

// detectOperation identifies the operation from the query
func (s *SubAgent) detectOperation(query string) string {
	// Check read-only operations first (in priority order)
	readOnlyOps := []struct {
		op       string
		patterns []string
	}{
		{"list", []string{"list", "show all", "get all", "what"}},
		{"get", []string{"get", "show", "describe", "details"}},
		{"describe", []string{"describe", "info about"}},
		{"logs", []string{"logs", "log"}},
		{"status", []string{"status", "health", "state"}},
		{"events", []string{"events", "event"}},
	}

	for _, item := range readOnlyOps {
		for _, pattern := range item.patterns {
			if strings.Contains(query, pattern) {
				return item.op
			}
		}
	}

	// Then check modify operations
	modifyOps := []struct {
		op       string
		patterns []string
	}{
		{"delete", []string{"delete", "remove", "destroy"}},
		{"rollback", []string{"rollback", "undo", "revert"}},
		{"restart", []string{"restart", "rollout restart"}},
		{"update", []string{"update", "set image", "change image"}},
		{"scale", []string{"scale", "resize"}},
		{"create", []string{"create", "deploy ", "run ", "launch"}}, // Note: "deploy " with space to avoid matching "deployments"
	}

	for _, item := range modifyOps {
		for _, pattern := range item.patterns {
			if strings.Contains(query, pattern) {
				return item.op
			}
		}
	}

	return "list" // Default operation
}

// extractResourceName attempts to extract a resource name from the query
func (s *SubAgent) extractResourceName(query string, workloadType WorkloadType) string {
	// Look for patterns like "deployment nginx" or "pod my-app"
	typeStr := string(workloadType)
	words := strings.Fields(query)

	for i, word := range words {
		if strings.Contains(word, typeStr) && i+1 < len(words) {
			// Next word might be the name
			candidate := words[i+1]
			// Skip common words
			if !isCommonWord(candidate) {
				return candidate
			}
		}
	}

	// Look for quoted strings
	if idx := strings.Index(query, `"`); idx != -1 {
		end := strings.Index(query[idx+1:], `"`)
		if end != -1 {
			return query[idx+1 : idx+1+end]
		}
	}

	return ""
}

// extractNamespace attempts to extract a namespace from the query
func (s *SubAgent) extractNamespace(query string) string {
	// Look for "in namespace X" or "namespace X" or "-n X"
	patterns := []string{"in namespace ", "namespace ", "in ns ", "-n "}

	for _, pattern := range patterns {
		if idx := strings.Index(query, pattern); idx != -1 {
			rest := query[idx+len(pattern):]
			words := strings.Fields(rest)
			if len(words) > 0 && !isCommonWord(words[0]) {
				return strings.Trim(words[0], `"'`)
			}
		}
	}

	// Check for common namespaces mentioned directly
	commonNamespaces := []string{"kube-system", "default", "kube-public"}
	for _, ns := range commonNamespaces {
		if strings.Contains(query, ns) {
			return ns
		}
	}

	return ""
}

// isCommonWord checks if a word is a common word that should be skipped
func isCommonWord(word string) bool {
	common := []string{
		"the", "a", "an", "in", "on", "at", "to", "for", "with", "from",
		"all", "my", "is", "are", "was", "were", "be", "been", "being",
		"and", "or", "but", "if", "then", "else", "when", "where", "how",
		"what", "which", "who", "this", "that", "these", "those",
	}
	word = strings.ToLower(word)
	for _, c := range common {
		if word == c {
			return true
		}
	}
	return false
}

// executeReadOnly handles read-only workload operations
func (s *SubAgent) executeReadOnly(ctx context.Context, query string, analysis queryAnalysis, opts QueryOptions) (*Response, error) {
	namespace := opts.Namespace
	if analysis.Namespace != "" {
		namespace = analysis.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}

	switch analysis.Operation {
	case "list":
		return s.handleList(ctx, analysis.WorkloadType, namespace, opts)
	case "get", "describe":
		if analysis.ResourceName == "" {
			return s.handleList(ctx, analysis.WorkloadType, namespace, opts)
		}
		return s.handleDescribe(ctx, analysis.WorkloadType, analysis.ResourceName, namespace)
	case "logs":
		if analysis.ResourceName == "" {
			return &Response{
				Type:    ResponseTypeResult,
				Message: "Please specify a pod name to get logs",
			}, nil
		}
		return s.handleLogs(ctx, analysis.ResourceName, namespace, LogOptions{TailLines: 100})
	case "status":
		return s.handleStatus(ctx, analysis.WorkloadType, analysis.ResourceName, namespace)
	case "events":
		return s.handleEvents(ctx, analysis.ResourceName, namespace)
	default:
		return s.handleList(ctx, analysis.WorkloadType, namespace, opts)
	}
}

// handleList lists workloads of the specified type
func (s *SubAgent) handleList(ctx context.Context, workloadType WorkloadType, namespace string, opts QueryOptions) (*Response, error) {
	var output string
	var err error

	resourceType := string(workloadType) + "s"
	if workloadType == WorkloadStatefulSet {
		resourceType = "statefulsets"
	}

	if opts.AllNamespaces {
		output, err = s.client.Run(ctx, "get", resourceType, "-A", "-o", "wide")
	} else {
		output, err = s.client.RunWithNamespace(ctx, namespace, "get", resourceType, "-o", "wide")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", resourceType, err)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Data:    output,
		Message: fmt.Sprintf("%s in namespace %s", resourceType, namespace),
	}, nil
}

// handleDescribe describes a specific workload
func (s *SubAgent) handleDescribe(ctx context.Context, workloadType WorkloadType, name, namespace string) (*Response, error) {
	resourceType := string(workloadType)

	output, err := s.client.Describe(ctx, resourceType, name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to describe %s %s: %w", resourceType, name, err)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Data:    output,
		Message: fmt.Sprintf("Details for %s %s", resourceType, name),
	}, nil
}

// handleLogs retrieves logs from a pod
func (s *SubAgent) handleLogs(ctx context.Context, podName, namespace string, opts LogOptions) (*Response, error) {
	output, err := s.client.Logs(ctx, podName, namespace, LogOptionsInternal{
		Container: opts.Container,
		Follow:    opts.Follow,
		Previous:  opts.Previous,
		TailLines: opts.TailLines,
		Since:     opts.Since,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get logs for pod %s: %w", podName, err)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Data:    output,
		Message: fmt.Sprintf("Logs from pod %s", podName),
	}, nil
}

// handleStatus gets the status of a workload
func (s *SubAgent) handleStatus(ctx context.Context, workloadType WorkloadType, name, namespace string) (*Response, error) {
	if workloadType == WorkloadDeployment && name != "" {
		output, err := s.client.Rollout(ctx, "status", "deployment", name, namespace)
		if err != nil {
			return nil, fmt.Errorf("failed to get rollout status: %w", err)
		}
		return &Response{
			Type:    ResponseTypeResult,
			Data:    output,
			Message: fmt.Sprintf("Rollout status for deployment %s", name),
		}, nil
	}

	// For other types or when no name specified, list with status
	return s.handleList(ctx, workloadType, namespace, QueryOptions{})
}

// handleEvents gets events for a resource
func (s *SubAgent) handleEvents(ctx context.Context, name, namespace string) (*Response, error) {
	args := []string{"get", "events", "--sort-by=.metadata.creationTimestamp"}
	if name != "" {
		args = append(args, "--field-selector", fmt.Sprintf("involvedObject.name=%s", name))
	}

	output, err := s.client.RunWithNamespace(ctx, namespace, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get events: %w", err)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Data:    output,
		Message: "Events",
	}, nil
}

// generatePlan creates a plan for workload modifications
func (s *SubAgent) generatePlan(ctx context.Context, query string, analysis queryAnalysis, opts QueryOptions) (*WorkloadPlan, error) {
	namespace := opts.Namespace
	if analysis.Namespace != "" {
		namespace = analysis.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}

	plan := &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Steps:     []WorkloadStep{},
	}

	switch analysis.Operation {
	case "create":
		return s.generateCreatePlan(analysis, namespace)
	case "scale":
		return s.generateScalePlan(analysis, namespace)
	case "restart":
		return s.generateRestartPlan(analysis, namespace)
	case "rollback":
		return s.generateRollbackPlan(analysis, namespace)
	case "delete":
		return s.generateDeletePlan(analysis, namespace)
	case "update":
		return s.generateUpdatePlan(analysis, namespace)
	default:
		plan.Summary = fmt.Sprintf("Unknown operation: %s", analysis.Operation)
		plan.Notes = append(plan.Notes, "Please specify a valid operation")
	}

	return plan, nil
}

// generateCreatePlan generates a plan for creating a workload
func (s *SubAgent) generateCreatePlan(analysis queryAnalysis, namespace string) (*WorkloadPlan, error) {
	plan := &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create %s", analysis.WorkloadType),
		Notes: []string{
			"Use --image to specify the container image",
			"Use --replicas to specify the number of replicas",
		},
	}

	if analysis.ResourceName == "" {
		plan.Notes = append(plan.Notes, "Please specify a name for the workload")
		return plan, nil
	}

	// Generate kubectl create deployment command
	plan.Steps = []WorkloadStep{
		{
			ID:          "create-workload",
			Description: fmt.Sprintf("Create %s %s", analysis.WorkloadType, analysis.ResourceName),
			Command:     "kubectl",
			Args:        []string{"create", string(analysis.WorkloadType), analysis.ResourceName, "-n", namespace},
			Reason:      fmt.Sprintf("Create the %s workload", analysis.WorkloadType),
		},
	}

	return plan, nil
}

// generateScalePlan generates a plan for scaling a workload
func (s *SubAgent) generateScalePlan(analysis queryAnalysis, namespace string) (*WorkloadPlan, error) {
	if analysis.ResourceName == "" {
		return &WorkloadPlan{
			Version:   1,
			CreatedAt: time.Now(),
			Summary:   "Scale workload",
			Notes:     []string{"Please specify the workload name and desired replica count"},
		}, nil
	}

	plan := &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Scale %s %s", analysis.WorkloadType, analysis.ResourceName),
		Steps: []WorkloadStep{
			{
				ID:          "scale-workload",
				Description: fmt.Sprintf("Scale %s %s", analysis.WorkloadType, analysis.ResourceName),
				Command:     "kubectl",
				Args:        []string{"scale", string(analysis.WorkloadType), analysis.ResourceName, "-n", namespace, "--replicas=<REPLICAS>"},
				Reason:      "Adjust the number of replicas",
			},
		},
		Notes: []string{"Replace <REPLICAS> with the desired replica count"},
	}

	return plan, nil
}

// generateRestartPlan generates a plan for restarting a workload
func (s *SubAgent) generateRestartPlan(analysis queryAnalysis, namespace string) (*WorkloadPlan, error) {
	if analysis.ResourceName == "" {
		return &WorkloadPlan{
			Version:   1,
			CreatedAt: time.Now(),
			Summary:   "Restart workload",
			Notes:     []string{"Please specify the workload name"},
		}, nil
	}

	plan := &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Restart %s %s", analysis.WorkloadType, analysis.ResourceName),
		Steps: []WorkloadStep{
			{
				ID:          "restart-workload",
				Description: fmt.Sprintf("Rollout restart %s %s", analysis.WorkloadType, analysis.ResourceName),
				Command:     "kubectl",
				Args:        []string{"rollout", "restart", string(analysis.WorkloadType), analysis.ResourceName, "-n", namespace},
				Reason:      "Trigger a rolling restart of all pods",
			},
			{
				ID:          "wait-rollout",
				Description: "Wait for rollout to complete",
				Command:     "kubectl",
				Args:        []string{"rollout", "status", string(analysis.WorkloadType), analysis.ResourceName, "-n", namespace},
				Reason:      "Verify the restart completed successfully",
			},
		},
	}

	return plan, nil
}

// generateRollbackPlan generates a plan for rolling back a workload
func (s *SubAgent) generateRollbackPlan(analysis queryAnalysis, namespace string) (*WorkloadPlan, error) {
	if analysis.ResourceName == "" {
		return &WorkloadPlan{
			Version:   1,
			CreatedAt: time.Now(),
			Summary:   "Rollback workload",
			Notes:     []string{"Please specify the workload name"},
		}, nil
	}

	plan := &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Rollback %s %s", analysis.WorkloadType, analysis.ResourceName),
		Steps: []WorkloadStep{
			{
				ID:          "rollback-workload",
				Description: fmt.Sprintf("Rollback %s %s to previous revision", analysis.WorkloadType, analysis.ResourceName),
				Command:     "kubectl",
				Args:        []string{"rollout", "undo", string(analysis.WorkloadType), analysis.ResourceName, "-n", namespace},
				Reason:      "Revert to the previous deployment revision",
			},
			{
				ID:          "wait-rollout",
				Description: "Wait for rollout to complete",
				Command:     "kubectl",
				Args:        []string{"rollout", "status", string(analysis.WorkloadType), analysis.ResourceName, "-n", namespace},
				Reason:      "Verify the rollback completed successfully",
			},
		},
	}

	return plan, nil
}

// generateDeletePlan generates a plan for deleting a workload
func (s *SubAgent) generateDeletePlan(analysis queryAnalysis, namespace string) (*WorkloadPlan, error) {
	if analysis.ResourceName == "" {
		return &WorkloadPlan{
			Version:   1,
			CreatedAt: time.Now(),
			Summary:   "Delete workload",
			Notes:     []string{"Please specify the workload name"},
		}, nil
	}

	plan := &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete %s %s", analysis.WorkloadType, analysis.ResourceName),
		Steps: []WorkloadStep{
			{
				ID:          "delete-workload",
				Description: fmt.Sprintf("Delete %s %s", analysis.WorkloadType, analysis.ResourceName),
				Command:     "kubectl",
				Args:        []string{"delete", string(analysis.WorkloadType), analysis.ResourceName, "-n", namespace},
				Reason:      "Remove the workload from the cluster",
			},
		},
		Notes: []string{"This operation cannot be undone"},
	}

	return plan, nil
}

// generateUpdatePlan generates a plan for updating a workload
func (s *SubAgent) generateUpdatePlan(analysis queryAnalysis, namespace string) (*WorkloadPlan, error) {
	if analysis.ResourceName == "" {
		return &WorkloadPlan{
			Version:   1,
			CreatedAt: time.Now(),
			Summary:   "Update workload",
			Notes:     []string{"Please specify the workload name and the update details"},
		}, nil
	}

	plan := &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Update %s %s", analysis.WorkloadType, analysis.ResourceName),
		Steps: []WorkloadStep{
			{
				ID:          "update-image",
				Description: fmt.Sprintf("Update container image for %s %s", analysis.WorkloadType, analysis.ResourceName),
				Command:     "kubectl",
				Args:        []string{"set", "image", string(analysis.WorkloadType) + "/" + analysis.ResourceName, "<CONTAINER>=<IMAGE>", "-n", namespace},
				Reason:      "Update the container image",
			},
			{
				ID:          "wait-rollout",
				Description: "Wait for rollout to complete",
				Command:     "kubectl",
				Args:        []string{"rollout", "status", string(analysis.WorkloadType), analysis.ResourceName, "-n", namespace},
				Reason:      "Verify the update completed successfully",
			},
		},
		Notes: []string{
			"Replace <CONTAINER> with the container name",
			"Replace <IMAGE> with the new image:tag",
		},
	}

	return plan, nil
}
