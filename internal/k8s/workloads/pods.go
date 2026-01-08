package workloads

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PodManager handles pod-specific operations
type PodManager struct {
	client K8sClient
	debug  bool
}

// NewPodManager creates a new pod manager
func NewPodManager(client K8sClient, debug bool) *PodManager {
	return &PodManager{
		client: client,
		debug:  debug,
	}
}

// ListPods returns all pods in a namespace
func (m *PodManager) ListPods(ctx context.Context, namespace string, opts QueryOptions) ([]PodInfo, error) {
	args := []string{"get", "pods", "-o", "json"}

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
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	return m.parsePodList([]byte(output))
}

// GetPod returns details for a specific pod
func (m *PodManager) GetPod(ctx context.Context, name, namespace string) (*PodInfo, error) {
	output, err := m.client.GetJSON(ctx, "pod", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod %s: %w", name, err)
	}

	return m.parsePod(output)
}

// DescribePod returns detailed description of a pod
func (m *PodManager) DescribePod(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Describe(ctx, "pod", name, namespace)
}

// GetLogs retrieves logs from a pod
func (m *PodManager) GetLogs(ctx context.Context, name, namespace string, opts LogOptions) (string, error) {
	return m.client.Logs(ctx, name, namespace, LogOptionsInternal{
		Container: opts.Container,
		Follow:    opts.Follow,
		Previous:  opts.Previous,
		TailLines: opts.TailLines,
		Since:     opts.Since,
	})
}

// GetPreviousLogs retrieves logs from a previous container instance
func (m *PodManager) GetPreviousLogs(ctx context.Context, name, namespace, container string, tailLines int) (string, error) {
	return m.client.Logs(ctx, name, namespace, LogOptionsInternal{
		Container: container,
		Previous:  true,
		TailLines: tailLines,
	})
}

// GetEvents returns events for a pod
func (m *PodManager) GetEvents(ctx context.Context, name, namespace string) (string, error) {
	return m.client.RunWithNamespace(ctx, namespace, "get", "events",
		"--field-selector", fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", name),
		"--sort-by=.metadata.creationTimestamp")
}

// Exec executes a command in a pod
func (m *PodManager) Exec(ctx context.Context, name, namespace string, opts ExecOptions) (string, error) {
	args := []string{"exec", name}

	if opts.Container != "" {
		args = append(args, "-c", opts.Container)
	}
	if opts.Stdin {
		args = append(args, "-i")
	}
	if opts.TTY {
		args = append(args, "-t")
	}

	args = append(args, "--")
	args = append(args, opts.Command...)

	return m.client.RunWithNamespace(ctx, namespace, args...)
}

// Delete deletes a pod
func (m *PodManager) Delete(ctx context.Context, name, namespace string, force bool) (string, error) {
	args := []string{"delete", "pod", name}
	if force {
		args = append(args, "--force", "--grace-period=0")
	}
	return m.client.RunWithNamespace(ctx, namespace, args...)
}

// DeletePlan generates a plan for deleting a pod
func (m *PodManager) DeletePlan(name, namespace string, force bool) *WorkloadPlan {
	if namespace == "" {
		namespace = "default"
	}

	args := []string{"delete", "pod", name, "-n", namespace}
	notes := []string{"The pod will be deleted"}

	if force {
		args = append(args, "--force", "--grace-period=0")
		notes = append(notes, "Force delete will not wait for graceful termination")
	}

	return &WorkloadPlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete pod %s", name),
		Steps: []WorkloadStep{
			{
				ID:          "delete-pod",
				Description: fmt.Sprintf("Delete pod %s", name),
				Command:     "kubectl",
				Args:        args,
				Reason:      "Remove the pod from the cluster",
			},
		},
		Notes: notes,
	}
}

// GetPodsByLabel returns pods matching a label selector
func (m *PodManager) GetPodsByLabel(ctx context.Context, namespace, labelSelector string) ([]PodInfo, error) {
	return m.ListPods(ctx, namespace, QueryOptions{LabelSelector: labelSelector})
}

// GetPodsByOwner returns pods owned by a specific resource
func (m *PodManager) GetPodsByOwner(ctx context.Context, namespace, ownerKind, ownerName string) ([]PodInfo, error) {
	pods, err := m.ListPods(ctx, namespace, QueryOptions{})
	if err != nil {
		return nil, err
	}

	var filtered []PodInfo
	for _, pod := range pods {
		for _, owner := range pod.Owners {
			if owner.Kind == ownerKind && owner.Name == ownerName {
				filtered = append(filtered, pod)
				break
			}
		}
	}

	return filtered, nil
}

// GetRunningPods returns only running pods in a namespace
func (m *PodManager) GetRunningPods(ctx context.Context, namespace string) ([]PodInfo, error) {
	return m.ListPods(ctx, namespace, QueryOptions{
		FieldSelector: "status.phase=Running",
	})
}

// GetFailedPods returns failed pods in a namespace
func (m *PodManager) GetFailedPods(ctx context.Context, namespace string) ([]PodInfo, error) {
	return m.ListPods(ctx, namespace, QueryOptions{
		FieldSelector: "status.phase=Failed",
	})
}

// GetPendingPods returns pending pods in a namespace
func (m *PodManager) GetPendingPods(ctx context.Context, namespace string) ([]PodInfo, error) {
	return m.ListPods(ctx, namespace, QueryOptions{
		FieldSelector: "status.phase=Pending",
	})
}

// GetPodsWithRestarts returns pods that have restarted
func (m *PodManager) GetPodsWithRestarts(ctx context.Context, namespace string) ([]PodInfo, error) {
	pods, err := m.ListPods(ctx, namespace, QueryOptions{})
	if err != nil {
		return nil, err
	}

	var restarted []PodInfo
	for _, pod := range pods {
		if pod.Restarts > 0 {
			restarted = append(restarted, pod)
		}
	}

	return restarted, nil
}

// parsePodList parses a pod list JSON response
func (m *PodManager) parsePodList(data []byte) ([]PodInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse pod list: %w", err)
	}

	pods := make([]PodInfo, 0, len(list.Items))
	for _, item := range list.Items {
		pod, err := m.parsePod(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[pods] failed to parse pod: %v\n", err)
			}
			continue
		}
		pods = append(pods, *pod)
	}

	return pods, nil
}

// parsePod parses a single pod JSON response
func (m *PodManager) parsePod(data []byte) (*PodInfo, error) {
	var pod struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
			OwnerReferences   []struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"ownerReferences"`
		} `json:"metadata"`
		Spec struct {
			NodeName   string `json:"nodeName"`
			Containers []struct {
				Name  string `json:"name"`
				Image string `json:"image"`
			} `json:"containers"`
		} `json:"spec"`
		Status struct {
			Phase      string `json:"phase"`
			PodIP      string `json:"podIP"`
			HostIP     string `json:"hostIP"`
			StartTime  string `json:"startTime"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
			ContainerStatuses []struct {
				Name         string `json:"name"`
				Ready        bool   `json:"ready"`
				RestartCount int    `json:"restartCount"`
				Image        string `json:"image"`
				State        struct {
					Running *struct{} `json:"running"`
					Waiting *struct {
						Reason  string `json:"reason"`
						Message string `json:"message"`
					} `json:"waiting"`
					Terminated *struct {
						Reason   string `json:"reason"`
						Message  string `json:"message"`
						ExitCode int    `json:"exitCode"`
					} `json:"terminated"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &pod); err != nil {
		return nil, fmt.Errorf("failed to parse pod: %w", err)
	}

	// Parse creation timestamp
	var createdAt time.Time
	if pod.Metadata.CreationTimestamp != "" {
		if t, err := time.Parse(time.RFC3339, pod.Metadata.CreationTimestamp); err == nil {
			createdAt = t
		}
	}

	// Parse start time
	var startedAt *time.Time
	if pod.Status.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, pod.Status.StartTime); err == nil {
			startedAt = &t
		}
	}

	// Calculate age
	age := ""
	if !createdAt.IsZero() {
		age = formatDuration(time.Since(createdAt))
	}

	// Parse containers
	containers := make([]ContainerInfo, 0)
	totalRestarts := 0
	readyCount := 0

	for _, cs := range pod.Status.ContainerStatuses {
		state := "Unknown"
		reason := ""
		message := ""

		if cs.State.Running != nil {
			state = "Running"
		} else if cs.State.Waiting != nil {
			state = "Waiting"
			reason = cs.State.Waiting.Reason
			message = cs.State.Waiting.Message
		} else if cs.State.Terminated != nil {
			state = "Terminated"
			reason = cs.State.Terminated.Reason
			message = cs.State.Terminated.Message
		}

		containers = append(containers, ContainerInfo{
			Name:         cs.Name,
			Image:        cs.Image,
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
			State:        state,
			Reason:       reason,
			Message:      message,
		})

		totalRestarts += cs.RestartCount
		if cs.Ready {
			readyCount++
		}
	}

	// Build ready string
	ready := fmt.Sprintf("%d/%d", readyCount, len(pod.Spec.Containers))

	// Parse owners
	var owners []OwnerRef
	for _, owner := range pod.Metadata.OwnerReferences {
		owners = append(owners, OwnerRef{
			Kind: owner.Kind,
			Name: owner.Name,
		})
	}

	// Determine status
	status := pod.Status.Phase
	for _, container := range containers {
		if container.State == "Waiting" && container.Reason != "" {
			status = container.Reason
			break
		}
	}

	info := &PodInfo{
		Name:       pod.Metadata.Name,
		Namespace:  pod.Metadata.Namespace,
		Status:     status,
		Phase:      pod.Status.Phase,
		Ready:      ready,
		Restarts:   totalRestarts,
		Age:        age,
		IP:         pod.Status.PodIP,
		Node:       pod.Spec.NodeName,
		Containers: containers,
		Labels:     pod.Metadata.Labels,
		Owners:     owners,
		CreatedAt:  createdAt,
		StartedAt:  startedAt,
	}

	return info, nil
}
