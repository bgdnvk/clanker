package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// mockClient implements K8sClient for testing
type mockClient struct {
	runResponse       string
	runError          error
	runWithNSResponse string
	runWithNSError    error
	getJSONResponse   []byte
	getJSONError      error
	describeResponse  string
	describeError     error
	deleteResponse    string
	deleteError       error
	applyResponse     string
	applyError        error
}

func (m *mockClient) Run(ctx context.Context, args ...string) (string, error) {
	return m.runResponse, m.runError
}

func (m *mockClient) RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error) {
	return m.runWithNSResponse, m.runWithNSError
}

func (m *mockClient) GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error) {
	return m.getJSONResponse, m.getJSONError
}

func (m *mockClient) Describe(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return m.describeResponse, m.describeError
}

func (m *mockClient) Delete(ctx context.Context, resourceType, name, namespace string) (string, error) {
	return m.deleteResponse, m.deleteError
}

func (m *mockClient) Apply(ctx context.Context, manifest string) (string, error) {
	return m.applyResponse, m.applyError
}

func TestNewSubAgent(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	if agent == nil {
		t.Fatal("expected non-nil agent")
	}
	if agent.client == nil {
		t.Error("expected client to be set")
	}
	if agent.pv == nil {
		t.Error("expected PV manager to be set")
	}
	if agent.pvc == nil {
		t.Error("expected PVC manager to be set")
	}
	if agent.configmap == nil {
		t.Error("expected ConfigMap manager to be set")
	}
	if agent.secret == nil {
		t.Error("expected Secret manager to be set")
	}
}

func TestAnalyzeQueryResourceType(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	// Use distinct patterns that don't overlap
	tests := []struct {
		query        string
		expectedType ResourceType
	}{
		{"list pv resources", ResourcePV},                // "pv " is unique to PV
		{"get pvc items", ResourcePVC},                   // "pvc" is unique
		{"describe storageclass config", ResourceStorageClass}, // "storageclass" is unique
		{"show configmap data", ResourceConfigMap},       // "configmap" is unique
		{"list secret items", ResourceSecret},            // "secret" is unique
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.ResourceType != tt.expectedType {
				t.Errorf("expected resource type %s, got %s", tt.expectedType, analysis.ResourceType)
			}
		})
	}
}

func TestAnalyzeQueryOperation(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	// Use unique patterns
	tests := []struct {
		query        string
		expectedOp   string
		expectedRead bool
	}{
		{"list all resources", "list", true},       // "list" is unique
		{"retrieve resource data", "get", true},    // "retrieve" is unique to get
		{"info about resource", "describe", true},  // "info about" is unique to describe
		{"add new resource", "create", false},      // "add" is unique to create
		{"drop resource", "delete", false},         // "drop" is unique to delete
		{"modify resource", "update", false},       // "modify" is unique to update
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.Operation != tt.expectedOp {
				t.Errorf("expected operation %s, got %s", tt.expectedOp, analysis.Operation)
			}
			if analysis.IsReadOnly != tt.expectedRead {
				t.Errorf("expected IsReadOnly=%v, got %v", tt.expectedRead, analysis.IsReadOnly)
			}
		})
	}
}

func TestAnalyzeQueryNamespace(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	tests := []struct {
		query      string
		expectedNS string
	}{
		{"list pvcs in namespace kube-system", "kube-system"},
		{"get configmaps -n default", "default"},
		{"show secrets in ns production", "production"},
		{"list pvcs", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			analysis := agent.analyzeQuery(tt.query)
			if analysis.Namespace != tt.expectedNS {
				t.Errorf("expected namespace %q, got %q", tt.expectedNS, analysis.Namespace)
			}
		})
	}
}

func TestPVManagerCreatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewPVManager(client, false)

	opts := CreatePVOptions{
		Name:          "my-pv",
		Capacity:      "10Gi",
		AccessModes:   []string{"ReadWriteOnce"},
		ReclaimPolicy: "Retain",
		HostPath:      "/data/pv",
	}

	plan := manager.CreatePVPlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) < 1 {
		t.Errorf("expected at least 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Command != "kubectl" {
		t.Errorf("expected command kubectl, got %s", plan.Steps[0].Command)
	}
	if plan.Steps[0].Manifest == "" {
		t.Error("expected manifest in step")
	}
}

func TestPVManagerDeletePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewPVManager(client, false)

	plan := manager.DeletePVPlan("my-pv")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Delete PersistentVolume my-pv" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestStorageClassManagerCreatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewPVManager(client, false)

	opts := CreateStorageClassOptions{
		Name:              "fast-storage",
		Provisioner:       "kubernetes.io/aws-ebs",
		ReclaimPolicy:     "Delete",
		VolumeBindingMode: "WaitForFirstConsumer",
		IsDefault:         true,
	}

	plan := manager.CreateStorageClassPlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Create StorageClass fast-storage" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
	if plan.Steps[0].Manifest == "" {
		t.Error("expected manifest in step")
	}
}

func TestPVCManagerCreatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewPVCManager(client, false)

	opts := CreatePVCOptions{
		Name:             "my-pvc",
		Namespace:        "default",
		Storage:          "5Gi",
		AccessModes:      []string{"ReadWriteOnce"},
		StorageClassName: "standard",
	}

	plan := manager.CreatePVCPlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Manifest == "" {
		t.Error("expected manifest in step")
	}
	// Check wait condition
	if plan.Steps[0].WaitFor == nil {
		t.Error("expected wait condition for PVC")
	}
}

func TestPVCManagerDeletePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewPVCManager(client, false)

	plan := manager.DeletePVCPlan("my-pvc", "default")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Delete PersistentVolumeClaim my-pvc" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestConfigMapManagerCreatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewConfigMapManager(client, false)

	opts := CreateConfigMapOptions{
		Name:      "app-config",
		Namespace: "default",
		Data: map[string]string{
			"config.json": `{"key": "value"}`,
		},
	}

	plan := manager.CreateConfigMapPlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) < 1 {
		t.Errorf("expected at least 1 step, got %d", len(plan.Steps))
	}
}

func TestConfigMapManagerDeletePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewConfigMapManager(client, false)

	plan := manager.DeleteConfigMapPlan("app-config", "default")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Delete ConfigMap app-config" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
}

func TestSecretManagerCreatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewSecretManager(client, false)

	opts := CreateSecretOptions{
		Name:      "my-secret",
		Namespace: "default",
		Type:      string(SecretTypeOpaque),
		StringData: map[string]string{
			"username": "admin",
			"password": "secret",
		},
	}

	plan := manager.CreateSecretPlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(plan.Steps) < 1 {
		t.Errorf("expected at least 1 step, got %d", len(plan.Steps))
	}
}

func TestSecretManagerDeletePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewSecretManager(client, false)

	plan := manager.DeleteSecretPlan("my-secret", "default")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Delete Secret my-secret" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
}

func TestSecretManagerDockerRegistryPlan(t *testing.T) {
	client := &mockClient{}
	manager := NewSecretManager(client, false)

	plan := manager.CreateDockerRegistrySecretPlan(
		"registry-secret",
		"default",
		"docker.io",
		"user",
		"pass",
		"user@example.com",
	)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}

	// Check args contain docker-registry
	hasType := false
	for _, arg := range plan.Steps[0].Args {
		if arg == "docker-registry" {
			hasType = true
			break
		}
	}
	if !hasType {
		t.Error("expected docker-registry in args")
	}
}

func TestSecretManagerTLSPlan(t *testing.T) {
	client := &mockClient{}
	manager := NewSecretManager(client, false)

	plan := manager.CreateTLSSecretPlan(
		"tls-secret",
		"default",
		"/path/to/cert.pem",
		"/path/to/key.pem",
	)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestParsePV(t *testing.T) {
	client := &mockClient{}
	manager := NewPVManager(client, false)

	pvJSON := `{
		"metadata": {
			"name": "my-pv",
			"labels": {"type": "local"},
			"creationTimestamp": "2024-01-01T00:00:00Z"
		},
		"spec": {
			"capacity": {"storage": "10Gi"},
			"accessModes": ["ReadWriteOnce"],
			"persistentVolumeReclaimPolicy": "Retain",
			"storageClassName": "standard",
			"volumeMode": "Filesystem",
			"hostPath": {"path": "/data/pv"}
		},
		"status": {
			"phase": "Available"
		}
	}`

	info, err := manager.parsePV([]byte(pvJSON))
	if err != nil {
		t.Fatalf("failed to parse PV: %v", err)
	}

	if info.Name != "my-pv" {
		t.Errorf("expected name my-pv, got %s", info.Name)
	}
	if info.Capacity != "10Gi" {
		t.Errorf("expected capacity 10Gi, got %s", info.Capacity)
	}
	if info.Status != "Available" {
		t.Errorf("expected status Available, got %s", info.Status)
	}
	if info.HostPath != "/data/pv" {
		t.Errorf("expected hostPath /data/pv, got %s", info.HostPath)
	}
}

func TestParseStorageClass(t *testing.T) {
	client := &mockClient{}
	manager := NewPVManager(client, false)

	scJSON := `{
		"metadata": {
			"name": "fast",
			"annotations": {"storageclass.kubernetes.io/is-default-class": "true"},
			"creationTimestamp": "2024-01-01T00:00:00Z"
		},
		"provisioner": "kubernetes.io/aws-ebs",
		"reclaimPolicy": "Delete",
		"volumeBindingMode": "WaitForFirstConsumer",
		"allowVolumeExpansion": true,
		"parameters": {"type": "gp2"}
	}`

	info, err := manager.parseStorageClass([]byte(scJSON))
	if err != nil {
		t.Fatalf("failed to parse StorageClass: %v", err)
	}

	if info.Name != "fast" {
		t.Errorf("expected name fast, got %s", info.Name)
	}
	if !info.IsDefault {
		t.Error("expected IsDefault to be true")
	}
	if info.Provisioner != "kubernetes.io/aws-ebs" {
		t.Errorf("expected provisioner kubernetes.io/aws-ebs, got %s", info.Provisioner)
	}
	if !info.AllowVolumeExpansion {
		t.Error("expected AllowVolumeExpansion to be true")
	}
}

func TestParsePVC(t *testing.T) {
	client := &mockClient{}
	manager := NewPVCManager(client, false)

	pvcJSON := `{
		"metadata": {
			"name": "my-pvc",
			"namespace": "default",
			"labels": {"app": "web"},
			"creationTimestamp": "2024-01-01T00:00:00Z"
		},
		"spec": {
			"accessModes": ["ReadWriteOnce"],
			"storageClassName": "standard",
			"volumeMode": "Filesystem",
			"volumeName": "my-pv",
			"resources": {
				"requests": {"storage": "5Gi"}
			}
		},
		"status": {
			"phase": "Bound",
			"capacity": {"storage": "5Gi"}
		}
	}`

	info, err := manager.parsePVC([]byte(pvcJSON))
	if err != nil {
		t.Fatalf("failed to parse PVC: %v", err)
	}

	if info.Name != "my-pvc" {
		t.Errorf("expected name my-pvc, got %s", info.Name)
	}
	if info.Namespace != "default" {
		t.Errorf("expected namespace default, got %s", info.Namespace)
	}
	if info.Status != "Bound" {
		t.Errorf("expected status Bound, got %s", info.Status)
	}
	if info.Volume != "my-pv" {
		t.Errorf("expected volume my-pv, got %s", info.Volume)
	}
}

func TestParseConfigMap(t *testing.T) {
	client := &mockClient{}
	manager := NewConfigMapManager(client, false)

	cmJSON := `{
		"metadata": {
			"name": "app-config",
			"namespace": "default",
			"labels": {"app": "web"},
			"creationTimestamp": "2024-01-01T00:00:00Z"
		},
		"data": {
			"config.json": "{\"key\": \"value\"}",
			"settings.yaml": "key: value"
		}
	}`

	info, err := manager.parseConfigMap([]byte(cmJSON))
	if err != nil {
		t.Fatalf("failed to parse ConfigMap: %v", err)
	}

	if info.Name != "app-config" {
		t.Errorf("expected name app-config, got %s", info.Name)
	}
	if info.Namespace != "default" {
		t.Errorf("expected namespace default, got %s", info.Namespace)
	}
	if info.DataCount != 2 {
		t.Errorf("expected 2 data keys, got %d", info.DataCount)
	}
}

func TestParseSecret(t *testing.T) {
	client := &mockClient{}
	manager := NewSecretManager(client, false)

	secretJSON := `{
		"metadata": {
			"name": "my-secret",
			"namespace": "default",
			"labels": {"app": "web"},
			"creationTimestamp": "2024-01-01T00:00:00Z"
		},
		"type": "Opaque",
		"data": {
			"username": "YWRtaW4=",
			"password": "c2VjcmV0"
		}
	}`

	info, err := manager.parseSecret([]byte(secretJSON))
	if err != nil {
		t.Fatalf("failed to parse Secret: %v", err)
	}

	if info.Name != "my-secret" {
		t.Errorf("expected name my-secret, got %s", info.Name)
	}
	if info.Type != "Opaque" {
		t.Errorf("expected type Opaque, got %s", info.Type)
	}
	if info.DataCount != 2 {
		t.Errorf("expected 2 data keys, got %d", info.DataCount)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{48 * time.Hour, "2d"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatDuration(tt.duration)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestStoragePlanJSON(t *testing.T) {
	plan := &StoragePlan{
		Version:   1,
		CreatedAt: time.Now(),
		Summary:   "Test plan",
		Steps: []StorageStep{
			{
				ID:          "step-1",
				Description: "Test step",
				Command:     "kubectl",
				Args:        []string{"apply", "-f", "-"},
				Reason:      "Testing",
			},
		},
		Notes: []string{"Test note"},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("failed to marshal plan: %v", err)
	}

	var parsed StoragePlan
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal plan: %v", err)
	}

	if parsed.Version != 1 {
		t.Errorf("expected version 1, got %d", parsed.Version)
	}
	if parsed.Summary != "Test plan" {
		t.Errorf("unexpected summary: %s", parsed.Summary)
	}
}

func TestParsePVCreationFromQuery(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	tests := []struct {
		query            string
		expectedName     string
		expectedCapacity string
	}{
		{"create pv my-volume with 10Gi capacity", "my-volume", "10Gi"},
		{"add pv named data-pv capacity 5Gi", "data-pv", "5Gi"},
		{"create pv test-pv", "test-pv", ""},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			opts := agent.parsePVCreationFromQuery(tt.query)
			if opts.Name != tt.expectedName {
				t.Errorf("expected name %s, got %s", tt.expectedName, opts.Name)
			}
			if opts.Capacity != tt.expectedCapacity {
				t.Errorf("expected capacity %s, got %s", tt.expectedCapacity, opts.Capacity)
			}
		})
	}
}

func TestParsePVCCreationFromQuery(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	tests := []struct {
		query           string
		expectedName    string
		expectedStorage string
	}{
		{"create pvc my-claim with 5Gi storage", "my-claim", "5Gi"},
		{"add pvc named data-claim storage 10Gi", "data-claim", "10Gi"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			opts := agent.parsePVCCreationFromQuery(tt.query, "default")
			if opts.Name != tt.expectedName {
				t.Errorf("expected name %s, got %s", tt.expectedName, opts.Name)
			}
			if opts.Storage != tt.expectedStorage {
				t.Errorf("expected storage %s, got %s", tt.expectedStorage, opts.Storage)
			}
		})
	}
}

func TestParseSecretCreationFromQuery(t *testing.T) {
	client := &mockClient{}
	agent := NewSubAgent(client, false)

	tests := []struct {
		query        string
		expectedName string
		expectedType string
	}{
		{"create secret my-secret", "my-secret", string(SecretTypeOpaque)},
		{"create tls secret tls-cert", "tls-cert", string(SecretTypeTLS)},
		{"create docker registry secret docker-creds", "docker-creds", string(SecretTypeDockerConfigJSON)},
		{"create basic-auth secret auth-creds", "auth-creds", string(SecretTypeBasicAuth)},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			opts := agent.parseSecretCreationFromQuery(tt.query, "default")
			if opts.Name != tt.expectedName {
				t.Errorf("expected name %s, got %s", tt.expectedName, opts.Name)
			}
			if opts.Type != tt.expectedType {
				t.Errorf("expected type %s, got %s", tt.expectedType, opts.Type)
			}
		})
	}
}

func TestPVCManagerResizePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewPVCManager(client, false)

	plan := manager.ResizePVCPlan("my-pvc", "default", "20Gi")

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Resize PersistentVolumeClaim my-pvc to 20Gi" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestConfigMapManagerUpdatePlan(t *testing.T) {
	client := &mockClient{}
	manager := NewConfigMapManager(client, false)

	opts := CreateConfigMapOptions{
		Name:      "app-config",
		Namespace: "default",
		Data: map[string]string{
			"new-key": "new-value",
		},
	}

	plan := manager.UpdateConfigMapPlan(opts)

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.Summary != "Update ConfigMap app-config" {
		t.Errorf("unexpected summary: %s", plan.Summary)
	}
}
