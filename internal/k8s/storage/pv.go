package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PVManager handles PersistentVolume and StorageClass operations
type PVManager struct {
	client K8sClient
	debug  bool
}

// NewPVManager creates a new PV manager
func NewPVManager(client K8sClient, debug bool) *PVManager {
	return &PVManager{
		client: client,
		debug:  debug,
	}
}

// ListPVs lists all PersistentVolumes
func (m *PVManager) ListPVs(ctx context.Context, opts QueryOptions) ([]PVInfo, error) {
	args := []string{"get", "pv", "-o", "json"}

	if opts.LabelSelector != "" {
		args = append(args, "-l", opts.LabelSelector)
	}

	output, err := m.client.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list PVs: %w", err)
	}

	return m.parsePVList([]byte(output))
}

// GetPV gets a specific PersistentVolume
func (m *PVManager) GetPV(ctx context.Context, name string) (*PVInfo, error) {
	data, err := m.client.GetJSON(ctx, "pv", name, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get PV %s: %w", name, err)
	}

	return m.parsePV(data)
}

// DescribePV describes a PersistentVolume
func (m *PVManager) DescribePV(ctx context.Context, name string) (string, error) {
	return m.client.Describe(ctx, "pv", name, "")
}

// ListStorageClasses lists all StorageClasses
func (m *PVManager) ListStorageClasses(ctx context.Context, opts QueryOptions) ([]StorageClassInfo, error) {
	args := []string{"get", "storageclass", "-o", "json"}

	if opts.LabelSelector != "" {
		args = append(args, "-l", opts.LabelSelector)
	}

	output, err := m.client.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list StorageClasses: %w", err)
	}

	return m.parseStorageClassList([]byte(output))
}

// GetStorageClass gets a specific StorageClass
func (m *PVManager) GetStorageClass(ctx context.Context, name string) (*StorageClassInfo, error) {
	data, err := m.client.GetJSON(ctx, "storageclass", name, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get StorageClass %s: %w", name, err)
	}

	return m.parseStorageClass(data)
}

// DescribeStorageClass describes a StorageClass
func (m *PVManager) DescribeStorageClass(ctx context.Context, name string) (string, error) {
	return m.client.Describe(ctx, "storageclass", name, "")
}

// CreatePVPlan creates a plan for creating a PersistentVolume
func (m *PVManager) CreatePVPlan(opts CreatePVOptions) *StoragePlan {
	manifest := m.generatePVManifest(opts)

	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create PersistentVolume %s with capacity %s", opts.Name, opts.Capacity),
		Steps: []StorageStep{
			{
				ID:          "create-pv",
				Description: fmt.Sprintf("Create PersistentVolume %s", opts.Name),
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Manifest:    manifest,
				Reason:      fmt.Sprintf("Create PV with %s capacity and %s reclaim policy", opts.Capacity, opts.ReclaimPolicy),
			},
		},
		Notes: []string{
			fmt.Sprintf("PV will be created with %s access mode", strings.Join(opts.AccessModes, ", ")),
			"PV is a cluster-scoped resource (not namespaced)",
		},
	}
}

// DeletePVPlan creates a plan for deleting a PersistentVolume
func (m *PVManager) DeletePVPlan(name string) *StoragePlan {
	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete PersistentVolume %s", name),
		Steps: []StorageStep{
			{
				ID:          "delete-pv",
				Description: fmt.Sprintf("Delete PersistentVolume %s", name),
				Command:     "kubectl",
				Args:        []string{"delete", "pv", name},
				Reason:      "Remove the PersistentVolume from the cluster",
			},
		},
		Notes: []string{
			"Ensure no PVCs are bound to this PV before deletion",
			"Data may be retained based on reclaim policy",
		},
	}
}

// CreateStorageClassPlan creates a plan for creating a StorageClass
func (m *PVManager) CreateStorageClassPlan(opts CreateStorageClassOptions) *StoragePlan {
	manifest := m.generateStorageClassManifest(opts)

	notes := []string{
		fmt.Sprintf("StorageClass will use %s provisioner", opts.Provisioner),
		fmt.Sprintf("Reclaim policy: %s", opts.ReclaimPolicy),
	}

	if opts.IsDefault {
		notes = append(notes, "This StorageClass will be set as the default")
	}

	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create StorageClass %s", opts.Name),
		Steps: []StorageStep{
			{
				ID:          "create-storageclass",
				Description: fmt.Sprintf("Create StorageClass %s", opts.Name),
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Manifest:    manifest,
				Reason:      fmt.Sprintf("Create StorageClass with %s provisioner", opts.Provisioner),
			},
		},
		Notes: notes,
	}
}

// DeleteStorageClassPlan creates a plan for deleting a StorageClass
func (m *PVManager) DeleteStorageClassPlan(name string) *StoragePlan {
	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete StorageClass %s", name),
		Steps: []StorageStep{
			{
				ID:          "delete-storageclass",
				Description: fmt.Sprintf("Delete StorageClass %s", name),
				Command:     "kubectl",
				Args:        []string{"delete", "storageclass", name},
				Reason:      "Remove the StorageClass from the cluster",
			},
		},
		Notes: []string{
			"Existing PVCs using this StorageClass will not be affected",
			"New PVCs cannot be created using this StorageClass after deletion",
		},
	}
}

// generatePVManifest generates a PV YAML manifest
func (m *PVManager) generatePVManifest(opts CreatePVOptions) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: v1\n")
	sb.WriteString("kind: PersistentVolume\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", opts.Name))

	if len(opts.Labels) > 0 {
		sb.WriteString("  labels:\n")
		for k, v := range opts.Labels {
			sb.WriteString(fmt.Sprintf("    %s: %s\n", k, v))
		}
	}

	sb.WriteString("spec:\n")
	sb.WriteString(fmt.Sprintf("  capacity:\n    storage: %s\n", opts.Capacity))

	sb.WriteString("  accessModes:\n")
	for _, mode := range opts.AccessModes {
		sb.WriteString(fmt.Sprintf("    - %s\n", mode))
	}

	sb.WriteString(fmt.Sprintf("  persistentVolumeReclaimPolicy: %s\n", opts.ReclaimPolicy))

	if opts.StorageClassName != "" {
		sb.WriteString(fmt.Sprintf("  storageClassName: %s\n", opts.StorageClassName))
	}

	if opts.VolumeMode != "" {
		sb.WriteString(fmt.Sprintf("  volumeMode: %s\n", opts.VolumeMode))
	}

	// Add volume source
	if opts.HostPath != "" {
		sb.WriteString("  hostPath:\n")
		sb.WriteString(fmt.Sprintf("    path: %s\n", opts.HostPath))
		sb.WriteString("    type: DirectoryOrCreate\n")
	} else if opts.NFS != nil {
		sb.WriteString("  nfs:\n")
		sb.WriteString(fmt.Sprintf("    server: %s\n", opts.NFS.Server))
		sb.WriteString(fmt.Sprintf("    path: %s\n", opts.NFS.Path))
		if opts.NFS.ReadOnly {
			sb.WriteString("    readOnly: true\n")
		}
	} else if opts.CSI != nil {
		sb.WriteString("  csi:\n")
		sb.WriteString(fmt.Sprintf("    driver: %s\n", opts.CSI.Driver))
		sb.WriteString(fmt.Sprintf("    volumeHandle: %s\n", opts.CSI.VolumeHandle))
		if opts.CSI.FSType != "" {
			sb.WriteString(fmt.Sprintf("    fsType: %s\n", opts.CSI.FSType))
		}
		if opts.CSI.ReadOnly {
			sb.WriteString("    readOnly: true\n")
		}
		if len(opts.CSI.VolumeAttributes) > 0 {
			sb.WriteString("    volumeAttributes:\n")
			for k, v := range opts.CSI.VolumeAttributes {
				sb.WriteString(fmt.Sprintf("      %s: %s\n", k, v))
			}
		}
	}

	return sb.String()
}

// generateStorageClassManifest generates a StorageClass YAML manifest
func (m *PVManager) generateStorageClassManifest(opts CreateStorageClassOptions) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: storage.k8s.io/v1\n")
	sb.WriteString("kind: StorageClass\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", opts.Name))

	if opts.IsDefault || len(opts.Annotations) > 0 {
		sb.WriteString("  annotations:\n")
		if opts.IsDefault {
			sb.WriteString("    storageclass.kubernetes.io/is-default-class: \"true\"\n")
		}
		for k, v := range opts.Annotations {
			sb.WriteString(fmt.Sprintf("    %s: \"%s\"\n", k, v))
		}
	}

	if len(opts.Labels) > 0 {
		sb.WriteString("  labels:\n")
		for k, v := range opts.Labels {
			sb.WriteString(fmt.Sprintf("    %s: %s\n", k, v))
		}
	}

	sb.WriteString(fmt.Sprintf("provisioner: %s\n", opts.Provisioner))
	sb.WriteString(fmt.Sprintf("reclaimPolicy: %s\n", opts.ReclaimPolicy))
	sb.WriteString(fmt.Sprintf("volumeBindingMode: %s\n", opts.VolumeBindingMode))

	if opts.AllowVolumeExpansion {
		sb.WriteString("allowVolumeExpansion: true\n")
	}

	if len(opts.Parameters) > 0 {
		sb.WriteString("parameters:\n")
		for k, v := range opts.Parameters {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}

	return sb.String()
}

// parsePVList parses a JSON list of PVs
func (m *PVManager) parsePVList(data []byte) ([]PVInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse PV list: %w", err)
	}

	pvs := make([]PVInfo, 0, len(list.Items))
	for _, item := range list.Items {
		pv, err := m.parsePV(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[storage] failed to parse PV: %v\n", err)
			}
			continue
		}
		pvs = append(pvs, *pv)
	}

	return pvs, nil
}

// parsePV parses a single PV JSON
func (m *PVManager) parsePV(data []byte) (*PVInfo, error) {
	var raw struct {
		Metadata struct {
			Name              string            `json:"name"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Capacity struct {
				Storage string `json:"storage"`
			} `json:"capacity"`
			AccessModes                   []string `json:"accessModes"`
			PersistentVolumeReclaimPolicy string   `json:"persistentVolumeReclaimPolicy"`
			StorageClassName              string   `json:"storageClassName"`
			VolumeMode                    string   `json:"volumeMode"`
			ClaimRef                      *struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"claimRef"`
			HostPath *struct {
				Path string `json:"path"`
			} `json:"hostPath"`
			NFS *struct {
				Server string `json:"server"`
				Path   string `json:"path"`
			} `json:"nfs"`
			CSI *struct {
				Driver       string `json:"driver"`
				VolumeHandle string `json:"volumeHandle"`
			} `json:"csi"`
			AWSElasticBlockStore *struct {
				VolumeID string `json:"volumeID"`
			} `json:"awsElasticBlockStore"`
		} `json:"spec"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse PV JSON: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339, raw.Metadata.CreationTimestamp)
	age := formatDuration(time.Since(createdAt))

	info := &PVInfo{
		Name:             raw.Metadata.Name,
		Capacity:         raw.Spec.Capacity.Storage,
		AccessModes:      raw.Spec.AccessModes,
		ReclaimPolicy:    raw.Spec.PersistentVolumeReclaimPolicy,
		Status:           raw.Status.Phase,
		StorageClassName: raw.Spec.StorageClassName,
		VolumeMode:       raw.Spec.VolumeMode,
		Labels:           raw.Metadata.Labels,
		Age:              age,
		CreatedAt:        createdAt,
	}

	if raw.Spec.ClaimRef != nil {
		info.Claim = fmt.Sprintf("%s/%s", raw.Spec.ClaimRef.Namespace, raw.Spec.ClaimRef.Name)
	}

	if raw.Spec.HostPath != nil {
		info.HostPath = raw.Spec.HostPath.Path
	}

	if raw.Spec.NFS != nil {
		info.NFS = fmt.Sprintf("%s:%s", raw.Spec.NFS.Server, raw.Spec.NFS.Path)
	}

	if raw.Spec.CSI != nil {
		info.CSI = fmt.Sprintf("%s:%s", raw.Spec.CSI.Driver, raw.Spec.CSI.VolumeHandle)
	}

	if raw.Spec.AWSElasticBlockStore != nil {
		info.AWSEBSVol = raw.Spec.AWSElasticBlockStore.VolumeID
	}

	return info, nil
}

// parseStorageClassList parses a JSON list of StorageClasses
func (m *PVManager) parseStorageClassList(data []byte) ([]StorageClassInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse StorageClass list: %w", err)
	}

	scs := make([]StorageClassInfo, 0, len(list.Items))
	for _, item := range list.Items {
		sc, err := m.parseStorageClass(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[storage] failed to parse StorageClass: %v\n", err)
			}
			continue
		}
		scs = append(scs, *sc)
	}

	return scs, nil
}

// parseStorageClass parses a single StorageClass JSON
func (m *PVManager) parseStorageClass(data []byte) (*StorageClassInfo, error) {
	var raw struct {
		Metadata struct {
			Name              string            `json:"name"`
			Labels            map[string]string `json:"labels"`
			Annotations       map[string]string `json:"annotations"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Provisioner          string            `json:"provisioner"`
		ReclaimPolicy        string            `json:"reclaimPolicy"`
		VolumeBindingMode    string            `json:"volumeBindingMode"`
		AllowVolumeExpansion bool              `json:"allowVolumeExpansion"`
		Parameters           map[string]string `json:"parameters"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse StorageClass JSON: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339, raw.Metadata.CreationTimestamp)
	age := formatDuration(time.Since(createdAt))

	isDefault := false
	if raw.Metadata.Annotations != nil {
		if val, ok := raw.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"]; ok && val == "true" {
			isDefault = true
		}
	}

	return &StorageClassInfo{
		Name:                 raw.Metadata.Name,
		Provisioner:          raw.Provisioner,
		ReclaimPolicy:        raw.ReclaimPolicy,
		VolumeBindingMode:    raw.VolumeBindingMode,
		AllowVolumeExpansion: raw.AllowVolumeExpansion,
		Parameters:           raw.Parameters,
		Labels:               raw.Metadata.Labels,
		Annotations:          raw.Metadata.Annotations,
		IsDefault:            isDefault,
		Age:                  age,
		CreatedAt:            createdAt,
	}, nil
}
