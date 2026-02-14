package networking

import (
	"strings"
	"testing"
)

func TestAKSIngressAnnotations(t *testing.T) {
	tests := []struct {
		name           string
		opts           AKSIngressOptions
		wantAnnotation string
		wantValue      string
	}{
		{
			name: "SSL Redirect",
			opts: AKSIngressOptions{
				UseAGIC:     true,
				SSLRedirect: true,
			},
			wantAnnotation: AGICAnnotationSSLRedirect,
			wantValue:      "true",
		},
		{
			name: "WAF Policy",
			opts: AKSIngressOptions{
				UseAGIC:     true,
				WAFPolicyID: "/subscriptions/xxx/resourceGroups/rg/providers/Microsoft.Network/ApplicationGatewayWebApplicationFirewallPolicies/mywaf",
			},
			wantAnnotation: AGICAnnotationWAFPolicy,
			wantValue:      "/subscriptions/xxx/resourceGroups/rg",
		},
		{
			name: "SSL Certificate",
			opts: AKSIngressOptions{
				UseAGIC:     true,
				SSLCertName: "my-ssl-cert",
			},
			wantAnnotation: AGICAnnotationAppGWSSLCert,
			wantValue:      "my-ssl-cert",
		},
		{
			name: "Backend Path Prefix",
			opts: AKSIngressOptions{
				UseAGIC:           true,
				BackendPathPrefix: "/api",
			},
			wantAnnotation: AGICAnnotationBackendPathPrefix,
			wantValue:      "/api",
		},
		{
			name: "Request Timeout",
			opts: AKSIngressOptions{
				UseAGIC:        true,
				RequestTimeout: 60,
			},
			wantAnnotation: AGICAnnotationRequestTimeout,
			wantValue:      "60",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := AKSIngressAnnotations(tt.opts)

			if val, ok := annotations[tt.wantAnnotation]; !ok {
				t.Errorf("expected annotation %s not found", tt.wantAnnotation)
			} else if !strings.Contains(val, tt.wantValue) {
				t.Errorf("annotation %s = %s, want containing %s", tt.wantAnnotation, val, tt.wantValue)
			}
		})
	}
}

func TestAKSIngressAnnotationsCustom(t *testing.T) {
	opts := AKSIngressOptions{
		UseAGIC: true,
		CustomAnnotations: map[string]string{
			"custom.annotation/key": "custom-value",
		},
	}

	annotations := AKSIngressAnnotations(opts)

	if val, ok := annotations["custom.annotation/key"]; !ok {
		t.Error("custom annotation not found")
	} else if val != "custom-value" {
		t.Errorf("custom annotation value = %s, want custom-value", val)
	}
}

func TestAKSIngressAnnotationsNonAGIC(t *testing.T) {
	opts := AKSIngressOptions{
		UseAGIC:     false,
		SSLRedirect: true,
	}

	annotations := AKSIngressAnnotations(opts)

	// AGIC annotations should not be added when UseAGIC is false
	if _, ok := annotations[AGICAnnotationSSLRedirect]; ok {
		t.Error("AGIC annotation should not be added when UseAGIC is false")
	}
}

func TestAKSIngressClassName(t *testing.T) {
	tests := []struct {
		name             string
		useAGIC          bool
		useWebAppRouting bool
		want             string
	}{
		{
			name:             "AGIC",
			useAGIC:          true,
			useWebAppRouting: false,
			want:             AKSIngressClassAGIC,
		},
		{
			name:             "Web App Routing",
			useAGIC:          false,
			useWebAppRouting: true,
			want:             AKSIngressClassWebAppRouting,
		},
		{
			name:             "Nginx (default)",
			useAGIC:          false,
			useWebAppRouting: false,
			want:             AKSIngressClassNginx,
		},
		{
			name:             "AGIC takes precedence",
			useAGIC:          true,
			useWebAppRouting: true,
			want:             AKSIngressClassAGIC,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AKSIngressClassName(tt.useAGIC, tt.useWebAppRouting)
			if got != tt.want {
				t.Errorf("AKSIngressClassName() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestAKSLoadBalancerAnnotations(t *testing.T) {
	tests := []struct {
		name           string
		opts           AKSLoadBalancerOptions
		wantAnnotation string
		wantValue      string
	}{
		{
			name: "Internal LB",
			opts: AKSLoadBalancerOptions{
				Internal: true,
			},
			wantAnnotation: AKSAnnotationLBInternal,
			wantValue:      "true",
		},
		{
			name: "Internal LB with subnet",
			opts: AKSLoadBalancerOptions{
				Internal: true,
				Subnet:   "my-subnet",
			},
			wantAnnotation: AKSAnnotationLBSubnet,
			wantValue:      "my-subnet",
		},
		{
			name: "Resource Group",
			opts: AKSLoadBalancerOptions{
				ResourceGroup: "my-rg",
			},
			wantAnnotation: AKSAnnotationLBResourceGroup,
			wantValue:      "my-rg",
		},
		{
			name: "Idle Timeout",
			opts: AKSLoadBalancerOptions{
				IdleTimeoutMin: 10,
			},
			wantAnnotation: AKSAnnotationLBIdleTimeout,
			wantValue:      "10",
		},
		{
			name: "Public IP Name",
			opts: AKSLoadBalancerOptions{
				PublicIPName: "my-pip",
			},
			wantAnnotation: AKSAnnotationPIPName,
			wantValue:      "my-pip",
		},
		{
			name: "DNS Label",
			opts: AKSLoadBalancerOptions{
				DNSLabel: "myapp",
			},
			wantAnnotation: AKSAnnotationDNSLabelName,
			wantValue:      "myapp",
		},
		{
			name: "Health Probe Protocol",
			opts: AKSLoadBalancerOptions{
				HealthProbeProto: "tcp",
			},
			wantAnnotation: AKSAnnotationLBHealthProbeProtocol,
			wantValue:      "tcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := AKSLoadBalancerAnnotations(tt.opts)

			if val, ok := annotations[tt.wantAnnotation]; !ok {
				t.Errorf("expected annotation %s not found", tt.wantAnnotation)
			} else if val != tt.wantValue {
				t.Errorf("annotation %s = %s, want %s", tt.wantAnnotation, val, tt.wantValue)
			}
		})
	}
}

func TestAKSNetworkingRecommendation(t *testing.T) {
	tests := []struct {
		name             string
		useCase          string
		wantIngressClass string
		wantServiceType  string
	}{
		{
			name:             "Internal service",
			useCase:          "internal backend service",
			wantIngressClass: AKSIngressClassAGIC,
			wantServiceType:  string(ServiceTypeClusterIP),
		},
		{
			name:             "WAF service",
			useCase:          "public api with waf protection",
			wantIngressClass: AKSIngressClassAGIC,
			wantServiceType:  string(ServiceTypeClusterIP),
		},
		{
			name:             "Public web service",
			useCase:          "public web application",
			wantIngressClass: AKSIngressClassAGIC,
			wantServiceType:  string(ServiceTypeClusterIP),
		},
		{
			name:             "Microservices",
			useCase:          "microservice mesh communication",
			wantIngressClass: "",
			wantServiceType:  string(ServiceTypeClusterIP),
		},
		{
			name:             "TCP service",
			useCase:          "tcp database connection",
			wantIngressClass: "",
			wantServiceType:  string(ServiceTypeLoadBalancer),
		},
		{
			name:             "Default",
			useCase:          "general application",
			wantIngressClass: AKSIngressClassWebAppRouting,
			wantServiceType:  string(ServiceTypeClusterIP),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := AKSNetworkingRecommendation(tt.useCase)

			if rec.IngressClass != tt.wantIngressClass {
				t.Errorf("IngressClass = %s, want %s", rec.IngressClass, tt.wantIngressClass)
			}

			if rec.ServiceType != tt.wantServiceType {
				t.Errorf("ServiceType = %s, want %s", rec.ServiceType, tt.wantServiceType)
			}

			if rec.Reason == "" {
				t.Error("recommendation should have a reason")
			}

			if len(rec.Considerations) == 0 {
				t.Error("recommendation should have considerations")
			}
		})
	}
}

func TestAKSNetworkingNotes(t *testing.T) {
	notes := AKSNetworkingNotes()

	if len(notes) == 0 {
		t.Error("expected at least one networking note")
	}

	notesText := strings.Join(notes, " ")

	expectedTopics := []string{
		"AGIC",
		"Application Gateway",
		"Web App Routing",
		"Azure Load Balancer",
		"internal",
		"CNI",
	}

	for _, topic := range expectedTopics {
		if !strings.Contains(notesText, topic) {
			t.Errorf("networking notes should mention %s", topic)
		}
	}
}

func TestIsAKSIngress(t *testing.T) {
	tests := []struct {
		ingressClass string
		want         bool
	}{
		{AKSIngressClassAGIC, true},
		{AKSIngressClassWebAppRouting, true},
		{AKSIngressClassNginx, true},
		{GKEIngressClass, false},
		{GKEIngressClassInternal, false},
		{"alb", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.ingressClass, func(t *testing.T) {
			got := IsAKSIngress(tt.ingressClass)
			if got != tt.want {
				t.Errorf("IsAKSIngress(%q) = %v, want %v", tt.ingressClass, got, tt.want)
			}
		})
	}
}

func TestIsAKSAnnotation(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{AKSAnnotationLBInternal, true},
		{AKSAnnotationDNSLabelName, true},
		{AGICAnnotationSSLRedirect, true},
		{AGICAnnotationWAFPolicy, true},
		{"kubernetes.azure.com/something", true},
		{GKEAnnotationNEG, false},
		{GKEAnnotationBackendConfig, false},
		{EKSAnnotationLBType, false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsAKSAnnotation(tt.key)
			if got != tt.want {
				t.Errorf("IsAKSAnnotation(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestAGICBackendConfigManifest(t *testing.T) {
	opts := AGICBackendOptions{
		SSLRedirect:               true,
		RequestTimeout:            120,
		ConnectionDraining:        true,
		ConnectionDrainingTimeout: 30,
		CookieBasedAffinity:       true,
		WAFPolicy:                 "/subscriptions/xxx/resourceGroups/rg/providers/wafPolicy",
		HealthProbePath:           "/health",
	}

	manifest := AGICBackendConfigManifest("test-ingress", "default", opts)

	expectedContents := []string{
		"kind: Ingress",
		"name: test-ingress",
		"namespace: default",
		"kubernetes.io/ingress.class: azure/application-gateway",
		"ssl-redirect: \"true\"",
		"request-timeout: \"120\"",
		"connection-draining: \"true\"",
		"connection-draining-timeout: \"30\"",
		"cookie-based-affinity: \"true\"",
		"waf-policy-for-path:",
		"health-probe-path: \"/health\"",
	}

	for _, expected := range expectedContents {
		if !strings.Contains(manifest, expected) {
			t.Errorf("manifest should contain %s", expected)
		}
	}
}

func TestGKENetworkingComparison(t *testing.T) {
	comparison := GKENetworkingComparison()

	if len(comparison) == 0 {
		t.Error("expected networking comparison entries")
	}

	// Verify AKS entries
	aksKeys := []string{"aks_l7_lb", "aks_internal_lb", "aks_waf", "aks_service_mesh", "aks_simple_ingress"}
	for _, key := range aksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify GKE entries
	gkeKeys := []string{"gke_l7_lb", "gke_internal_lb", "gke_waf", "gke_service_mesh", "gke_simple_ingress"}
	for _, key := range gkeKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}

	// Verify EKS entries
	eksKeys := []string{"eks_l7_lb", "eks_internal_lb", "eks_waf", "eks_service_mesh", "eks_simple_ingress"}
	for _, key := range eksKeys {
		if _, ok := comparison[key]; !ok {
			t.Errorf("expected comparison key %s", key)
		}
	}
}

func TestAKSIngressClassConstants(t *testing.T) {
	if AKSIngressClassAGIC != "azure/application-gateway" {
		t.Errorf("AKSIngressClassAGIC = %s, want azure/application-gateway", AKSIngressClassAGIC)
	}

	if AKSIngressClassWebAppRouting != "webapprouting.kubernetes.azure.com" {
		t.Errorf("AKSIngressClassWebAppRouting = %s, want webapprouting.kubernetes.azure.com", AKSIngressClassWebAppRouting)
	}

	if AKSIngressClassNginx != "nginx" {
		t.Errorf("AKSIngressClassNginx = %s, want nginx", AKSIngressClassNginx)
	}
}

func TestAGICAnnotationConstants(t *testing.T) {
	expectedAnnotations := map[string]string{
		"AGICAnnotationBackendPathPrefix":   "appgw.ingress.kubernetes.io/backend-path-prefix",
		"AGICAnnotationSSLRedirect":         "appgw.ingress.kubernetes.io/ssl-redirect",
		"AGICAnnotationWAFPolicy":           "appgw.ingress.kubernetes.io/waf-policy-for-path",
		"AGICAnnotationAppGWSSLCert":        "appgw.ingress.kubernetes.io/appgw-ssl-certificate",
		"AGICAnnotationHealthProbeHostname": "appgw.ingress.kubernetes.io/health-probe-hostname",
		"AGICAnnotationHealthProbePath":     "appgw.ingress.kubernetes.io/health-probe-path",
	}

	constants := map[string]string{
		"AGICAnnotationBackendPathPrefix":   AGICAnnotationBackendPathPrefix,
		"AGICAnnotationSSLRedirect":         AGICAnnotationSSLRedirect,
		"AGICAnnotationWAFPolicy":           AGICAnnotationWAFPolicy,
		"AGICAnnotationAppGWSSLCert":        AGICAnnotationAppGWSSLCert,
		"AGICAnnotationHealthProbeHostname": AGICAnnotationHealthProbeHostname,
		"AGICAnnotationHealthProbePath":     AGICAnnotationHealthProbePath,
	}

	for name, expected := range expectedAnnotations {
		if constants[name] != expected {
			t.Errorf("%s = %s, want %s", name, constants[name], expected)
		}
	}
}

func TestAKSLoadBalancerAnnotationConstants(t *testing.T) {
	if AKSAnnotationLBInternal != "service.beta.kubernetes.io/azure-load-balancer-internal" {
		t.Errorf("AKSAnnotationLBInternal = %s, want service.beta.kubernetes.io/azure-load-balancer-internal", AKSAnnotationLBInternal)
	}

	if !strings.HasPrefix(AKSAnnotationLBResourceGroup, "service.beta.kubernetes.io/azure-") {
		t.Errorf("AKSAnnotationLBResourceGroup should have azure prefix")
	}

	if !strings.HasPrefix(AKSAnnotationDNSLabelName, "service.beta.kubernetes.io/azure-") {
		t.Errorf("AKSAnnotationDNSLabelName should have azure prefix")
	}
}
