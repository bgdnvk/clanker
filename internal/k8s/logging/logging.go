package logging

import (
	"context"
	"fmt"
	"strings"
)

// SubAgent handles log related queries
type SubAgent struct {
	client       K8sClient
	aiDecisionFn AIDecisionFunc
	collector    *LogCollector
	analyzer     *LogAnalyzer
	debug        bool
}

// NewSubAgent creates a new logging subagent
func NewSubAgent(client K8sClient, debug bool) *SubAgent {
	return &SubAgent{
		client:    client,
		collector: NewLogCollector(client, debug),
		analyzer:  NewLogAnalyzer(debug),
		debug:     debug,
	}
}

// SetAIDecisionFunction sets the AI function for analysis
func (s *SubAgent) SetAIDecisionFunction(fn AIDecisionFunc) {
	s.aiDecisionFn = fn
	s.analyzer.SetAIDecisionFunction(fn)
}

// HandleQuery processes a logging query and returns the result
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	if s.debug {
		fmt.Printf("[logging] handling query: %s\n", query)
	}

	// Analyze the natural language query
	analysis := s.analyzeQuery(query)

	// Apply analysis results to options if not already set
	s.applyQueryAnalysis(&opts, analysis)

	if s.debug {
		fmt.Printf("[logging] analysis: scope=%s, resource=%s, patterns=%v, wantsAnalysis=%v, wantsFix=%v\n",
			analysis.Scope, analysis.ResourceName, analysis.Patterns, analysis.WantsAnalysis, analysis.WantsFix)
	}

	// Collect logs based on scope
	logs, err := s.collectLogs(ctx, opts)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to collect logs: %v", err),
			Error:   err,
		}, nil
	}

	// Filter logs if patterns specified
	if len(opts.Patterns) > 0 {
		logs = filterByPatterns(logs, opts.Patterns)
	}

	// Filter by level if specified
	if len(opts.LevelFilter) > 0 {
		logs = filterByLevel(logs, opts.LevelFilter)
	}

	// If analysis mode or user wants analysis, use AI to analyze
	if opts.AnalyzeMode || analysis.WantsAnalysis || analysis.WantsFix {
		return s.handleAnalysis(ctx, query, logs, analysis)
	}

	// Return raw logs with summary
	return s.buildResponse(logs, opts)
}

// analyzeQuery parses natural language query to understand intent
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	q := strings.ToLower(query)
	analysis := QueryAnalysis{
		Scope: ScopePod, // Default
	}

	// Detect scope from keywords
	if containsAny(q, []string{"cluster", "all pods", "everywhere", "all namespaces"}) {
		analysis.Scope = ScopeCluster
	} else if containsAny(q, []string{"node", "worker", "control-plane", "master"}) {
		analysis.Scope = ScopeNode
		analysis.NodeName = extractNodeName(q)
	} else if containsAny(q, []string{"deployment", "deploy"}) {
		analysis.Scope = ScopeDeployment
		analysis.ResourceName = extractResourceName(q, "deployment")
		if analysis.ResourceName == "" {
			analysis.ResourceName = extractResourceName(q, "deploy")
		}
	} else if containsAny(q, []string{"namespace logs", "ns logs", "in namespace", "from namespace", "namespace"}) && !containsAny(q, []string{"pod", "deployment", "deploy"}) {
		analysis.Scope = ScopeNamespace
	} else if containsAny(q, []string{"pod"}) {
		analysis.Scope = ScopePod
		analysis.ResourceName = extractPodName(q)
	}

	// Extract namespace
	analysis.Namespace = extractNamespace(q)

	// Extract error patterns
	analysis.Patterns = extractErrorPatterns(q)

	// Detect if user wants analysis
	analysis.WantsAnalysis = containsAny(q, []string{
		"analyze", "analysis", "what", "investigate",
		"happening", "wrong", "issue", "problem",
		"root cause", "cause", "reason",
	})

	// Detect if user wants fixes or is asking why something is not working
	analysis.WantsFix = containsAny(q, []string{
		"fix", "solve", "resolve", "how to", "suggest",
		"recommend", "help", "not starting", "failing",
		"why is", "why are", "why does", "why do",
		"not working", "crashed", "crashing", "broken",
	})

	// Extract time constraints
	analysis.TimeConstraint = extractTimeConstraint(q)

	return analysis
}

// applyQueryAnalysis applies analyzed query parameters to options
func (s *SubAgent) applyQueryAnalysis(opts *QueryOptions, analysis QueryAnalysis) {
	// Only override if not already set
	if opts.Scope == "" {
		opts.Scope = analysis.Scope
	}

	if opts.Namespace == "" && analysis.Namespace != "" {
		opts.Namespace = analysis.Namespace
	}

	if opts.PodName == "" && analysis.Scope == ScopePod && analysis.ResourceName != "" {
		opts.PodName = analysis.ResourceName
	}

	if opts.DeploymentName == "" && analysis.Scope == ScopeDeployment && analysis.ResourceName != "" {
		opts.DeploymentName = analysis.ResourceName
	}

	if opts.NodeName == "" && analysis.NodeName != "" {
		opts.NodeName = analysis.NodeName
	}

	if len(opts.Patterns) == 0 && len(analysis.Patterns) > 0 {
		opts.Patterns = analysis.Patterns
	}

	if opts.Since == "" && analysis.TimeConstraint != "" {
		opts.Since = analysis.TimeConstraint
	}

	// Set defaults
	if opts.TailLines == 0 {
		opts.TailLines = 100
	}

	if opts.Namespace == "" && opts.Scope != ScopeCluster && opts.Scope != ScopeNode {
		opts.Namespace = "default"
	}
}

// collectLogs collects logs based on scope
func (s *SubAgent) collectLogs(ctx context.Context, opts QueryOptions) (*AggregatedLogs, error) {
	switch opts.Scope {
	case ScopeCluster:
		return s.collector.CollectClusterLogs(ctx, opts)
	case ScopeNode:
		if opts.NodeName == "" {
			return nil, fmt.Errorf("node name is required for node scope")
		}
		return s.collector.CollectNodeLogs(ctx, opts.NodeName, opts)
	case ScopeDeployment:
		if opts.DeploymentName == "" {
			return nil, fmt.Errorf("deployment name is required for deployment scope")
		}
		return s.collector.CollectDeploymentLogs(ctx, opts.DeploymentName, opts)
	case ScopeNamespace:
		return s.collector.CollectNamespaceLogs(ctx, opts.Namespace, opts)
	case ScopePod:
		if opts.PodName == "" {
			return nil, fmt.Errorf("pod name is required for pod scope")
		}
		return s.collector.CollectPodLogs(ctx, opts.PodName, opts)
	default:
		// Default to namespace logs
		return s.collector.CollectNamespaceLogs(ctx, opts.Namespace, opts)
	}
}

// handleAnalysis performs AI powered log analysis
func (s *SubAgent) handleAnalysis(ctx context.Context, query string, logs *AggregatedLogs, analysis QueryAnalysis) (*Response, error) {
	// If no AI function configured, use quick analysis
	if s.aiDecisionFn == nil {
		if s.debug {
			fmt.Printf("[logging] no AI function configured, using quick analysis\n")
		}
		logAnalysis := s.analyzer.QuickAnalyze(logs)
		return &Response{
			Type:     ResponseTypeAnalysis,
			Message:  logAnalysis.Summary,
			Analysis: logAnalysis,
		}, nil
	}

	var logAnalysis *LogAnalysis
	var err error

	if analysis.WantsFix {
		logAnalysis, err = s.analyzer.AnalyzeWithFixes(ctx, query, logs)
	} else {
		logAnalysis, err = s.analyzer.Analyze(ctx, query, logs)
	}

	if err != nil {
		// Fallback to quick analysis on AI failure
		if s.debug {
			fmt.Printf("[logging] AI analysis failed, falling back to quick analysis: %v\n", err)
		}
		logAnalysis = s.analyzer.QuickAnalyze(logs)
		logAnalysis.Summary = fmt.Sprintf("(Quick analysis - AI unavailable) %s", logAnalysis.Summary)
	}

	return &Response{
		Type:     ResponseTypeAnalysis,
		Message:  logAnalysis.Summary,
		Analysis: logAnalysis,
	}, nil
}

// buildResponse builds a response for raw log output
func (s *SubAgent) buildResponse(logs *AggregatedLogs, opts QueryOptions) (*Response, error) {
	summary := buildLogSummary(logs)

	message := fmt.Sprintf("Retrieved %d log lines from %d pods", logs.TotalLines, logs.PodCount)
	if logs.ErrorCount > 0 {
		message = fmt.Sprintf("%s (%d errors, %d warnings)", message, logs.ErrorCount, logs.WarnCount)
	}

	return &Response{
		Type:    ResponseTypeRawLogs,
		Message: message,
		RawLogs: formatLogsForDisplay(logs, opts.Timestamps),
		Summary: summary,
	}, nil
}

// GetLogs is a simpler method for direct log retrieval without NL parsing
func (s *SubAgent) GetLogs(ctx context.Context, opts QueryOptions) (*Response, error) {
	logs, err := s.collectLogs(ctx, opts)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to collect logs: %v", err),
			Error:   err,
		}, nil
	}

	if len(opts.Patterns) > 0 {
		logs = filterByPatterns(logs, opts.Patterns)
	}

	return s.buildResponse(logs, opts)
}

// AnalyzeLogs analyzes logs with AI without NL query parsing
func (s *SubAgent) AnalyzeLogs(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	logs, err := s.collectLogs(ctx, opts)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to collect logs: %v", err),
			Error:   err,
		}, nil
	}

	if len(opts.Patterns) > 0 {
		logs = filterByPatterns(logs, opts.Patterns)
	}

	return s.handleAnalysis(ctx, query, logs, QueryAnalysis{WantsAnalysis: true})
}

// GetPodLogs retrieves logs from a specific pod
func (s *SubAgent) GetPodLogs(ctx context.Context, podName, namespace string, tailLines int, since string) (*Response, error) {
	opts := QueryOptions{
		Scope:     ScopePod,
		PodName:   podName,
		Namespace: namespace,
		TailLines: tailLines,
		Since:     since,
	}
	return s.GetLogs(ctx, opts)
}

// GetDeploymentLogs retrieves logs from all pods of a deployment
func (s *SubAgent) GetDeploymentLogs(ctx context.Context, deploymentName, namespace string, tailLines int, since string) (*Response, error) {
	opts := QueryOptions{
		Scope:          ScopeDeployment,
		DeploymentName: deploymentName,
		Namespace:      namespace,
		TailLines:      tailLines,
		Since:          since,
	}
	return s.GetLogs(ctx, opts)
}

// GetNodeLogs retrieves logs from all pods on a node
func (s *SubAgent) GetNodeLogs(ctx context.Context, nodeName string, tailLines int, since string) (*Response, error) {
	opts := QueryOptions{
		Scope:     ScopeNode,
		NodeName:  nodeName,
		TailLines: tailLines,
		Since:     since,
	}
	return s.GetLogs(ctx, opts)
}

// GetClusterLogs retrieves logs from across the cluster
func (s *SubAgent) GetClusterLogs(ctx context.Context, tailLines int, since string) (*Response, error) {
	opts := QueryOptions{
		Scope:         ScopeCluster,
		TailLines:     tailLines,
		Since:         since,
		AllNamespaces: true,
	}
	return s.GetLogs(ctx, opts)
}

// GetNamespaceLogs retrieves logs from all pods in a namespace
func (s *SubAgent) GetNamespaceLogs(ctx context.Context, namespace string, tailLines int, since string) (*Response, error) {
	opts := QueryOptions{
		Scope:     ScopeNamespace,
		Namespace: namespace,
		TailLines: tailLines,
		Since:     since,
	}
	return s.GetLogs(ctx, opts)
}

// FindErrors searches for error patterns in logs
func (s *SubAgent) FindErrors(ctx context.Context, opts QueryOptions, patterns []string) (*Response, error) {
	opts.Patterns = patterns
	if len(patterns) == 0 {
		opts.Patterns = []string{"error", "fail", "exception", "panic"}
	}

	logs, err := s.collectLogs(ctx, opts)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to collect logs: %v", err),
			Error:   err,
		}, nil
	}

	filtered := filterByPatterns(logs, opts.Patterns)
	return s.buildResponse(filtered, opts)
}

// InvestigateIssue uses AI to investigate a specific issue in logs
func (s *SubAgent) InvestigateIssue(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	logs, err := s.collectLogs(ctx, opts)
	if err != nil {
		return &Response{
			Type:    ResponseTypeError,
			Message: fmt.Sprintf("Failed to collect logs: %v", err),
			Error:   err,
		}, nil
	}

	analysis := QueryAnalysis{
		WantsAnalysis: true,
		WantsFix:      true,
	}

	return s.handleAnalysis(ctx, query, logs, analysis)
}
