package networking

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// mockClient implements K8sClient for testing
type mockClient struct {
	runResponse       string
	runError          error
	runWithNSResponse string
	runWithNSError    error
	getJSONResponse   []byte
	getJSONError      error
	describeResponse  string
	describeError     error
	deleteResponse    string
	deleteError       error
	applyResponse     string
	applyError        error
}

func (m *mockClient) Run(ctx context.Context, args ...string) (string, error) {
	return m.runResponse, m.runError
}

func (m *mockClient) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return m.runWithNSResponse, m.runWithNSError
}

func (m *mockClient) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return m.getJSONResponse, m.getJSONError
}

func (m *mockClient) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return m.describeResponse, m.describeError
}

func (m *mockClient) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return m.deleteResponse, m.deleteError
}

func (m *mockClient) Apply(ctx context.Context, manifest string) (string, error) {
	return m.applyResponse, m.applyError
}

func TestNewSubAgent(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	if agent == nil {
		t.Fatal("expected non-nil agent")
	}
	if agent.client == nil {
		t.Error("expected client to be set")
	}
	if agent.services == nil {
		t.Error("expected services manager to be set")
	}
	if agent.ingress == nil {
		t.Error("expected ingress manager to be set")
	}
	if agent.netpol == nil {
		t.Error("expected network policy manager to be set")
	}
}

func TestAnalyzeQueryResourceType(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	// Note: Map iteration is random in Go, so use distinct patterns that don't overlap
	tests := []struct {
		query        string
		expectedType ResourceType
	}{
		{"list svc resources", ResourceService},      // "svc" is unique to service
		{"get clusterip info", ResourceService},      // "clusterip" is unique
		{"describe ing resource", ResourceIngress},   // "ing" is unique to ingress
		{"list netpol rules", ResourceNetworkPolicy}, // "netpol" is unique
		{"show ep addresses", ResourceEndpoint},      // "ep" is unique to endpoint
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.ResourceType != tt.expectedType {
				t.Errorf("expected resource type %s, got %s", tt.expectedType, analysis.ResourceType)
			}
		})
	}
}

func TestAnalyzeQueryOperation(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	// Note: Map iteration is random in Go, so use unique patterns
	tests := []struct {
		query        string
		expectedOp   string
		expectedRead bool
	}{
		{"list all resources", "list", true},      // "list" is unique
		{"retrieve resource data", "get", true},   // "retrieve" is unique to get
		{"info about resource", "describe", true}, // "info about" is unique to describe
		{"add new resource", "create", false},     // "add" is unique to create
		{"drop resource", "delete", false},        // "drop" is unique to delete
		{"expose workload", "expose", false},      // "expose" is unique
		{"modify resource", "update", false},      // "modify" is unique to update
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.Operation != tt.expectedOp {
				t.Errorf("expected operation %s, got %s", tt.expectedOp, analysis.Operation)
			}
			if analysis.IsReadOnly != tt.expectedRead {
				t.Errorf("expected IsReadOnly=%v, got %v", tt.expectedRead, analysis.IsReadOnly)
			}
		})
	}
}

func TestAnalyzeQueryNamespace(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	tests := []struct {
		query      string
		expectedNS string
	}{
		{"list services in namespace kube-system", "kube-system"},
		{"get services -n default", "default"},
		{"show ingress in ns production", "production"},
		{"list services", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.Namespace != tt.expectedNS {
				t.Errorf("expected namespace %q, got %q", tt.expectedNS, analysis.Namespace)
			}
		})
	}
}

func TestServiceManagerCreatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewServiceManager(client, false)

	opts := CreateServiceOptions{
		Name:      "my-svc",
		Namespace: "default",
		Type:      ServiceTypeClusterIP,
		Ports: []ServicePortSpec{
			{Name: "http", Protocol: "TCP", Port: 80, TargetPort: 8080},
		},
	}

	plan := manager.CreateServicePlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) < 1 {
		t.Errorf("expected at least 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Command != "kubectl" {
		t.Errorf("expected command kubectl, got %s", plan.Steps[0].Command)
	}
}

func TestServiceManagerDeletePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewServiceManager(client, false)

	plan := manager.DeleteServicePlan("nginx-svc", "default")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Delete service nginx-svc" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestServiceManagerExposePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewServiceManager(client, false)

	plan := manager.ExposeDeploymentPlan("nginx", "default", 80, ServiceTypeLoadBalancer)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}

	// Check args contain type
	hasType := false
	for _, arg := range plan.Steps[0].Args {
		if arg == "LoadBalancer" {
			hasType = true
			break
		}
	}
	if !hasType {
		t.Error("expected LoadBalancer type in args")
	}
}

func TestIngressManagerCreatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewIngressManager(client, false)

	opts := CreateIngressOptions{
		Name:             "my-ingress",
		Namespace:        "default",
		IngressClassName: "nginx",
		Rules: []IngressRuleSpec{
			{
				Host: "example.com",
				Paths: []IngressPathSpec{
					{Path: "/", PathType: "Prefix", ServiceName: "my-svc", ServicePort: 80},
				},
			},
		},
	}

	plan := manager.CreateIngressPlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Manifest == "" {
		t.Error("expected manifest in step")
	}
}

func TestIngressManagerDeletePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewIngressManager(client, false)

	plan := manager.DeleteIngressPlan("my-ingress", "default")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Delete ingress my-ingress" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
}

func TestNetworkPolicyManagerCreatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewNetworkPolicyManager(client, false)

	opts := CreateNetworkPolicyOptions{
		Name:        "deny-all",
		Namespace:   "default",
		PolicyTypes: []string{"Ingress"},
	}

	plan := manager.CreateNetworkPolicyPlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Manifest == "" {
		t.Error("expected manifest in step")
	}
}

func TestNetworkPolicyManagerDenyAllPlan(t *testing.T) {
	client := &mockClient{}
	manager := NewNetworkPolicyManager(client, false)

	plan := manager.DenyAllIngressPlan("default")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Steps[0].Manifest == "" {
		t.Error("expected manifest in step")
	}
	if len(plan.Notes) < 1 {
		t.Error("expected notes about deny-all")
	}
}

func TestParseService(t *testing.T) {
	client := &mockClient{}
	manager := NewServiceManager(client, false)

	serviceJSON := `{
		"metadata": {
			"name": "nginx-svc",
			"namespace": "default",
			"labels": {"app": "nginx"},
			"creationTimestamp": "2024-01-01T00:00:00Z"
		},
		"spec": {
			"type": "ClusterIP",
			"clusterIP": "10.96.0.1",
			"sessionAffinity": "None",
			"selector": {"app": "nginx"},
			"ports": [
				{"name": "http", "protocol": "TCP", "port": 80, "targetPort": 8080}
			]
		},
		"status": {}
	}`

	info, err := manager.parseService([]byte(serviceJSON))
	if err != nil {
		t.Fatalf("failed to parse service: %v", err)
	}

	if info.Name != "nginx-svc" {
		t.Errorf("expected name nginx-svc, got %s", info.Name)
	}
	if info.Type != ServiceTypeClusterIP {
		t.Errorf("expected type ClusterIP, got %s", info.Type)
	}
	if info.ClusterIP != "10.96.0.1" {
		t.Errorf("expected clusterIP 10.96.0.1, got %s", info.ClusterIP)
	}
	if len(info.Ports) != 1 {
		t.Errorf("expected 1 port, got %d", len(info.Ports))
	}
}

func TestParseIngress(t *testing.T) {
	client := &mockClient{}
	manager := NewIngressManager(client, false)

	ingressJSON := `{
		"metadata": {
			"name": "my-ingress",
			"namespace": "default",
			"labels": {"app": "web"},
			"annotations": {"kubernetes.io/ingress.class": "nginx"},
			"creationTimestamp": "2024-01-01T00:00:00Z"
		},
		"spec": {
			"ingressClassName": "nginx",
			"rules": [
				{
					"host": "example.com",
					"http": {
						"paths": [
							{
								"path": "/",
								"pathType": "Prefix",
								"backend": {
									"service": {
										"name": "my-svc",
										"port": {"number": 80}
									}
								}
							}
						]
					}
				}
			]
		},
		"status": {
			"loadBalancer": {
				"ingress": [{"ip": "1.2.3.4"}]
			}
		}
	}`

	info, err := manager.parseIngress([]byte(ingressJSON))
	if err != nil {
		t.Fatalf("failed to parse ingress: %v", err)
	}

	if info.Name != "my-ingress" {
		t.Errorf("expected name my-ingress, got %s", info.Name)
	}
	if info.IngressClassName != "nginx" {
		t.Errorf("expected ingressClassName nginx, got %s", info.IngressClassName)
	}
	if len(info.Rules) != 1 {
		t.Errorf("expected 1 rule, got %d", len(info.Rules))
	}
	if info.Rules[0].Host != "example.com" {
		t.Errorf("expected host example.com, got %s", info.Rules[0].Host)
	}
	if len(info.Address) != 1 || info.Address[0] != "1.2.3.4" {
		t.Errorf("expected address 1.2.3.4, got %v", info.Address)
	}
}

func TestParseNetworkPolicy(t *testing.T) {
	client := &mockClient{}
	manager := NewNetworkPolicyManager(client, false)

	policyJSON := `{
		"metadata": {
			"name": "deny-all",
			"namespace": "default",
			"creationTimestamp": "2024-01-01T00:00:00Z"
		},
		"spec": {
			"podSelector": {
				"matchLabels": {"app": "web"}
			},
			"policyTypes": ["Ingress", "Egress"],
			"ingress": [
				{
					"from": [
						{
							"podSelector": {"matchLabels": {"role": "frontend"}}
						}
					],
					"ports": [
						{"protocol": "TCP", "port": 80}
					]
				}
			]
		}
	}`

	info, err := manager.parseNetworkPolicy([]byte(policyJSON))
	if err != nil {
		t.Fatalf("failed to parse network policy: %v", err)
	}

	if info.Name != "deny-all" {
		t.Errorf("expected name deny-all, got %s", info.Name)
	}
	if len(info.PolicyTypes) != 2 {
		t.Errorf("expected 2 policy types, got %d", len(info.PolicyTypes))
	}
	if len(info.Ingress) != 1 {
		t.Errorf("expected 1 ingress rule, got %d", len(info.Ingress))
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{48 * time.Hour, "2d"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatDuration(tt.duration)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestNetworkingPlanJSON(t *testing.T) {
	plan := &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   "Test plan",
		Steps: []NetworkingStep{
			{
				ID:          "step-1",
				Description: "Test step",
				Command:     "kubectl",
				Args:        []string{"get", "services"},
				Reason:      "Testing",
			},
		},
		Notes: []string{"Test note"},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("failed to marshal plan: %v", err)
	}

	var parsed NetworkingPlan
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal plan: %v", err)
	}

	if parsed.Version != 1 {
		t.Errorf("expected version 1, got %d", parsed.Version)
	}
	if parsed.Summary != "Test plan" {
		t.Errorf("unexpected summary: %s", parsed.Summary)
	}
}

func TestExtractServiceType(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	tests := []struct {
		query    string
		expected ServiceType
	}{
		{"create loadbalancer service", ServiceTypeLoadBalancer},
		{"create load balancer", ServiceTypeLoadBalancer},
		{"create nodeport service", ServiceTypeNodePort},
		{"create clusterip service", ServiceTypeClusterIP},
		{"create service", ServiceTypeClusterIP}, // default
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.extractServiceType(tt.query)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestExtractPort(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	tests := []struct {
		query    string
		expected int
	}{
		{"expose on port 8080", 8080},
		{"create service port 443", 443},
		{"create service", 0}, // no port
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := agent.extractPort(tt.query)
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}
