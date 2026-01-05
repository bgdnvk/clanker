package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PVCManager handles PersistentVolumeClaim operations
type PVCManager struct {
	client K8sClient
	debug  bool
}

// NewPVCManager creates a new PVC manager
func NewPVCManager(client K8sClient, debug bool) *PVCManager {
	return &PVCManager{
		client: client,
		debug:  debug,
	}
}

// ListPVCs lists PersistentVolumeClaims in a namespace
func (m *PVCManager) ListPVCs(ctx context.Context, namespace string, opts QueryOptions) ([]PVCInfo, error) {
	args := []string{"get", "pvc", "-o", "json"}

	if opts.AllNamespaces {
		args = append(args, "--all-namespaces")
	}

	if opts.LabelSelector != "" {
		args = append(args, "-l", opts.LabelSelector)
	}

	var output string
	var err error

	if opts.AllNamespaces {
		output, err = m.client.Run(ctx, args...)
	} else {
		output, err = m.client.RunWithNamespace(ctx, namespace, args...)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list PVCs: %w", err)
	}

	return m.parsePVCList([]byte(output))
}

// GetPVC gets a specific PersistentVolumeClaim
func (m *PVCManager) GetPVC(ctx context.Context, name, namespace string) (*PVCInfo, error) {
	data, err := m.client.GetJSON(ctx, "pvc", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get PVC %s: %w", name, err)
	}

	return m.parsePVC(data)
}

// DescribePVC describes a PersistentVolumeClaim
func (m *PVCManager) DescribePVC(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Describe(ctx, "pvc", name, namespace)
}

// CreatePVCPlan creates a plan for creating a PersistentVolumeClaim
func (m *PVCManager) CreatePVCPlan(opts CreatePVCOptions) *StoragePlan {
	manifest := m.generatePVCManifest(opts)

	notes := []string{
		fmt.Sprintf("PVC will request %s storage", opts.Storage),
		fmt.Sprintf("Access modes: %s", strings.Join(opts.AccessModes, ", ")),
	}

	if opts.StorageClassName != "" {
		notes = append(notes, fmt.Sprintf("Using StorageClass: %s", opts.StorageClassName))
	}

	if opts.VolumeName != "" {
		notes = append(notes, fmt.Sprintf("Binding to specific PV: %s", opts.VolumeName))
	}

	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create PersistentVolumeClaim %s in namespace %s", opts.Name, opts.Namespace),
		Steps: []StorageStep{
			{
				ID:          "create-pvc",
				Description: fmt.Sprintf("Create PersistentVolumeClaim %s", opts.Name),
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Manifest:    manifest,
				Reason:      fmt.Sprintf("Create PVC requesting %s storage", opts.Storage),
				WaitFor: &WaitCondition{
					Resource:  fmt.Sprintf("pvc/%s", opts.Name),
					Condition: "jsonpath={.status.phase}=Bound",
					Timeout:   60 * time.Second,
				},
			},
		},
		Notes: notes,
	}
}

// DeletePVCPlan creates a plan for deleting a PersistentVolumeClaim
func (m *PVCManager) DeletePVCPlan(name, namespace string) *StoragePlan {
	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete PersistentVolumeClaim %s", name),
		Steps: []StorageStep{
			{
				ID:          "delete-pvc",
				Description: fmt.Sprintf("Delete PersistentVolumeClaim %s", name),
				Command:     "kubectl",
				Args:        []string{"delete", "pvc", name, "-n", namespace},
				Reason:      "Remove the PersistentVolumeClaim from the namespace",
			},
		},
		Notes: []string{
			"Ensure no pods are using this PVC before deletion",
			"The underlying PV will be handled according to its reclaim policy",
		},
	}
}

// ResizePVCPlan creates a plan for resizing a PersistentVolumeClaim
func (m *PVCManager) ResizePVCPlan(name, namespace, newSize string) *StoragePlan {
	patchJSON := fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":"%s"}}}}`, newSize)

	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Resize PersistentVolumeClaim %s to %s", name, newSize),
		Steps: []StorageStep{
			{
				ID:          "resize-pvc",
				Description: fmt.Sprintf("Resize PersistentVolumeClaim %s to %s", name, newSize),
				Command:     "kubectl",
				Args:        []string{"patch", "pvc", name, "-n", namespace, "-p", patchJSON},
				Reason:      fmt.Sprintf("Increase PVC storage capacity to %s", newSize),
			},
		},
		Notes: []string{
			"The StorageClass must have allowVolumeExpansion: true",
			"Some storage providers may require pod restart for resize to take effect",
			"Volume shrinking is generally not supported",
		},
	}
}

// generatePVCManifest generates a PVC YAML manifest
func (m *PVCManager) generatePVCManifest(opts CreatePVCOptions) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: v1\n")
	sb.WriteString("kind: PersistentVolumeClaim\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", opts.Name))
	sb.WriteString(fmt.Sprintf("  namespace: %s\n", opts.Namespace))

	if len(opts.Labels) > 0 {
		sb.WriteString("  labels:\n")
		for k, v := range opts.Labels {
			sb.WriteString(fmt.Sprintf("    %s: %s\n", k, v))
		}
	}

	sb.WriteString("spec:\n")

	sb.WriteString("  accessModes:\n")
	for _, mode := range opts.AccessModes {
		sb.WriteString(fmt.Sprintf("    - %s\n", mode))
	}

	sb.WriteString("  resources:\n")
	sb.WriteString("    requests:\n")
	sb.WriteString(fmt.Sprintf("      storage: %s\n", opts.Storage))

	if opts.StorageClassName != "" {
		sb.WriteString(fmt.Sprintf("  storageClassName: %s\n", opts.StorageClassName))
	}

	if opts.VolumeMode != "" {
		sb.WriteString(fmt.Sprintf("  volumeMode: %s\n", opts.VolumeMode))
	}

	if opts.VolumeName != "" {
		sb.WriteString(fmt.Sprintf("  volumeName: %s\n", opts.VolumeName))
	}

	if len(opts.Selector) > 0 {
		sb.WriteString("  selector:\n")
		sb.WriteString("    matchLabels:\n")
		for k, v := range opts.Selector {
			sb.WriteString(fmt.Sprintf("      %s: %s\n", k, v))
		}
	}

	return sb.String()
}

// parsePVCList parses a JSON list of PVCs
func (m *PVCManager) parsePVCList(data []byte) ([]PVCInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse PVC list: %w", err)
	}

	pvcs := make([]PVCInfo, 0, len(list.Items))
	for _, item := range list.Items {
		pvc, err := m.parsePVC(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[storage] failed to parse PVC: %v\n", err)
			}
			continue
		}
		pvcs = append(pvcs, *pvc)
	}

	return pvcs, nil
}

// parsePVC parses a single PVC JSON
func (m *PVCManager) parsePVC(data []byte) (*PVCInfo, error) {
	var raw struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			AccessModes      []string `json:"accessModes"`
			StorageClassName string   `json:"storageClassName"`
			VolumeMode       string   `json:"volumeMode"`
			VolumeName       string   `json:"volumeName"`
			Resources        struct {
				Requests struct {
					Storage string `json:"storage"`
				} `json:"requests"`
			} `json:"resources"`
		} `json:"spec"`
		Status struct {
			Phase    string `json:"phase"`
			Capacity struct {
				Storage string `json:"storage"`
			} `json:"capacity"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse PVC JSON: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339, raw.Metadata.CreationTimestamp)
	age := formatDuration(time.Since(createdAt))

	return &PVCInfo{
		Name:             raw.Metadata.Name,
		Namespace:        raw.Metadata.Namespace,
		Status:           raw.Status.Phase,
		Volume:           raw.Spec.VolumeName,
		Capacity:         raw.Status.Capacity.Storage,
		RequestedStorage: raw.Spec.Resources.Requests.Storage,
		AccessModes:      raw.Spec.AccessModes,
		StorageClassName: raw.Spec.StorageClassName,
		VolumeMode:       raw.Spec.VolumeMode,
		Labels:           raw.Metadata.Labels,
		Age:              age,
		CreatedAt:        createdAt,
	}, nil
}
