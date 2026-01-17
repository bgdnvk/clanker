package networking

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// IngressManager handles ingress-specific operations
type IngressManager struct {
	client K8sClient
	debug  bool
}

// NewIngressManager creates a new ingress manager
func NewIngressManager(client K8sClient, debug bool) *IngressManager {
	return &IngressManager{
		client: client,
		debug:  debug,
	}
}

// ListIngresses returns all ingresses in a namespace
func (m *IngressManager) ListIngresses(ctx context.Context, namespace string, opts QueryOptions) ([]IngressInfo, error) {
	args := []string{"get", "ingress", "-o", "json"}

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
		return nil, fmt.Errorf("failed to list ingresses: %w", err)
	}

	return m.parseIngressList([]byte(output))
}

// GetIngress returns details for a specific ingress
func (m *IngressManager) GetIngress(ctx context.Context, name, namespace string) (*IngressInfo, error) {
	output, err := m.client.GetJSON(ctx, "ingress", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get ingress %s: %w", name, err)
	}

	return m.parseIngress(output)
}

// DescribeIngress returns detailed description of an ingress
func (m *IngressManager) DescribeIngress(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Describe(ctx, "ingress", name, namespace)
}

// CreateIngressPlan generates a plan for creating an ingress
func (m *IngressManager) CreateIngressPlan(opts CreateIngressOptions) *NetworkingPlan {
	if opts.Namespace == "" {
		opts.Namespace = "default"
	}

	// Generate ingress manifest
	manifest := m.generateIngressManifest(opts)

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create ingress %s in namespace %s", opts.Name, opts.Namespace),
		Steps: []NetworkingStep{
			{
				ID:          "create-ingress",
				Description: fmt.Sprintf("Create ingress %s", opts.Name),
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Manifest:    manifest,
				Reason:      "Create ingress resource for HTTP routing",
			},
		},
		Notes: []string{
			"Ingress controller must be installed in the cluster",
			"DNS records should point to the ingress controller's external IP",
		},
	}
}

// DeleteIngressPlan generates a plan for deleting an ingress
func (m *IngressManager) DeleteIngressPlan(name, namespace string) *NetworkingPlan {
	if namespace == "" {
		namespace = "default"
	}

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete ingress %s", name),
		Steps: []NetworkingStep{
			{
				ID:          "delete-ingress",
				Description: fmt.Sprintf("Delete ingress %s", name),
				Command:     "kubectl",
				Args:        []string{"delete", "ingress", name, "-n", namespace},
				Reason:      "Remove the ingress from the cluster",
			},
		},
		Notes: []string{
			"HTTP routing for this ingress will stop working",
			"DNS records may need to be updated",
		},
	}
}

// AddIngressRulePlan generates a plan for adding a rule to an existing ingress
func (m *IngressManager) AddIngressRulePlan(name, namespace string, rule IngressRuleSpec) *NetworkingPlan {
	if namespace == "" {
		namespace = "default"
	}

	// Build the patch
	rulePatch := m.buildRulePatch(rule)

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Add routing rule to ingress %s", name),
		Steps: []NetworkingStep{
			{
				ID:          "add-ingress-rule",
				Description: fmt.Sprintf("Add host %s rule to ingress", rule.Host),
				Command:     "kubectl",
				Args:        []string{"patch", "ingress", name, "-n", namespace, "--type=json", "-p", rulePatch},
				Reason:      "Add new HTTP routing rule",
			},
		},
		Notes: []string{
			fmt.Sprintf("New rule will route traffic for host: %s", rule.Host),
		},
	}
}

// AddTLSPlan generates a plan for adding TLS to an ingress
func (m *IngressManager) AddTLSPlan(name, namespace string, tls IngressTLSSpec) *NetworkingPlan {
	if namespace == "" {
		namespace = "default"
	}

	tlsPatch := m.buildTLSPatch(tls)

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Add TLS to ingress %s", name),
		Steps: []NetworkingStep{
			{
				ID:          "add-tls",
				Description: "Configure TLS for ingress",
				Command:     "kubectl",
				Args:        []string{"patch", "ingress", name, "-n", namespace, "--type=json", "-p", tlsPatch},
				Reason:      "Enable HTTPS for the ingress",
			},
		},
		Notes: []string{
			fmt.Sprintf("TLS certificate will be loaded from secret: %s", tls.SecretName),
			"Ensure the TLS secret exists with valid certificate and key",
		},
	}
}

// generateIngressManifest generates a YAML manifest for an ingress
func (m *IngressManager) generateIngressManifest(opts CreateIngressOptions) string {
	manifest := fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  namespace: %s`, opts.Name, opts.Namespace)

	// Add annotations if any
	if len(opts.Annotations) > 0 {
		manifest += "\n  annotations:"
		for k, v := range opts.Annotations {
			manifest += fmt.Sprintf("\n    %s: %q", k, v)
		}
	}

	// Add labels if any
	if len(opts.Labels) > 0 {
		manifest += "\n  labels:"
		for k, v := range opts.Labels {
			manifest += fmt.Sprintf("\n    %s: %q", k, v)
		}
	}

	manifest += "\nspec:"

	// Add ingress class if specified
	if opts.IngressClassName != "" {
		manifest += fmt.Sprintf("\n  ingressClassName: %s", opts.IngressClassName)
	}

	// Add TLS if any
	if len(opts.TLS) > 0 {
		manifest += "\n  tls:"
		for _, tls := range opts.TLS {
			manifest += "\n  - hosts:"
			for _, host := range tls.Hosts {
				manifest += fmt.Sprintf("\n    - %s", host)
			}
			manifest += fmt.Sprintf("\n    secretName: %s", tls.SecretName)
		}
	}

	// Add rules
	if len(opts.Rules) > 0 {
		manifest += "\n  rules:"
		for _, rule := range opts.Rules {
			if rule.Host != "" {
				manifest += fmt.Sprintf("\n  - host: %s", rule.Host)
			} else {
				manifest += "\n  - host: \"\""
			}
			manifest += "\n    http:"
			manifest += "\n      paths:"
			for _, path := range rule.Paths {
				pathType := path.PathType
				if pathType == "" {
					pathType = "Prefix"
				}
				pathVal := path.Path
				if pathVal == "" {
					pathVal = "/"
				}
				manifest += fmt.Sprintf(`
      - path: %s
        pathType: %s
        backend:
          service:
            name: %s
            port:
              number: %d`, pathVal, pathType, path.ServiceName, path.ServicePort)
			}
		}
	}

	return manifest
}

// buildRulePatch builds a JSON patch for adding a rule
func (m *IngressManager) buildRulePatch(rule IngressRuleSpec) string {
	paths := make([]map[string]interface{}, 0, len(rule.Paths))
	for _, p := range rule.Paths {
		pathType := p.PathType
		if pathType == "" {
			pathType = "Prefix"
		}
		pathVal := p.Path
		if pathVal == "" {
			pathVal = "/"
		}
		paths = append(paths, map[string]interface{}{
			"path":     pathVal,
			"pathType": pathType,
			"backend": map[string]interface{}{
				"service": map[string]interface{}{
					"name": p.ServiceName,
					"port": map[string]interface{}{
						"number": p.ServicePort,
					},
				},
			},
		})
	}

	ruleMap := map[string]interface{}{
		"host": rule.Host,
		"http": map[string]interface{}{
			"paths": paths,
		},
	}

	patch := []map[string]interface{}{
		{
			"op":    "add",
			"path":  "/spec/rules/-",
			"value": ruleMap,
		},
	}

	data, _ := json.Marshal(patch)
	return string(data)
}

// buildTLSPatch builds a JSON patch for adding TLS
func (m *IngressManager) buildTLSPatch(tls IngressTLSSpec) string {
	tlsMap := map[string]interface{}{
		"hosts":      tls.Hosts,
		"secretName": tls.SecretName,
	}

	patch := []map[string]interface{}{
		{
			"op":    "add",
			"path":  "/spec/tls/-",
			"value": tlsMap,
		},
	}

	data, _ := json.Marshal(patch)
	return string(data)
}

// parseIngressList parses an ingress list JSON response
func (m *IngressManager) parseIngressList(data []byte) ([]IngressInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse ingress list: %w", err)
	}

	ingresses := make([]IngressInfo, 0, len(list.Items))
	for _, item := range list.Items {
		ing, err := m.parseIngress(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[ingress] failed to parse ingress: %v\n", err)
			}
			continue
		}
		ingresses = append(ingresses, *ing)
	}

	return ingresses, nil
}

// parseIngress parses a single ingress JSON response
func (m *IngressManager) parseIngress(data []byte) (*IngressInfo, error) {
	var ing struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			Annotations       map[string]string `json:"annotations"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			IngressClassName string `json:"ingressClassName"`
			TLS              []struct {
				Hosts      []string `json:"hosts"`
				SecretName string   `json:"secretName"`
			} `json:"tls"`
			Rules []struct {
				Host string `json:"host"`
				HTTP struct {
					Paths []struct {
						Path     string `json:"path"`
						PathType string `json:"pathType"`
						Backend  struct {
							Service struct {
								Name string `json:"name"`
								Port struct {
									Number int    `json:"number,omitempty"`
									Name   string `json:"name,omitempty"`
								} `json:"port"`
							} `json:"service"`
						} `json:"backend"`
					} `json:"paths"`
				} `json:"http"`
			} `json:"rules"`
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

	if err := json.Unmarshal(data, &ing); err != nil {
		return nil, fmt.Errorf("failed to parse ingress: %w", err)
	}

	// Parse creation timestamp
	var createdAt time.Time
	if ing.Metadata.CreationTimestamp != "" {
		if t, err := time.Parse(time.RFC3339, ing.Metadata.CreationTimestamp); err == nil {
			createdAt = t
		}
	}

	// Calculate age
	age := ""
	if !createdAt.IsZero() {
		age = formatDuration(time.Since(createdAt))
	}

	// Parse TLS
	tls := make([]IngressTLS, 0, len(ing.Spec.TLS))
	for _, t := range ing.Spec.TLS {
		tls = append(tls, IngressTLS{
			Hosts:      t.Hosts,
			SecretName: t.SecretName,
		})
	}

	// Parse rules
	rules := make([]IngressRule, 0, len(ing.Spec.Rules))
	for _, r := range ing.Spec.Rules {
		paths := make([]IngressPath, 0, len(r.HTTP.Paths))
		for _, p := range r.HTTP.Paths {
			portStr := ""
			if p.Backend.Service.Port.Number > 0 {
				portStr = fmt.Sprintf("%d", p.Backend.Service.Port.Number)
			} else {
				portStr = p.Backend.Service.Port.Name
			}
			paths = append(paths, IngressPath{
				Path:        p.Path,
				PathType:    p.PathType,
				ServiceName: p.Backend.Service.Name,
				ServicePort: portStr,
			})
		}
		rules = append(rules, IngressRule{
			Host:  r.Host,
			Paths: paths,
		})
	}

	// Parse addresses
	var addresses []string
	for _, lb := range ing.Status.LoadBalancer.Ingress {
		if lb.IP != "" {
			addresses = append(addresses, lb.IP)
		} else if lb.Hostname != "" {
			addresses = append(addresses, lb.Hostname)
		}
	}

	info := &IngressInfo{
		Name:             ing.Metadata.Name,
		Namespace:        ing.Metadata.Namespace,
		IngressClassName: ing.Spec.IngressClassName,
		Rules:            rules,
		TLS:              tls,
		Labels:           ing.Metadata.Labels,
		Annotations:      ing.Metadata.Annotations,
		Address:          addresses,
		Age:              age,
		CreatedAt:        createdAt,
	}

	return info, nil
}
