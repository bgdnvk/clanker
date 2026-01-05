package networking

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ServiceManager handles service-specific operations
type ServiceManager struct {
	client K8sClient
	debug  bool
}

// NewServiceManager creates a new service manager
func NewServiceManager(client K8sClient, debug bool) *ServiceManager {
	return &ServiceManager{
		client: client,
		debug:  debug,
	}
}

// ListServices returns all services in a namespace
func (m *ServiceManager) ListServices(ctx context.Context, namespace string, opts QueryOptions) ([]ServiceInfo, error) {
	args := []string{"get", "services", "-o", "json"}

	if opts.LabelSelector != "" {
		args = append(args, "-l", opts.LabelSelector)
	}
	if opts.FieldSelector != "" {
		args = append(args, "--field-selector", opts.FieldSelector)
	}
	if opts.AllNamespaces {
		args = append(args, "-A")
	}

	var output string
	var err error

	if opts.AllNamespaces {
		output, err = m.client.Run(ctx, args...)
	} else {
		output, err = m.client.RunWithNamespace(ctx, namespace, args...)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}

	return m.parseServiceList([]byte(output))
}

// GetService returns details for a specific service
func (m *ServiceManager) GetService(ctx context.Context, name, namespace string) (*ServiceInfo, error) {
	output, err := m.client.GetJSON(ctx, "service", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get service %s: %w", name, err)
	}

	return m.parseService(output)
}

// DescribeService returns detailed description of a service
func (m *ServiceManager) DescribeService(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Describe(ctx, "service", name, namespace)
}

// ListEndpoints returns all endpoints in a namespace
func (m *ServiceManager) ListEndpoints(ctx context.Context, namespace string, opts QueryOptions) ([]EndpointInfo, error) {
	args := []string{"get", "endpoints", "-o", "json"}

	if opts.LabelSelector != "" {
		args = append(args, "-l", opts.LabelSelector)
	}
	if opts.AllNamespaces {
		args = append(args, "-A")
	}

	var output string
	var err error

	if opts.AllNamespaces {
		output, err = m.client.Run(ctx, args...)
	} else {
		output, err = m.client.RunWithNamespace(ctx, namespace, args...)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list endpoints: %w", err)
	}

	return m.parseEndpointList([]byte(output))
}

// GetEndpoints returns endpoints for a specific service
func (m *ServiceManager) GetEndpoints(ctx context.Context, name, namespace string) (*EndpointInfo, error) {
	output, err := m.client.GetJSON(ctx, "endpoints", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get endpoints %s: %w", name, err)
	}

	return m.parseEndpoint(output)
}

// CreateServicePlan generates a plan for creating a service
func (m *ServiceManager) CreateServicePlan(opts CreateServiceOptions) *NetworkingPlan {
	if opts.Namespace == "" {
		opts.Namespace = "default"
	}

	args := []string{"create", "service", string(opts.Type), opts.Name, "-n", opts.Namespace}

	// Build port specifications
	if len(opts.Ports) > 0 {
		for _, p := range opts.Ports {
			portSpec := fmt.Sprintf("%d:%d", p.Port, p.TargetPort)
			if p.Protocol != "" && p.Protocol != "TCP" {
				portSpec = fmt.Sprintf("%d:%d/%s", p.Port, p.TargetPort, p.Protocol)
			}
			args = append(args, "--tcp", portSpec)
		}
	}

	steps := []NetworkingStep{
		{
			ID:          "create-service",
			Description: fmt.Sprintf("Create %s service %s", opts.Type, opts.Name),
			Command:     "kubectl",
			Args:        args,
			Reason:      "Create the service with specified configuration",
		},
	}

	// Add selector if specified
	if len(opts.Selector) > 0 {
		patchArgs := []string{"patch", "service", opts.Name, "-n", opts.Namespace, "--type=json", "-p"}
		selectorPatch := fmt.Sprintf(`[{"op":"add","path":"/spec/selector","value":%s}]`, toJSON(opts.Selector))
		patchArgs = append(patchArgs, selectorPatch)
		steps = append(steps, NetworkingStep{
			ID:          "add-selector",
			Description: "Add pod selector to service",
			Command:     "kubectl",
			Args:        patchArgs,
			Reason:      "Configure service to route traffic to matching pods",
		})
	}

	notes := []string{
		fmt.Sprintf("Service type: %s", opts.Type),
	}

	if opts.Type == ServiceTypeLoadBalancer {
		notes = append(notes, "LoadBalancer services may take time to provision external IP")
	}
	if opts.Type == ServiceTypeNodePort {
		notes = append(notes, "NodePort will be automatically assigned if not specified")
	}

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create %s service %s in namespace %s", opts.Type, opts.Name, opts.Namespace),
		Steps:     steps,
		Notes:     notes,
	}
}

// DeleteServicePlan generates a plan for deleting a service
func (m *ServiceManager) DeleteServicePlan(name, namespace string) *NetworkingPlan {
	if namespace == "" {
		namespace = "default"
	}

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete service %s", name),
		Steps: []NetworkingStep{
			{
				ID:          "delete-service",
				Description: fmt.Sprintf("Delete service %s", name),
				Command:     "kubectl",
				Args:        []string{"delete", "service", name, "-n", namespace},
				Reason:      "Remove the service from the cluster",
			},
		},
		Notes: []string{
			"Service will no longer route traffic to pods",
			"If this is a LoadBalancer service, the external IP will be released",
		},
	}
}

// ExposeDeploymentPlan generates a plan for exposing a deployment as a service
func (m *ServiceManager) ExposeDeploymentPlan(deployName, namespace string, port int, serviceType ServiceType) *NetworkingPlan {
	if namespace == "" {
		namespace = "default"
	}
	if port == 0 {
		port = 80
	}
	if serviceType == "" {
		serviceType = ServiceTypeClusterIP
	}

	args := []string{"expose", "deployment", deployName, "-n", namespace,
		"--port", fmt.Sprintf("%d", port),
		"--type", string(serviceType),
	}

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Expose deployment %s as %s service on port %d", deployName, serviceType, port),
		Steps: []NetworkingStep{
			{
				ID:          "expose-deployment",
				Description: fmt.Sprintf("Expose deployment %s as a service", deployName),
				Command:     "kubectl",
				Args:        args,
				Reason:      "Create a service to expose the deployment",
			},
		},
		Notes: []string{
			fmt.Sprintf("Service will be created with the same name as the deployment: %s", deployName),
			"Service selector will match the deployment's pod labels",
		},
	}
}

// UpdateServiceTypePlan generates a plan for changing service type
func (m *ServiceManager) UpdateServiceTypePlan(name, namespace string, newType ServiceType) *NetworkingPlan {
	if namespace == "" {
		namespace = "default"
	}

	patchData := fmt.Sprintf(`{"spec":{"type":"%s"}}`, newType)

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Update service %s type to %s", name, newType),
		Steps: []NetworkingStep{
			{
				ID:          "update-service-type",
				Description: fmt.Sprintf("Change service type to %s", newType),
				Command:     "kubectl",
				Args:        []string{"patch", "service", name, "-n", namespace, "-p", patchData},
				Reason:      "Update service exposure type",
			},
		},
		Notes: []string{
			fmt.Sprintf("Service type will be changed to %s", newType),
		},
	}
}

// parseServiceList parses a service list JSON response
func (m *ServiceManager) parseServiceList(data []byte) ([]ServiceInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse service list: %w", err)
	}

	services := make([]ServiceInfo, 0, len(list.Items))
	for _, item := range list.Items {
		svc, err := m.parseService(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[services] failed to parse service: %v\n", err)
			}
			continue
		}
		services = append(services, *svc)
	}

	return services, nil
}

// parseService parses a single service JSON response
func (m *ServiceManager) parseService(data []byte) (*ServiceInfo, error) {
	var svc struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Type                  string `json:"type"`
			ClusterIP             string `json:"clusterIP"`
			ExternalName          string `json:"externalName"`
			SessionAffinity       string `json:"sessionAffinity"`
			LoadBalancerIP        string `json:"loadBalancerIP"`
			Selector              map[string]string `json:"selector"`
			Ports                 []struct {
				Name       string `json:"name"`
				Protocol   string `json:"protocol"`
				Port       int    `json:"port"`
				TargetPort interface{} `json:"targetPort"`
				NodePort   int    `json:"nodePort,omitempty"`
			} `json:"ports"`
		} `json:"spec"`
		Status struct {
			LoadBalancer struct {
				Ingress []struct {
					IP       string `json:"ip,omitempty"`
					Hostname string `json:"hostname,omitempty"`
				} `json:"ingress"`
			} `json:"loadBalancer"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &svc); err != nil {
		return nil, fmt.Errorf("failed to parse service: %w", err)
	}

	// Parse creation timestamp
	var createdAt time.Time
	if svc.Metadata.CreationTimestamp != "" {
		if t, err := time.Parse(time.RFC3339, svc.Metadata.CreationTimestamp); err == nil {
			createdAt = t
		}
	}

	// Calculate age
	age := ""
	if !createdAt.IsZero() {
		age = formatDuration(time.Since(createdAt))
	}

	// Parse ports
	ports := make([]ServicePort, 0, len(svc.Spec.Ports))
	for _, p := range svc.Spec.Ports {
		targetPort := fmt.Sprintf("%v", p.TargetPort)
		ports = append(ports, ServicePort{
			Name:       p.Name,
			Protocol:   p.Protocol,
			Port:       p.Port,
			TargetPort: targetPort,
			NodePort:   p.NodePort,
		})
	}

	// Parse external IP from status
	var externalIP string
	var lbIngress []string
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			lbIngress = append(lbIngress, ing.IP)
			if externalIP == "" {
				externalIP = ing.IP
			}
		} else if ing.Hostname != "" {
			lbIngress = append(lbIngress, ing.Hostname)
			if externalIP == "" {
				externalIP = ing.Hostname
			}
		}
	}

	info := &ServiceInfo{
		Name:                svc.Metadata.Name,
		Namespace:           svc.Metadata.Namespace,
		Type:                ServiceType(svc.Spec.Type),
		ClusterIP:           svc.Spec.ClusterIP,
		ExternalIP:          externalIP,
		ExternalName:        svc.Spec.ExternalName,
		Ports:               ports,
		Selector:            svc.Spec.Selector,
		Labels:              svc.Metadata.Labels,
		Age:                 age,
		CreatedAt:           createdAt,
		LoadBalancerIP:      svc.Spec.LoadBalancerIP,
		LoadBalancerIngress: lbIngress,
		SessionAffinity:     svc.Spec.SessionAffinity,
	}

	return info, nil
}

// parseEndpointList parses an endpoint list JSON response
func (m *ServiceManager) parseEndpointList(data []byte) ([]EndpointInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse endpoint list: %w", err)
	}

	endpoints := make([]EndpointInfo, 0, len(list.Items))
	for _, item := range list.Items {
		ep, err := m.parseEndpoint(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[services] failed to parse endpoint: %v\n", err)
			}
			continue
		}
		endpoints = append(endpoints, *ep)
	}

	return endpoints, nil
}

// parseEndpoint parses a single endpoint JSON response
func (m *ServiceManager) parseEndpoint(data []byte) (*EndpointInfo, error) {
	var ep struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Subsets []struct {
			Addresses []struct {
				IP        string `json:"ip"`
				Hostname  string `json:"hostname,omitempty"`
				NodeName  string `json:"nodeName,omitempty"`
				TargetRef *struct {
					Kind      string `json:"kind"`
					Name      string `json:"name"`
					Namespace string `json:"namespace,omitempty"`
				} `json:"targetRef,omitempty"`
			} `json:"addresses"`
			NotReadyAddresses []struct {
				IP        string `json:"ip"`
				Hostname  string `json:"hostname,omitempty"`
				NodeName  string `json:"nodeName,omitempty"`
				TargetRef *struct {
					Kind      string `json:"kind"`
					Name      string `json:"name"`
					Namespace string `json:"namespace,omitempty"`
				} `json:"targetRef,omitempty"`
			} `json:"notReadyAddresses,omitempty"`
			Ports []struct {
				Name     string `json:"name,omitempty"`
				Port     int    `json:"port"`
				Protocol string `json:"protocol"`
			} `json:"ports"`
		} `json:"subsets"`
	}

	if err := json.Unmarshal(data, &ep); err != nil {
		return nil, fmt.Errorf("failed to parse endpoint: %w", err)
	}

	// Parse creation timestamp
	var createdAt time.Time
	if ep.Metadata.CreationTimestamp != "" {
		if t, err := time.Parse(time.RFC3339, ep.Metadata.CreationTimestamp); err == nil {
			createdAt = t
		}
	}

	// Calculate age
	age := ""
	if !createdAt.IsZero() {
		age = formatDuration(time.Since(createdAt))
	}

	// Parse subsets
	subsets := make([]EndpointSubset, 0, len(ep.Subsets))
	for _, s := range ep.Subsets {
		addresses := make([]EndpointAddress, 0, len(s.Addresses))
		for _, a := range s.Addresses {
			addr := EndpointAddress{
				IP:       a.IP,
				Hostname: a.Hostname,
				NodeName: a.NodeName,
			}
			if a.TargetRef != nil {
				addr.TargetRef = &ObjectReference{
					Kind:      a.TargetRef.Kind,
					Name:      a.TargetRef.Name,
					Namespace: a.TargetRef.Namespace,
				}
			}
			addresses = append(addresses, addr)
		}

		notReady := make([]EndpointAddress, 0, len(s.NotReadyAddresses))
		for _, a := range s.NotReadyAddresses {
			addr := EndpointAddress{
				IP:       a.IP,
				Hostname: a.Hostname,
				NodeName: a.NodeName,
			}
			if a.TargetRef != nil {
				addr.TargetRef = &ObjectReference{
					Kind:      a.TargetRef.Kind,
					Name:      a.TargetRef.Name,
					Namespace: a.TargetRef.Namespace,
				}
			}
			notReady = append(notReady, addr)
		}

		ports := make([]EndpointPort, 0, len(s.Ports))
		for _, p := range s.Ports {
			ports = append(ports, EndpointPort{
				Name:     p.Name,
				Port:     p.Port,
				Protocol: p.Protocol,
			})
		}

		subsets = append(subsets, EndpointSubset{
			Addresses:         addresses,
			NotReadyAddresses: notReady,
			Ports:             ports,
		})
	}

	info := &EndpointInfo{
		Name:      ep.Metadata.Name,
		Namespace: ep.Metadata.Namespace,
		Subsets:   subsets,
		Labels:    ep.Metadata.Labels,
		Age:       age,
		CreatedAt: createdAt,
	}

	return info, nil
}

// toJSON converts a value to a JSON string
func toJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}
