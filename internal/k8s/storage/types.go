package storage

import (
	"context"
	"time"
)

// K8sClient defines the interface for kubectl operations
// This interface is satisfied by k8s.Client via an adapter
type K8sClient interface {
	Run(ctx context.Context, args ...string) (string, error)
	RunWithNamespace(ctx context.Context, namespace string, args ...string) (string, error)
	GetJSON(ctx context.Context, resourceType, name, namespace string) ([]byte, error)
	Describe(ctx context.Context, resourceType, name, namespace string) (string, error)
	Delete(ctx context.Context, resourceType, name, namespace string) (string, error)
	Apply(ctx context.Context, manifest string) (string, error)
}

// ResourceType identifies the type of storage resource
type ResourceType string

const (
	ResourcePV          ResourceType = "persistentvolume"
	ResourcePVC         ResourceType = "persistentvolumeclaim"
	ResourceStorageClass ResourceType = "storageclass"
	ResourceConfigMap   ResourceType = "configmap"
	ResourceSecret      ResourceType = "secret"
)

// ResponseType indicates the type of response from the sub-agent
type ResponseType string

const (
	ResponseTypeResult ResponseType = "result"
	ResponseTypePlan   ResponseType = "plan"
)

// QueryOptions contains options for storage queries
type QueryOptions struct {
	Namespace     string
	LabelSelector string
	FieldSelector string
	AllNamespaces bool
}

// Response represents the response from the storage sub-agent
type Response struct {
	Type    ResponseType
	Data    interface{}
	Plan    *StoragePlan
	Message string
}

// StoragePlan represents a plan for storage modifications
type StoragePlan struct {
	Version   int           `json:"version"`
	CreatedAt time.Time     `json:"createdAt"`
	Summary   string        `json:"summary"`
	Steps     []StorageStep `json:"steps"`
	Notes     []string      `json:"notes,omitempty"`
}

// StorageStep represents a single step in a storage plan
type StorageStep struct {
	ID          string            `json:"id"`
	Description string            `json:"description"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Manifest    string            `json:"manifest,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	Produces    map[string]string `json:"produces,omitempty"`
	WaitFor     *WaitCondition    `json:"waitFor,omitempty"`
}

// WaitCondition specifies a condition to wait for
type WaitCondition struct {
	Resource  string        `json:"resource"`
	Condition string        `json:"condition"`
	Timeout   time.Duration `json:"timeout"`
}

// PVInfo contains PersistentVolume information
type PVInfo struct {
	Name             string            `json:"name"`
	Capacity         string            `json:"capacity"`
	AccessModes      []string          `json:"accessModes"`
	ReclaimPolicy    string            `json:"reclaimPolicy"`
	Status           string            `json:"status"`
	Claim            string            `json:"claim,omitempty"`
	StorageClassName string            `json:"storageClassName,omitempty"`
	VolumeMode       string            `json:"volumeMode"`
	Labels           map[string]string `json:"labels"`
	Age              string            `json:"age"`
	CreatedAt        time.Time         `json:"createdAt"`

	// Source information
	HostPath  string `json:"hostPath,omitempty"`
	NFS       string `json:"nfs,omitempty"`
	CSI       string `json:"csi,omitempty"`
	AWSEBSVol string `json:"awsElasticBlockStore,omitempty"`
}

// PVCInfo contains PersistentVolumeClaim information
type PVCInfo struct {
	Name             string            `json:"name"`
	Namespace        string            `json:"namespace"`
	Status           string            `json:"status"`
	Volume           string            `json:"volume,omitempty"`
	Capacity         string            `json:"capacity,omitempty"`
	RequestedStorage string            `json:"requestedStorage"`
	AccessModes      []string          `json:"accessModes"`
	StorageClassName string            `json:"storageClassName,omitempty"`
	VolumeMode       string            `json:"volumeMode"`
	Labels           map[string]string `json:"labels"`
	Age              string            `json:"age"`
	CreatedAt        time.Time         `json:"createdAt"`
}

// StorageClassInfo contains StorageClass information
type StorageClassInfo struct {
	Name                 string            `json:"name"`
	Provisioner          string            `json:"provisioner"`
	ReclaimPolicy        string            `json:"reclaimPolicy"`
	VolumeBindingMode    string            `json:"volumeBindingMode"`
	AllowVolumeExpansion bool              `json:"allowVolumeExpansion"`
	Parameters           map[string]string `json:"parameters"`
	Labels               map[string]string `json:"labels"`
	Annotations          map[string]string `json:"annotations"`
	IsDefault            bool              `json:"isDefault"`
	Age                  string            `json:"age"`
	CreatedAt            time.Time         `json:"createdAt"`
}

// ConfigMapInfo contains ConfigMap information
type ConfigMapInfo struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	DataKeys  []string          `json:"dataKeys"`
	DataCount int               `json:"dataCount"`
	Labels    map[string]string `json:"labels"`
	Age       string            `json:"age"`
	CreatedAt time.Time         `json:"createdAt"`
}

// SecretInfo contains Secret information
type SecretInfo struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Type      string            `json:"type"`
	DataKeys  []string          `json:"dataKeys"`
	DataCount int               `json:"dataCount"`
	Labels    map[string]string `json:"labels"`
	Age       string            `json:"age"`
	CreatedAt time.Time         `json:"createdAt"`
}

// CreatePVOptions contains options for creating a PersistentVolume
type CreatePVOptions struct {
	Name             string
	Capacity         string
	AccessModes      []string
	ReclaimPolicy    string
	StorageClassName string
	VolumeMode       string
	Labels           map[string]string

	// Source options (one of these)
	HostPath string
	NFS      *NFSVolumeSource
	CSI      *CSIVolumeSource
}

// NFSVolumeSource contains NFS volume configuration
type NFSVolumeSource struct {
	Server   string
	Path     string
	ReadOnly bool
}

// CSIVolumeSource contains CSI volume configuration
type CSIVolumeSource struct {
	Driver           string
	VolumeHandle     string
	FSType           string
	ReadOnly         bool
	VolumeAttributes map[string]string
}

// CreatePVCOptions contains options for creating a PersistentVolumeClaim
type CreatePVCOptions struct {
	Name             string
	Namespace        string
	StorageClassName string
	AccessModes      []string
	Storage          string
	VolumeMode       string
	Labels           map[string]string
	Selector         map[string]string
	VolumeName       string
}

// CreateStorageClassOptions contains options for creating a StorageClass
type CreateStorageClassOptions struct {
	Name                 string
	Provisioner          string
	ReclaimPolicy        string
	VolumeBindingMode    string
	AllowVolumeExpansion bool
	Parameters           map[string]string
	Labels               map[string]string
	Annotations          map[string]string
	IsDefault            bool
}

// CreateConfigMapOptions contains options for creating a ConfigMap
type CreateConfigMapOptions struct {
	Name        string
	Namespace   string
	Data        map[string]string
	BinaryData  map[string][]byte
	Labels      map[string]string
	Annotations map[string]string
	FromFile    []string
	FromLiteral []string
}

// CreateSecretOptions contains options for creating a Secret
type CreateSecretOptions struct {
	Name        string
	Namespace   string
	Type        string
	Data        map[string]string
	StringData  map[string]string
	Labels      map[string]string
	Annotations map[string]string
	FromFile    []string
	FromLiteral []string
}

// SecretType defines secret types
type SecretType string

const (
	SecretTypeOpaque              SecretType = "Opaque"
	SecretTypeServiceAccountToken SecretType = "kubernetes.io/service-account-token"
	SecretTypeDockerConfigJSON    SecretType = "kubernetes.io/dockerconfigjson"
	SecretTypeTLS                 SecretType = "kubernetes.io/tls"
	SecretTypeBasicAuth           SecretType = "kubernetes.io/basic-auth"
	SecretTypeSSHAuth             SecretType = "kubernetes.io/ssh-auth"
)

// AccessMode defines PV/PVC access modes
type AccessMode string

const (
	AccessModeReadWriteOnce AccessMode = "ReadWriteOnce"
	AccessModeReadOnlyMany  AccessMode = "ReadOnlyMany"
	AccessModeReadWriteMany AccessMode = "ReadWriteMany"
	AccessModeReadWriteOncePod AccessMode = "ReadWriteOncePod"
)

// ReclaimPolicy defines PV reclaim policies
type ReclaimPolicy string

const (
	ReclaimPolicyRetain  ReclaimPolicy = "Retain"
	ReclaimPolicyRecycle ReclaimPolicy = "Recycle"
	ReclaimPolicyDelete  ReclaimPolicy = "Delete"
)

// VolumeBindingMode defines when volume binding occurs
type VolumeBindingMode string

const (
	VolumeBindingImmediate            VolumeBindingMode = "Immediate"
	VolumeBindingWaitForFirstConsumer VolumeBindingMode = "WaitForFirstConsumer"
)
