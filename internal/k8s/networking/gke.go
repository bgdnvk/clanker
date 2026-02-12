package networking

import (
	"fmt"
	"strings"
)

// GKEIngressOptions contains options for creating a GKE-optimized ingress
type GKEIngressOptions struct {
	Name             string
	Namespace        string
	Internal         bool
	StaticIPName     string
	ManagedCert      string
	PreSharedCert    string
	AllowHTTP        bool
	EnableNEG        bool
	BackendConfig    string
	CustomAnnotations map[string]string
}

// GKEIngressAnnotations returns the appropriate annotations for a GKE ingress
func GKEIngressAnnotations(opts GKEIngressOptions) map[string]string {
	annotations := make(map[string]string)

	// Static IP
	if opts.StaticIPName != "" {
		if opts.Internal {
			annotations[GKEAnnotationStaticIPRegional] = opts.StaticIPName
		} else {
			annotations[GKEAnnotationStaticIP] = opts.StaticIPName
		}
	}

	// Google-managed certificate
	if opts.ManagedCert != "" {
		annotations["networking.gke.io/managed-certificates"] = opts.ManagedCert
	}

	// Pre-shared certificate
	if opts.PreSharedCert != "" {
		annotations[GKEAnnotationPreSharedCert] = opts.PreSharedCert
	}

	// HTTP handling
	if !opts.AllowHTTP {
		annotations[GKEAnnotationAllowHTTP] = "false"
	}

	// Backend config
	if opts.BackendConfig != "" {
		annotations[GKEAnnotationBackendConfig] = fmt.Sprintf(`{"default": "%s"}`, opts.BackendConfig)
	}

	// Add custom annotations
	for k, v := range opts.CustomAnnotations {
		annotations[k] = v
	}

	return annotations
}

// GKEIngressClass returns the appropriate ingress class for GKE
func GKEIngressClassName(internal bool) string {
	if internal {
		return GKEIngressClassInternal
	}
	return GKEIngressClass
}

// GKELoadBalancerOptions contains options for creating a GKE load balancer service
type GKELoadBalancerOptions struct {
	Internal      bool
	NetworkTier   string // "Premium" or "Standard"
	Subnetwork    string // Required for internal LB
	StaticIP      string
	EnableNEG     bool
	NEGPorts      []int
}

// GKELoadBalancerAnnotations returns the appropriate annotations for a GKE load balancer
func GKELoadBalancerAnnotations(opts GKELoadBalancerOptions) map[string]string {
	annotations := make(map[string]string)

	// Internal vs External
	if opts.Internal {
		annotations[GKELBAnnotationType] = GKELBTypeInternal

		// Subnetwork is required for internal LB
		if opts.Subnetwork != "" {
			annotations[GKELBAnnotationSubnetwork] = opts.Subnetwork
		}
	}

	// Network tier
	if opts.NetworkTier != "" {
		annotations[GKELBAnnotationNetworkTier] = opts.NetworkTier
	}

	// NEG configuration
	if opts.EnableNEG {
		if len(opts.NEGPorts) > 0 {
			// Build exposed_ports JSON
			var ports []string
			for _, port := range opts.NEGPorts {
				ports = append(ports, fmt.Sprintf(`"%d": {}`, port))
			}
			annotations[GKEAnnotationNEG] = fmt.Sprintf(`{"exposed_ports": {%s}}`, strings.Join(ports, ", "))
		} else {
			annotations[GKEAnnotationNEG] = GKENEGTypeIngress
		}
	}

	return annotations
}

// GKENEGAnnotation returns the NEG annotation for container-native load balancing
func GKENEGAnnotation(ports ...int) string {
	if len(ports) == 0 {
		return GKENEGTypeIngress
	}

	var portMappings []string
	for _, port := range ports {
		portMappings = append(portMappings, fmt.Sprintf(`"%d": {}`, port))
	}
	return fmt.Sprintf(`{"exposed_ports": {%s}}`, strings.Join(portMappings, ", "))
}

// GKEBackendConfigAnnotation returns the backend config annotation
func GKEBackendConfigAnnotation(configName string) string {
	return fmt.Sprintf(`{"default": "%s"}`, configName)
}

// GKEBackendConfigAnnotationWithPorts returns backend config with per-port configuration
func GKEBackendConfigAnnotationWithPorts(configs map[int]string) string {
	if len(configs) == 0 {
		return ""
	}

	var parts []string
	for port, config := range configs {
		parts = append(parts, fmt.Sprintf(`"%d": "%s"`, port, config))
	}
	return fmt.Sprintf(`{"ports": {%s}}`, strings.Join(parts, ", "))
}

// GKENetworkingRecommendation returns GKE-specific networking recommendations
func GKENetworkingRecommendation(useCase string) NetworkingRecommendation {
	useCaseLower := strings.ToLower(useCase)

	// Internal services
	if containsAny(useCaseLower, []string{"internal", "private", "vpc", "backend"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeClusterIP),
			IngressClass:  GKEIngressClassInternal,
			UseNEG:        true,
			Reason:        "Internal services should use ClusterIP with internal ingress for VPC-only access",
			Considerations: []string{
				"Use gce-internal ingress class for internal HTTP(S) load balancer",
				"Enable NEG for container-native load balancing",
				"Consider using Service with Internal LoadBalancer for TCP/UDP",
			},
		}
	}

	// Public web services
	if containsAny(useCaseLower, []string{"public", "web", "api", "external", "internet"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeLoadBalancer),
			IngressClass:  GKEIngressClass,
			UseNEG:        true,
			Reason:        "Public services should use external load balancer with NEG for optimal performance",
			Considerations: []string{
				"Use Google-managed SSL certificates for HTTPS",
				"Enable Cloud CDN via BackendConfig for static content",
				"Consider Cloud Armor for DDoS protection",
				"Use Premium network tier for global load balancing",
			},
		}
	}

	// Microservices
	if containsAny(useCaseLower, []string{"microservice", "service mesh", "istio", "grpc"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeClusterIP),
			IngressClass:  "",
			UseNEG:        false,
			Reason:        "Microservices typically use ClusterIP with service mesh for internal communication",
			Considerations: []string{
				"Use Istio or Anthos Service Mesh for advanced traffic management",
				"Consider headless services for direct pod-to-pod communication",
				"gRPC services benefit from HTTP/2 support in NEG",
			},
		}
	}

	// WebSocket or long-lived connections
	if containsAny(useCaseLower, []string{"websocket", "streaming", "long-lived", "realtime"}) {
		return NetworkingRecommendation{
			ServiceType:   string(ServiceTypeLoadBalancer),
			IngressClass:  GKEIngressClass,
			UseNEG:        true,
			Reason:        "WebSocket and streaming services require proper timeout configuration",
			Considerations: []string{
				"Configure BackendConfig with appropriate timeout settings",
				"GKE Ingress supports WebSocket natively",
				"Consider connection draining settings for graceful shutdown",
			},
		}
	}

	// Default recommendation
	return NetworkingRecommendation{
		ServiceType:   string(ServiceTypeClusterIP),
		IngressClass:  GKEIngressClass,
		UseNEG:        true,
		Reason:        "ClusterIP with Ingress provides flexible HTTP(S) routing",
		Considerations: []string{
			"Use Ingress for HTTP(S) traffic routing",
			"Enable NEG for container-native load balancing",
			"Use LoadBalancer service for non-HTTP protocols",
		},
	}
}

// NetworkingRecommendation represents a networking configuration recommendation
type NetworkingRecommendation struct {
	ServiceType    string
	IngressClass   string
	UseNEG         bool
	Reason         string
	Considerations []string
}

// GKEManagedCertificateManifest generates a ManagedCertificate manifest
func GKEManagedCertificateManifest(name, namespace string, domains []string) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: networking.gke.io/v1\n")
	sb.WriteString("kind: ManagedCertificate\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", name))
	if namespace != "" {
		sb.WriteString(fmt.Sprintf("  namespace: %s\n", namespace))
	}
	sb.WriteString("spec:\n")
	sb.WriteString("  domains:\n")
	for _, domain := range domains {
		sb.WriteString(fmt.Sprintf("    - %s\n", domain))
	}

	return sb.String()
}

// GKEBackendConfigManifest generates a BackendConfig manifest
func GKEBackendConfigManifest(name, namespace string, opts GKEBackendConfigOptions) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: cloud.google.com/v1\n")
	sb.WriteString("kind: BackendConfig\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", name))
	if namespace != "" {
		sb.WriteString(fmt.Sprintf("  namespace: %s\n", namespace))
	}
	sb.WriteString("spec:\n")

	// Timeout settings
	if opts.TimeoutSec > 0 {
		sb.WriteString(fmt.Sprintf("  timeoutSec: %d\n", opts.TimeoutSec))
	}

	// Connection draining
	if opts.ConnectionDrainingTimeoutSec > 0 {
		sb.WriteString("  connectionDraining:\n")
		sb.WriteString(fmt.Sprintf("    drainingTimeoutSec: %d\n", opts.ConnectionDrainingTimeoutSec))
	}

	// Session affinity
	if opts.SessionAffinity != "" {
		sb.WriteString("  sessionAffinity:\n")
		sb.WriteString(fmt.Sprintf("    affinityType: \"%s\"\n", opts.SessionAffinity))
		if opts.SessionAffinityTTL > 0 {
			sb.WriteString(fmt.Sprintf("    affinityCookieTtlSec: %d\n", opts.SessionAffinityTTL))
		}
	}

	// CDN
	if opts.EnableCDN {
		sb.WriteString("  cdn:\n")
		sb.WriteString("    enabled: true\n")
		if opts.CDNCachePolicy != "" {
			sb.WriteString(fmt.Sprintf("    cachePolicy:\n      cacheMode: %s\n", opts.CDNCachePolicy))
		}
	}

	// Health check
	if opts.HealthCheckPath != "" {
		sb.WriteString("  healthCheck:\n")
		sb.WriteString(fmt.Sprintf("    requestPath: %s\n", opts.HealthCheckPath))
		if opts.HealthCheckPort > 0 {
			sb.WriteString(fmt.Sprintf("    port: %d\n", opts.HealthCheckPort))
		}
	}

	// Cloud Armor security policy
	if opts.SecurityPolicy != "" {
		sb.WriteString("  securityPolicy:\n")
		sb.WriteString(fmt.Sprintf("    name: \"%s\"\n", opts.SecurityPolicy))
	}

	return sb.String()
}

// GKEBackendConfigOptions contains options for GKE BackendConfig
type GKEBackendConfigOptions struct {
	TimeoutSec                   int
	ConnectionDrainingTimeoutSec int
	SessionAffinity              string // "CLIENT_IP", "GENERATED_COOKIE", "HEADER_FIELD", "HTTP_COOKIE"
	SessionAffinityTTL           int
	EnableCDN                    bool
	CDNCachePolicy               string // "CACHE_ALL_STATIC", "USE_ORIGIN_HEADERS", "FORCE_CACHE_ALL"
	HealthCheckPath              string
	HealthCheckPort              int
	SecurityPolicy               string // Cloud Armor policy name
}

// IsGKEIngress checks if an ingress class is a GKE ingress
func IsGKEIngress(ingressClass string) bool {
	return ingressClass == GKEIngressClass || ingressClass == GKEIngressClassInternal
}

// IsGKEAnnotation checks if an annotation key is a GKE-specific annotation
func IsGKEAnnotation(key string) bool {
	gkeAnnotationPrefixes := []string{
		"cloud.google.com/",
		"networking.gke.io/",
		"ingress.gcp.kubernetes.io/",
	}

	for _, prefix := range gkeAnnotationPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	// Also check kubernetes.io annotations that are GKE-specific
	if strings.HasPrefix(key, "kubernetes.io/ingress.") {
		return true
	}

	return false
}

// GKENetworkingNotes returns important notes about GKE networking
func GKENetworkingNotes() []string {
	return []string{
		"GKE uses Network Endpoint Groups (NEG) for container-native load balancing",
		"Use 'gce' ingress class for external and 'gce-internal' for internal load balancers",
		"ManagedCertificate CRD provides free Google-managed SSL certificates",
		"BackendConfig CRD allows advanced configuration like Cloud CDN, Cloud Armor, and timeouts",
		"Premium network tier provides global load balancing with anycast IPs",
		"Standard network tier is regional and more cost effective for single-region deployments",
		"Internal load balancers require a subnetwork annotation",
	}
}

// containsAny checks if the string contains any of the patterns
func containsAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
