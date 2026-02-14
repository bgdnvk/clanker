package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// GetNodesViaKubectl retrieves node information using kubectl.
// This is a shared utility used by multiple providers to get node status
// after kubeconfig has been configured.
func GetNodesViaKubectl(ctx context.Context) ([]NodeInfo, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "nodes", "-o", "json")
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kubectl failed: %w, stderr: %s", err, stderr.String())
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

	if err := json.Unmarshal(stdout.Bytes(), &nodeList); err != nil {
		return nil, err
	}

	nodes := make([]NodeInfo, 0, len(nodeList.Items))
	for _, item := range nodeList.Items {
		node := NodeInfo{
			Name:   item.Metadata.Name,
			Labels: item.Metadata.Labels,
			Role:   "worker",
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

		// Get status from conditions
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

// CountReadyNodes returns the number of ready nodes from a node list
func CountReadyNodes(nodes []NodeInfo) int {
	count := 0
	for _, node := range nodes {
		if node.Status == "Ready" {
			count++
		}
	}
	return count
}

// AllNodesReady checks if all nodes are in Ready state
func AllNodesReady(nodes []NodeInfo) bool {
	if len(nodes) == 0 {
		return false
	}
	for _, node := range nodes {
		if node.Status != "Ready" {
			return false
		}
	}
	return true
}
