package helm

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SubAgent handles helm-related queries delegated from the main K8s agent
type SubAgent struct {
	client   HelmClient
	releases *ReleaseManager
	charts   *ChartManager
	debug    bool
}

// NewSubAgent creates a new helm sub-agent
func NewSubAgent(client HelmClient, debug bool) *SubAgent {
	return &SubAgent{
		client:   client,
		releases: NewReleaseManager(client, debug),
		charts:   NewChartManager(client, debug),
		debug:    debug,
	}
}

// QueryAnalysis contains the analysis of a helm query
type QueryAnalysis struct {
	ResourceType ResourceType
	Operation    string
	ResourceName string
	Namespace    string
	ChartName    string
	RepoName     string
	Revision     int
	IsReadOnly   bool
}

// HandleQuery processes a helm-related query
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[helm] query analysis: type=%s op=%s name=%s ns=%s chart=%s readonly=%v\n",
			analysis.ResourceType, analysis.Operation, analysis.ResourceName, analysis.Namespace, analysis.ChartName, analysis.IsReadOnly)
	}

	// Use namespace from query analysis or options
	namespace := analysis.Namespace
	if namespace == "" {
		namespace = opts.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}

	// Handle read-only operations immediately
	if analysis.IsReadOnly {
		return s.handleReadOperation(ctx, analysis, namespace, opts)
	}

	// Generate plan for modification operations
	return s.handleModifyOperation(ctx, query, analysis, namespace, opts)
}

// handleReadOperation executes read-only operations
func (s *SubAgent) handleReadOperation(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.ResourceType {
	case ResourceRelease:
		return s.handleReleaseReadOp(ctx, analysis, namespace, opts)
	case ResourceChart:
		return s.handleChartReadOp(ctx, analysis, opts)
	case ResourceRepo:
		return s.handleRepoReadOp(ctx, analysis, opts)
	default:
		// Default to listing releases
		return s.handleReleaseReadOp(ctx, analysis, namespace, opts)
	}
}

// handleReleaseReadOp handles release read operations
func (s *SubAgent) handleReleaseReadOp(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "list":
		releases, err := s.releases.ListReleases(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: releases,
		}, nil

	case "status":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("release name required for status operation")
		}
		release, err := s.releases.GetRelease(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: release,
		}, nil

	case "history":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("release name required for history operation")
		}
		history, err := s.releases.GetReleaseHistory(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: history,
		}, nil

	case "values":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("release name required for values operation")
		}
		values, err := s.releases.GetReleaseValues(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type:    ResponseTypeResult,
			Message: values,
		}, nil

	default:
		// Default to list
		releases, err := s.releases.ListReleases(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: releases,
		}, nil
	}
}

// handleChartReadOp handles chart read operations
func (s *SubAgent) handleChartReadOp(ctx context.Context, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "search":
		charts, err := s.charts.SearchCharts(ctx, analysis.ChartName)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: charts,
		}, nil

	case "show", "info":
		if analysis.ChartName == "" {
			return nil, fmt.Errorf("chart name required for show operation")
		}
		info, err := s.charts.ShowChart(ctx, analysis.ChartName)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type:    ResponseTypeResult,
			Message: info,
		}, nil

	default:
		// Default to search
		charts, err := s.charts.SearchCharts(ctx, analysis.ChartName)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: charts,
		}, nil
	}
}

// handleRepoReadOp handles repo read operations
func (s *SubAgent) handleRepoReadOp(ctx context.Context, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	repos, err := s.charts.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	return &Response{
		Type: ResponseTypeResult,
		Data: repos,
	}, nil
}

// handleModifyOperation generates plans for modification operations
func (s *SubAgent) handleModifyOperation(ctx context.Context, query string, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.ResourceType {
	case ResourceRelease:
		return s.handleReleaseModifyOp(ctx, query, analysis, namespace)
	case ResourceRepo:
		return s.handleRepoModifyOp(ctx, query, analysis)
	default:
		return nil, fmt.Errorf("unable to determine resource type for modification from query: %s", query)
	}
}

// handleReleaseModifyOp handles release modification operations
func (s *SubAgent) handleReleaseModifyOp(ctx context.Context, query string, analysis QueryAnalysis, namespace string) (*Response, error) {
	switch analysis.Operation {
	case "install":
		installOpts := s.parseInstallFromQuery(query, namespace)
		plan := s.releases.InstallReleasePlan(installOpts)
		return &Response{
			Type:    ResponseTypePlan,
			Plan:    plan,
			Message: plan.Summary,
		}, nil

	case "upgrade":
		upgradeOpts := s.parseUpgradeFromQuery(query, namespace)
		plan := s.releases.UpgradeReleasePlan(upgradeOpts)
		return &Response{
			Type:    ResponseTypePlan,
			Plan:    plan,
			Message: plan.Summary,
		}, nil

	case "rollback":
		rollbackOpts := s.parseRollbackFromQuery(query, namespace, analysis.Revision)
		plan := s.releases.RollbackReleasePlan(rollbackOpts)
		return &Response{
			Type:    ResponseTypePlan,
			Plan:    plan,
			Message: plan.Summary,
		}, nil

	case "uninstall", "delete":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("release name required for uninstall operation")
		}
		uninstallOpts := UninstallOptions{
			ReleaseName: analysis.ResourceName,
			Namespace:   namespace,
		}
		plan := s.releases.UninstallReleasePlan(uninstallOpts)
		return &Response{
			Type:    ResponseTypePlan,
			Plan:    plan,
			Message: plan.Summary,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported release operation: %s", analysis.Operation)
	}
}

// handleRepoModifyOp handles repo modification operations
func (s *SubAgent) handleRepoModifyOp(ctx context.Context, query string, analysis QueryAnalysis) (*Response, error) {
	switch analysis.Operation {
	case "add":
		addOpts := s.parseAddRepoFromQuery(query)
		plan := s.charts.AddRepoPlan(addOpts)
		return &Response{
			Type:    ResponseTypePlan,
			Plan:    plan,
			Message: plan.Summary,
		}, nil

	case "update":
		plan := s.charts.UpdateReposPlan()
		return &Response{
			Type:    ResponseTypePlan,
			Plan:    plan,
			Message: plan.Summary,
		}, nil

	case "remove", "delete":
		if analysis.RepoName == "" {
			return nil, fmt.Errorf("repo name required for remove operation")
		}
		plan := s.charts.RemoveRepoPlan(analysis.RepoName)
		return &Response{
			Type:    ResponseTypePlan,
			Plan:    plan,
			Message: plan.Summary,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported repo operation: %s", analysis.Operation)
	}
}

// analyzeQuery analyzes a query to determine resource type and operation
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	lower := strings.ToLower(query)

	analysis := QueryAnalysis{
		ResourceType: s.detectResourceType(lower),
		Operation:    s.detectOperation(lower),
		ResourceName: s.extractReleaseName(lower),
		Namespace:    s.extractNamespace(lower),
		ChartName:    s.extractChartName(lower),
		RepoName:     s.extractRepoName(lower),
		Revision:     s.extractRevision(lower),
	}

	analysis.IsReadOnly = s.isReadOnlyOperation(analysis.Operation)

	return analysis
}

// detectResourceType determines which helm resource type the query is about
func (s *SubAgent) detectResourceType(query string) ResourceType {
	// Check for repo operations first (more specific)
	if strings.Contains(query, "repo") || strings.Contains(query, "repository") {
		return ResourceRepo
	}

	// Check for chart operations
	if strings.Contains(query, "chart") || strings.Contains(query, "search") {
		return ResourceChart
	}

	// Default to release operations
	return ResourceRelease
}

// detectOperation determines the operation from the query
func (s *SubAgent) detectOperation(query string) string {
	operations := []struct {
		op       string
		patterns []string
	}{
		{"list", []string{"list", "show all", "what releases", "which releases"}},
		{"status", []string{"status", "state of"}},
		{"history", []string{"history", "revisions", "versions of"}},
		{"values", []string{"values", "configuration of", "config of"}},
		{"search", []string{"search", "find chart", "look for"}},
		{"show", []string{"show chart", "chart info", "describe chart"}},
		// Check uninstall before install since "uninstall" contains "install"
		{"uninstall", []string{"uninstall", "remove release", "delete release"}},
		{"install", []string{"install", "deploy chart", "add release"}},
		{"upgrade", []string{"upgrade", "update release"}},
		{"rollback", []string{"rollback", "revert", "go back to"}},
		{"add", []string{"add repo", "add repository"}},
		{"update", []string{"update repo", "update repositories", "refresh repo"}},
		{"remove", []string{"remove repo", "delete repo"}},
	}

	for _, op := range operations {
		for _, pattern := range op.patterns {
			if strings.Contains(query, pattern) {
				return op.op
			}
		}
	}

	return "list" // Default to list
}

// extractReleaseName extracts the release name from the query
func (s *SubAgent) extractReleaseName(query string) string {
	patterns := []string{
		`release\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`status\s+(?:of\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`history\s+(?:of\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`uninstall\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`upgrade\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`rollback\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// extractNamespace extracts the namespace from the query
func (s *SubAgent) extractNamespace(query string) string {
	patterns := []string{
		`(?:in\s+)?namespace\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`-n\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`(?:in\s+)?ns\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// extractChartName extracts the chart name from the query
func (s *SubAgent) extractChartName(query string) string {
	patterns := []string{
		`chart\s+([a-z0-9][a-z0-9-/]*[a-z0-9])`,
		`install\s+[a-z0-9-]+\s+([a-z0-9][a-z0-9-/]*[a-z0-9])`,
		`search\s+(?:for\s+)?([a-z0-9][a-z0-9-/]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// extractRepoName extracts the repo name from the query
func (s *SubAgent) extractRepoName(query string) string {
	patterns := []string{
		`repo\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`repository\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// extractRevision extracts the revision number from the query
func (s *SubAgent) extractRevision(query string) int {
	patterns := []string{
		`revision\s+(\d+)`,
		`to\s+revision\s+(\d+)`,
		`version\s+(\d+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			var rev int
			_, _ = fmt.Sscanf(matches[1], "%d", &rev)
			return rev
		}
	}

	return 0
}

// isReadOnlyOperation determines if an operation is read-only
func (s *SubAgent) isReadOnlyOperation(operation string) bool {
	readOnlyOps := map[string]bool{
		"list":    true,
		"status":  true,
		"history": true,
		"values":  true,
		"search":  true,
		"show":    true,
	}
	return readOnlyOps[operation]
}

// parseInstallFromQuery parses install options from a query
func (s *SubAgent) parseInstallFromQuery(query string, namespace string) InstallOptions {
	opts := InstallOptions{
		Namespace: namespace,
		Wait:      true,
		Timeout:   5 * time.Minute,
	}

	lower := strings.ToLower(query)

	// Extract release name (skip filler words)
	releasePattern := regexp.MustCompile(`install\s+([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := releasePattern.FindStringSubmatch(lower); len(matches) > 1 {
		name := matches[1]
		// Skip if release name is a filler word
		if !isFillerWord(name) {
			opts.ReleaseName = name
		}
	}

	// Try to extract explicit chart reference (repo/chart format)
	chartRefPattern := regexp.MustCompile(`([a-z0-9-]+/[a-z0-9-]+)`)
	if matches := chartRefPattern.FindStringSubmatch(lower); len(matches) > 1 {
		opts.Chart = matches[1]
	}

	// If no explicit chart found, infer from release name
	if opts.Chart == "" && opts.ReleaseName != "" {
		opts.Chart = inferChartName(opts.ReleaseName)
	}

	// Check for create-namespace
	if strings.Contains(lower, "create namespace") {
		opts.CreateNamespace = true
	}

	return opts
}

// isFillerWord returns true if the word is a common filler word in queries
func isFillerWord(word string) bool {
	fillerWords := map[string]bool{
		"using": true, "with": true, "from": true, "the": true, "a": true,
		"helm": true, "chart": true, "release": true, "to": true, "on": true,
		"in": true, "my": true, "this": true, "that": true,
	}
	return fillerWords[word]
}

// inferChartName infers the full chart name from a common application name
func inferChartName(name string) string {
	// Map of common application names to their chart references
	commonCharts := map[string]string{
		"redis":         "bitnami/redis",
		"nginx":         "bitnami/nginx",
		"postgresql":    "bitnami/postgresql",
		"postgres":      "bitnami/postgresql",
		"mysql":         "bitnami/mysql",
		"mongodb":       "bitnami/mongodb",
		"mongo":         "bitnami/mongodb",
		"kafka":         "bitnami/kafka",
		"rabbitmq":      "bitnami/rabbitmq",
		"elasticsearch": "bitnami/elasticsearch",
		"grafana":       "grafana/grafana",
		"prometheus":    "prometheus-community/prometheus",
		"jenkins":       "jenkins/jenkins",
		"wordpress":     "bitnami/wordpress",
		"mariadb":       "bitnami/mariadb",
		"minio":         "bitnami/minio",
		"apache":        "bitnami/apache",
		"tomcat":        "bitnami/tomcat",
		"memcached":     "bitnami/memcached",
		"consul":        "hashicorp/consul",
		"vault":         "hashicorp/vault",
	}

	if chart, ok := commonCharts[name]; ok {
		return chart
	}

	// Default to bitnami if unknown
	return "bitnami/" + name
}

// parseUpgradeFromQuery parses upgrade options from a query
func (s *SubAgent) parseUpgradeFromQuery(query string, namespace string) UpgradeOptions {
	opts := UpgradeOptions{
		Namespace: namespace,
		Wait:      true,
		Timeout:   5 * time.Minute,
	}

	lower := strings.ToLower(query)

	// Extract release name (skip filler words)
	releasePattern := regexp.MustCompile(`upgrade\s+([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := releasePattern.FindStringSubmatch(lower); len(matches) > 1 {
		name := matches[1]
		if !isFillerWord(name) {
			opts.ReleaseName = name
		}
	}

	// Try to extract explicit chart reference (repo/chart format)
	chartRefPattern := regexp.MustCompile(`([a-z0-9-]+/[a-z0-9-]+)`)
	if matches := chartRefPattern.FindStringSubmatch(lower); len(matches) > 1 {
		opts.Chart = matches[1]
	}

	// If no explicit chart found, infer from release name
	if opts.Chart == "" && opts.ReleaseName != "" {
		opts.Chart = inferChartName(opts.ReleaseName)
	}

	// Check for install flag
	if strings.Contains(lower, "install if not exists") {
		opts.Install = true
	}

	return opts
}

// parseRollbackFromQuery parses rollback options from a query
func (s *SubAgent) parseRollbackFromQuery(query string, namespace string, revision int) RollbackOptions {
	opts := RollbackOptions{
		Namespace: namespace,
		Revision:  revision,
		Wait:      true,
		Timeout:   5 * time.Minute,
	}

	// Extract release name
	releasePattern := regexp.MustCompile(`rollback\s+([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := releasePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.ReleaseName = matches[1]
	}

	return opts
}

// parseAddRepoFromQuery parses add repo options from a query
func (s *SubAgent) parseAddRepoFromQuery(query string) AddRepoOptions {
	opts := AddRepoOptions{}

	// Extract repo name
	namePattern := regexp.MustCompile(`add\s+repo\s+([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := namePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Name = matches[1]
	}

	// Extract URL
	urlPattern := regexp.MustCompile(`(https?://[^\s]+)`)
	if matches := urlPattern.FindStringSubmatch(query); len(matches) > 1 {
		opts.URL = matches[1]
	}

	return opts
}
