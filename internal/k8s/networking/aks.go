package networking

import (
	"fmt"
	"strings"
)

// AKSIngressOptions contains options for creating an AKS-optimized ingress
type AKSIngressOptions struct {
	Name              string
	Namespace         string
	Internal          bool
	UseAGIC           bool
	UseWebAppRouting  bool
	SSLRedirect       bool
	WAFPolicyID       string
	SSLCertName       string
	BackendPathPrefix string
	BackendHostname   string
	RequestTimeout    int
	CustomAnnotations map[string]string
}

// AKSIngressAnnotations returns appropriate annotations for an AKS ingress
func AKSIngressAnnotations(opts AKSIngressOptions) map[string]string {
	annotations := make(map[string]string)

	// AGIC-specific annotations
	if opts.UseAGIC {
		// SSL redirect
		if opts.SSLRedirect {
			annotations[AGICAnnotationSSLRedirect] = "true"
		}

		// WAF policy
		if opts.WAFPolicyID != "" {
			annotations[AGICAnnotationWAFPolicy] = opts.WAFPolicyID
		}

		// SSL certificate
		if opts.SSLCertName != "" {
			annotations[AGICAnnotationAppGWSSLCert] = opts.SSLCertName
		}

		// Backend path prefix
		if opts.BackendPathPrefix != "" {
			annotations[AGICAnnotationBackendPathPrefix] = opts.BackendPathPrefix
		}

		// Backend hostname
		if opts.BackendHostname != "" {
			annotations[AGICAnnotationBackendHostname] = opts.BackendHostname
		}

		// Request timeout
		if opts.RequestTimeout > 0 {
			annotations[AGICAnnotationRequestTimeout] = fmt.Sprintf("%d", opts.RequestTimeout)
		}
	}

	// Add custom annotations
	for k, v := range opts.CustomAnnotations {
		annotations[k] = v
	}

	return annotations
}

// AKSIngressClassName returns the appropriate ingress class for AKS
func AKSIngressClassName(useAGIC bool, useWebAppRouting bool) string {
	if useAGIC {
		return AKSIngressClassAGIC
	}
	if useWebAppRouting {
		return AKSIngressClassWebAppRouting
	}
	return AKSIngressClassNginx
}

// AKSLoadBalancerOptions contains options for creating an AKS load balancer
type AKSLoadBalancerOptions struct {
	Internal         bool
	ResourceGroup    string
	IdleTimeoutMin   int
	PublicIPName     string
	DNSLabel         string
	Subnet           string
	HealthProbeProto string
}

// AKSLoadBalancerAnnotations returns annotations for an AKS load balancer
func AKSLoadBalancerAnnotations(opts AKSLoadBalancerOptions) map[string]string {
	annotations := make(map[string]string)

	// Internal vs External
	if opts.Internal {
		annotations[AKSAnnotationLBInternal] = "true"

		// Subnet is optional for internal LB
		if opts.Subnet != "" {
			annotations[AKSAnnotationLBSubnet] = opts.Subnet
		}
	}

	// Resource group
	if opts.ResourceGroup != "" {
		annotations[AKSAnnotationLBResourceGroup] = opts.ResourceGroup
	}

	// Idle timeout
	if opts.IdleTimeoutMin > 0 {
		annotations[AKSAnnotationLBIdleTimeout] = fmt.Sprintf("%d", opts.IdleTimeoutMin)
	}

	// Public IP name
	if opts.PublicIPName != "" {
		annotations[AKSAnnotationPIPName] = opts.PublicIPName
	}

	// DNS label
	if opts.DNSLabel != "" {
		annotations[AKSAnnotationDNSLabelName] = opts.DNSLabel
	}

	// Health probe protocol
	if opts.HealthProbeProto != "" {
		annotations[AKSAnnotationLBHealthProbeProtocol] = opts.HealthProbeProto
	}

	return annotations
}

// AKSNetworkingRecommendation returns AKS-specific networking recommendations
func AKSNetworkingRecommendation(useCase string) NetworkingRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// Internal services
	if containsAny(useCaseLower, []string{"internal", "private", "vnet", "backend"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeClusterIP),
			IngressClass:  AKSIngressClassAGIC,
			UseNEG:        false,
			Reason:        "Internal services should use ClusterIP with AGIC for private VNet access",
			Considerations: []string{
				"Use internal load balancer annotation for internal TCP/UDP services",
				"AGIC can be configured for internal-only access",
				"Consider Azure Private Link for cross-VNet access",
				"Use Web App Routing for simpler internal HTTP routing",
			},
		}
	}

	// Public web services with WAF
	if containsAny(useCaseLower, []string{"waf", "firewall", "security", "ddos"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeClusterIP),
			IngressClass:  AKSIngressClassAGIC,
			UseNEG:        false,
			Reason:        "Application Gateway provides WAF and DDoS protection for public services",
			Considerations: []string{
				"Configure WAF policy with OWASP rules",
				"Use Application Gateway v2 for autoscaling",
				"Enable Azure DDoS Protection Standard for additional protection",
				"Use managed SSL certificates with Key Vault integration",
			},
		}
	}

	// Public web services
	if containsAny(useCaseLower, []string{"public", "web", "api", "external", "internet"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeClusterIP),
			IngressClass:  AKSIngressClassAGIC,
			UseNEG:        false,
			Reason:        "AGIC with Application Gateway provides L7 load balancing with SSL termination",
			Considerations: []string{
				"Use AGIC for HTTP(S) traffic with Application Gateway features",
				"Enable SSL redirect for HTTPS enforcement",
				"Configure health probes for backend monitoring",
				"Consider Web App Routing for simpler setup without App Gateway",
			},
		}
	}

	// Microservices
	if containsAny(useCaseLower, []string{"microservice", "service mesh", "istio", "grpc", "linkerd"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeClusterIP),
			IngressClass:  "",
			UseNEG:        false,
			Reason:        "Microservices typically use ClusterIP with service mesh for internal communication",
			Considerations: []string{
				"Use Open Service Mesh (OSM) for managed service mesh on AKS",
				"Consider Istio for advanced traffic management",
				"gRPC services work well with OSM and AGIC",
				"Use headless services for direct pod-to-pod communication",
			},
		}
	}

	// WebSocket or long-lived connections
	if containsAny(useCaseLower, []string{"websocket", "streaming", "long-lived", "realtime"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeClusterIP),
			IngressClass:  AKSIngressClassAGIC,
			UseNEG:        false,
			Reason:        "Application Gateway supports WebSocket with proper timeout configuration",
			Considerations: []string{
				"Configure request timeout with appgw.ingress.kubernetes.io/request-timeout",
				"AGIC supports WebSocket natively",
				"Consider connection draining for graceful shutdown",
				"Set appropriate idle timeout for long-lived connections",
			},
		}
	}

	// TCP/UDP services
	if containsAny(useCaseLower, []string{"tcp", "udp", "database", "redis", "mqtt"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeLoadBalancer),
			IngressClass:  "",
			UseNEG:        false,
			Reason:        "TCP/UDP services require Azure Load Balancer (L4)",
			Considerations: []string{
				"Use Standard Load Balancer SKU (default in AKS)",
				"Configure health probes appropriately",
				"Use internal LB annotation for private services",
				"Consider Azure Private Link for secure access",
			},
		}
	}

	// Default recommendation
	return NetworkingRecommendation{
		ServiceType:   string(ServiceTypeClusterIP),
		IngressClass:  AKSIngressClassWebAppRouting,
		UseNEG:        false,
		Reason:        "Web App Routing provides simple ingress for HTTP(S) services",
		Considerations: []string{
			"Web App Routing is an AKS addon for easy ingress setup",
			"Use AGIC for advanced L7 features and WAF",
			"Use LoadBalancer service for non-HTTP protocols",
			"Consider nginx ingress for compatibility with other K8s clusters",
		},
	}
}

// AKSNetworkingNotes returns important notes about AKS networking
func AKSNetworkingNotes() []string {
	return []string{
		"AGIC (Application Gateway Ingress Controller) provides L7 load balancing with WAF",
		"Web App Routing is a simpler AKS addon for basic ingress needs",
		"Azure Load Balancer Standard SKU is default and supports availability zones",
		"Internal load balancer requires azure-load-balancer-internal annotation",
		"Azure CNI networking provides VNet-native pod IPs for better integration",
		"kubenet networking uses NAT for pod-to-VNet communication",
		"Azure Network Policy or Calico can be used for network policies",
		"Private Link enables secure access to AKS from other VNets",
	}
}

// IsAKSIngress checks if an ingress class is an AKS ingress
func IsAKSIngress(ingressClass string) bool {
	return ingressClass == AKSIngressClassAGIC ||
		ingressClass == AKSIngressClassWebAppRouting ||
		ingressClass == AKSIngressClassNginx
}

// IsAKSAnnotation checks if an annotation key is AKS-specific
func IsAKSAnnotation(key string) bool {
	aksAnnotationPrefixes := []string{
		"service.beta.kubernetes.io/azure-",
		"appgw.ingress.kubernetes.io/",
		"kubernetes.azure.com/",
	}

	for _, prefix := range aksAnnotationPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}

// AGICBackendConfigManifest generates a manifest for AGIC backend configuration via annotations
func AGICBackendConfigManifest(name, namespace string, opts AGICBackendOptions) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: networking.k8s.io/v1\n")
	sb.WriteString("kind: Ingress\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", name))
	if namespace != "" {
		sb.WriteString(fmt.Sprintf("  namespace: %s\n", namespace))
	}
	sb.WriteString("  annotations:\n")
	sb.WriteString("    kubernetes.io/ingress.class: azure/application-gateway\n")

	if opts.SSLRedirect {
		sb.WriteString("    appgw.ingress.kubernetes.io/ssl-redirect: \"true\"\n")
	}

	if opts.RequestTimeout > 0 {
		sb.WriteString(fmt.Sprintf("    appgw.ingress.kubernetes.io/request-timeout: \"%d\"\n", opts.RequestTimeout))
	}

	if opts.ConnectionDraining {
		sb.WriteString("    appgw.ingress.kubernetes.io/connection-draining: \"true\"\n")
		if opts.ConnectionDrainingTimeout > 0 {
			sb.WriteString(fmt.Sprintf("    appgw.ingress.kubernetes.io/connection-draining-timeout: \"%d\"\n", opts.ConnectionDrainingTimeout))
		}
	}

	if opts.CookieBasedAffinity {
		sb.WriteString("    appgw.ingress.kubernetes.io/cookie-based-affinity: \"true\"\n")
	}

	if opts.WAFPolicy != "" {
		sb.WriteString(fmt.Sprintf("    appgw.ingress.kubernetes.io/waf-policy-for-path: \"%s\"\n", opts.WAFPolicy))
	}

	if opts.HealthProbePath != "" {
		sb.WriteString(fmt.Sprintf("    appgw.ingress.kubernetes.io/health-probe-path: \"%s\"\n", opts.HealthProbePath))
	}

	return sb.String()
}

// AGICBackendOptions contains options for AGIC backend configuration
type AGICBackendOptions struct {
	SSLRedirect               bool
	RequestTimeout            int
	ConnectionDraining        bool
	ConnectionDrainingTimeout int
	CookieBasedAffinity       bool
	WAFPolicy                 string
	HealthProbePath           string
}

// GKENetworkingComparison returns comparison notes between AKS and GKE networking
func GKENetworkingComparison() map[string]string {
	return map[string]string{
		"aks_l7_lb":         "Application Gateway (AGIC)",
		"gke_l7_lb":         "GKE Ingress (gce)",
		"eks_l7_lb":         "ALB Ingress Controller",
		"aks_internal_lb":   "azure-load-balancer-internal annotation",
		"gke_internal_lb":   "gce-internal ingress class",
		"eks_internal_lb":   "aws-load-balancer-scheme: internal",
		"aks_waf":           "Azure WAF (Application Gateway)",
		"gke_waf":           "Cloud Armor",
		"eks_waf":           "AWS WAF",
		"aks_service_mesh":  "Open Service Mesh (OSM)",
		"gke_service_mesh":  "Anthos Service Mesh",
		"eks_service_mesh":  "App Mesh",
		"aks_simple_ingress": "Web App Routing addon",
		"gke_simple_ingress": "GKE native Ingress",
		"eks_simple_ingress": "AWS Load Balancer Controller",
	}
}

