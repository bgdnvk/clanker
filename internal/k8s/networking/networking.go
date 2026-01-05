package networking

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SubAgent handles networking-related queries delegated from the main K8s agent
type SubAgent struct {
	client   K8sClient
	services *ServiceManager
	ingress  *IngressManager
	netpol   *NetworkPolicyManager
	debug    bool
}

// NewSubAgent creates a new networking sub-agent
func NewSubAgent(client K8sClient, debug bool) *SubAgent {
	return &SubAgent{
		client:   client,
		services: NewServiceManager(client, debug),
		ingress:  NewIngressManager(client, debug),
		netpol:   NewNetworkPolicyManager(client, debug),
		debug:    debug,
	}
}

// QueryAnalysis contains the analysis of a networking query
type QueryAnalysis struct {
	ResourceType ResourceType
	Operation    string
	ResourceName string
	Namespace    string
	IsReadOnly   bool
}

// HandleQuery processes a networking-related query
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[networking] query analysis: type=%s op=%s name=%s ns=%s readonly=%v\n",
			analysis.ResourceType, analysis.Operation, analysis.ResourceName, analysis.Namespace, analysis.IsReadOnly)
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
	case ResourceService:
		return s.handleServiceReadOp(ctx, analysis, namespace, opts)
	case ResourceIngress:
		return s.handleIngressReadOp(ctx, analysis, namespace, opts)
	case ResourceNetworkPolicy:
		return s.handleNetworkPolicyReadOp(ctx, analysis, namespace, opts)
	case ResourceEndpoint:
		return s.handleEndpointReadOp(ctx, analysis, namespace, opts)
	default:
		// If no specific type detected, list all networking resources
		return s.listAllNetworkingResources(ctx, namespace, opts)
	}
}

// handleServiceReadOp handles service read operations
func (s *SubAgent) handleServiceReadOp(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "list":
		services, err := s.services.ListServices(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: services,
		}, nil

	case "get", "describe":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("service name required for %s operation", analysis.Operation)
		}
		if analysis.Operation == "describe" {
			desc, err := s.services.DescribeService(ctx, analysis.ResourceName, namespace)
			if err != nil {
				return nil, err
			}
			return &Response{
				Type:    ResponseTypeResult,
				Message: desc,
			}, nil
		}
		svc, err := s.services.GetService(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: svc,
		}, nil

	case "endpoints":
		if analysis.ResourceName == "" {
			endpoints, err := s.services.ListEndpoints(ctx, namespace, opts)
			if err != nil {
				return nil, err
			}
			return &Response{
				Type: ResponseTypeResult,
				Data: endpoints,
			}, nil
		}
		ep, err := s.services.GetEndpoints(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: ep,
		}, nil

	default:
		services, err := s.services.ListServices(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: services,
		}, nil
	}
}

// handleIngressReadOp handles ingress read operations
func (s *SubAgent) handleIngressReadOp(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "list":
		ingresses, err := s.ingress.ListIngresses(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: ingresses,
		}, nil

	case "get", "describe":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("ingress name required for %s operation", analysis.Operation)
		}
		if analysis.Operation == "describe" {
			desc, err := s.ingress.DescribeIngress(ctx, analysis.ResourceName, namespace)
			if err != nil {
				return nil, err
			}
			return &Response{
				Type:    ResponseTypeResult,
				Message: desc,
			}, nil
		}
		ing, err := s.ingress.GetIngress(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: ing,
		}, nil

	default:
		ingresses, err := s.ingress.ListIngresses(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: ingresses,
		}, nil
	}
}

// handleNetworkPolicyReadOp handles network policy read operations
func (s *SubAgent) handleNetworkPolicyReadOp(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "list":
		policies, err := s.netpol.ListNetworkPolicies(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: policies,
		}, nil

	case "get", "describe":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("network policy name required for %s operation", analysis.Operation)
		}
		if analysis.Operation == "describe" {
			desc, err := s.netpol.DescribeNetworkPolicy(ctx, analysis.ResourceName, namespace)
			if err != nil {
				return nil, err
			}
			return &Response{
				Type:    ResponseTypeResult,
				Message: desc,
			}, nil
		}
		policy, err := s.netpol.GetNetworkPolicy(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: policy,
		}, nil

	default:
		policies, err := s.netpol.ListNetworkPolicies(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: policies,
		}, nil
	}
}

// handleEndpointReadOp handles endpoint read operations
func (s *SubAgent) handleEndpointReadOp(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	if analysis.ResourceName == "" {
		endpoints, err := s.services.ListEndpoints(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: endpoints,
		}, nil
	}

	ep, err := s.services.GetEndpoints(ctx, analysis.ResourceName, namespace)
	if err != nil {
		return nil, err
	}
	return &Response{
		Type: ResponseTypeResult,
		Data: ep,
	}, nil
}

// listAllNetworkingResources lists services, ingresses, and network policies
func (s *SubAgent) listAllNetworkingResources(ctx context.Context, namespace string, opts QueryOptions) (*Response, error) {
	services, _ := s.services.ListServices(ctx, namespace, opts)
	ingresses, _ := s.ingress.ListIngresses(ctx, namespace, opts)
	policies, _ := s.netpol.ListNetworkPolicies(ctx, namespace, opts)

	return &Response{
		Type: ResponseTypeResult,
		Data: map[string]interface{}{
			"services":        services,
			"ingresses":       ingresses,
			"networkPolicies": policies,
		},
	}, nil
}

// handleModifyOperation generates plans for modification operations
func (s *SubAgent) handleModifyOperation(ctx context.Context, query string, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.ResourceType {
	case ResourceService:
		return s.handleServiceModifyOp(ctx, query, analysis, namespace)
	case ResourceIngress:
		return s.handleIngressModifyOp(ctx, query, analysis, namespace)
	case ResourceNetworkPolicy:
		return s.handleNetworkPolicyModifyOp(ctx, query, analysis, namespace)
	default:
		return nil, fmt.Errorf("unable to determine resource type for modification from query: %s", query)
	}
}

// handleServiceModifyOp handles service modification operations
func (s *SubAgent) handleServiceModifyOp(ctx context.Context, query string, analysis QueryAnalysis, namespace string) (*Response, error) {
	switch analysis.Operation {
	case "create":
		// Parse service creation options from query
		serviceOpts := s.parseServiceCreationFromQuery(query, namespace)
		plan := s.services.CreateServicePlan(serviceOpts)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	case "delete", "remove":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("service name required for delete operation")
		}
		plan := s.services.DeleteServicePlan(analysis.ResourceName, namespace)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	case "expose":
		// Expose a deployment as a service
		deployName := s.extractDeploymentName(query)
		if deployName == "" {
			return nil, fmt.Errorf("deployment name required for expose operation")
		}
		port := s.extractPort(query)
		serviceType := s.extractServiceType(query)
		plan := s.services.ExposeDeploymentPlan(deployName, namespace, port, serviceType)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported service operation: %s", analysis.Operation)
	}
}

// handleIngressModifyOp handles ingress modification operations
func (s *SubAgent) handleIngressModifyOp(ctx context.Context, query string, analysis QueryAnalysis, namespace string) (*Response, error) {
	switch analysis.Operation {
	case "create":
		ingressOpts := s.parseIngressCreationFromQuery(query, namespace)
		plan := s.ingress.CreateIngressPlan(ingressOpts)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	case "delete", "remove":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("ingress name required for delete operation")
		}
		plan := s.ingress.DeleteIngressPlan(analysis.ResourceName, namespace)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported ingress operation: %s", analysis.Operation)
	}
}

// handleNetworkPolicyModifyOp handles network policy modification operations
func (s *SubAgent) handleNetworkPolicyModifyOp(ctx context.Context, query string, analysis QueryAnalysis, namespace string) (*Response, error) {
	switch analysis.Operation {
	case "create":
		policyOpts := s.parseNetworkPolicyFromQuery(query, namespace)
		plan := s.netpol.CreateNetworkPolicyPlan(policyOpts)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	case "delete", "remove":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("network policy name required for delete operation")
		}
		plan := s.netpol.DeleteNetworkPolicyPlan(analysis.ResourceName, namespace)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported network policy operation: %s", analysis.Operation)
	}
}

// analyzeQuery analyzes a query to determine resource type and operation
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	lower := strings.ToLower(query)

	analysis := QueryAnalysis{
		ResourceType: s.detectResourceType(lower),
		Operation:    s.detectOperation(lower),
		ResourceName: s.extractResourceName(lower),
		Namespace:    s.extractNamespace(lower),
	}

	analysis.IsReadOnly = s.isReadOnlyOperation(analysis.Operation)

	return analysis
}

// detectResourceType determines which networking resource type the query is about
func (s *SubAgent) detectResourceType(query string) ResourceType {
	// Map of patterns to resource types
	resourcePatterns := map[ResourceType][]string{
		ResourceService:       {"service", "svc", "clusterip", "nodeport", "loadbalancer"},
		ResourceIngress:       {"ingress", "ing", "route", "routing"},
		ResourceNetworkPolicy: {"networkpolicy", "netpol", "network policy", "network-policy"},
		ResourceEndpoint:      {"endpoint", "ep"},
	}

	for resourceType, patterns := range resourcePatterns {
		for _, pattern := range patterns {
			if strings.Contains(query, pattern) {
				return resourceType
			}
		}
	}

	return "" // Unknown resource type
}

// detectOperation determines the operation from the query
func (s *SubAgent) detectOperation(query string) string {
	operations := map[string][]string{
		"list":     {"list", "show", "what", "which", "all"},
		"get":      {"get", "fetch", "retrieve"},
		"describe": {"describe", "details", "info about"},
		"create":   {"create", "add", "new", "make"},
		"delete":   {"delete", "remove", "drop"},
		"expose":   {"expose"},
		"update":   {"update", "modify", "change", "edit"},
		"endpoints": {"endpoints", "endpoint"},
	}

	for op, patterns := range operations {
		for _, pattern := range patterns {
			if strings.Contains(query, pattern) {
				return op
			}
		}
	}

	return "list" // Default to list
}

// extractResourceName extracts the resource name from the query
func (s *SubAgent) extractResourceName(query string) string {
	// Patterns to match resource names
	patterns := []string{
		`service\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`svc\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`ingress\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`ing\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`networkpolicy\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`netpol\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
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
		`(?:in\s+)([a-z0-9][a-z0-9-]*[a-z0-9])\s+namespace`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	// Check for common namespaces mentioned directly
	commonNamespaces := []string{"kube-system", "kube-public", "default"}
	for _, ns := range commonNamespaces {
		if strings.Contains(query, ns) {
			return ns
		}
	}

	return ""
}

// isReadOnlyOperation determines if an operation is read-only
func (s *SubAgent) isReadOnlyOperation(operation string) bool {
	readOnlyOps := map[string]bool{
		"list":      true,
		"get":       true,
		"describe":  true,
		"show":      true,
		"endpoints": true,
	}
	return readOnlyOps[operation]
}

// parseServiceCreationFromQuery parses service creation options from a query
func (s *SubAgent) parseServiceCreationFromQuery(query string, namespace string) CreateServiceOptions {
	opts := CreateServiceOptions{
		Namespace: namespace,
		Type:      ServiceTypeClusterIP,
	}

	// Extract service name
	namePattern := regexp.MustCompile(`(?:service|svc)\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := namePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Name = matches[1]
	}

	// Extract service type
	opts.Type = s.extractServiceType(query)

	// Extract port
	port := s.extractPort(query)
	if port > 0 {
		opts.Ports = []ServicePortSpec{{
			Name:       "http",
			Protocol:   "TCP",
			Port:       port,
			TargetPort: port,
		}}
	}

	return opts
}

// parseIngressCreationFromQuery parses ingress creation options from a query
func (s *SubAgent) parseIngressCreationFromQuery(query string, namespace string) CreateIngressOptions {
	opts := CreateIngressOptions{
		Namespace: namespace,
	}

	// Extract ingress name
	namePattern := regexp.MustCompile(`(?:ingress|ing)\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := namePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Name = matches[1]
	}

	// Extract host
	hostPattern := regexp.MustCompile(`host\s+([a-z0-9][a-z0-9.-]*)`)
	if matches := hostPattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Rules = append(opts.Rules, IngressRuleSpec{Host: matches[1]})
	}

	return opts
}

// parseNetworkPolicyFromQuery parses network policy options from a query
func (s *SubAgent) parseNetworkPolicyFromQuery(query string, namespace string) CreateNetworkPolicyOptions {
	opts := CreateNetworkPolicyOptions{
		Namespace: namespace,
	}

	// Extract policy name
	namePattern := regexp.MustCompile(`(?:networkpolicy|netpol|network policy)\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := namePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Name = matches[1]
	}

	// Determine policy types based on query
	if strings.Contains(strings.ToLower(query), "ingress") {
		opts.PolicyTypes = append(opts.PolicyTypes, "Ingress")
	}
	if strings.Contains(strings.ToLower(query), "egress") {
		opts.PolicyTypes = append(opts.PolicyTypes, "Egress")
	}

	return opts
}

// extractServiceType extracts the service type from the query
func (s *SubAgent) extractServiceType(query string) ServiceType {
	lower := strings.ToLower(query)

	if strings.Contains(lower, "loadbalancer") || strings.Contains(lower, "load balancer") {
		return ServiceTypeLoadBalancer
	}
	if strings.Contains(lower, "nodeport") || strings.Contains(lower, "node port") {
		return ServiceTypeNodePort
	}
	if strings.Contains(lower, "externalname") || strings.Contains(lower, "external name") {
		return ServiceTypeExternalName
	}

	return ServiceTypeClusterIP
}

// extractPort extracts a port number from the query
func (s *SubAgent) extractPort(query string) int {
	portPattern := regexp.MustCompile(`port\s+(\d+)`)
	if matches := portPattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		var port int
		fmt.Sscanf(matches[1], "%d", &port)
		return port
	}
	return 0
}

// extractDeploymentName extracts a deployment name from expose queries
func (s *SubAgent) extractDeploymentName(query string) string {
	patterns := []string{
		`expose\s+(?:deployment\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`deployment\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`deploy\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// formatDuration formats a duration into a human-readable string
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
