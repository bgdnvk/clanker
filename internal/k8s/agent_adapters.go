package k8s

import (
	"context"

	"github.com/bgdnvk/clanker/internal/k8s/workloads"
)

// clientAdapter wraps Client to implement workloads.K8sClient interface
type clientAdapter struct {
	client *Client
}

func (a *clientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.Run(ctx, args...)
}

func (a *clientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunWithNamespace(ctx, namespace, args...)
}

func (a *clientAdapter) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return a.client.GetJSON(ctx, resourceType, name, namespace)
}

func (a *clientAdapter) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Describe(ctx, resourceType, name, namespace)
}

func (a *clientAdapter) Scale(ctx context.Context, resourceType, name, namespace string, replicas int) (string, error) {
	return a.client.Scale(ctx, resourceType, name, namespace, replicas)
}

func (a *clientAdapter) Rollout(ctx context.Context, action, resourceType, name, namespace string) (string, error) {
	return a.client.Rollout(ctx, action, resourceType, name, namespace)
}

func (a *clientAdapter) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Delete(ctx, resourceType, name, namespace)
}

func (a *clientAdapter) Logs(ctx context.Context, podName, namespace string, opts workloads.LogOptionsInternal) (string, error) {
	return a.client.Logs(ctx, podName, namespace, LogOptions{
		Container: opts.Container,
		Follow:    opts.Follow,
		Previous:  opts.Previous,
		TailLines: opts.TailLines,
		Since:     opts.Since,
	})
}

// networkingClientAdapter wraps Client to implement networking.K8sClient interface
type networkingClientAdapter struct {
	client *Client
}

func (a *networkingClientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.Run(ctx, args...)
}

func (a *networkingClientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunWithNamespace(ctx, namespace, args...)
}

func (a *networkingClientAdapter) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return a.client.GetJSON(ctx, resourceType, name, namespace)
}

func (a *networkingClientAdapter) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Describe(ctx, resourceType, name, namespace)
}

func (a *networkingClientAdapter) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Delete(ctx, resourceType, name, namespace)
}

func (a *networkingClientAdapter) Apply(ctx context.Context, manifest string) (string, error) {
	// Pass empty namespace - kubectl will use the namespace from the manifest
	return a.client.Apply(ctx, manifest, "")
}

// storageClientAdapter wraps Client to implement storage.K8sClient interface
type storageClientAdapter struct {
	client *Client
}

func (a *storageClientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.Run(ctx, args...)
}

func (a *storageClientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunWithNamespace(ctx, namespace, args...)
}

func (a *storageClientAdapter) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return a.client.GetJSON(ctx, resourceType, name, namespace)
}

func (a *storageClientAdapter) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Describe(ctx, resourceType, name, namespace)
}

func (a *storageClientAdapter) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return a.client.Delete(ctx, resourceType, name, namespace)
}

func (a *storageClientAdapter) Apply(ctx context.Context, manifest string) (string, error) {
	// Pass empty namespace - kubectl will use the namespace from the manifest
	return a.client.Apply(ctx, manifest, "")
}

// helmClientAdapter wraps Client to implement helm.HelmClient interface
type helmClientAdapter struct {
	client *Client
}

func (a *helmClientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.RunHelm(ctx, args...)
}

func (a *helmClientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunHelmWithNamespace(ctx, namespace, args...)
}

// sreClientAdapter wraps Client to implement sre.K8sClient interface
type sreClientAdapter struct {
	client *Client
}

func (a *sreClientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.Run(ctx, args...)
}

func (a *sreClientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunWithNamespace(ctx, namespace, args...)
}

func (a *sreClientAdapter) RunJSON(ctx context.Context, args ...string) ([]byte, error) {
	return a.client.RunJSON(ctx, args...)
}

// telemetryClientAdapter wraps Client to implement telemetry.K8sClient interface
type telemetryClientAdapter struct {
	client *Client
}

func (a *telemetryClientAdapter) Run(ctx context.Context, args ...string) (string, error) {
	return a.client.Run(ctx, args...)
}

func (a *telemetryClientAdapter) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return a.client.RunWithNamespace(ctx, namespace, args...)
}

func (a *telemetryClientAdapter) RunJSON(ctx context.Context, args ...string) ([]byte, error) {
	return a.client.RunJSON(ctx, args...)
}

func (a *telemetryClientAdapter) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return a.client.GetJSON(ctx, resourceType, name, namespace)
}
