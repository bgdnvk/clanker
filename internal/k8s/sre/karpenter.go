package sre

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// KarpenterDetector inspects a cluster for Karpenter-managed autoscaling.
// All operations are read-only.
type KarpenterDetector struct {
	client K8sClient
	debug  bool
}

// NewKarpenterDetector returns a detector that uses the supplied kubectl
// client. The client is expected to behave like sre.K8sClient — `Run` and
// `RunJSON` for read-only kubectl invocations.
func NewKarpenterDetector(client K8sClient, debug bool) *KarpenterDetector {
	return &KarpenterDetector{client: client, debug: debug}
}

// KarpenterPresence describes whether Karpenter is installed and what
// resource versions / counts are visible.
type KarpenterPresence struct {
	Installed           bool   `json:"installed"`
	APIGroup            string `json:"apiGroup,omitempty"`  // typically "karpenter.sh"
	NodePoolsAvailable  bool   `json:"nodePoolsAvailable"`  // CRD installed
	NodeClaimsAvailable bool   `json:"nodeClaimsAvailable"` // CRD installed
	Notes               string `json:"notes,omitempty"`
}

// NodePoolSummary is a flattened view of a Karpenter NodePool suitable for
// CLI / JSON consumption. We deliberately avoid pulling in the full karpenter
// API types so this package compiles without a network dependency.
type NodePoolSummary struct {
	Name             string            `json:"name"`
	Namespace        string            `json:"namespace,omitempty"`
	Limits           map[string]string `json:"limits,omitempty"`
	NodeClass        string            `json:"nodeClass,omitempty"`
	Disruption       string            `json:"disruption,omitempty"`
	Weight           int               `json:"weight,omitempty"`
	NodesProvisioned int               `json:"nodesProvisioned"`
	Taints           []string          `json:"taints,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	Age              string            `json:"age,omitempty"`
	CreatedAt        time.Time         `json:"createdAt,omitempty"`
}

// NodeClaimSummary is one Karpenter NodeClaim — the running-instance side of
// a NodePool.
type NodeClaimSummary struct {
	Name       string            `json:"name"`
	NodePool   string            `json:"nodePool,omitempty"`
	NodeName   string            `json:"nodeName,omitempty"`
	Status     string            `json:"status,omitempty"`
	Capacity   map[string]string `json:"capacity,omitempty"`
	InstanceID string            `json:"instanceID,omitempty"`
	CreatedAt  time.Time         `json:"createdAt,omitempty"`
}

// Detect reports whether Karpenter CRDs are installed in the cluster. It
// uses `kubectl api-resources` to look for the karpenter.sh API group, which
// avoids relying on cluster-scoped get permissions.
func (d *KarpenterDetector) Detect(ctx context.Context) (*KarpenterPresence, error) {
	out, err := d.client.Run(ctx, "api-resources", "--api-group=karpenter.sh", "-o", "name")
	if err != nil {
		return &KarpenterPresence{Installed: false, Notes: fmt.Sprintf("api-resources lookup failed: %v", err)}, nil
	}

	presence := &KarpenterPresence{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		presence.Installed = true
		presence.APIGroup = "karpenter.sh"
		switch {
		case strings.HasPrefix(line, "nodepools"):
			presence.NodePoolsAvailable = true
		case strings.HasPrefix(line, "nodeclaims"):
			presence.NodeClaimsAvailable = true
		}
	}
	return presence, nil
}

// ListNodePools returns the NodePools known to the cluster. Returns an empty
// slice (not an error) when Karpenter is not installed, so callers can render
// "no Karpenter resources" without sniffing error strings.
func (d *KarpenterDetector) ListNodePools(ctx context.Context) ([]NodePoolSummary, error) {
	raw, err := d.client.RunJSON(ctx, "get", "nodepools.karpenter.sh", "-A", "-o", "json")
	if err != nil {
		// Karpenter not installed → return empty, no error.
		if isMissingResource(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list Karpenter NodePools: %w", err)
	}

	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse NodePool list: %w", err)
	}

	pools := make([]NodePoolSummary, 0, len(list.Items))
	for _, item := range list.Items {
		summary, err := parseNodePool(item)
		if err != nil {
			if d.debug {
				// stderr so we don't corrupt stdout when the caller is consuming
				// `-o json` output.
				fmt.Fprintf(os.Stderr, "[karpenter] skipping unparseable NodePool: %v\n", err)
			}
			continue
		}
		pools = append(pools, summary)
	}
	return pools, nil
}

// ListNodeClaims returns the running NodeClaims (one per Karpenter-provisioned
// node). Behaviour around uninstalled Karpenter mirrors ListNodePools.
func (d *KarpenterDetector) ListNodeClaims(ctx context.Context) ([]NodeClaimSummary, error) {
	raw, err := d.client.RunJSON(ctx, "get", "nodeclaims.karpenter.sh", "-A", "-o", "json")
	if err != nil {
		if isMissingResource(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list Karpenter NodeClaims: %w", err)
	}

	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse NodeClaim list: %w", err)
	}

	claims := make([]NodeClaimSummary, 0, len(list.Items))
	for _, item := range list.Items {
		summary, err := parseNodeClaim(item)
		if err != nil {
			if d.debug {
				fmt.Fprintf(os.Stderr, "[karpenter] skipping unparseable NodeClaim: %v\n", err)
			}
			continue
		}
		claims = append(claims, summary)
	}
	return claims, nil
}

func parseNodePool(data []byte) (NodePoolSummary, error) {
	var np struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Limits   map[string]string `json:"limits"`
			Weight   int               `json:"weight"`
			Template struct {
				Spec struct {
					NodeClassRef struct {
						Name string `json:"name"`
					} `json:"nodeClassRef"`
					Taints []struct {
						Key    string `json:"key"`
						Effect string `json:"effect"`
					} `json:"taints"`
				} `json:"spec"`
			} `json:"template"`
			Disruption struct {
				ConsolidationPolicy string `json:"consolidationPolicy"`
			} `json:"disruption"`
		} `json:"spec"`
		Status struct {
			Resources map[string]string `json:"resources"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &np); err != nil {
		return NodePoolSummary{}, err
	}

	out := NodePoolSummary{
		Name:       np.Metadata.Name,
		Namespace:  np.Metadata.Namespace,
		Limits:     np.Spec.Limits,
		NodeClass:  np.Spec.Template.Spec.NodeClassRef.Name,
		Disruption: np.Spec.Disruption.ConsolidationPolicy,
		Weight:     np.Spec.Weight,
		Labels:     np.Metadata.Labels,
	}

	for _, t := range np.Spec.Template.Spec.Taints {
		out.Taints = append(out.Taints, fmt.Sprintf("%s:%s", t.Key, t.Effect))
	}

	if t, err := time.Parse(time.RFC3339, np.Metadata.CreationTimestamp); err == nil {
		out.CreatedAt = t
		out.Age = humanAge(time.Since(t))
	}

	return out, nil
}

func parseNodeClaim(data []byte) (NodeClaimSummary, error) {
	var nc struct {
		Metadata struct {
			Name              string            `json:"name"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Status struct {
			Capacity   map[string]string `json:"capacity"`
			NodeName   string            `json:"nodeName"`
			ProviderID string            `json:"providerID"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &nc); err != nil {
		return NodeClaimSummary{}, err
	}

	status := "Pending"
	for _, c := range nc.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			status = "Ready"
			break
		}
	}

	out := NodeClaimSummary{
		Name:     nc.Metadata.Name,
		NodePool: nc.Metadata.Labels["karpenter.sh/nodepool"],
		NodeName: nc.Status.NodeName,
		Status:   status,
		Capacity: nc.Status.Capacity,
	}
	out.InstanceID = providerInstanceID(nc.Status.ProviderID)
	if t, err := time.Parse(time.RFC3339, nc.Metadata.CreationTimestamp); err == nil {
		out.CreatedAt = t
	}
	return out, nil
}

// providerInstanceID strips the "aws:///<az>/" prefix from a Kubernetes
// providerID, returning just the instance identifier.
func providerInstanceID(providerID string) string {
	if providerID == "" {
		return ""
	}
	idx := strings.LastIndex(providerID, "/")
	if idx < 0 {
		return providerID
	}
	return providerID[idx+1:]
}

// isMissingResource reports whether the kubectl error means the requested
// CRD / resource KIND is unknown (Karpenter not installed). We deliberately
// do NOT match "no resources found" — that string also appears for an empty
// but installed CRD, which would mis-classify a working Karpenter cluster
// with zero NodePools as "not installed".
func isMissingResource(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "the server doesn't have a resource type") ||
		strings.Contains(msg, "doesn't have a resource") ||
		strings.Contains(msg, "no matches for kind")
}

// humanAge is a minimal duration→string formatter for the "AGE" column.
// kubectl-style: 5m, 3h, 2d, 14d. Exactly 24h is rendered as "1d" rather
// than "24h" to match `kubectl get` conventions.
func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		days := int(d.Hours()) / 24
		if days < 1 {
			days = 1
		}
		return fmt.Sprintf("%dd", days)
	}
}
