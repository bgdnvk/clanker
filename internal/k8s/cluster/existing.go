package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ExistingProvider connects to already provisioned clusters
type ExistingProvider struct {
	kubeconfig string
	debug      bool
}

// NewExistingProvider creates a provider for existing clusters
func NewExistingProvider(kubeconfig string, debug bool) *ExistingProvider {
	if kubeconfig == "" {
		// Default to standard kubeconfig location
		if home, err := os.UserHomeDir(); err == nil {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}
	return &ExistingProvider{
		kubeconfig: kubeconfig,
		debug:      debug,
	}
}

// Type returns the cluster type
func (p *ExistingProvider) Type() ClusterType {
	return ClusterTypeExisting
}

// Create is not supported for existing clusters
func (p *ExistingProvider) Create(ctx context.Context, opts CreateOptions) (*ClusterInfo, error) {
	return nil, fmt.Errorf("create not supported for existing clusters; use GetCluster to connect")
}

// Delete is not supported for existing clusters
func (p *ExistingProvider) Delete(ctx context.Context, clusterName string) error {
	return fmt.Errorf("delete not supported for existing clusters")
}

// Scale is not directly supported; requires kubectl or provider specific tools
func (p *ExistingProvider) Scale(ctx context.Context, clusterName string, opts ScaleOptions) error {
	return fmt.Errorf("scale not directly supported for existing clusters; use kubectl or the original provisioning tool")
}

// GetKubeconfig returns the kubeconfig path
func (p *ExistingProvider) GetKubeconfig(ctx context.Context, clusterName string) (string, error) {
	if _, err := os.Stat(p.kubeconfig); err != nil {
		return "", fmt.Errorf("kubeconfig not found at %s: %w", p.kubeconfig, err)
	}
	return p.kubeconfig, nil
}

// Health checks cluster health via kubectl
func (p *ExistingProvider) Health(ctx context.Context, clusterName string) (*HealthStatus, error) {
	status := &HealthStatus{
		Components:  make(map[string]string),
		NodeStatus:  make(map[string]string),
		LastChecked: time.Now(),
	}

	// Check connection
	if _, err := p.runKubectl(ctx, clusterName, "cluster-info"); err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("cannot connect to cluster: %v", err)
		return status, nil
	}

	// Get nodes status
	nodes, err := p.getNodes(ctx, clusterName)
	if err != nil {
		status.Healthy = false
		status.Message = fmt.Sprintf("cannot get nodes: %v", err)
		return status, nil
	}

	readyNodes := 0
	for _, node := range nodes {
		status.NodeStatus[node.Name] = node.Status
		if node.Status == "Ready" {
			readyNodes++
		}
	}

	if readyNodes == len(nodes) && len(nodes) > 0 {
		status.Healthy = true
		status.Message = fmt.Sprintf("all %d nodes ready", len(nodes))
	} else {
		status.Healthy = false
		status.Message = fmt.Sprintf("%d/%d nodes ready", readyNodes, len(nodes))
	}

	// Check core components
	componentStatus, err := p.runKubectl(ctx, clusterName, "get", "componentstatuses", "-o", "jsonpath={range .items[*]}{.metadata.name}={.conditions[0].status}{\"\\n\"}{end}")
	if err == nil {
		for _, line := range splitLines(componentStatus) {
			if parts := splitEquals(line); len(parts) == 2 {
				status.Components[parts[0]] = parts[1]
			}
		}
	}

	return status, nil
}

// ListClusters returns clusters from kubeconfig contexts
func (p *ExistingProvider) ListClusters(ctx context.Context) ([]ClusterInfo, error) {
	contexts, err := p.getContexts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get contexts: %w", err)
	}

	clusters := make([]ClusterInfo, 0, len(contexts))
	for _, ctxName := range contexts {
		clusters = append(clusters, ClusterInfo{
			Name:   ctxName,
			Type:   ClusterTypeExisting,
			Status: "available",
		})
	}

	return clusters, nil
}

// GetCluster returns information about a specific cluster
func (p *ExistingProvider) GetCluster(ctx context.Context, clusterName string) (*ClusterInfo, error) {
	// Verify the context exists
	contexts, err := p.getContexts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get contexts: %w", err)
	}

	found := false
	for _, ctxName := range contexts {
		if ctxName == clusterName {
			found = true
			break
		}
	}

	if !found {
		return nil, &ErrClusterNotFound{ClusterName: clusterName}
	}

	info := &ClusterInfo{
		Name:   clusterName,
		Type:   ClusterTypeExisting,
		Status: "unknown",
	}

	// Try to get version
	version, err := p.runKubectl(ctx, clusterName, "version")
	if err == nil {
		info.KubernetesVersion = extractServerVersion(version)
		info.Status = "connected"
	}

	// Try to get nodes
	nodes, err := p.getNodes(ctx, clusterName)
	if err == nil {
		for _, node := range nodes {
			if node.Role == "control-plane" {
				info.ControlPlaneNodes = append(info.ControlPlaneNodes, node)
			} else {
				info.WorkerNodes = append(info.WorkerNodes, node)
			}
		}
	}

	return info, nil
}

// runKubectl executes a kubectl command
func (p *ExistingProvider) runKubectl(ctx context.Context, contextName string, args ...string) (string, error) {
	cmdArgs := make([]string, 0, len(args)+4)

	if p.kubeconfig != "" {
		cmdArgs = append(cmdArgs, "--kubeconfig", p.kubeconfig)
	}
	if contextName != "" {
		cmdArgs = append(cmdArgs, "--context", contextName)
	}
	cmdArgs = append(cmdArgs, args...)

	if p.debug {
		fmt.Printf("[kubectl] %s\n", strings.Join(cmdArgs, " "))
	}

	cmd := exec.CommandContext(ctx, "kubectl", cmdArgs...)
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("kubectl command failed: %w, stderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// getContexts returns all available kubectl contexts
func (p *ExistingProvider) getContexts(ctx context.Context) ([]string, error) {
	output, err := p.runKubectl(ctx, "", "config", "get-contexts", "-o", "name")
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	contexts := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			contexts = append(contexts, line)
		}
	}
	return contexts, nil
}

// getNodes returns cluster nodes
func (p *ExistingProvider) getNodes(ctx context.Context, contextName string) ([]NodeInfo, error) {
	output, err := p.runKubectl(ctx, contextName, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, err
	}

	var nodeList struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Addresses []struct {
					Type    string `json:"type"`
					Address string `json:"address"`
				} `json:"addresses"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal([]byte(output), &nodeList); err != nil {
		return nil, fmt.Errorf("failed to parse nodes: %w", err)
	}

	nodes := make([]NodeInfo, 0, len(nodeList.Items))
	for _, item := range nodeList.Items {
		node := NodeInfo{
			Name:   item.Metadata.Name,
			Labels: item.Metadata.Labels,
		}

		// Determine role
		if _, ok := item.Metadata.Labels["node-role.kubernetes.io/control-plane"]; ok {
			node.Role = "control-plane"
		} else if _, ok := item.Metadata.Labels["node-role.kubernetes.io/master"]; ok {
			node.Role = "control-plane"
		} else {
			node.Role = "worker"
		}

		// Get addresses
		for _, addr := range item.Status.Addresses {
			switch addr.Type {
			case "InternalIP":
				node.InternalIP = addr.Address
			case "ExternalIP":
				node.ExternalIP = addr.Address
			}
		}

		// Get status
		for _, cond := range item.Status.Conditions {
			if cond.Type == "Ready" {
				if cond.Status == "True" {
					node.Status = "Ready"
				} else {
					node.Status = "NotReady"
				}
				break
			}
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// Helper functions

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitEquals(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

func extractServerVersion(versionOutput string) string {
	// Simple extraction; assumes format like "Server Version: v1.28.0"
	lines := splitLines(versionOutput)
	for _, line := range lines {
		if len(line) > 16 && line[:15] == "Server Version:" {
			return line[16:]
		}
	}
	return "unknown"
}
