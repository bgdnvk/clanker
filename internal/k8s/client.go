package k8s

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Client provides kubectl command execution capabilities
type Client struct {
	kubeconfig string
	context    string
	namespace  string
	debug      bool
}

// NewClient creates a new K8s client
func NewClient(kubeconfig, kubeContext string, debug bool) *Client {
	return &Client{
		kubeconfig: kubeconfig,
		context:    kubeContext,
		namespace:  "default",
		debug:      debug,
	}
}

// BackendKubernetesCredentials represents Kubernetes credentials from the backend
type BackendKubernetesCredentials struct {
	KubeconfigContent string
	ContextName       string
}

// NewClientWithCredentials creates a new K8s client using credentials from the backend
// It writes the kubeconfig to a temp file and returns the client along with the temp file path
// The caller is responsible for cleaning up the temp file using CleanupKubeconfig
func NewClientWithCredentials(creds *BackendKubernetesCredentials, debug bool) (*Client, string, error) {
	if creds == nil {
		return nil, "", fmt.Errorf("credentials cannot be nil")
	}

	if creds.KubeconfigContent == "" {
		return nil, "", fmt.Errorf("kubeconfig content is required")
	}

	// Decode base64 kubeconfig content
	decodedConfig, err := base64DecodeString(creds.KubeconfigContent)
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode kubeconfig: %w", err)
	}

	// Write to temp file
	tmpFile, err := os.CreateTemp("", "kubeconfig-backend-*.yaml")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp kubeconfig file: %w", err)
	}

	if _, err := tmpFile.Write(decodedConfig); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, "", fmt.Errorf("failed to write kubeconfig file: %w", err)
	}
	tmpFile.Close()

	return &Client{
		kubeconfig: tmpFile.Name(),
		context:    creds.ContextName,
		namespace:  "default",
		debug:      debug,
	}, tmpFile.Name(), nil
}

// CleanupKubeconfig removes the temporary kubeconfig file created by NewClientWithCredentials
func CleanupKubeconfig(path string) {
	if path != "" {
		os.Remove(path)
	}
}

// base64DecodeString decodes a base64 encoded string
func base64DecodeString(s string) ([]byte, error) {
	// Try standard base64 first
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return decoded, nil
	}
	// Try URL-safe base64
	decoded, err = base64.URLEncoding.DecodeString(s)
	if err == nil {
		return decoded, nil
	}
	// Try without padding
	decoded, err = base64.RawStdEncoding.DecodeString(s)
	if err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

// SetNamespace sets the default namespace for operations
func (c *Client) SetNamespace(namespace string) {
	c.namespace = namespace
}

// SetContext sets the kubectl context
func (c *Client) SetContext(context string) {
	c.context = context
}

// Run executes a kubectl command and returns the output
func (c *Client) Run(ctx context.Context, args ...string) (string, error) {
	return c.RunWithNamespace(ctx, "", args...)
}

// RunWithNamespace executes a kubectl command in a specific namespace
func (c *Client) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	cmdArgs := c.buildArgs(namespace, args)

	if c.debug {
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

// RunJSON executes a kubectl command and returns JSON output
func (c *Client) RunJSON(ctx context.Context, args ...string) ([]byte, error) {
	argsWithOutput := append(args, "-o", "json")
	output, err := c.Run(ctx, argsWithOutput...)
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

// Apply applies a manifest from a string
func (c *Client) Apply(ctx context.Context, manifest string, namespace string) (string, error) {
	cmdArgs := c.buildArgs(namespace, []string{"apply", "-f", "-"})

	if c.debug {
		fmt.Printf("[kubectl] apply manifest (%d bytes)\n", len(manifest))
	}

	cmd := exec.CommandContext(ctx, "kubectl", cmdArgs...)
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(manifest)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("kubectl apply failed: %w, stderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// Delete deletes resources matching the given args
func (c *Client) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	args := []string{"delete", resourceType, name}
	return c.RunWithNamespace(ctx, namespace, args...)
}

// Get retrieves a resource
func (c *Client) Get(ctx context.Context, resourceType, name, namespace string) (string, error) {
	args := []string{"get", resourceType}
	if name != "" {
		args = append(args, name)
	}
	return c.RunWithNamespace(ctx, namespace, args...)
}

// GetJSON retrieves a resource as JSON
func (c *Client) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	args := []string{"get", resourceType}
	if name != "" {
		args = append(args, name)
	}
	args = append(args, "-o", "json")
	output, err := c.RunWithNamespace(ctx, namespace, args...)
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

// Describe describes a resource
func (c *Client) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	args := []string{"describe", resourceType}
	if name != "" {
		args = append(args, name)
	}
	return c.RunWithNamespace(ctx, namespace, args...)
}

// Logs retrieves logs from a pod
func (c *Client) Logs(ctx context.Context, podName, namespace string, opts LogOptions) (string, error) {
	args := []string{"logs", podName}

	if opts.Container != "" {
		args = append(args, "-c", opts.Container)
	}
	if opts.Follow {
		args = append(args, "-f")
	}
	if opts.Previous {
		args = append(args, "-p")
	}
	if opts.TailLines > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", opts.TailLines))
	}
	if opts.Since != "" {
		args = append(args, "--since", opts.Since)
	}

	return c.RunWithNamespace(ctx, namespace, args...)
}

// LogOptions contains options for log retrieval
type LogOptions struct {
	Container string
	Follow    bool
	Previous  bool
	TailLines int
	Since     string
}

// Scale scales a deployment or statefulset
func (c *Client) Scale(ctx context.Context, resourceType, name, namespace string, replicas int) (string, error) {
	args := []string{"scale", resourceType, name, "--replicas", fmt.Sprintf("%d", replicas)}
	return c.RunWithNamespace(ctx, namespace, args...)
}

// Rollout performs rollout operations
func (c *Client) Rollout(ctx context.Context, action, resourceType, name, namespace string) (string, error) {
	args := []string{"rollout", action, resourceType, name}
	return c.RunWithNamespace(ctx, namespace, args...)
}

// Exec executes a command in a pod
func (c *Client) Exec(ctx context.Context, podName, namespace string, command []string) (string, error) {
	args := []string{"exec", podName, "--"}
	args = append(args, command...)
	return c.RunWithNamespace(ctx, namespace, args...)
}

// PortForward starts port forwarding to a pod
func (c *Client) PortForward(ctx context.Context, podName, namespace string, localPort, remotePort int) (*exec.Cmd, error) {
	args := c.buildArgs(namespace, []string{
		"port-forward", podName,
		fmt.Sprintf("%d:%d", localPort, remotePort),
	})

	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start port forward: %w", err)
	}

	return cmd, nil
}

// Wait waits for a condition on a resource
func (c *Client) Wait(ctx context.Context, resourceType, name, namespace string, condition string, timeout time.Duration) error {
	args := []string{
		"wait", resourceType, name,
		"--for", condition,
		"--timeout", fmt.Sprintf("%ds", int(timeout.Seconds())),
	}

	_, err := c.RunWithNamespace(ctx, namespace, args...)
	return err
}

// GetCurrentContext returns the current kubectl context
func (c *Client) GetCurrentContext(ctx context.Context) (string, error) {
	output, err := c.Run(ctx, "config", "current-context")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// GetContexts returns all available contexts
func (c *Client) GetContexts(ctx context.Context) ([]string, error) {
	output, err := c.Run(ctx, "config", "get-contexts", "-o", "name")
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

// UseContext switches to a different context
func (c *Client) UseContext(ctx context.Context, contextName string) error {
	_, err := c.Run(ctx, "config", "use-context", contextName)
	return err
}

// GetNodes returns cluster nodes
func (c *Client) GetNodes(ctx context.Context) ([]NodeInfo, error) {
	output, err := c.RunJSON(ctx, "get", "nodes")
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

	if err := json.Unmarshal(output, &nodeList); err != nil {
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

// GetNamespaces returns all namespaces
func (c *Client) GetNamespaces(ctx context.Context) ([]string, error) {
	output, err := c.Run(ctx, "get", "namespaces", "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil, err
	}

	namespaces := strings.Fields(output)
	return namespaces, nil
}

// GetPods returns pods in a namespace
func (c *Client) GetPods(ctx context.Context, namespace string) (string, error) {
	return c.RunWithNamespace(ctx, namespace, "get", "pods", "-o", "wide")
}

// GetDeployments returns deployments in a namespace
func (c *Client) GetDeployments(ctx context.Context, namespace string) (string, error) {
	return c.RunWithNamespace(ctx, namespace, "get", "deployments", "-o", "wide")
}

// GetServices returns services in a namespace
func (c *Client) GetServices(ctx context.Context, namespace string) (string, error) {
	return c.RunWithNamespace(ctx, namespace, "get", "services", "-o", "wide")
}

// GetEvents returns events in a namespace
func (c *Client) GetEvents(ctx context.Context, namespace string) (string, error) {
	return c.RunWithNamespace(ctx, namespace, "get", "events", "--sort-by=.metadata.creationTimestamp")
}

// GetClusterInfo returns cluster information
func (c *Client) GetClusterInfo(ctx context.Context) (string, error) {
	return c.Run(ctx, "cluster-info")
}

// GetVersion returns kubectl and server version
func (c *Client) GetVersion(ctx context.Context) (string, error) {
	return c.Run(ctx, "version")
}

// TopNodes executes kubectl top nodes and returns the raw output
func (c *Client) TopNodes(ctx context.Context) (string, error) {
	return c.Run(ctx, "top", "nodes", "--no-headers")
}

// TopNodesWithHeaders executes kubectl top nodes with headers
func (c *Client) TopNodesWithHeaders(ctx context.Context) (string, error) {
	return c.Run(ctx, "top", "nodes")
}

// TopPods executes kubectl top pods in a namespace
func (c *Client) TopPods(ctx context.Context, namespace string, allNamespaces bool) (string, error) {
	args := []string{"top", "pods", "--no-headers"}
	if allNamespaces {
		args = append(args, "--all-namespaces")
		return c.Run(ctx, args...)
	}
	return c.RunWithNamespace(ctx, namespace, args...)
}

// TopPodsWithHeaders executes kubectl top pods with headers
func (c *Client) TopPodsWithHeaders(ctx context.Context, namespace string, allNamespaces bool) (string, error) {
	args := []string{"top", "pods"}
	if allNamespaces {
		args = append(args, "--all-namespaces")
		return c.Run(ctx, args...)
	}
	return c.RunWithNamespace(ctx, namespace, args...)
}

// TopPod executes kubectl top pod for a specific pod
func (c *Client) TopPod(ctx context.Context, podName, namespace string) (string, error) {
	return c.RunWithNamespace(ctx, namespace, "top", "pod", podName, "--no-headers")
}

// TopPodContainers executes kubectl top pods --containers
func (c *Client) TopPodContainers(ctx context.Context, namespace string, allNamespaces bool) (string, error) {
	args := []string{"top", "pods", "--containers", "--no-headers"}
	if allNamespaces {
		args = append(args, "--all-namespaces")
		return c.Run(ctx, args...)
	}
	return c.RunWithNamespace(ctx, namespace, args...)
}

// TopPodContainersForPod executes kubectl top pod --containers for a specific pod
func (c *Client) TopPodContainersForPod(ctx context.Context, podName, namespace string) (string, error) {
	return c.RunWithNamespace(ctx, namespace, "top", "pod", podName, "--containers", "--no-headers")
}

// CheckConnection verifies kubectl can connect to the cluster
func (c *Client) CheckConnection(ctx context.Context) error {
	_, err := c.Run(ctx, "cluster-info")
	return err
}

// buildArgs builds the kubectl command arguments
func (c *Client) buildArgs(namespace string, args []string) []string {
	cmdArgs := make([]string, 0, len(args)+6)

	if c.kubeconfig != "" {
		cmdArgs = append(cmdArgs, "--kubeconfig", c.kubeconfig)
	}

	if c.context != "" {
		cmdArgs = append(cmdArgs, "--context", c.context)
	}

	// Use specified namespace or default
	ns := namespace
	if ns == "" {
		ns = c.namespace
	}
	if ns != "" && ns != "all" {
		cmdArgs = append(cmdArgs, "-n", ns)
	}

	cmdArgs = append(cmdArgs, args...)
	return cmdArgs
}

// IsKubectlAvailable checks if kubectl is installed
func IsKubectlAvailable() bool {
	_, err := exec.LookPath("kubectl")
	return err == nil
}

// GetKubectlVersion returns the kubectl client version
func GetKubectlVersion(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "version", "--client", "--short")
	output, err := cmd.Output()
	if err != nil {
		// Try without --short for newer versions
		cmd = exec.CommandContext(ctx, "kubectl", "version", "--client", "-o", "yaml")
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to get kubectl version: %w", err)
		}
	}
	return strings.TrimSpace(string(output)), nil
}

// RunHelm executes a helm command and returns the output
func (c *Client) RunHelm(ctx context.Context, args ...string) (string, error) {
	return c.RunHelmWithNamespace(ctx, "", args...)
}

// RunHelmWithNamespace executes a helm command in a specific namespace
func (c *Client) RunHelmWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	cmdArgs := c.buildHelmArgs(namespace, args)

	if c.debug {
		fmt.Printf("[helm] %s\n", strings.Join(cmdArgs, " "))
	}

	cmd := exec.CommandContext(ctx, "helm", cmdArgs...)
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("helm command failed: %w, stderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// buildHelmArgs builds the helm command arguments
func (c *Client) buildHelmArgs(namespace string, args []string) []string {
	cmdArgs := make([]string, 0, len(args)+4)

	if c.kubeconfig != "" {
		cmdArgs = append(cmdArgs, "--kubeconfig", c.kubeconfig)
	}

	if c.context != "" {
		cmdArgs = append(cmdArgs, "--kube-context", c.context)
	}

	// Add namespace if specified
	ns := namespace
	if ns == "" {
		ns = c.namespace
	}
	if ns != "" && ns != "all" {
		cmdArgs = append(cmdArgs, "-n", ns)
	}

	cmdArgs = append(cmdArgs, args...)
	return cmdArgs
}

// IsHelmAvailable checks if helm is installed
func IsHelmAvailable() bool {
	_, err := exec.LookPath("helm")
	return err == nil
}

// GetHelmVersion returns the helm client version
func GetHelmVersion(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "helm", "version", "--short")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get helm version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}
