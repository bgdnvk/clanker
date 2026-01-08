package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SecretManager handles Secret operations
type SecretManager struct {
	client K8sClient
	debug  bool
}

// NewSecretManager creates a new Secret manager
func NewSecretManager(client K8sClient, debug bool) *SecretManager {
	return &SecretManager{
		client: client,
		debug:  debug,
	}
}

// ListSecrets lists Secrets in a namespace
func (m *SecretManager) ListSecrets(ctx context.Context, namespace string, opts QueryOptions) ([]SecretInfo, error) {
	args := []string{"get", "secret", "-o", "json"}

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
		return nil, fmt.Errorf("failed to list Secrets: %w", err)
	}

	return m.parseSecretList([]byte(output))
}

// GetSecret gets a specific Secret (without exposing data values)
func (m *SecretManager) GetSecret(ctx context.Context, name, namespace string) (*SecretInfo, error) {
	data, err := m.client.GetJSON(ctx, "secret", name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get Secret %s: %w", name, err)
	}

	return m.parseSecret(data)
}

// DescribeSecret describes a Secret
func (m *SecretManager) DescribeSecret(ctx context.Context, name, namespace string) (string, error) {
	return m.client.Describe(ctx, "secret", name, namespace)
}

// CreateSecretPlan creates a plan for creating a Secret
func (m *SecretManager) CreateSecretPlan(opts CreateSecretOptions) *StoragePlan {
	steps := []StorageStep{}

	// Determine how to create the secret
	if len(opts.FromFile) > 0 || len(opts.FromLiteral) > 0 {
		// Use kubectl create for file or literal based secrets
		args := m.buildCreateSecretArgs(opts)
		steps = append(steps, StorageStep{
			ID:          "create-secret",
			Description: fmt.Sprintf("Create Secret %s", opts.Name),
			Command:     "kubectl",
			Args:        args,
			Reason:      fmt.Sprintf("Create %s secret with specified data", opts.Type),
		})
	} else {
		// Use a manifest for data-based creation
		manifest := m.generateSecretManifest(opts)
		steps = append(steps, StorageStep{
			ID:          "create-secret",
			Description: fmt.Sprintf("Create Secret %s", opts.Name),
			Command:     "kubectl",
			Args:        []string{"apply", "-f", "-"},
			Manifest:    manifest,
			Reason:      fmt.Sprintf("Create %s secret", opts.Type),
		})
	}

	notes := []string{
		fmt.Sprintf("Secret will be created in namespace %s", opts.Namespace),
		fmt.Sprintf("Secret type: %s", opts.Type),
	}

	if opts.Type == string(SecretTypeTLS) {
		notes = append(notes, "TLS secrets require valid certificate and key data")
	}

	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create Secret %s in namespace %s", opts.Name, opts.Namespace),
		Steps:     steps,
		Notes:     notes,
	}
}

// DeleteSecretPlan creates a plan for deleting a Secret
func (m *SecretManager) DeleteSecretPlan(name, namespace string) *StoragePlan {
	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Delete Secret %s", name),
		Steps: []StorageStep{
			{
				ID:          "delete-secret",
				Description: fmt.Sprintf("Delete Secret %s", name),
				Command:     "kubectl",
				Args:        []string{"delete", "secret", name, "-n", namespace},
				Reason:      "Remove the Secret from the namespace",
			},
		},
		Notes: []string{
			"Ensure no pods are using this Secret before deletion",
			"Pods mounting this Secret may fail to start after deletion",
			"Service account tokens are automatically managed - do not delete them manually",
		},
	}
}

// CreateDockerRegistrySecretPlan creates a plan for creating a Docker registry secret
func (m *SecretManager) CreateDockerRegistrySecretPlan(name, namespace, server, username, password, email string) *StoragePlan {
	args := []string{
		"create", "secret", "docker-registry", name,
		"-n", namespace,
		"--docker-server=" + server,
		"--docker-username=" + username,
		"--docker-password=" + password,
	}

	if email != "" {
		args = append(args, "--docker-email="+email)
	}

	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create Docker registry secret %s", name),
		Steps: []StorageStep{
			{
				ID:          "create-docker-secret",
				Description: fmt.Sprintf("Create Docker registry secret %s", name),
				Command:     "kubectl",
				Args:        args,
				Reason:      fmt.Sprintf("Create Docker registry credentials for %s", server),
			},
		},
		Notes: []string{
			fmt.Sprintf("Secret will allow pulling images from %s", server),
			"Add imagePullSecrets to pods or patch default service account",
		},
	}
}

// CreateTLSSecretPlan creates a plan for creating a TLS secret
func (m *SecretManager) CreateTLSSecretPlan(name, namespace, certFile, keyFile string) *StoragePlan {
	return &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   fmt.Sprintf("Create TLS secret %s", name),
		Steps: []StorageStep{
			{
				ID:          "create-tls-secret",
				Description: fmt.Sprintf("Create TLS secret %s", name),
				Command:     "kubectl",
				Args: []string{
					"create", "secret", "tls", name,
					"-n", namespace,
					"--cert=" + certFile,
					"--key=" + keyFile,
				},
				Reason: "Create TLS secret with certificate and private key",
			},
		},
		Notes: []string{
			"The certificate and key files must exist on the system",
			"Certificate should be PEM encoded",
			"Private key should be PEM encoded and not password protected",
		},
	}
}

// buildCreateSecretArgs builds the kubectl create secret arguments
func (m *SecretManager) buildCreateSecretArgs(opts CreateSecretOptions) []string {
	var secretType string
	switch opts.Type {
	case string(SecretTypeDockerConfigJSON):
		secretType = "docker-registry"
	case string(SecretTypeTLS):
		secretType = "tls"
	default:
		secretType = "generic"
	}

	args := []string{"create", "secret", secretType, opts.Name, "-n", opts.Namespace}

	for _, file := range opts.FromFile {
		args = append(args, "--from-file="+file)
	}

	for _, literal := range opts.FromLiteral {
		args = append(args, "--from-literal="+literal)
	}

	return args
}

// generateSecretManifest generates a Secret YAML manifest
func (m *SecretManager) generateSecretManifest(opts CreateSecretOptions) string {
	var sb strings.Builder

	sb.WriteString("apiVersion: v1\n")
	sb.WriteString("kind: Secret\n")
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

	sb.WriteString(fmt.Sprintf("type: %s\n", opts.Type))

	// Use stringData for non-base64 encoded values
	if len(opts.StringData) > 0 {
		sb.WriteString("stringData:\n")
		for k, v := range opts.StringData {
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

	// Use data for base64 encoded values
	if len(opts.Data) > 0 {
		sb.WriteString("data:\n")
		for k, v := range opts.Data {
			sb.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}

	return sb.String()
}

// parseSecretList parses a JSON list of Secrets
func (m *SecretManager) parseSecretList(data []byte) ([]SecretInfo, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("failed to parse Secret list: %w", err)
	}

	secrets := make([]SecretInfo, 0, len(list.Items))
	for _, item := range list.Items {
		secret, err := m.parseSecret(item)
		if err != nil {
			if m.debug {
				fmt.Printf("[storage] failed to parse Secret: %v\n", err)
			}
			continue
		}
		secrets = append(secrets, *secret)
	}

	return secrets, nil
}

// parseSecret parses a single Secret JSON (does not expose data values)
func (m *SecretManager) parseSecret(data []byte) (*SecretInfo, error) {
	var raw struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp string            `json:"creationTimestamp"`
		} `json:"metadata"`
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse Secret JSON: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339, raw.Metadata.CreationTimestamp)
	age := formatDuration(time.Since(createdAt))

	// Collect only keys, not values (for security)
	dataKeys := make([]string, 0, len(raw.Data))
	for k := range raw.Data {
		dataKeys = append(dataKeys, k)
	}

	return &SecretInfo{
		Name:      raw.Metadata.Name,
		Namespace: raw.Metadata.Namespace,
		Type:      raw.Type,
		DataKeys:  dataKeys,
		DataCount: len(raw.Data),
		Labels:    raw.Metadata.Labels,
		Age:       age,
		CreatedAt: createdAt,
	}, nil
}
