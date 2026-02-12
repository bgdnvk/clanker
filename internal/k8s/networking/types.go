package networking

import (
	"context"
	"time"
)

// K8sClient defines the interface for kubectl operations
// This interface is satisfied by k8s.Client via an adapter
type K8sClient interface {
	Run(ctx context.Context, args ...string) (string, error)
	RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error)
	GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error)
	Describe(ctx context.Context, resourceType, name, namespace string) (string, error)
	Delete(ctx context.Context, resourceType, name, namespace string) (string, error)
	Apply(ctx context.Context, manifest string) (string, error)
}

// ResourceType identifies the type of networking resource
type ResourceType string

const (
	ResourceService       ResourceType = "service"
	ResourceIngress       ResourceType = "ingress"
	ResourceNetworkPolicy ResourceType = "networkpolicy"
	ResourceEndpoint      ResourceType = "endpoint"
)

// ServiceType identifies the type of Kubernetes service
type ServiceType string

const (
	ServiceTypeClusterIP    ServiceType = "ClusterIP"
	ServiceTypeNodePort     ServiceType = "NodePort"
	ServiceTypeLoadBalancer ServiceType = "LoadBalancer"
	ServiceTypeExternalName ServiceType = "ExternalName"
)

// ResponseType indicates the type of response from the sub-agent
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypePlan   ResponseType = "plan"
)

// QueryOptions contains options for networking queries
type QueryOptions struct {
	Namespace     string
	LabelSelector string
	FieldSelector string
	AllNamespaces bool
}

// Response represents the response from the networking sub-agent
type Response struct {
	Type    ResponseType
	Data    interface{}
	Plan    *NetworkingPlan
	Message string
}

// NetworkingPlan represents a plan for networking modifications
type NetworkingPlan struct {
	Version   int              `json:"version"`
	CreatedAt time.Time        `json:"createdAt"`
	Summary   string           `json:"summary"`
	Steps     []NetworkingStep `json:"steps"`
	Notes     []string         `json:"notes,omitempty"`
}

// NetworkingStep represents a single step in a networking plan
type NetworkingStep struct {
	ID          string            `json:"id"`
	Description string            `json:"description"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Manifest    string            `json:"manifest,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	Produces    map[string]string `json:"produces,omitempty"`
	WaitFor     *WaitCondition    `json:"waitFor,omitempty"`
}

// WaitCondition specifies a condition to wait for
type WaitCondition struct {
	Resource  string        `json:"resource"`
	Condition string        `json:"condition"`
	Timeout   time.Duration `json:"timeout"`
}

// ServiceInfo contains service information
type ServiceInfo struct {
	Name         string            `json:"name"`
	Namespace    string            `json:"namespace"`
	Type         ServiceType       `json:"type"`
	ClusterIP    string            `json:"clusterIP"`
	ExternalIP   string            `json:"externalIP,omitempty"`
	ExternalName string            `json:"externalName,omitempty"`
	Ports        []ServicePort     `json:"ports"`
	Selector     map[string]string `json:"selector"`
	Labels       map[string]string `json:"labels"`
	Age          string            `json:"age"`
	CreatedAt    time.Time         `json:"createdAt"`

	// LoadBalancer specific
	LoadBalancerIP      string   `json:"loadBalancerIP,omitempty"`
	LoadBalancerIngress []string `json:"loadBalancerIngress,omitempty"`

	// Session affinity
	SessionAffinity string `json:"sessionAffinity,omitempty"`
}

// ServicePort contains port mapping information
type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Protocol   string `json:"protocol"`
	Port       int    `json:"port"`
	TargetPort string `json:"targetPort"`
	NodePort   int    `json:"nodePort,omitempty"`
}

// IngressInfo contains ingress information
type IngressInfo struct {
	Name             string            `json:"name"`
	Namespace        string            `json:"namespace"`
	IngressClassName string            `json:"ingressClassName,omitempty"`
	Rules            []IngressRule     `json:"rules"`
	TLS              []IngressTLS      `json:"tls,omitempty"`
	Labels           map[string]string `json:"labels"`
	Annotations      map[string]string `json:"annotations"`
	Address          []string          `json:"address,omitempty"`
	Age              string            `json:"age"`
	CreatedAt        time.Time         `json:"createdAt"`
}

// IngressRule contains ingress routing rules
type IngressRule struct {
	Host  string        `json:"host,omitempty"`
	Paths []IngressPath `json:"paths"`
}

// IngressPath contains path routing information
type IngressPath struct {
	Path        string `json:"path"`
	PathType    string `json:"pathType"`
	ServiceName string `json:"serviceName"`
	ServicePort string `json:"servicePort"`
}

// IngressTLS contains TLS configuration
type IngressTLS struct {
	Hosts      []string `json:"hosts"`
	SecretName string   `json:"secretName"`
}

// NetworkPolicyInfo contains network policy information
type NetworkPolicyInfo struct {
	Name        string              `json:"name"`
	Namespace   string              `json:"namespace"`
	PodSelector map[string]string   `json:"podSelector"`
	PolicyTypes []string            `json:"policyTypes"`
	Ingress     []NetworkPolicyRule `json:"ingress,omitempty"`
	Egress      []NetworkPolicyRule `json:"egress,omitempty"`
	Labels      map[string]string   `json:"labels"`
	Age         string              `json:"age"`
	CreatedAt   time.Time           `json:"createdAt"`
}

// NetworkPolicyRule contains ingress or egress rules
type NetworkPolicyRule struct {
	Ports []NetworkPolicyPort `json:"ports,omitempty"`
	From  []NetworkPolicyPeer `json:"from,omitempty"`
	To    []NetworkPolicyPeer `json:"to,omitempty"`
}

// NetworkPolicyPort specifies port for network policy
type NetworkPolicyPort struct {
	Protocol string `json:"protocol,omitempty"`
	Port     string `json:"port,omitempty"`
	EndPort  int    `json:"endPort,omitempty"`
}

// NetworkPolicyPeer specifies peer for network policy
type NetworkPolicyPeer struct {
	PodSelector       map[string]string `json:"podSelector,omitempty"`
	NamespaceSelector map[string]string `json:"namespaceSelector,omitempty"`
	IPBlock           *IPBlock          `json:"ipBlock,omitempty"`
}

// IPBlock specifies CIDR block for network policy
type IPBlock struct {
	CIDR   string   `json:"cidr"`
	Except []string `json:"except,omitempty"`
}

// EndpointInfo contains endpoint information
type EndpointInfo struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Subsets   []EndpointSubset  `json:"subsets"`
	Labels    map[string]string `json:"labels"`
	Age       string            `json:"age"`
	CreatedAt time.Time         `json:"createdAt"`
}

// EndpointSubset contains endpoint addresses and ports
type EndpointSubset struct {
	Addresses         []EndpointAddress `json:"addresses"`
	NotReadyAddresses []EndpointAddress `json:"notReadyAddresses,omitempty"`
	Ports             []EndpointPort    `json:"ports"`
}

// EndpointAddress contains endpoint address details
type EndpointAddress struct {
	IP        string           `json:"ip"`
	Hostname  string           `json:"hostname,omitempty"`
	NodeName  string           `json:"nodeName,omitempty"`
	TargetRef *ObjectReference `json:"targetRef,omitempty"`
}

// EndpointPort contains endpoint port details
type EndpointPort struct {
	Name     string `json:"name,omitempty"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// ObjectReference contains a reference to a Kubernetes object
type ObjectReference struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// CreateServiceOptions contains options for creating a service
type CreateServiceOptions struct {
	Name        string
	Namespace   string
	Type        ServiceType
	Ports       []ServicePortSpec
	Selector    map[string]string
	Labels      map[string]string
	Annotations map[string]string

	// LoadBalancer options
	LoadBalancerIP string

	// ExternalName options
	ExternalName string

	// Session affinity
	SessionAffinity string
}

// ServicePortSpec specifies a port for service creation
type ServicePortSpec struct {
	Name       string
	Protocol   string
	Port       int
	TargetPort int
	NodePort   int
}

// CreateIngressOptions contains options for creating an ingress
type CreateIngressOptions struct {
	Name             string
	Namespace        string
	IngressClassName string
	Rules            []IngressRuleSpec
	TLS              []IngressTLSSpec
	Labels           map[string]string
	Annotations      map[string]string
}

// IngressRuleSpec specifies a rule for ingress creation
type IngressRuleSpec struct {
	Host  string
	Paths []IngressPathSpec
}

// IngressPathSpec specifies a path for ingress creation
type IngressPathSpec struct {
	Path        string
	PathType    string
	ServiceName string
	ServicePort int
}

// IngressTLSSpec specifies TLS for ingress creation
type IngressTLSSpec struct {
	Hosts      []string
	SecretName string
}

// CreateNetworkPolicyOptions contains options for creating a network policy
type CreateNetworkPolicyOptions struct {
	Name         string
	Namespace    string
	PodSelector  map[string]string
	PolicyTypes  []string
	IngressRules []NetworkPolicyRuleSpec
	EgressRules  []NetworkPolicyRuleSpec
	Labels       map[string]string
}

// NetworkPolicyRuleSpec specifies a rule for network policy creation
type NetworkPolicyRuleSpec struct {
	Ports []NetworkPolicyPortSpec
	From  []NetworkPolicyPeerSpec
	To    []NetworkPolicyPeerSpec
}

// NetworkPolicyPortSpec specifies a port for network policy
type NetworkPolicyPortSpec struct {
	Protocol string
	Port     int
	EndPort  int
}

// NetworkPolicyPeerSpec specifies a peer for network policy
type NetworkPolicyPeerSpec struct {
	PodSelector       map[string]string
	NamespaceSelector map[string]string
	CIDR              string
	Except            []string
}

// GKE Ingress classes
const (
	// GKEIngressClass is the default GKE ingress class for external load balancers
	GKEIngressClass = "gce"
	// GKEIngressClassInternal is the GKE ingress class for internal load balancers
	GKEIngressClassInternal = "gce-internal"
)

// GKE Ingress annotations
const (
	// GKEAnnotationNEG enables Network Endpoint Groups for container-native load balancing
	GKEAnnotationNEG = "cloud.google.com/neg"
	// GKEAnnotationBackendConfig specifies the BackendConfig for the service
	GKEAnnotationBackendConfig = "cloud.google.com/backend-config"
	// GKEAnnotationStaticIP specifies a reserved static IP for the ingress
	GKEAnnotationStaticIP = "kubernetes.io/ingress.global-static-ip-name"
	// GKEAnnotationStaticIPRegional specifies a regional static IP for the ingress
	GKEAnnotationStaticIPRegional = "kubernetes.io/ingress.regional-static-ip-name"
	// GKEAnnotationAllowHTTP controls HTTP to HTTPS redirect
	GKEAnnotationAllowHTTP = "kubernetes.io/ingress.allow-http"
	// GKEAnnotationPreSharedCert specifies pre-shared SSL certificates
	GKEAnnotationPreSharedCert = "ingress.gcp.kubernetes.io/pre-shared-cert"
)

// GKE Service annotations
const (
	// GKELBAnnotationType specifies the load balancer type (Internal or External)
	GKELBAnnotationType = "cloud.google.com/load-balancer-type"
	// GKELBAnnotationNetworkTier specifies the network tier (Premium or Standard)
	GKELBAnnotationNetworkTier = "cloud.google.com/network-tier"
	// GKELBAnnotationSubnetwork specifies the subnetwork for internal LB
	GKELBAnnotationSubnetwork = "cloud.google.com/load-balancer-subnetwork"
)

// GKE Network Endpoint Group types
const (
	// GKENEGTypeIngress uses NEG with Ingress for container-native LB
	GKENEGTypeIngress = `{"ingress": true}`
	// GKENEGExposedPorts specifies which ports to expose via NEG
	GKENEGExposedPorts = `{"exposed_ports": {"%d": {}}}`
)

// GKE Load Balancer types
const (
	// GKELBTypeInternal creates an internal TCP/UDP load balancer
	GKELBTypeInternal = "Internal"
	// GKELBTypeExternal is the default external load balancer
	GKELBTypeExternal = "External"
)

// GKE Network tiers
const (
	// GKENetworkTierPremium uses Google's premium network
	GKENetworkTierPremium = "Premium"
	// GKENetworkTierStandard uses standard network tier
	GKENetworkTierStandard = "Standard"
)

// EKS/AWS annotations for comparison
const (
	// EKSAnnotationLBType specifies the load balancer type for AWS
	EKSAnnotationLBType = "service.beta.kubernetes.io/aws-load-balancer-type"
	// EKSAnnotationLBInternal marks the load balancer as internal
	EKSAnnotationLBInternal = "service.beta.kubernetes.io/aws-load-balancer-internal"
	// EKSAnnotationLBScheme specifies internal or internet-facing
	EKSAnnotationLBScheme = "service.beta.kubernetes.io/aws-load-balancer-scheme"
)

// AKS Ingress classes
const (
	// AKSIngressClassAGIC is the Application Gateway Ingress Controller
	AKSIngressClassAGIC = "azure/application-gateway"
	// AKSIngressClassNginx is nginx ingress on AKS
	AKSIngressClassNginx = "nginx"
	// AKSIngressClassWebAppRouting is AKS Web App Routing addon
	AKSIngressClassWebAppRouting = "webapprouting.kubernetes.azure.com"
)

// AKS Service annotations for Azure Load Balancer
const (
	// AKSAnnotationLBInternal creates an internal load balancer
	AKSAnnotationLBInternal = "service.beta.kubernetes.io/azure-load-balancer-internal"
	// AKSAnnotationLBResourceGroup specifies resource group for LB
	AKSAnnotationLBResourceGroup = "service.beta.kubernetes.io/azure-load-balancer-resource-group"
	// AKSAnnotationLBHealthProbeProtocol specifies health probe protocol
	AKSAnnotationLBHealthProbeProtocol = "service.beta.kubernetes.io/azure-load-balancer-health-probe-protocol"
	// AKSAnnotationLBIdleTimeout specifies idle timeout in minutes
	AKSAnnotationLBIdleTimeout = "service.beta.kubernetes.io/azure-load-balancer-tcp-idle-timeout"
	// AKSAnnotationPIPName specifies public IP name
	AKSAnnotationPIPName = "service.beta.kubernetes.io/azure-pip-name"
	// AKSAnnotationDNSLabelName specifies DNS label for public IP
	AKSAnnotationDNSLabelName = "service.beta.kubernetes.io/azure-dns-label-name"
	// AKSAnnotationLBSubnet specifies subnet for internal LB
	AKSAnnotationLBSubnet = "service.beta.kubernetes.io/azure-load-balancer-internal-subnet"
)

// AGIC (Application Gateway Ingress Controller) annotations
const (
	// AGICAnnotationBackendPathPrefix sets backend path prefix
	AGICAnnotationBackendPathPrefix = "appgw.ingress.kubernetes.io/backend-path-prefix"
	// AGICAnnotationSSLRedirect enables SSL redirect
	AGICAnnotationSSLRedirect = "appgw.ingress.kubernetes.io/ssl-redirect"
	// AGICAnnotationWAFPolicy specifies WAF policy resource ID
	AGICAnnotationWAFPolicy = "appgw.ingress.kubernetes.io/waf-policy-for-path"
	// AGICAnnotationAppGWSSLCert specifies SSL certificate name
	AGICAnnotationAppGWSSLCert = "appgw.ingress.kubernetes.io/appgw-ssl-certificate"
	// AGICAnnotationHealthProbeHostname sets health probe hostname
	AGICAnnotationHealthProbeHostname = "appgw.ingress.kubernetes.io/health-probe-hostname"
	// AGICAnnotationHealthProbePath sets health probe path
	AGICAnnotationHealthProbePath = "appgw.ingress.kubernetes.io/health-probe-path"
	// AGICAnnotationBackendHostname sets backend hostname
	AGICAnnotationBackendHostname = "appgw.ingress.kubernetes.io/backend-hostname"
	// AGICAnnotationConnectionDraining enables connection draining
	AGICAnnotationConnectionDraining = "appgw.ingress.kubernetes.io/connection-draining"
	// AGICAnnotationCookieBasedAffinity enables cookie-based session affinity
	AGICAnnotationCookieBasedAffinity = "appgw.ingress.kubernetes.io/cookie-based-affinity"
	// AGICAnnotationRequestTimeout sets request timeout in seconds
	AGICAnnotationRequestTimeout = "appgw.ingress.kubernetes.io/request-timeout"
)
