package verda

// Types mirror the Verda Cloud OpenAPI 3.1 spec at https://api.verda.com/v1/openapi.json
// We hand-write the subset used by the client/CLI/ask flows rather than pull in a
// code generator — the surface is small and stable enough that the dependency is
// not worth it.

// TokenResponse is the body of POST /v1/oauth2/token.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// APIError is the body of any non-2xx Verda response.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return "[" + e.Code + "] " + e.Message
}

// ActionResult is an entry in a 207 Multi-Status response from PUT /v1/instances
// (and similar bulk-action endpoints). Verda returns one of these per target ID.
type ActionResult struct {
	InstanceID string `json:"instanceId"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	StatusCode int    `json:"statusCode,omitempty"`
}

// Resource status enum (shared by instances and clusters).
const (
	StatusRunning            = "running"
	StatusProvisioning       = "provisioning"
	StatusOffline            = "offline"
	StatusDiscontinued       = "discontinued"
	StatusUnknown            = "unknown"
	StatusOrdered            = "ordered"
	StatusNotFound           = "notfound"
	StatusNew                = "new"
	StatusError              = "error"
	StatusDeleting           = "deleting"
	StatusValidating         = "validating"
	StatusNoCapacity         = "no_capacity"
	StatusInstallationFailed = "installation_failed"
)

// Instance action enum (body of PUT /v1/instances).
const (
	InstanceActionBoot          = "boot"
	InstanceActionStart         = "start"
	InstanceActionShutdown      = "shutdown"
	InstanceActionForceShutdown = "force_shutdown"
	InstanceActionDelete        = "delete"
	InstanceActionDiscontinue   = "discontinue"
	InstanceActionHibernate     = "hibernate"
	InstanceActionConfigureSpot = "configure_spot"
	InstanceActionDeleteStuck   = "delete_stuck"
	InstanceActionDeploy        = "deploy"
	InstanceActionTransfer      = "transfer"
)

// Volume action enum (body of PUT /v1/volumes).
const (
	VolumeActionAttach  = "attach"
	VolumeActionDetach  = "detach"
	VolumeActionDelete  = "delete"
	VolumeActionRename  = "rename"
	VolumeActionResize  = "resize"
	VolumeActionRestore = "restore"
	VolumeActionClone   = "clone"
	VolumeActionCancel  = "cancel"
	VolumeActionCreate  = "create"
	VolumeActionExport  = "export"
)

// Volume type enum.
const (
	VolumeTypeHDD               = "HDD"
	VolumeTypeNVMe              = "NVMe"
	VolumeTypeHDDShared         = "HDD_Shared"
	VolumeTypeNVMeShared        = "NVMe_Shared"
	VolumeTypeNVMeLocalStorage  = "NVMe_Local_Storage"
	VolumeTypeNVMeSharedCluster = "NVMe_Shared_Cluster"
	VolumeTypeNVMeOSCluster     = "NVMe_OS_Cluster"
)

// Contract enum.
const (
	ContractLongTerm     = "LONG_TERM"
	ContractPayAsYouGo   = "PAY_AS_YOU_GO"
	ContractSpot         = "SPOT"
	ExtensionAutoRenew   = "auto_renew"
	ExtensionPayAsYouGo  = "pay_as_you_go"
	ExtensionEndContract = "end_contract"
)

// HardwareDescriptor is the uniform shape Verda uses for cpu/gpu/memory/storage
// blocks across instance, cluster, and instance-type responses.
type HardwareDescriptor struct {
	Description     string `json:"description,omitempty"`
	NumberOfCores   int    `json:"number_of_cores,omitempty"`
	NumberOfGpus    int    `json:"number_of_gpus,omitempty"`
	SizeInGigabytes int    `json:"size_in_gigabytes,omitempty"`
}

// Instance is the GET /v1/instances/{id} payload.
type Instance struct {
	ID              string             `json:"id"`
	IP              string             `json:"ip,omitempty"`
	Status          string             `json:"status"`
	CreatedAt       string             `json:"created_at,omitempty"`
	CPU             HardwareDescriptor `json:"cpu"`
	GPU             HardwareDescriptor `json:"gpu"`
	GPUMemory       HardwareDescriptor `json:"gpu_memory"`
	Memory          HardwareDescriptor `json:"memory"`
	Storage         HardwareDescriptor `json:"storage"`
	Hostname        string             `json:"hostname,omitempty"`
	Description     string             `json:"description,omitempty"`
	Location        string             `json:"location,omitempty"`
	PricePerHour    float64            `json:"price_per_hour,omitempty"`
	IsSpot          bool               `json:"is_spot,omitempty"`
	InstanceType    string             `json:"instance_type,omitempty"`
	Image           string             `json:"image,omitempty"`
	OSName          string             `json:"os_name,omitempty"`
	StartupScriptID string             `json:"startup_script_id,omitempty"`
	SSHKeyIDs       []string           `json:"ssh_key_ids,omitempty"`
	OSVolumeID      string             `json:"os_volume_id,omitempty"`
	JupyterToken    string             `json:"jupyter_token,omitempty"`
	Contract        string             `json:"contract,omitempty"`
	Pricing         string             `json:"pricing,omitempty"`
	VolumeIDs       []string           `json:"volume_ids,omitempty"`
}

// DeployInstanceRequest is the body of POST /v1/instances.
type DeployInstanceRequest struct {
	InstanceType    string       `json:"instance_type"`
	Image           string       `json:"image"`
	Hostname        string       `json:"hostname"`
	Description     string       `json:"description"`
	LocationCode    string       `json:"location_code"`
	SSHKeyIDs       []string     `json:"ssh_key_ids,omitempty"`
	StartupScriptID string       `json:"startup_script_id,omitempty"`
	IsSpot          bool         `json:"is_spot,omitempty"`
	Coupon          string       `json:"coupon,omitempty"`
	Contract        string       `json:"contract,omitempty"`
	OSVolume        *OSVolumeDto `json:"os_volume,omitempty"`
	Volumes         []VolumeDto  `json:"volumes,omitempty"`
	ExistingVolumes []string     `json:"existing_volumes,omitempty"`
}

// OSVolumeDto is the nested OS-volume spec on DeployInstanceRequest.
type OSVolumeDto struct {
	Name              string `json:"name"`
	Size              int    `json:"size"`
	OnSpotDiscontinue string `json:"on_spot_discontinue,omitempty"`
}

// VolumeDto is the nested non-OS-volume spec on DeployInstanceRequest.
type VolumeDto struct {
	Name              string `json:"name"`
	Size              int    `json:"size"`
	Type              string `json:"type"`
	OnSpotDiscontinue string `json:"on_spot_discontinue,omitempty"`
}

// PerformInstanceActionRequest is the body of PUT /v1/instances.
type PerformInstanceActionRequest struct {
	Action            string      `json:"action"`
	ID                interface{} `json:"id"` // string or []string
	VolumeIDs         []string    `json:"volume_ids,omitempty"`
	DeletePermanently bool        `json:"delete_permanently,omitempty"`
}

// WorkerNode is an entry in Cluster.WorkerNodes.
type WorkerNode struct {
	ID        string `json:"id"`
	Hostname  string `json:"hostname,omitempty"`
	PublicIP  string `json:"public_ip,omitempty"`
	PrivateIP string `json:"private_ip,omitempty"`
	Status    string `json:"status,omitempty"`
}

// ClusterSharedVolume describes an SFS mounted on a cluster.
type ClusterSharedVolume struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	MountPoint      string `json:"mount_point,omitempty"`
	SizeInGigabytes int    `json:"size_in_gigabytes,omitempty"`
}

// Cluster is the GET /v1/clusters/{id} payload.
type Cluster struct {
	ID                string                `json:"id"`
	IP                string                `json:"ip,omitempty"` // jump host
	Status            string                `json:"status"`
	CreatedAt         string                `json:"created_at,omitempty"`
	CPU               HardwareDescriptor    `json:"cpu"`
	GPU               HardwareDescriptor    `json:"gpu"`
	GPUMemory         HardwareDescriptor    `json:"gpu_memory"`
	Memory            HardwareDescriptor    `json:"memory"`
	Hostname          string                `json:"hostname,omitempty"`
	Description       string                `json:"description,omitempty"`
	Location          string                `json:"location,omitempty"`
	PricePerHour      float64               `json:"price_per_hour,omitempty"`
	ClusterType       string                `json:"cluster_type,omitempty"`
	Image             string                `json:"image,omitempty"`
	OSName            string                `json:"os_name,omitempty"`
	StartupScriptID   string                `json:"startup_script_id,omitempty"`
	SSHKeyIDs         []string              `json:"ssh_key_ids,omitempty"`
	Contract          string                `json:"contract,omitempty"`
	ExtensionSettings string                `json:"extension_settings,omitempty"`
	LongTermPeriod    string                `json:"long_term_period,omitempty"`
	WorkerNodes       []WorkerNode          `json:"worker_nodes,omitempty"`
	SharedVolumes     []ClusterSharedVolume `json:"shared_volumes,omitempty"`
}

// DeployClusterRequest is the body of POST /v1/clusters.
type DeployClusterRequest struct {
	ClusterType       string                    `json:"cluster_type"`
	Image             string                    `json:"image"`
	SSHKeyIDs         []string                  `json:"ssh_key_ids,omitempty"`
	StartupScriptID   string                    `json:"startup_script_id,omitempty"`
	Hostname          string                    `json:"hostname"`
	Description       string                    `json:"description"`
	LocationCode      string                    `json:"location_code"`
	Contract          string                    `json:"contract,omitempty"`
	ExtensionSettings string                    `json:"extension_settings,omitempty"`
	SharedVolume      *SharedVolumeDto          `json:"shared_volume,omitempty"`
	ExistingVolumes   []ExistingSharedVolumeDto `json:"existing_volumes,omitempty"`
	Coupon            string                    `json:"coupon,omitempty"`
}

// SharedVolumeDto is the shared_volume block on cluster creation.
type SharedVolumeDto struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

// ExistingSharedVolumeDto attaches a previously created SFS on cluster creation.
type ExistingSharedVolumeDto struct {
	ID string `json:"id"`
}

// Volume is the GET /v1/volumes/{id} payload (active; trash has additional fields).
type Volume struct {
	ID           string   `json:"id"`
	InstanceID   string   `json:"instance_id,omitempty"`
	Name         string   `json:"name"`
	CreatedAt    string   `json:"created_at,omitempty"`
	Status       string   `json:"status"`
	Size         int      `json:"size"`
	IsOSVolume   bool     `json:"is_os_volume,omitempty"`
	Target       string   `json:"target,omitempty"`
	Type         string   `json:"type"`
	Location     string   `json:"location,omitempty"`
	SSHKeyIDs    []string `json:"ssh_key_ids,omitempty"`
	PseudoPath   string   `json:"pseudo_path,omitempty"`
	Contract     string   `json:"contract,omitempty"`
	BaseHourly   float64  `json:"base_hourly_cost,omitempty"`
	MonthlyPrice float64  `json:"monthly_price,omitempty"`
	Currency     string   `json:"currency,omitempty"`
}

// SSHKey is the GET /v1/ssh-keys/{id} payload.
type SSHKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Script is the GET /v1/scripts/{id} payload.
type Script struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Script    string `json:"script,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// InstanceType is an entry in GET /v1/instance-types.
type InstanceType struct {
	ID                  string             `json:"id"`
	InstanceType        string             `json:"instance_type"`
	Name                string             `json:"name,omitempty"`
	DisplayName         string             `json:"display_name,omitempty"`
	Model               string             `json:"model,omitempty"`
	Description         string             `json:"description,omitempty"`
	Manufacturer        string             `json:"manufacturer,omitempty"`
	CPU                 HardwareDescriptor `json:"cpu"`
	GPU                 HardwareDescriptor `json:"gpu"`
	GPUMemory           HardwareDescriptor `json:"gpu_memory"`
	Memory              HardwareDescriptor `json:"memory"`
	Storage             HardwareDescriptor `json:"storage"`
	P2P                 string             `json:"p2p,omitempty"`
	PricePerHour        string             `json:"price_per_hour,omitempty"`
	SpotPrice           string             `json:"spot_price,omitempty"`
	ServerlessPrice     string             `json:"serverless_price,omitempty"`
	ServerlessSpotPrice string             `json:"serverless_spot_price,omitempty"`
	Currency            string             `json:"currency,omitempty"`
	BestFor             []string           `json:"best_for,omitempty"`
	DeployWarning       string             `json:"deploy_warning,omitempty"`
	SupportedOS         []string           `json:"supported_os,omitempty"`
}

// Location is an entry in GET /v1/locations.
type Location struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	CountryCode string `json:"country_code,omitempty"`
}

// Balance is the GET /v1/balance payload.
type Balance struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency,omitempty"`
}

// Image is an entry in GET /v1/images or /v1/images/cluster.
type Image struct {
	ID        string   `json:"id,omitempty"`
	ImageType string   `json:"image_type,omitempty"`
	Name      string   `json:"name,omitempty"`
	Category  string   `json:"category,omitempty"`
	IsDefault bool     `json:"is_default,omitempty"`
	IsCluster bool     `json:"is_cluster,omitempty"`
	Details   []string `json:"details,omitempty"`
}
