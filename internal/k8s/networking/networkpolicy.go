package networking

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// NetworkPolicyManager handles network policy operations
type NetworkPolicyManager struct {
	client K8sClient
	debug  bool
}

// NewNetworkPolicyManager creates a new network policy manager
func NewNetworkPolicyManager(client K8sClient, debug bool) *NetworkPolicyManager {
	return &NetworkPolicyManager{
		client: client,
		debug:  debug,
	}
}

// ListNetworkPolicies returns all network policies in a namespace
func (m *NetworkPolicyManager) ListNetworkPolicies(ctx context.Context, namespace string, opts QueryOptions) ([]NetworkPolicyInfo, error) {
	args := []string{"get", "networkpolicy", "-o", "json"}

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
		return nil, fmt.Errorf("failed to list network policies: %w", err)
	}

	return m.parseNetworkPolicyList([]byte(output))
}

// GetNetworkPolicy returns details for a specific network policy
func (m *NetworkPolicyManager) GetNetworkPolicy(ctx context.Context, name, namespace string) (*NetworkPolicyInfo, error) {
	output, err := m.client.GetJSON(ctx, "networkpolicy", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get network policy %s: %w", name, err)
	}

	return m.parseNetworkPolicy(output)
}

// DescribeNetworkPolicy returns detailed description of a network policy
func (m *NetworkPolicyManager) DescribeNetworkPolicy(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Describe(ctx, "networkpolicy", name, namespace)
}

// CreateNetworkPolicyPlan generates a plan for creating a network policy
func (m *NetworkPolicyManager) CreateNetworkPolicyPlan(opts CreateNetworkPolicyOptions) *NetworkingPlan {
	if opts.Namespace == "" {
		opts.Namespace = "default"
	}

	manifest := m.generateNetworkPolicyManifest(opts)

	notes := []string{
		"Network policies require a CNI plugin that supports them (e.g., Calico, Cilium)",
	}

	if len(opts.PolicyTypes) == 0 || containsString(opts.PolicyTypes, "Ingress") {
		notes = append(notes, "Ingress policy will restrict incoming traffic to selected pods")
	}
	if containsString(opts.PolicyTypes, "Egress") {
		notes = append(notes, "Egress policy will restrict outgoing traffic from selected pods")
	}

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create network policy %s in namespace %s", opts.Name, opts.Namespace),
		Steps: []NetworkingStep{
			{
				ID:          "create-networkpolicy",
				Description: fmt.Sprintf("Create network policy %s", opts.Name),
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Manifest:    manifest,
				Reason:      "Define network access rules for pods",
			},
		},
		Notes: notes,
	}
}

// DeleteNetworkPolicyPlan generates a plan for deleting a network policy
func (m *NetworkPolicyManager) DeleteNetworkPolicyPlan(name, namespace string) *NetworkingPlan {
	if namespace == "" {
		namespace = "default"
	}

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete network policy %s", name),
		Steps: []NetworkingStep{
			{
				ID:          "delete-networkpolicy",
				Description: fmt.Sprintf("Delete network policy %s", name),
				Command:     "kubectl",
				Args:        []string{"delete", "networkpolicy", name, "-n", namespace},
				Reason:      "Remove network access restrictions",
			},
		},
		Notes: []string{
			"Pods previously restricted by this policy will have default network access",
		},
	}
}

// DenyAllIngressPlan generates a plan to deny all ingress traffic to a namespace
func (m *NetworkPolicyManager) DenyAllIngressPlan(namespace string) *NetworkingPlan {
	if namespace == "" {
		namespace = "default"
	}

	manifest := fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-all-ingress
  namespace: %s
spec:
  podSelector: {}
  policyTypes:
  - Ingress`, namespace)

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Deny all ingress traffic in namespace %s", namespace),
		Steps: []NetworkingStep{
			{
				ID:          "deny-all-ingress",
				Description: "Create deny-all-ingress network policy",
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Manifest:    manifest,
				Reason:      "Block all incoming traffic to pods in the namespace",
			},
		},
		Notes: []string{
			"All incoming traffic to pods in this namespace will be blocked",
			"Create additional policies to allow specific traffic",
		},
	}
}

// DenyAllEgressPlan generates a plan to deny all egress traffic from a namespace
func (m *NetworkPolicyManager) DenyAllEgressPlan(namespace string) *NetworkingPlan {
	if namespace == "" {
		namespace = "default"
	}

	manifest := fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-all-egress
  namespace: %s
spec:
  podSelector: {}
  policyTypes:
  - Egress`, namespace)

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Deny all egress traffic from namespace %s", namespace),
		Steps: []NetworkingStep{
			{
				ID:          "deny-all-egress",
				Description: "Create deny-all-egress network policy",
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Manifest:    manifest,
				Reason:      "Block all outgoing traffic from pods in the namespace",
			},
		},
		Notes: []string{
			"All outgoing traffic from pods in this namespace will be blocked",
			"This includes DNS lookups, so pods may not resolve service names",
			"Create additional policies to allow specific traffic",
		},
	}
}

// AllowFromNamespacePlan generates a plan to allow traffic from a specific namespace
func (m *NetworkPolicyManager) AllowFromNamespacePlan(targetNamespace, sourceNamespace string, podSelector map[string]string) *NetworkingPlan {
	if targetNamespace == "" {
		targetNamespace = "default"
	}

	name := fmt.Sprintf("allow-from-%s", sourceNamespace)

	opts := CreateNetworkPolicyOptions{
		Name:        name,
		Namespace:   targetNamespace,
		PodSelector: podSelector,
		PolicyTypes: []string{"Ingress"},
		IngressRules: []NetworkPolicyRuleSpec{
			{
				From: []NetworkPolicyPeerSpec{
					{
						NamespaceSelector: map[string]string{
							"kubernetes.io/metadata.name": sourceNamespace,
						},
					},
				},
			},
		},
	}

	manifest := m.generateNetworkPolicyManifest(opts)

	return &NetworkingPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Allow ingress from namespace %s to %s", sourceNamespace, targetNamespace),
		Steps: []NetworkingStep{
			{
				ID:          "allow-from-namespace",
				Description: fmt.Sprintf("Allow traffic from namespace %s", sourceNamespace),
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Manifest:    manifest,
				Reason:      fmt.Sprintf("Allow pods in %s to receive traffic from %s", targetNamespace, sourceNamespace),
			},
		},
		Notes: []string{
			fmt.Sprintf("Traffic from namespace %s will be allowed to selected pods", sourceNamespace),
		},
	}
}

// generateNetworkPolicyManifest generates a YAML manifest for a network policy
func (m *NetworkPolicyManager) generateNetworkPolicyManifest(opts CreateNetworkPolicyOptions) string {
	manifest := fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: %s
  namespace: %s`, opts.Name, opts.Namespace)

	// Add labels if any
	if len(opts.Labels) > 0 {
		manifest += "\n  labels:"
		for k, v := range opts.Labels {
			manifest += fmt.Sprintf("\n    %s: %q", k, v)
		}
	}

	manifest += "\nspec:"

	// Pod selector
	manifest += "\n  podSelector:"
	if len(opts.PodSelector) > 0 {
		manifest += "\n    matchLabels:"
		for k, v := range opts.PodSelector {
			manifest += fmt.Sprintf("\n      %s: %q", k, v)
		}
	} else {
		manifest += " {}"
	}

	// Policy types
	if len(opts.PolicyTypes) > 0 {
		manifest += "\n  policyTypes:"
		for _, pt := range opts.PolicyTypes {
			manifest += fmt.Sprintf("\n  - %s", pt)
		}
	}

	// Ingress rules
	if len(opts.IngressRules) > 0 {
		manifest += "\n  ingress:"
		for _, rule := range opts.IngressRules {
			manifest += "\n  -"
			if len(rule.From) > 0 {
				manifest += "\n    from:"
				for _, from := range rule.From {
					manifest += "\n    -"
					if len(from.PodSelector) > 0 {
						manifest += "\n      podSelector:"
						manifest += "\n        matchLabels:"
						for k, v := range from.PodSelector {
							manifest += fmt.Sprintf("\n          %s: %q", k, v)
						}
					}
					if len(from.NamespaceSelector) > 0 {
						manifest += "\n      namespaceSelector:"
						manifest += "\n        matchLabels:"
						for k, v := range from.NamespaceSelector {
							manifest += fmt.Sprintf("\n          %s: %q", k, v)
						}
					}
					if from.CIDR != "" {
						manifest += "\n      ipBlock:"
						manifest += fmt.Sprintf("\n        cidr: %s", from.CIDR)
						if len(from.Except) > 0 {
							manifest += "\n        except:"
							for _, e := range from.Except {
								manifest += fmt.Sprintf("\n        - %s", e)
							}
						}
					}
				}
			}
			if len(rule.Ports) > 0 {
				manifest += "\n    ports:"
				for _, p := range rule.Ports {
					manifest += "\n    -"
					if p.Protocol != "" {
						manifest += fmt.Sprintf(" protocol: %s", p.Protocol)
					}
					if p.Port > 0 {
						manifest += fmt.Sprintf("\n      port: %d", p.Port)
					}
					if p.EndPort > 0 {
						manifest += fmt.Sprintf("\n      endPort: %d", p.EndPort)
					}
				}
			}
		}
	}

	// Egress rules
	if len(opts.EgressRules) > 0 {
		manifest += "\n  egress:"
		for _, rule := range opts.EgressRules {
			manifest += "\n  -"
			if len(rule.To) > 0 {
				manifest += "\n    to:"
				for _, to := range rule.To {
					manifest += "\n    -"
					if len(to.PodSelector) > 0 {
						manifest += "\n      podSelector:"
						manifest += "\n        matchLabels:"
						for k, v := range to.PodSelector {
							manifest += fmt.Sprintf("\n          %s: %q", k, v)
						}
					}
					if len(to.NamespaceSelector) > 0 {
						manifest += "\n      namespaceSelector:"
						manifest += "\n        matchLabels:"
						for k, v := range to.NamespaceSelector {
							manifest += fmt.Sprintf("\n          %s: %q", k, v)
						}
					}
					if to.CIDR != "" {
						manifest += "\n      ipBlock:"
						manifest += fmt.Sprintf("\n        cidr: %s", to.CIDR)
						if len(to.Except) > 0 {
							manifest += "\n        except:"
							for _, e := range to.Except {
								manifest += fmt.Sprintf("\n        - %s", e)
							}
						}
					}
				}
			}
			if len(rule.Ports) > 0 {
				manifest += "\n    ports:"
				for _, p := range rule.Ports {
					manifest += "\n    -"
					if p.Protocol != "" {
						manifest += fmt.Sprintf(" protocol: %s", p.Protocol)
					}
					if p.Port > 0 {
						manifest += fmt.Sprintf("\n      port: %d", p.Port)
					}
					if p.EndPort > 0 {
						manifest += fmt.Sprintf("\n      endPort: %d", p.EndPort)
					}
				}
			}
		}
	}

	return manifest
}

// parseNetworkPolicyList parses a network policy list JSON response
func (m *NetworkPolicyManager) parseNetworkPolicyList(data []byte) ([]NetworkPolicyInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse network policy list: %w", err)
	}

	policies := make([]NetworkPolicyInfo, 0, len(list.Items))
	for _, item := range list.Items {
		policy, err := m.parseNetworkPolicy(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[networkpolicy] failed to parse policy: %v\n", err)
			}
			continue
		}
		policies = append(policies, *policy)
	}

	return policies, nil
}

// parseNetworkPolicy parses a single network policy JSON response
func (m *NetworkPolicyManager) parseNetworkPolicy(data []byte) (*NetworkPolicyInfo, error) {
	var np struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			PodSelector struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"podSelector"`
			PolicyTypes []string `json:"policyTypes"`
			Ingress     []struct {
				From []struct {
					PodSelector struct {
						MatchLabels map[string]string `json:"matchLabels"`
					} `json:"podSelector,omitempty"`
					NamespaceSelector struct {
						MatchLabels map[string]string `json:"matchLabels"`
					} `json:"namespaceSelector,omitempty"`
					IPBlock *struct {
						CIDR   string   `json:"cidr"`
						Except []string `json:"except"`
					} `json:"ipBlock,omitempty"`
				} `json:"from,omitempty"`
				Ports []struct {
					Protocol string `json:"protocol,omitempty"`
					Port     interface{} `json:"port,omitempty"`
					EndPort  int    `json:"endPort,omitempty"`
				} `json:"ports,omitempty"`
			} `json:"ingress,omitempty"`
			Egress []struct {
				To []struct {
					PodSelector struct {
						MatchLabels map[string]string `json:"matchLabels"`
					} `json:"podSelector,omitempty"`
					NamespaceSelector struct {
						MatchLabels map[string]string `json:"matchLabels"`
					} `json:"namespaceSelector,omitempty"`
					IPBlock *struct {
						CIDR   string   `json:"cidr"`
						Except []string `json:"except"`
					} `json:"ipBlock,omitempty"`
				} `json:"to,omitempty"`
				Ports []struct {
					Protocol string `json:"protocol,omitempty"`
					Port     interface{} `json:"port,omitempty"`
					EndPort  int    `json:"endPort,omitempty"`
				} `json:"ports,omitempty"`
			} `json:"egress,omitempty"`
		} `json:"spec"`
	}

	if err := json.Unmarshal(data, &np); err != nil {
		return nil, fmt.Errorf("failed to parse network policy: %w", err)
	}

	// Parse creation timestamp
	var createdAt time.Time
	if np.Metadata.CreationTimestamp != "" {
		if t, err := time.Parse(time.RFC3339, np.Metadata.CreationTimestamp); err == nil {
			createdAt = t
		}
	}

	// Calculate age
	age := ""
	if !createdAt.IsZero() {
		age = formatDuration(time.Since(createdAt))
	}

	// Parse ingress rules
	ingress := make([]NetworkPolicyRule, 0, len(np.Spec.Ingress))
	for _, i := range np.Spec.Ingress {
		rule := NetworkPolicyRule{}

		// Parse from peers
		for _, f := range i.From {
			peer := NetworkPolicyPeer{}
			if len(f.PodSelector.MatchLabels) > 0 {
				peer.PodSelector = f.PodSelector.MatchLabels
			}
			if len(f.NamespaceSelector.MatchLabels) > 0 {
				peer.NamespaceSelector = f.NamespaceSelector.MatchLabels
			}
			if f.IPBlock != nil {
				peer.IPBlock = &IPBlock{
					CIDR:   f.IPBlock.CIDR,
					Except: f.IPBlock.Except,
				}
			}
			rule.From = append(rule.From, peer)
		}

		// Parse ports
		for _, p := range i.Ports {
			port := NetworkPolicyPort{
				Protocol: p.Protocol,
				EndPort:  p.EndPort,
			}
			if p.Port != nil {
				port.Port = fmt.Sprintf("%v", p.Port)
			}
			rule.Ports = append(rule.Ports, port)
		}

		ingress = append(ingress, rule)
	}

	// Parse egress rules
	egress := make([]NetworkPolicyRule, 0, len(np.Spec.Egress))
	for _, e := range np.Spec.Egress {
		rule := NetworkPolicyRule{}

		// Parse to peers
		for _, t := range e.To {
			peer := NetworkPolicyPeer{}
			if len(t.PodSelector.MatchLabels) > 0 {
				peer.PodSelector = t.PodSelector.MatchLabels
			}
			if len(t.NamespaceSelector.MatchLabels) > 0 {
				peer.NamespaceSelector = t.NamespaceSelector.MatchLabels
			}
			if t.IPBlock != nil {
				peer.IPBlock = &IPBlock{
					CIDR:   t.IPBlock.CIDR,
					Except: t.IPBlock.Except,
				}
			}
			rule.To = append(rule.To, peer)
		}

		// Parse ports
		for _, p := range e.Ports {
			port := NetworkPolicyPort{
				Protocol: p.Protocol,
				EndPort:  p.EndPort,
			}
			if p.Port != nil {
				port.Port = fmt.Sprintf("%v", p.Port)
			}
			rule.Ports = append(rule.Ports, port)
		}

		egress = append(egress, rule)
	}

	info := &NetworkPolicyInfo{
		Name:        np.Metadata.Name,
		Namespace:   np.Metadata.Namespace,
		PodSelector: np.Spec.PodSelector.MatchLabels,
		PolicyTypes: np.Spec.PolicyTypes,
		Ingress:     ingress,
		Egress:      egress,
		Labels:      np.Metadata.Labels,
		Age:         age,
		CreatedAt:   createdAt,
	}

	return info, nil
}

// containsString checks if a slice contains a string
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
