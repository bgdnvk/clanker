package storage

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SubAgent handles storage-related queries delegated from the main K8s agent
type SubAgent struct {
	client    K8sClient
	pv        *PVManager
	pvc       *PVCManager
	configmap *ConfigMapManager
	secret    *SecretManager
	debug     bool
}

// NewSubAgent creates a new storage sub-agent
func NewSubAgent(client K8sClient, debug bool) *SubAgent {
	return &SubAgent{
		client:    client,
		pv:        NewPVManager(client, debug),
		pvc:       NewPVCManager(client, debug),
		configmap: NewConfigMapManager(client, debug),
		secret:    NewSecretManager(client, debug),
		debug:     debug,
	}
}

// QueryAnalysis contains the analysis of a storage query
type QueryAnalysis struct {
	ResourceType ResourceType
	Operation    string
	ResourceName string
	Namespace    string
	IsReadOnly   bool
}

// HandleQuery processes a storage-related query
func (s *SubAgent) HandleQuery(ctx context.Context, query string, opts QueryOptions) (*Response, error) {
	analysis := s.analyzeQuery(query)

	if s.debug {
		fmt.Printf("[storage] query analysis: type=%s op=%s name=%s ns=%s readonly=%v\n",
			analysis.ResourceType, analysis.Operation, analysis.ResourceName, analysis.Namespace, analysis.IsReadOnly)
	}

	// Use namespace from query analysis or options
	namespace := analysis.Namespace
	if namespace == "" {
		namespace = opts.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}

	// Handle read-only operations immediately
	if analysis.IsReadOnly {
		return s.handleReadOperation(ctx, analysis, namespace, opts)
	}

	// Generate plan for modification operations
	return s.handleModifyOperation(ctx, query, analysis, namespace, opts)
}

// handleReadOperation executes read-only operations
func (s *SubAgent) handleReadOperation(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.ResourceType {
	case ResourcePV:
		return s.handlePVReadOp(ctx, analysis, opts)
	case ResourcePVC:
		return s.handlePVCReadOp(ctx, analysis, namespace, opts)
	case ResourceStorageClass:
		return s.handleStorageClassReadOp(ctx, analysis, opts)
	case ResourceConfigMap:
		return s.handleConfigMapReadOp(ctx, analysis, namespace, opts)
	case ResourceSecret:
		return s.handleSecretReadOp(ctx, analysis, namespace, opts)
	default:
		// If no specific type detected, list all storage resources
		return s.listAllStorageResources(ctx, namespace, opts)
	}
}

// handlePVReadOp handles PersistentVolume read operations
func (s *SubAgent) handlePVReadOp(ctx context.Context, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "list":
		pvs, err := s.pv.ListPVs(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: pvs,
		}, nil

	case "get", "describe":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("PV name required for %s operation", analysis.Operation)
		}
		if analysis.Operation == "describe" {
			desc, err := s.pv.DescribePV(ctx, analysis.ResourceName)
			if err != nil {
				return nil, err
			}
			return &Response{
				Type:    ResponseTypeResult,
				Message: desc,
			}, nil
		}
		pv, err := s.pv.GetPV(ctx, analysis.ResourceName)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: pv,
		}, nil

	default:
		pvs, err := s.pv.ListPVs(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: pvs,
		}, nil
	}
}

// handlePVCReadOp handles PersistentVolumeClaim read operations
func (s *SubAgent) handlePVCReadOp(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "list":
		pvcs, err := s.pvc.ListPVCs(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: pvcs,
		}, nil

	case "get", "describe":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("PVC name required for %s operation", analysis.Operation)
		}
		if analysis.Operation == "describe" {
			desc, err := s.pvc.DescribePVC(ctx, analysis.ResourceName, namespace)
			if err != nil {
				return nil, err
			}
			return &Response{
				Type:    ResponseTypeResult,
				Message: desc,
			}, nil
		}
		pvc, err := s.pvc.GetPVC(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: pvc,
		}, nil

	default:
		pvcs, err := s.pvc.ListPVCs(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: pvcs,
		}, nil
	}
}

// handleStorageClassReadOp handles StorageClass read operations
func (s *SubAgent) handleStorageClassReadOp(ctx context.Context, analysis QueryAnalysis, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "list":
		scs, err := s.pv.ListStorageClasses(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: scs,
		}, nil

	case "get", "describe":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("StorageClass name required for %s operation", analysis.Operation)
		}
		if analysis.Operation == "describe" {
			desc, err := s.pv.DescribeStorageClass(ctx, analysis.ResourceName)
			if err != nil {
				return nil, err
			}
			return &Response{
				Type:    ResponseTypeResult,
				Message: desc,
			}, nil
		}
		sc, err := s.pv.GetStorageClass(ctx, analysis.ResourceName)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: sc,
		}, nil

	default:
		scs, err := s.pv.ListStorageClasses(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: scs,
		}, nil
	}
}

// handleConfigMapReadOp handles ConfigMap read operations
func (s *SubAgent) handleConfigMapReadOp(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "list":
		cms, err := s.configmap.ListConfigMaps(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: cms,
		}, nil

	case "get", "describe":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("ConfigMap name required for %s operation", analysis.Operation)
		}
		if analysis.Operation == "describe" {
			desc, err := s.configmap.DescribeConfigMap(ctx, analysis.ResourceName, namespace)
			if err != nil {
				return nil, err
			}
			return &Response{
				Type:    ResponseTypeResult,
				Message: desc,
			}, nil
		}
		cm, err := s.configmap.GetConfigMap(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: cm,
		}, nil

	default:
		cms, err := s.configmap.ListConfigMaps(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: cms,
		}, nil
	}
}

// handleSecretReadOp handles Secret read operations
func (s *SubAgent) handleSecretReadOp(ctx context.Context, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.Operation {
	case "list":
		secrets, err := s.secret.ListSecrets(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: secrets,
		}, nil

	case "get", "describe":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("Secret name required for %s operation", analysis.Operation)
		}
		if analysis.Operation == "describe" {
			desc, err := s.secret.DescribeSecret(ctx, analysis.ResourceName, namespace)
			if err != nil {
				return nil, err
			}
			return &Response{
				Type:    ResponseTypeResult,
				Message: desc,
			}, nil
		}
		secret, err := s.secret.GetSecret(ctx, analysis.ResourceName, namespace)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: secret,
		}, nil

	default:
		secrets, err := s.secret.ListSecrets(ctx, namespace, opts)
		if err != nil {
			return nil, err
		}
		return &Response{
			Type: ResponseTypeResult,
			Data: secrets,
		}, nil
	}
}

// listAllStorageResources lists PVs, PVCs, ConfigMaps, and Secrets
func (s *SubAgent) listAllStorageResources(ctx context.Context, namespace string, opts QueryOptions) (*Response, error) {
	pvs, _ := s.pv.ListPVs(ctx, opts)
	pvcs, _ := s.pvc.ListPVCs(ctx, namespace, opts)
	scs, _ := s.pv.ListStorageClasses(ctx, opts)
	cms, _ := s.configmap.ListConfigMaps(ctx, namespace, opts)
	secrets, _ := s.secret.ListSecrets(ctx, namespace, opts)

	return &Response{
		Type: ResponseTypeResult,
		Data: map[string]interface{}{
			"persistentVolumes":      pvs,
			"persistentVolumeClaims": pvcs,
			"storageClasses":         scs,
			"configMaps":             cms,
			"secrets":                secrets,
		},
	}, nil
}

// handleModifyOperation generates plans for modification operations
func (s *SubAgent) handleModifyOperation(ctx context.Context, query string, analysis QueryAnalysis, namespace string, opts QueryOptions) (*Response, error) {
	switch analysis.ResourceType {
	case ResourcePV:
		return s.handlePVModifyOp(ctx, query, analysis, namespace)
	case ResourcePVC:
		return s.handlePVCModifyOp(ctx, query, analysis, namespace)
	case ResourceStorageClass:
		return s.handleStorageClassModifyOp(ctx, query, analysis)
	case ResourceConfigMap:
		return s.handleConfigMapModifyOp(ctx, query, analysis, namespace)
	case ResourceSecret:
		return s.handleSecretModifyOp(ctx, query, analysis, namespace)
	default:
		return nil, fmt.Errorf("unable to determine resource type for modification from query: %s", query)
	}
}

// handlePVModifyOp handles PV modification operations
func (s *SubAgent) handlePVModifyOp(ctx context.Context, query string, analysis QueryAnalysis, namespace string) (*Response, error) {
	switch analysis.Operation {
	case "create":
		pvOpts := s.parsePVCreationFromQuery(query)
		plan := s.pv.CreatePVPlan(pvOpts)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	case "delete", "remove":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("PV name required for delete operation")
		}
		plan := s.pv.DeletePVPlan(analysis.ResourceName)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported PV operation: %s", analysis.Operation)
	}
}

// handlePVCModifyOp handles PVC modification operations
func (s *SubAgent) handlePVCModifyOp(ctx context.Context, query string, analysis QueryAnalysis, namespace string) (*Response, error) {
	switch analysis.Operation {
	case "create":
		pvcOpts := s.parsePVCCreationFromQuery(query, namespace)
		plan := s.pvc.CreatePVCPlan(pvcOpts)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	case "delete", "remove":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("PVC name required for delete operation")
		}
		plan := s.pvc.DeletePVCPlan(analysis.ResourceName, namespace)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported PVC operation: %s", analysis.Operation)
	}
}

// handleStorageClassModifyOp handles StorageClass modification operations
func (s *SubAgent) handleStorageClassModifyOp(ctx context.Context, query string, analysis QueryAnalysis) (*Response, error) {
	switch analysis.Operation {
	case "create":
		scOpts := s.parseStorageClassCreationFromQuery(query)
		plan := s.pv.CreateStorageClassPlan(scOpts)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	case "delete", "remove":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("StorageClass name required for delete operation")
		}
		plan := s.pv.DeleteStorageClassPlan(analysis.ResourceName)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported StorageClass operation: %s", analysis.Operation)
	}
}

// handleConfigMapModifyOp handles ConfigMap modification operations
func (s *SubAgent) handleConfigMapModifyOp(ctx context.Context, query string, analysis QueryAnalysis, namespace string) (*Response, error) {
	switch analysis.Operation {
	case "create":
		cmOpts := s.parseConfigMapCreationFromQuery(query, namespace)
		plan := s.configmap.CreateConfigMapPlan(cmOpts)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	case "delete", "remove":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("ConfigMap name required for delete operation")
		}
		plan := s.configmap.DeleteConfigMapPlan(analysis.ResourceName, namespace)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported ConfigMap operation: %s", analysis.Operation)
	}
}

// handleSecretModifyOp handles Secret modification operations
func (s *SubAgent) handleSecretModifyOp(ctx context.Context, query string, analysis QueryAnalysis, namespace string) (*Response, error) {
	switch analysis.Operation {
	case "create":
		secretOpts := s.parseSecretCreationFromQuery(query, namespace)
		plan := s.secret.CreateSecretPlan(secretOpts)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	case "delete", "remove":
		if analysis.ResourceName == "" {
			return nil, fmt.Errorf("Secret name required for delete operation")
		}
		plan := s.secret.DeleteSecretPlan(analysis.ResourceName, namespace)
		return &Response{
			Type: ResponseTypePlan,
			Plan: plan,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported Secret operation: %s", analysis.Operation)
	}
}

// analyzeQuery analyzes a query to determine resource type and operation
func (s *SubAgent) analyzeQuery(query string) QueryAnalysis {
	lower := strings.ToLower(query)

	analysis := QueryAnalysis{
		ResourceType: s.detectResourceType(lower),
		Operation:    s.detectOperation(lower),
		ResourceName: s.extractResourceName(lower),
		Namespace:    s.extractNamespace(lower),
	}

	analysis.IsReadOnly = s.isReadOnlyOperation(analysis.Operation)

	return analysis
}

// detectResourceType determines which storage resource type the query is about
func (s *SubAgent) detectResourceType(query string) ResourceType {
	// Order matters - check more specific patterns first
	resourcePatterns := []struct {
		resourceType ResourceType
		patterns     []string
	}{
		{ResourceStorageClass, []string{"storageclass", "sc ", "storage class"}},
		{ResourcePVC, []string{"pvc", "persistentvolumeclaim", "volume claim"}},
		{ResourcePV, []string{"pv ", "persistentvolume", "persistent volume"}},
		{ResourceConfigMap, []string{"configmap", "cm ", "config map"}},
		{ResourceSecret, []string{"secret"}},
	}

	for _, rp := range resourcePatterns {
		for _, pattern := range rp.patterns {
			if strings.Contains(query, pattern) {
				return rp.resourceType
			}
		}
	}

	return "" // Unknown resource type
}

// detectOperation determines the operation from the query
func (s *SubAgent) detectOperation(query string) string {
	operations := []struct {
		op       string
		patterns []string
	}{
		{"list", []string{"list", "show", "what", "which", "all"}},
		{"get", []string{"get", "fetch", "retrieve"}},
		{"describe", []string{"describe", "details", "info about"}},
		{"create", []string{"create", "add", "new", "make"}},
		{"delete", []string{"delete", "remove", "drop"}},
		{"update", []string{"update", "modify", "change", "edit"}},
	}

	for _, op := range operations {
		for _, pattern := range op.patterns {
			if strings.Contains(query, pattern) {
				return op.op
			}
		}
	}

	return "list" // Default to list
}

// extractResourceName extracts the resource name from the query
func (s *SubAgent) extractResourceName(query string) string {
	patterns := []string{
		`pv\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`pvc\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`storageclass\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`configmap\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`cm\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`secret\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// extractNamespace extracts the namespace from the query
func (s *SubAgent) extractNamespace(query string) string {
	patterns := []string{
		`(?:in\s+)?namespace\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`-n\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`(?:in\s+)?ns\s+([a-z0-9][a-z0-9-]*[a-z0-9])`,
		`(?:in\s+)([a-z0-9][a-z0-9-]*[a-z0-9])\s+namespace`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(query); len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// isReadOnlyOperation determines if an operation is read-only
func (s *SubAgent) isReadOnlyOperation(operation string) bool {
	readOnlyOps := map[string]bool{
		"list":     true,
		"get":      true,
		"describe": true,
		"show":     true,
	}
	return readOnlyOps[operation]
}

// parsePVCreationFromQuery parses PV creation options from a query
func (s *SubAgent) parsePVCreationFromQuery(query string) CreatePVOptions {
	opts := CreatePVOptions{
		AccessModes:   []string{string(AccessModeReadWriteOnce)},
		ReclaimPolicy: string(ReclaimPolicyRetain),
		VolumeMode:    "Filesystem",
	}

	// Extract PV name
	namePattern := regexp.MustCompile(`pv\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := namePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Name = matches[1]
	}

	// Extract capacity
	capacityPattern := regexp.MustCompile(`(\d+(?:Gi|Mi|Ti))`)
	if matches := capacityPattern.FindStringSubmatch(query); len(matches) > 1 {
		opts.Capacity = matches[1]
	}

	// Extract host path
	hostPathPattern := regexp.MustCompile(`hostpath\s+([/a-z0-9-]+)`)
	if matches := hostPathPattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.HostPath = matches[1]
	}

	return opts
}

// parsePVCCreationFromQuery parses PVC creation options from a query
func (s *SubAgent) parsePVCCreationFromQuery(query string, namespace string) CreatePVCOptions {
	opts := CreatePVCOptions{
		Namespace:   namespace,
		AccessModes: []string{string(AccessModeReadWriteOnce)},
		VolumeMode:  "Filesystem",
	}

	// Extract PVC name
	namePattern := regexp.MustCompile(`pvc\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := namePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Name = matches[1]
	}

	// Extract storage size
	storagePattern := regexp.MustCompile(`(\d+(?:Gi|Mi|Ti))`)
	if matches := storagePattern.FindStringSubmatch(query); len(matches) > 1 {
		opts.Storage = matches[1]
	}

	// Extract storage class
	scPattern := regexp.MustCompile(`(?:storageclass|sc)\s+([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := scPattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.StorageClassName = matches[1]
	}

	return opts
}

// parseStorageClassCreationFromQuery parses StorageClass creation options from a query
func (s *SubAgent) parseStorageClassCreationFromQuery(query string) CreateStorageClassOptions {
	opts := CreateStorageClassOptions{
		ReclaimPolicy:     string(ReclaimPolicyDelete),
		VolumeBindingMode: string(VolumeBindingImmediate),
	}

	// Extract StorageClass name
	namePattern := regexp.MustCompile(`(?:storageclass|sc)\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := namePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Name = matches[1]
	}

	// Extract provisioner
	provisionerPattern := regexp.MustCompile(`provisioner\s+([a-z0-9][a-z0-9./-]*)`)
	if matches := provisionerPattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Provisioner = matches[1]
	}

	// Check for default
	if strings.Contains(strings.ToLower(query), "default") {
		opts.IsDefault = true
	}

	return opts
}

// parseConfigMapCreationFromQuery parses ConfigMap creation options from a query
func (s *SubAgent) parseConfigMapCreationFromQuery(query string, namespace string) CreateConfigMapOptions {
	opts := CreateConfigMapOptions{
		Namespace: namespace,
		Data:      make(map[string]string),
	}

	// Extract ConfigMap name
	namePattern := regexp.MustCompile(`(?:configmap|cm)\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := namePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Name = matches[1]
	}

	return opts
}

// parseSecretCreationFromQuery parses Secret creation options from a query
func (s *SubAgent) parseSecretCreationFromQuery(query string, namespace string) CreateSecretOptions {
	opts := CreateSecretOptions{
		Namespace: namespace,
		Type:      string(SecretTypeOpaque),
		Data:      make(map[string]string),
	}

	// Extract Secret name
	namePattern := regexp.MustCompile(`secret\s+(?:named?\s+)?([a-z0-9][a-z0-9-]*[a-z0-9])`)
	if matches := namePattern.FindStringSubmatch(strings.ToLower(query)); len(matches) > 1 {
		opts.Name = matches[1]
	}

	// Detect secret type
	lower := strings.ToLower(query)
	if strings.Contains(lower, "tls") {
		opts.Type = string(SecretTypeTLS)
	} else if strings.Contains(lower, "docker") || strings.Contains(lower, "registry") {
		opts.Type = string(SecretTypeDockerConfigJSON)
	} else if strings.Contains(lower, "basic-auth") || strings.Contains(lower, "basic auth") {
		opts.Type = string(SecretTypeBasicAuth)
	} else if strings.Contains(lower, "ssh") {
		opts.Type = string(SecretTypeSSHAuth)
	}

	return opts
}

// formatDuration formats a duration into a human-readable string
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
