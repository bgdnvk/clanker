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
