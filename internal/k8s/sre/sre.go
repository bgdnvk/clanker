package sre

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// SubAgent handles SRE related queries for diagnostics, health checks, and remediation
type SubAgent struct {
	client      K8sClient
	diagnostics *DiagnosticsManager
	health      *HealthChecker
	debug       bool
}

// NewSubAgent creates a new SRE sub-agent
func NewSubAgent(client K8sClient, debug bool) *SubAgent {
	return &SubAgent{
		client:      client,
		diagnostics: NewDiagnosticsManager(client, debug),
		health:      NewHealthChecker(client, debug),
		debug:       debug,
	}
}

// HandleQuery processes an SRE related query and returns a response
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	query = strings.ToLower(query)

	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[sre] analysis: type=%s, op=%s, name=%s, ns=%s, readonly=%v\n",
			analysis.ResourceType, analysis.Operation, analysis.ResourceName, analysis.Namespace, analysis.IsReadOnly)
	}

	// Override namespace from options if provided
	if opts.Namespace != "" {
		analysis.Namespace = opts.Namespace
	}

	// Route to appropriate handler based on operation
	switch analysis.Operation {
	case "health":
		return s.handleHealthCheck(ctx, query, analysis, opts)
	case "diagnose":
		return s.handleDiagnose(ctx, query, analysis, opts)
	case "logs":
		return s.handleLogs(ctx, query, analysis, opts)
	case "events":
		return s.handleEvents(ctx, query, analysis, opts)
	case "issues":
		return s.handleIssues(ctx, query, analysis, opts)
	case "why":
		return s.handleWhy(ctx, query, analysis, opts)
	case "fix":
		return s.handleFix(ctx, query, analysis, opts)
	default:
		// Default to health check for general queries
		return s.handleHealthCheck(ctx, query, analysis, opts)
	}
}

// analyzeQuery analyzes the query to determine the operation and target
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	analysis := QueryAnalysis{
		ResourceType: s.detectResourceType(query),
		Operation:    s.detectOperation(query),
		ResourceName: s.extractResourceName(query),
		Namespace:    s.extractNamespace(query),
		IsReadOnly:   true, // Most SRE operations are read-only
	}

	// Fix operations are not read-only
	if analysis.Operation == "fix" {
		analysis.IsReadOnly = false
	}

	return analysis
}

// detectResourceType determines the resource type from the query
func (s *SubAgent) detectResourceType(query string) ResourceType {
	resourcePatterns := []struct {
		resourceType ResourceType
		patterns     []string
	}{
		{ResourcePod, []string{"pod", "pods", "container"}},
		{ResourceDeployment, []string{"deployment", "deployments", "deploy"}},
		{ResourceStatefulSet, []string{"statefulset", "statefulsets", "sts"}},
		{ResourceDaemonSet, []string{"daemonset", "daemonsets", "ds"}},
		{ResourceNode, []string{"node", "nodes"}},
		{ResourceService, []string{"service", "services", "svc"}},
		{ResourcePVC, []string{"pvc", "persistentvolumeclaim", "volume"}},
		{ResourceEvent, []string{"event", "events"}},
	}

	for _, rp := range resourcePatterns {
		for _, pattern := range rp.patterns {
			if strings.Contains(query, pattern) {
				return rp.resourceType
			}
		}
	}

	return "" // No specific resource type
}

// detectOperation determines the operation from the query
func (s *SubAgent) detectOperation(query string) string {
	operations := []struct {
		op       string
		patterns []string
	}{
		{"health", []string{"health", "healthy", "status", "overview", "summary"}},
		{"diagnose", []string{"diagnose", "diagnostic", "analyze", "analysis", "troubleshoot", "investigate"}},
		{"logs", []string{"logs", "log", "output"}},
		{"events", []string{"events", "event", "what happened"}},
		{"issues", []string{"issues", "problems", "errors", "failures", "failing", "crashed", "crashing"}},
		{"why", []string{"why is", "why are", "what is wrong", "what's wrong", "reason"}},
		{"fix", []string{"fix", "repair", "remediate", "resolve", "restart"}},
	}

	for _, op := range operations {
		for _, pattern := range op.patterns {
			if strings.Contains(query, pattern) {
				return op.op
			}
		}
	}

	return "health" // Default to health check
}

// extractResourceName extracts the resource name from the query
func (s *SubAgent) extractResourceName(query string) string {
	// Common patterns for resource names
	patterns := []string{
		`pod\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`deployment\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`service\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`node\s+([a-z0-9][a-z0-9.-]*[a-z0-9])`,
		`named?\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`called?\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`for\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			name := matches[1]
			// Skip common words that are not resource names
			if !s.isCommonWord(name) {
				return name
			}
		}
	}

	return ""
}

// extractNamespace extracts the namespace from the query
func (s *SubAgent) extractNamespace(query string) string {
	patterns := []string{
		`namespace\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`-n\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`in\s+ns\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`in\s+([a-z0-9][a-z0-9-]*[a-z0-9])\s+namespace`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// isCommonWord checks if a word is a common word that should not be treated as a resource name
func (s *SubAgent) isCommonWord(word string) bool {
	commonWords := map[string]bool{
		"the": true, "all": true, "any": true, "some": true,
		"this": true, "that": true, "these": true, "those": true,
		"my": true, "your": true, "our": true, "their": true,
		"cluster": true, "namespace": true, "resource": true,
		"status": true, "health": true, "logs": true, "events": true,
		"issues": true, "problems": true, "errors": true,
	}
	return commonWords[word]
}

// handleHealthCheck performs a health check
func (s *SubAgent) handleHealthCheck(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[sre] performing health check\n")
	}

	// Determine scope of health check
	if analysis.ResourceName != "" && analysis.ResourceType != "" {
		// Resource-specific health check
		result, err := s.health.CheckResource(ctx, string(analysis.ResourceType), analysis.ResourceName, analysis.Namespace)
		if err != nil {
			return nil, fmt.Errorf("health check failed: %w", err)
		}

		return &Response{
			Type:    ResponseTypeResult,
			Message: result.Summary,
			Data:    result,
		}, nil
	}

	if analysis.Namespace != "" && !opts.AllNamespaces {
		// Namespace health check
		result, err := s.health.CheckNamespace(ctx, analysis.Namespace)
		if err != nil {
			return nil, fmt.Errorf("namespace health check failed: %w", err)
		}

		return &Response{
			Type:    ResponseTypeResult,
			Message: result.Summary,
			Data:    result,
		}, nil
	}

	// Cluster-wide health check
	summary, err := s.health.CheckCluster(ctx)
	if err != nil {
		return nil, fmt.Errorf("cluster health check failed: %w", err)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Message: fmt.Sprintf("Cluster health: %s (score: %d/100)", summary.OverallHealth, summary.Score),
		Data:    summary,
	}, nil
}

// handleDiagnose performs diagnostic analysis
func (s *SubAgent) handleDiagnose(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[sre] performing diagnostic analysis\n")
	}

	var report *DiagnosticReport
	var err error

	if analysis.ResourceName != "" && analysis.ResourceType != "" {
		// Resource-specific diagnosis
		report, err = s.diagnostics.DiagnoseResource(ctx, string(analysis.ResourceType), analysis.ResourceName, analysis.Namespace)
	} else if analysis.Namespace != "" {
		// Namespace diagnosis
		report, err = s.diagnostics.DiagnoseNamespace(ctx, analysis.Namespace)
	} else {
		// Cluster-wide diagnosis
		report, err = s.diagnostics.DiagnoseCluster(ctx)
	}

	if err != nil {
		return nil, fmt.Errorf("diagnostic analysis failed: %w", err)
	}

	return &Response{
		Type:    ResponseTypeReport,
		Message: report.Summary,
		Report:  report,
	}, nil
}

// handleLogs retrieves and analyzes logs
func (s *SubAgent) handleLogs(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[sre] retrieving logs\n")
	}

	if analysis.ResourceName == "" {
		return nil, fmt.Errorf("please specify a pod name for log analysis")
	}

	namespace := analysis.Namespace
	if namespace == "" {
		namespace = "default"
	}

	// Get logs with error detection
	logs, err := s.diagnostics.GetLogsWithAnalysis(ctx, analysis.ResourceName, namespace, opts.TailLines, opts.Since)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve logs: %w", err)
	}

	// Count errors
	errorCount := 0
	for _, entry := range logs {
		if entry.IsError {
			errorCount++
		}
	}

	message := fmt.Sprintf("Retrieved %d log entries", len(logs))
	if errorCount > 0 {
		message = fmt.Sprintf("Retrieved %d log entries (%d errors detected)", len(logs), errorCount)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Message: message,
		Data:    logs,
	}, nil
}

// handleEvents retrieves and analyzes events
func (s *SubAgent) handleEvents(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[sre] retrieving events\n")
	}

	namespace := analysis.Namespace
	if opts.AllNamespaces {
		namespace = ""
	}

	events, err := s.diagnostics.GetEvents(ctx, namespace, analysis.ResourceName)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve events: %w", err)
	}

	// Count warning events
	warningCount := 0
	for _, event := range events {
		if event.Type == "Warning" {
			warningCount++
		}
	}

	message := fmt.Sprintf("Found %d events", len(events))
	if warningCount > 0 {
		message = fmt.Sprintf("Found %d events (%d warnings)", len(events), warningCount)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Message: message,
		Data:    events,
	}, nil
}

// handleIssues detects and reports issues
func (s *SubAgent) handleIssues(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[sre] detecting issues\n")
	}

	var issues []Issue
	var err error

	if analysis.Namespace != "" && !opts.AllNamespaces {
		issues, err = s.diagnostics.DetectIssuesInNamespace(ctx, analysis.Namespace)
	} else {
		issues, err = s.diagnostics.DetectClusterIssues(ctx)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to detect issues: %w", err)
	}

	// Count by severity
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

	var message string
	if len(issues) == 0 {
		message = "No issues detected"
	} else {
		message = fmt.Sprintf("Found %d issues (%d critical, %d warnings)", len(issues), criticalCount, warningCount)
	}

	return &Response{
		Type:    ResponseTypeResult,
		Message: message,
		Data:    issues,
	}, nil
}

// handleWhy analyzes why a resource is in a particular state
func (s *SubAgent) handleWhy(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[sre] analyzing root cause\n")
	}

	if analysis.ResourceName == "" {
		return nil, fmt.Errorf("please specify a resource name to analyze")
	}

	resourceType := string(analysis.ResourceType)
	if resourceType == "" {
		resourceType = "pod" // Default to pod
	}

	namespace := analysis.Namespace
	if namespace == "" {
		namespace = "default"
	}

	report, err := s.diagnostics.DiagnoseResource(ctx, resourceType, analysis.ResourceName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze resource: %w", err)
	}

	return &Response{
		Type:    ResponseTypeReport,
		Message: report.Summary,
		Report:  report,
	}, nil
}

// handleFix generates a remediation plan
func (s *SubAgent) handleFix(ctx context.Context, query string, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[sre] generating remediation plan\n")
	}

	if analysis.ResourceName == "" {
		return nil, fmt.Errorf("please specify a resource name to fix")
	}

	resourceType := string(analysis.ResourceType)
	if resourceType == "" {
		resourceType = "pod"
	}

	namespace := analysis.Namespace
	if namespace == "" {
		namespace = "default"
	}

	// First diagnose the issue
	report, err := s.diagnostics.DiagnoseResource(ctx, resourceType, analysis.ResourceName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to diagnose resource: %w", err)
	}

	// Generate remediation plan based on issues
	plan := s.generateRemediationPlan(report)

	return &Response{
		Type:    ResponseTypePlan,
		Message: plan.Summary,
		Plan:    plan,
	}, nil
}

// generateRemediationPlan creates a remediation plan based on diagnostic report
func (s *SubAgent) generateRemediationPlan(report *DiagnosticReport) *SREPlan {
	plan := &SREPlan{
		Version: 1,
		Summary: fmt.Sprintf("Remediation plan for %s/%s", report.ResourceType, report.ResourceName),
		Steps:   []RemediationStep{},
		Notes:   []string{},
	}

	if len(report.Issues) == 0 {
		plan.Summary = "No issues found, no remediation needed"
		return plan
	}

	order := 1
	for _, issue := range report.Issues {
		steps := s.getRemediationStepsForIssue(issue, report.ResourceName, report.Namespace)
		for _, step := range steps {
			step.Order = order
			plan.Steps = append(plan.Steps, step)
			order++
		}
	}

	if len(plan.Steps) == 0 {
		plan.Notes = append(plan.Notes, "No automated remediation available for detected issues")
		plan.Notes = append(plan.Notes, "Manual investigation recommended")
	}

	return plan
}

// getRemediationStepsForIssue returns remediation steps for a specific issue
func (s *SubAgent) getRemediationStepsForIssue(issue Issue, resourceName, namespace string) []RemediationStep {
	var steps []RemediationStep

	switch issue.Category {
	case CategoryCrash:
		steps = append(steps, RemediationStep{
			Action:      "Restart pod",
			Description: "Delete the crashing pod to trigger a restart",
			Command:     "kubectl",
			Args:        []string{"delete", "pod", resourceName, "-n", namespace},
			Risk:        "low",
			Automated:   true,
		})

	case CategoryImagePull:
		steps = append(steps, RemediationStep{
			Action:      "Check image pull secret",
			Description: "Verify that image pull secrets are configured correctly",
			Command:     "kubectl",
			Args:        []string{"get", "secrets", "-n", namespace, "-o", "name"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Verify image exists",
			Description: "Check if the container image exists and is accessible",
			Risk:        "low",
			Automated:   false,
		})

	case CategoryResourceLimit:
		steps = append(steps, RemediationStep{
			Action:      "Check resource usage",
			Description: "Review current resource usage for the pod",
			Command:     "kubectl",
			Args:        []string{"top", "pod", resourceName, "-n", namespace},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Consider increasing limits",
			Description: "Modify deployment to increase memory or CPU limits",
			Risk:        "medium",
			Automated:   false,
		})

	case CategoryPending:
		steps = append(steps, RemediationStep{
			Action:      "Check node resources",
			Description: "Verify cluster has sufficient resources to schedule pod",
			Command:     "kubectl",
			Args:        []string{"describe", "nodes"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Check pod scheduling constraints",
			Description: "Review node selectors, affinity rules, and tolerations",
			Command:     "kubectl",
			Args:        []string{"describe", "pod", resourceName, "-n", namespace},
			Risk:        "low",
			Automated:   false,
		})

	case CategoryProbe:
		steps = append(steps, RemediationStep{
			Action:      "Check probe configuration",
			Description: "Review liveness and readiness probe settings",
			Command:     "kubectl",
			Args:        []string{"get", "pod", resourceName, "-n", namespace, "-o", "yaml"},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Test probe endpoint",
			Description: "Verify the probe endpoint is accessible from within the pod",
			Risk:        "low",
			Automated:   false,
		})

	case CategoryNodePressure:
		steps = append(steps, RemediationStep{
			Action:      "Check node status",
			Description: "Review node conditions and resource pressure",
			Command:     "kubectl",
			Args:        []string{"describe", "node", resourceName},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Drain node if needed",
			Description: "Consider draining the node to redistribute workloads",
			Command:     "kubectl",
			Args:        []string{"drain", resourceName, "--ignore-daemonsets", "--delete-emptydir-data"},
			Risk:        "high",
			Automated:   false,
		})

	case CategoryStorage:
		steps = append(steps, RemediationStep{
			Action:      "Check PVC status",
			Description: "Verify the PersistentVolumeClaim is bound",
			Command:     "kubectl",
			Args:        []string{"get", "pvc", "-n", namespace},
			Risk:        "low",
			Automated:   false,
		})
		steps = append(steps, RemediationStep{
			Action:      "Check storage class",
			Description: "Verify the storage class exists and has available capacity",
			Command:     "kubectl",
			Args:        []string{"get", "sc"},
			Risk:        "low",
			Automated:   false,
		})
	}

	return steps
}
