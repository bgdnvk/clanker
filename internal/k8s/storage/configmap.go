package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ConfigMapManager handles ConfigMap operations
type ConfigMapManager struct {
	client K8sClient
	debug  bool
}

// NewConfigMapManager creates a new ConfigMap manager
func NewConfigMapManager(client K8sClient, debug bool) *ConfigMapManager {
	return &ConfigMapManager{
		client: client,
		debug:  debug,
	}
}

// ListConfigMaps lists ConfigMaps in a namespace
func (m *ConfigMapManager) ListConfigMaps(ctx context.Context, namespace string, opts QueryOptions) ([]ConfigMapInfo, error) {
	args := []string{"get", "configmap", "-o", "json"}

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
		return nil, fmt.Errorf("failed to list ConfigMaps: %w", err)
	}

	return m.parseConfigMapList([]byte(output))
}

// GetConfigMap gets a specific ConfigMap
func (m *ConfigMapManager) GetConfigMap(ctx context.Context, name, namespace string) (*ConfigMapInfo, error) {
	data, err := m.client.GetJSON(ctx, "configmap", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s: %w", name, err)
	}

	return m.parseConfigMap(data)
}

// DescribeConfigMap describes a ConfigMap
func (m *ConfigMapManager) DescribeConfigMap(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Describe(ctx, "configmap", name, namespace)
}

// GetConfigMapData retrieves the data from a ConfigMap
func (m *ConfigMapManager) GetConfigMapData(ctx context.Context, name, namespace string) (map[string]string, error) {
	data, err := m.client.GetJSON(ctx, "configmap", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s: %w", name, err)
	}

	var raw struct {
		Data map[string]string `json:"data"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse ConfigMap JSON: %w", err)
	}

	return raw.Data, nil
}

// CreateConfigMapPlan creates a plan for creating a ConfigMap
func (m *ConfigMapManager) CreateConfigMapPlan(opts CreateConfigMapOptions) *StoragePlan {
	steps := []StorageStep{}

	// If we have from-file or from-literal, use kubectl create
	if len(opts.FromFile) > 0 || len(opts.FromLiteral) > 0 {
		args := []string{"create", "configmap", opts.Name, "-n", opts.Namespace}

		for _, file := range opts.FromFile {
			args = append(args, "--from-file="+file)
		}

		for _, literal := range opts.FromLiteral {
			args = append(args, "--from-literal="+literal)
		}

		steps = append(steps, StorageStep{
			ID:          "create-configmap",
			Description: fmt.Sprintf("Create ConfigMap %s", opts.Name),
			Command:     "kubectl",
			Args:        args,
			Reason:      "Create ConfigMap with specified data",
		})
	} else {
		// Use a manifest for data-based creation
		manifest := m.generateConfigMapManifest(opts)
		steps = append(steps, StorageStep{
			ID:          "create-configmap",
			Description: fmt.Sprintf("Create ConfigMap %s", opts.Name),
			Command:     "kubectl",
			Args:        []string{"apply", "-f", "-"},
			Manifest:    manifest,
			Reason:      "Create ConfigMap with specified data",
		})
	}

	notes := []string{
		fmt.Sprintf("ConfigMap will be created in namespace %s", opts.Namespace),
	}

	if len(opts.Data) > 0 {
		notes = append(notes, fmt.Sprintf("Contains %d data keys", len(opts.Data)))
	}

	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create ConfigMap %s in namespace %s", opts.Name, opts.Namespace),
		Steps:     steps,
		Notes:     notes,
	}
}

// DeleteConfigMapPlan creates a plan for deleting a ConfigMap
func (m *ConfigMapManager) DeleteConfigMapPlan(name, namespace string) *StoragePlan {
	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete ConfigMap %s", name),
		Steps: []StorageStep{
			{
				ID:          "delete-configmap",
				Description: fmt.Sprintf("Delete ConfigMap %s", name),
				Command:     "kubectl",
				Args:        []string{"delete", "configmap", name, "-n", namespace},
				Reason:      "Remove the ConfigMap from the namespace",
			},
		},
		Notes: []string{
			"Ensure no pods are using this ConfigMap before deletion",
			"Pods mounting this ConfigMap may fail to start after deletion",
		},
	}
}

// UpdateConfigMapPlan creates a plan for updating a ConfigMap
func (m *ConfigMapManager) UpdateConfigMapPlan(opts CreateConfigMapOptions) *StoragePlan {
	manifest := m.generateConfigMapManifest(opts)

	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Update ConfigMap %s", opts.Name),
		Steps: []StorageStep{
			{
				ID:          "update-configmap",
				Description: fmt.Sprintf("Update ConfigMap %s", opts.Name),
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Manifest:    manifest,
				Reason:      "Update ConfigMap data",
			},
		},
		Notes: []string{
			"Pods using this ConfigMap as volume will see updates eventually",
			"Pods using ConfigMap as environment variables need restart to see updates",
		},
	}
}

// generateConfigMapManifest generates a ConfigMap YAML manifest
func (m *ConfigMapManager) generateConfigMapManifest(opts CreateConfigMapOptions) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: v1\n")
	sb.WriteString("kind: ConfigMap\n")
	sb.WriteString("metadata:\n")
	sb.WriteString(fmt.Sprintf("  name: %s\n", opts.Name))
	sb.WriteString(fmt.Sprintf("  namespace: %s\n", opts.Namespace))

	if len(opts.Labels) > 0 {
		sb.WriteString("  labels:\n")
		for k, v := range opts.Labels {
			sb.WriteString(fmt.Sprintf("    %s: %s\n", k, v))
		}
	}

	if len(opts.Annotations) > 0 {
		sb.WriteString("  annotations:\n")
		for k, v := range opts.Annotations {
			sb.WriteString(fmt.Sprintf("    %s: \"%s\"\n", k, v))
		}
	}

	if len(opts.Data) > 0 {
		sb.WriteString("data:\n")
		for k, v := range opts.Data {
			// Check if value is multiline
			if strings.Contains(v, "\n") {
				sb.WriteString(fmt.Sprintf("  %s: |\n", k))
				for _, line := range strings.Split(v, "\n") {
					sb.WriteString(fmt.Sprintf("    %s\n", line))
				}
			} else {
				sb.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
			}
		}
	}

	if len(opts.BinaryData) > 0 {
		sb.WriteString("binaryData:\n")
		for k := range opts.BinaryData {
			// Binary data would need to be base64 encoded
			sb.WriteString(fmt.Sprintf("  %s: <base64-encoded-data>\n", k))
		}
	}

	return sb.String()
}

// parseConfigMapList parses a JSON list of ConfigMaps
func (m *ConfigMapManager) parseConfigMapList(data []byte) ([]ConfigMapInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse ConfigMap list: %w", err)
	}

	cms := make([]ConfigMapInfo, 0, len(list.Items))
	for _, item := range list.Items {
		cm, err := m.parseConfigMap(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[storage] failed to parse ConfigMap: %v\n", err)
			}
			continue
		}
		cms = append(cms, *cm)
	}

	return cms, nil
}

// parseConfigMap parses a single ConfigMap JSON
func (m *ConfigMapManager) parseConfigMap(data []byte) (*ConfigMapInfo, error) {
	var raw struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Data       map[string]string `json:"data"`
		BinaryData map[string]string `json:"binaryData"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse ConfigMap JSON: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339, raw.Metadata.CreationTimestamp)
	age := formatDuration(time.Since(createdAt))

	// Collect all keys
	dataKeys := make([]string, 0)
	for k := range raw.Data {
		dataKeys = append(dataKeys, k)
	}
	for k := range raw.BinaryData {
		dataKeys = append(dataKeys, k)
	}

	return &ConfigMapInfo{
		Name:      raw.Metadata.Name,
		Namespace: raw.Metadata.Namespace,
		DataKeys:  dataKeys,
		DataCount: len(raw.Data) + len(raw.BinaryData),
		Labels:    raw.Metadata.Labels,
		Age:       age,
		CreatedAt: createdAt,
	}, nil
}
