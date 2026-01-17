package workloads

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// DeploymentManager handles deployment-specific operations
type DeploymentManager struct {
	client K8sClient
	debug  bool
}

// NewDeploymentManager creates a new deployment manager
func NewDeploymentManager(client K8sClient, debug bool) *DeploymentManager {
	return &DeploymentManager{
		client: client,
		debug:  debug,
	}
}

// ListDeployments returns all deployments in a namespace
func (m *DeploymentManager) ListDeployments(ctx context.Context, namespace string, allNamespaces bool) ([]DeploymentInfo, error) {
	args := []string{"get", "deployments", "-o", "json"}
	if allNamespaces {
		args = append(args, "-A")
	}

	var output []byte
	var err error

	if allNamespaces {
		var out string
		out, err = m.client.Run(ctx, args...)
		output = []byte(out)
	} else {
		output, err = m.client.GetJSON(ctx, "deployments", "", namespace)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}

	return m.parseDeploymentList(output)
}

// GetDeployment returns details for a specific deployment
func (m *DeploymentManager) GetDeployment(ctx context.Context, name, namespace string) (*DeploymentInfo, error) {
	output, err := m.client.GetJSON(ctx, "deployment", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment %s: %w", name, err)
	}

	return m.parseDeployment(output)
}

// DescribeDeployment returns detailed description of a deployment
func (m *DeploymentManager) DescribeDeployment(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Describe(ctx, "deployment", name, namespace)
}

// GetRolloutStatus returns the rollout status of a deployment
func (m *DeploymentManager) GetRolloutStatus(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Rollout(ctx, "status", "deployment", name, namespace)
}

// GetRolloutHistory returns the rollout history of a deployment
func (m *DeploymentManager) GetRolloutHistory(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Rollout(ctx, "history", "deployment", name, namespace)
}

// Scale scales a deployment to the specified number of replicas
func (m *DeploymentManager) Scale(ctx context.Context, name, namespace string, replicas int) (string, error) {
	return m.client.Scale(ctx, "deployment", name, namespace, replicas)
}

// RolloutRestart triggers a rolling restart of a deployment
func (m *DeploymentManager) RolloutRestart(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Rollout(ctx, "restart", "deployment", name, namespace)
}

// RolloutUndo rolls back a deployment to the previous revision
func (m *DeploymentManager) RolloutUndo(ctx context.Context, name, namespace string, revision int) (string, error) {
	if revision > 0 {
		return m.client.RunWithNamespace(ctx, namespace, "rollout", "undo", "deployment", name, "--to-revision", fmt.Sprintf("%d", revision))
	}
	return m.client.Rollout(ctx, "undo", "deployment", name, namespace)
}

// RolloutPause pauses a deployment rollout
func (m *DeploymentManager) RolloutPause(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Rollout(ctx, "pause", "deployment", name, namespace)
}

// RolloutResume resumes a paused deployment rollout
func (m *DeploymentManager) RolloutResume(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Rollout(ctx, "resume", "deployment", name, namespace)
}

// SetImage updates the container image for a deployment
func (m *DeploymentManager) SetImage(ctx context.Context, name, namespace, container, image string) (string, error) {
	containerImage := fmt.Sprintf("%s=%s", container, image)
	return m.client.RunWithNamespace(ctx, namespace, "set", "image", "deployment/"+name, containerImage)
}

// Delete deletes a deployment
func (m *DeploymentManager) Delete(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Delete(ctx, "deployment", name, namespace)
}

// CreateDeploymentPlan generates a plan for creating a deployment
func (m *DeploymentManager) CreateDeploymentPlan(opts CreateDeploymentOptions) *WorkloadPlan {
	if opts.Namespace == "" {
		opts.Namespace = "default"
	}
	if opts.Replicas <= 0 {
		opts.Replicas = 1
	}

	plan := &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create deployment %s with image %s", opts.Name, opts.Image),
	}

	// Build the kubectl create deployment command
	args := []string{"create", "deployment", opts.Name, "--image", opts.Image, "-n", opts.Namespace}

	if opts.Replicas > 1 {
		args = append(args, "--replicas", fmt.Sprintf("%d", opts.Replicas))
	}

	if opts.Port > 0 {
		args = append(args, "--port", fmt.Sprintf("%d", opts.Port))
	}

	plan.Steps = []WorkloadStep{
		{
			ID:          "create-deployment",
			Description: fmt.Sprintf("Create deployment %s", opts.Name),
			Command:     "kubectl",
			Args:        args,
			Reason:      fmt.Sprintf("Create deployment with %d replicas using image %s", opts.Replicas, opts.Image),
		},
		{
			ID:          "wait-available",
			Description: "Wait for deployment to be available",
			Command:     "kubectl",
			Args:        []string{"wait", "deployment", opts.Name, "-n", opts.Namespace, "--for=condition=Available", "--timeout=120s"},
			Reason:      "Ensure the deployment is ready",
		},
	}

	return plan
}

// ScaleDeploymentPlan generates a plan for scaling a deployment
func (m *DeploymentManager) ScaleDeploymentPlan(name, namespace string, replicas int) *WorkloadPlan {
	if namespace == "" {
		namespace = "default"
	}

	return &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Scale deployment %s to %d replicas", name, replicas),
		Steps: []WorkloadStep{
			{
				ID:          "scale-deployment",
				Description: fmt.Sprintf("Scale deployment %s to %d replicas", name, replicas),
				Command:     "kubectl",
				Args:        []string{"scale", "deployment", name, "-n", namespace, "--replicas", fmt.Sprintf("%d", replicas)},
				Reason:      "Adjust the number of running pods",
			},
			{
				ID:          "verify-scale",
				Description: "Verify scaling completed",
				Command:     "kubectl",
				Args:        []string{"rollout", "status", "deployment", name, "-n", namespace},
				Reason:      "Ensure all replicas are ready",
			},
		},
	}
}

// UpdateImagePlan generates a plan for updating a deployment image
func (m *DeploymentManager) UpdateImagePlan(name, namespace, container, image string) *WorkloadPlan {
	if namespace == "" {
		namespace = "default"
	}

	containerImage := fmt.Sprintf("%s=%s", container, image)

	return &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Update deployment %s image to %s", name, image),
		Steps: []WorkloadStep{
			{
				ID:          "update-image",
				Description: fmt.Sprintf("Update container %s to image %s", container, image),
				Command:     "kubectl",
				Args:        []string{"set", "image", "deployment/" + name, containerImage, "-n", namespace},
				Reason:      "Update the container image",
			},
			{
				ID:          "wait-rollout",
				Description: "Wait for rollout to complete",
				Command:     "kubectl",
				Args:        []string{"rollout", "status", "deployment", name, "-n", namespace},
				Reason:      "Ensure the update completed successfully",
			},
		},
	}
}

// RestartPlan generates a plan for restarting a deployment
func (m *DeploymentManager) RestartPlan(name, namespace string) *WorkloadPlan {
	if namespace == "" {
		namespace = "default"
	}

	return &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Restart deployment %s", name),
		Steps: []WorkloadStep{
			{
				ID:          "restart-deployment",
				Description: fmt.Sprintf("Trigger rolling restart of deployment %s", name),
				Command:     "kubectl",
				Args:        []string{"rollout", "restart", "deployment", name, "-n", namespace},
				Reason:      "Restart all pods with a rolling update",
			},
			{
				ID:          "wait-rollout",
				Description: "Wait for restart to complete",
				Command:     "kubectl",
				Args:        []string{"rollout", "status", "deployment", name, "-n", namespace},
				Reason:      "Ensure all pods have restarted",
			},
		},
	}
}

// RollbackPlan generates a plan for rolling back a deployment
func (m *DeploymentManager) RollbackPlan(name, namespace string, revision int) *WorkloadPlan {
	if namespace == "" {
		namespace = "default"
	}

	args := []string{"rollout", "undo", "deployment", name, "-n", namespace}
	description := fmt.Sprintf("Rollback deployment %s to previous revision", name)

	if revision > 0 {
		args = append(args, "--to-revision", fmt.Sprintf("%d", revision))
		description = fmt.Sprintf("Rollback deployment %s to revision %d", name, revision)
	}

	return &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   description,
		Steps: []WorkloadStep{
			{
				ID:          "rollback-deployment",
				Description: description,
				Command:     "kubectl",
				Args:        args,
				Reason:      "Revert to a previous deployment revision",
			},
			{
				ID:          "wait-rollout",
				Description: "Wait for rollback to complete",
				Command:     "kubectl",
				Args:        []string{"rollout", "status", "deployment", name, "-n", namespace},
				Reason:      "Ensure the rollback completed successfully",
			},
		},
	}
}

// DeletePlan generates a plan for deleting a deployment
func (m *DeploymentManager) DeletePlan(name, namespace string) *WorkloadPlan {
	if namespace == "" {
		namespace = "default"
	}

	return &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete deployment %s", name),
		Steps: []WorkloadStep{
			{
				ID:          "delete-deployment",
				Description: fmt.Sprintf("Delete deployment %s", name),
				Command:     "kubectl",
				Args:        []string{"delete", "deployment", name, "-n", namespace},
				Reason:      "Remove the deployment and its pods",
			},
		},
		Notes: []string{
			"This will delete the deployment and all its pods",
			"This action cannot be undone",
		},
	}
}

// parseDeploymentList parses a deployment list JSON response
func (m *DeploymentManager) parseDeploymentList(data []byte) ([]DeploymentInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse deployment list: %w", err)
	}

	deployments := make([]DeploymentInfo, 0, len(list.Items))
	for _, item := range list.Items {
		dep, err := m.parseDeployment(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[deployments] failed to parse deployment: %v\n", err)
			}
			continue
		}
		deployments = append(deployments, *dep)
	}

	return deployments, nil
}

// parseDeployment parses a single deployment JSON response
func (m *DeploymentManager) parseDeployment(data []byte) (*DeploymentInfo, error) {
	var dep struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Replicas int `json:"replicas"`
			Selector struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"selector"`
			Strategy struct {
				Type          string `json:"type"`
				RollingUpdate struct {
					MaxSurge       interface{} `json:"maxSurge"`
					MaxUnavailable interface{} `json:"maxUnavailable"`
				} `json:"rollingUpdate"`
			} `json:"strategy"`
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			Replicas            int `json:"replicas"`
			ReadyReplicas       int `json:"readyReplicas"`
			AvailableReplicas   int `json:"availableReplicas"`
			UnavailableReplicas int `json:"unavailableReplicas"`
			UpdatedReplicas     int `json:"updatedReplicas"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &dep); err != nil {
		return nil, fmt.Errorf("failed to parse deployment: %w", err)
	}

	// Parse creation timestamp
	var createdAt time.Time
	if dep.Metadata.CreationTimestamp != "" {
		if t, err := time.Parse(time.RFC3339, dep.Metadata.CreationTimestamp); err == nil {
			createdAt = t
		}
	}

	// Calculate age
	age := ""
	if !createdAt.IsZero() {
		age = formatDuration(time.Since(createdAt))
	}

	// Extract images
	var images []string
	for _, c := range dep.Spec.Template.Spec.Containers {
		images = append(images, c.Image)
	}

	// Determine status
	status := "Progressing"
	if dep.Status.AvailableReplicas >= dep.Spec.Replicas && dep.Status.UnavailableReplicas == 0 {
		status = "Available"
	} else if dep.Status.UnavailableReplicas > 0 {
		status = "Degraded"
	}

	// Format strategy values
	maxSurge := formatStrategyValue(dep.Spec.Strategy.RollingUpdate.MaxSurge)
	maxUnavailable := formatStrategyValue(dep.Spec.Strategy.RollingUpdate.MaxUnavailable)

	info := &DeploymentInfo{
		WorkloadInfo: WorkloadInfo{
			Name:      dep.Metadata.Name,
			Namespace: dep.Metadata.Namespace,
			Type:      WorkloadDeployment,
			Replicas:  dep.Spec.Replicas,
			Ready:     dep.Status.ReadyReplicas,
			Available: dep.Status.AvailableReplicas,
			Status:    status,
			Age:       age,
			Images:    images,
			Labels:    dep.Metadata.Labels,
			Selector:  dep.Spec.Selector.MatchLabels,
			CreatedAt: createdAt,
		},
		Strategy:            dep.Spec.Strategy.Type,
		MaxSurge:            maxSurge,
		MaxUnavailable:      maxUnavailable,
		UpdatedReplicas:     dep.Status.UpdatedReplicas,
		ReadyReplicas:       dep.Status.ReadyReplicas,
		AvailableReplicas:   dep.Status.AvailableReplicas,
		UnavailableReplicas: dep.Status.UnavailableReplicas,
	}

	return info, nil
}

// formatDuration formats a duration in a human-readable format
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// formatStrategyValue formats a strategy value (can be int or string)
func formatStrategyValue(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case float64:
		return fmt.Sprintf("%d", int(val))
	case string:
		return val
	default:
		return fmt.Sprintf("%v", val)
	}
}
